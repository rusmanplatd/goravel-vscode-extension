// Package lsp implements a stdio JSON-RPC Language Server for Goravel projects.
package lsp

import (
	"bufio"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/rusmanplatd/goravel-lsp/internal/framework"
	"github.com/rusmanplatd/goravel-lsp/internal/indexer"
	"github.com/rusmanplatd/goravel-lsp/internal/orm"
	"github.com/rusmanplatd/goravel-lsp/internal/rawsql"
)

type message struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type Position struct {
	Line      int `json:"line"`
	Character int `json:"character"`
}
type Range struct {
	Start Position `json:"start"`
	End   Position `json:"end"`
}
type LocationT struct {
	URI   string `json:"uri"`
	Range Range  `json:"range"`
}

// Server is the language server state.
type Server struct {
	in   *bufio.Reader
	out  io.Writer
	wmu  sync.Mutex
	root string
	cat  *indexer.Catalog
	docs map[string]string // uri -> text
	exit bool
}

func New(r io.Reader, w io.Writer) *Server {
	return &Server{in: bufio.NewReader(r), out: w, cat: indexer.NewCatalog(), docs: map[string]string{}}
}

func (s *Server) Run() error {
	for !s.exit {
		raw, err := s.read()
		if err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
		var m message
		if json.Unmarshal(raw, &m) != nil {
			continue
		}
		s.dispatch(&m)
	}
	return nil
}

func (s *Server) read() ([]byte, error) {
	n := -1
	for {
		line, err := s.in.ReadString('\n')
		if err != nil {
			return nil, err
		}
		line = strings.TrimSpace(line)
		if line == "" {
			break
		}
		if strings.HasPrefix(strings.ToLower(line), "content-length:") {
			n, _ = strconv.Atoi(strings.TrimSpace(line[len("content-length:"):]))
		}
	}
	if n < 0 {
		return nil, fmt.Errorf("missing content-length")
	}
	buf := make([]byte, n)
	_, err := io.ReadFull(s.in, buf)
	return buf, err
}

func (s *Server) send(v any) {
	b, _ := json.Marshal(v)
	s.wmu.Lock()
	defer s.wmu.Unlock()
	fmt.Fprintf(s.out, "Content-Length: %d\r\n\r\n%s", len(b), b)
}

func (s *Server) reply(id json.RawMessage, result any) {
	if result == nil {
		s.send(map[string]any{"jsonrpc": "2.0", "id": id, "result": nil})
		return
	}
	s.send(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
}

func (s *Server) notify(method string, params any) {
	s.send(map[string]any{"jsonrpc": "2.0", "method": method, "params": params})
}

func (s *Server) dispatch(m *message) {
	switch m.Method {
	case "initialize":
		var p struct {
			RootURI string `json:"rootUri"`
			Folders []struct {
				URI string `json:"uri"`
			} `json:"workspaceFolders"`
			InitOpts struct {
				LiveIntrospection bool `json:"liveIntrospection"`
			} `json:"initializationOptions"`
		}
		_ = json.Unmarshal(m.Params, &p)
		if p.InitOpts.LiveIntrospection {
			indexer.LiveDBEnabled = true
		}
		uri := p.RootURI
		if uri == "" && len(p.Folders) > 0 {
			uri = p.Folders[0].URI
		}
		s.root = uriToPath(uri)
		s.reindex()
		s.reply(m.ID, map[string]any{
			"capabilities": map[string]any{
				"textDocumentSync":   1,
				"completionProvider": map[string]any{"triggerCharacters": []string{".", " ", "\"", "|", ":", ","}},
				"hoverProvider":      true,
				"definitionProvider": true,
				"referencesProvider": true,
				"renameProvider":     true,
				"codeActionProvider": true,
				"codeLensProvider":   map[string]any{"resolveProvider": false},
				"inlayHintProvider":  true,
				"semanticTokensProvider": map[string]any{
					"legend": map[string]any{
						"tokenTypes":     rawsql.SemanticTokenTypes,
						"tokenModifiers": []string{"declaration", "readonly"},
					},
					"full": true,
				},
			},
			"serverInfo": map[string]any{"name": "goravel-lsp", "version": "0.6.0"},
		})
	case "shutdown":
		s.reply(m.ID, nil)
	case "exit":
		s.exit = true
	case "textDocument/didOpen":
		var p struct {
			TextDocument struct {
				URI  string `json:"uri"`
				Text string `json:"text"`
			} `json:"textDocument"`
		}
		_ = json.Unmarshal(m.Params, &p)
		s.docs[p.TextDocument.URI] = p.TextDocument.Text
		s.diagnose(p.TextDocument.URI)
	case "textDocument/didChange":
		var p struct {
			TextDocument struct {
				URI string `json:"uri"`
			} `json:"textDocument"`
			Changes []struct {
				Text string `json:"text"`
			} `json:"contentChanges"`
		}
		_ = json.Unmarshal(m.Params, &p)
		if n := len(p.Changes); n > 0 {
			s.docs[p.TextDocument.URI] = p.Changes[n-1].Text
			s.diagnose(p.TextDocument.URI)
		}
	case "textDocument/didSave":
		var p struct {
			TextDocument struct {
				URI string `json:"uri"`
			} `json:"textDocument"`
		}
		_ = json.Unmarshal(m.Params, &p)
		path := filepath.ToSlash(uriToPath(p.TextDocument.URI))
		if shouldReindex(path) {
			s.reindex()
			for uri := range s.docs {
				s.diagnose(uri)
			}
		}
	case "workspace/didChangeWatchedFiles":
		s.reindex()
		for uri := range s.docs {
			s.diagnose(uri)
		}
	case "workspace/didChangeConfiguration":
		var p struct {
			Settings map[string]any `json:"settings"`
		}
		_ = json.Unmarshal(m.Params, &p)
		if enabled, ok := extractLiveIntrospectionSetting(p.Settings); ok {
			if indexer.LiveDBEnabled != enabled {
				indexer.LiveDBEnabled = enabled
				s.reindex()
				for uri := range s.docs {
					s.diagnose(uri)
				}
			}
		}
	case "textDocument/didClose":
		var p struct {
			TextDocument struct {
				URI string `json:"uri"`
			} `json:"textDocument"`
		}
		_ = json.Unmarshal(m.Params, &p)
		delete(s.docs, p.TextDocument.URI)
		s.notify("textDocument/publishDiagnostics", map[string]any{"uri": p.TextDocument.URI, "diagnostics": []any{}})
	case "textDocument/completion":
		s.reply(m.ID, s.completion(m.Params))
	case "textDocument/hover":
		s.reply(m.ID, s.hover(m.Params))
	case "textDocument/codeAction":
		s.reply(m.ID, s.codeAction(m.Params))
	case "textDocument/definition":
		s.reply(m.ID, s.definition(m.Params))
	case "textDocument/references":
		s.reply(m.ID, s.references(m.Params))
	case "textDocument/rename":
		s.reply(m.ID, s.rename(m.Params))
	case "textDocument/codeLens":
		s.reply(m.ID, s.codeLens(m.Params))
	case "textDocument/inlayHint":
		s.reply(m.ID, s.inlayHint(m.Params))
	case "textDocument/semanticTokens/full":
		s.reply(m.ID, s.semanticTokens(m.Params))
	case "goravel/overview":
		s.reply(m.ID, s.overview())
	case "goravel/explainQuery":
		s.reply(m.ID, s.explainQuery(m.Params))
	case "goravel/previewQuery":
		s.reply(m.ID, s.previewQuery(m.Params))
	case "goravel/routeSample":
		s.reply(m.ID, s.routeSample(m.Params))
	case "goravel/registerBootstrap":
		s.reply(m.ID, s.registerBootstrap(m.Params))
	case "goravel/erDiagram":
		s.reply(m.ID, s.erDiagram())
	default:
		if len(m.ID) > 0 && m.Method != "" {
			s.send(map[string]any{"jsonrpc": "2.0", "id": m.ID, "error": &rpcError{-32601, "method not found: " + m.Method}})
		}
	}
}

func shouldReindex(path string) bool {
	return strings.Contains(path, "/app/models/") ||
		strings.Contains(path, "/database/migrations") ||
		strings.Contains(path, "/database/seeders/") ||
		strings.Contains(path, "/config/") ||
		strings.Contains(path, "/routes/") ||
		strings.Contains(path, "/app/rules/") ||
		strings.Contains(path, "/app/console/") ||
		strings.Contains(path, "/app/http/controllers/") ||
		strings.Contains(path, "/app/http/requests/") ||
		strings.Contains(path, "/app/http/middleware/") ||
		strings.Contains(path, "/app/grpc/") ||
		strings.Contains(path, "/app/events/") ||
		strings.Contains(path, "/app/listeners/") ||
		strings.Contains(path, "/app/jobs/") ||
		strings.Contains(path, "/app/observers/") ||
		strings.Contains(path, "/app/providers/") ||
		strings.Contains(path, "/bootstrap/") ||
		strings.Contains(path, "/resources/views/") ||
		strings.Contains(path, "/lang/") ||
		strings.HasSuffix(path, ".proto") ||
		strings.HasSuffix(path, "/.env") ||
		strings.HasSuffix(path, "/.env.example")
}

func (s *Server) reindex() {
	if s.root != "" {
		s.cat = indexer.Build(s.root)
	}
}

type posParams struct {
	TextDocument struct {
		URI string `json:"uri"`
	} `json:"textDocument"`
	Position Position `json:"position"`
}

func (s *Server) resolveInput(raw json.RawMessage) (uri, path, text string, p posParams, off int, ok bool) {
	_ = json.Unmarshal(raw, &p)
	uri = p.TextDocument.URI
	text, found := s.docs[uri]
	path = uriToPath(uri)
	if !found || (!strings.HasSuffix(uri, ".go") && !framework.IsTemplateFile(path)) {
		return
	}
	off = posToOffset(text, p.Position)
	ok = true
	return
}

// resolve finds file, doc, scope and virtual offset for a raw SQL request.
func (s *Server) resolve(raw json.RawMessage) (text string, p posParams, off int, fi *rawsql.FileInfo, d *rawsql.Doc, v int, sc rawsql.Scope, ok bool) {
	_, path, text, p, off, ok := s.resolveInput(raw)
	if !ok || !strings.HasSuffix(path, ".go") {
		ok = false
		return
	}
	fi = rawsql.Extract(path, []byte(text), s.cat)
	d = fi.DocAt(off)
	if d == nil {
		ok = false
		return
	}
	var vok bool
	v, vok = d.ToVirtual(off)
	if !vok {
		ok = false
		return
	}
	sc = fi.BuildScope(d, s.cat)
	ok = true
	return
}

func (s *Server) completion(raw json.RawMessage) any {
	_, path, text, _, off, inputOK := s.resolveInput(raw)
	if !inputOK {
		return nil
	}
	if framework.IsTemplateFile(path) {
		if tItems := framework.TemplateCompletions(path, text, s.cat, off); len(tItems) > 0 {
			var lspItems []map[string]any
			for _, it := range tItems {
				lspItems = append(lspItems, map[string]any{"label": it.Label, "detail": it.Detail, "kind": it.Kind})
			}
			return map[string]any{"isIncomplete": false, "items": lspItems}
		}
		return nil
	}
	// 1. Check ORM / Config / Validation spans first
	fa := orm.AnalyzeFile(path, []byte(text), s.cat)
	if items, ok := fa.Completion(off, s.cat); ok && len(items) > 0 {
		return map[string]any{"isIncomplete": false, "items": items}
	}

	// 1b. Check Form Request keys & View templates
	if fItems := append(framework.RequestCompletions(path, text, s.cat, off), framework.ViewCompletions(path, text, s.cat, off)...); len(fItems) > 0 {
		var lspItems []map[string]any
		for _, it := range fItems {
			lspItems = append(lspItems, map[string]any{"label": it.Label, "detail": it.Detail, "kind": it.Kind})
		}
		return map[string]any{"isIncomplete": false, "items": lspItems}
	}

	// 2. Check Raw SQL documents
	_, _, _, fi, d, v, sc, ok := s.resolve(raw)
	if !ok {
		return nil
	}
	cc := d.CompletionAt(v)
	var items []map[string]any
	switch {
	case cc.Qualifier != "":
		t := sc[cc.Qualifier]
		if t == nil {
			return nil
		}
		for _, c := range t.Columns {
			items = append(items, colItem(t, c))
		}
	case cc.WantJoinOn:
		jt := s.cat.Table(cc.JoinTable)
		if jt == nil {
			jt = sc[cc.JoinAlias]
		}
		for _, cond := range rawsql.JoinConditions(jt, cc.JoinAlias, sc) {
			items = append(items, map[string]any{
				"label": cond, "kind": 15, "detail": "Foreign Key Join Condition",
				"sortText": "0_" + cond,
			})
		}
		if jt != nil {
			for _, c := range jt.Columns {
				items = append(items, colItem(jt, c))
			}
		}
	case cc.WantTable:
		// Suggest in-scope Go table variables first!
		for varName, tblName := range fi.TableVars {
			t := s.cat.Table(tblName)
			items = append(items, map[string]any{
				"label":      varName,
				"kind":       6,
				"detail":     fmt.Sprintf("Go table var -> %s", tblName),
				"insertText": "` + " + varName + " + `",
				"sortText":   "0_" + varName,
				"documentation": map[string]any{"kind": "markdown", "value": tableMD(t)},
			})
		}
		names := s.cat.TableNames()
		sort.Strings(names)
		for _, n := range names {
			t := s.cat.Table(n)
			items = append(items, map[string]any{
				"label": n, "kind": 7, "detail": modelDetail(t),
				"sortText":      "1_" + n,
				"documentation": map[string]any{"kind": "markdown", "value": tableMD(t)},
			})
		}
	default:
		if tblName := insertTargetTable(d.Text, v); tblName != "" {
			if t := s.cat.Table(tblName); t != nil {
				for _, c := range t.Columns {
					items = append(items, colItem(t, c))
				}
				return map[string]any{"isIncomplete": false, "items": items}
			}
		}
		if cc.WantColAlias {
			for _, oc := range d.SelectAliases() {
				detail := "column alias"
				if oc.Expr != "" {
					detail = "alias -> " + oc.Expr
				}
				items = append(items, map[string]any{
					"label":    oc.Name,
					"kind":     5,
					"detail":   detail,
					"sortText": "0_" + oc.Name,
				})
			}
		}
		for alias, t := range sc {
			if alias != strings.ToLower(t.Name) {
				items = append(items, map[string]any{
					"label":         alias,
					"kind":          7,
					"detail":        "alias -> " + t.Name,
					"sortText":      "1_" + alias,
					"documentation": map[string]any{"kind": "markdown", "value": tableMD(t)},
				})
			}
		}
		for _, t := range sc.Tables() {
			for _, c := range t.Columns {
				items = append(items, colItem(t, c))
			}
		}
		for _, fn := range sqlFunctions {
			items = append(items, map[string]any{"label": fn, "kind": 3, "detail": "SQL function"})
		}
		for _, fn := range s.cat.DBFunctions {
			detail := fn.Kind
			if fn.ReturnType != "" {
				detail += " -> " + fn.ReturnType
			}
			items = append(items, map[string]any{"label": fn.Name, "kind": 3, "detail": detail})
		}
		for _, kw := range keywords {
			items = append(items, map[string]any{"label": kw, "kind": 14})
		}
	}
	return map[string]any{"isIncomplete": false, "items": items}
}

var (
	keywords     = strings.Fields("SELECT DISTINCT FROM WHERE JOIN LEFT RIGHT INNER OUTER FULL CROSS ON USING AND OR NOT IN IS NULL TRUE FALSE AS INTO VALUES UPDATE SET DELETE RETURNING ILIKE LIKE BETWEEN EXISTS CASE WHEN THEN ELSE END GROUP BY ORDER ASC DESC HAVING LIMIT OFFSET UNION ALL")
	sqlFunctions = strings.Fields("COUNT SUM AVG MIN MAX COALESCE NULLIF CAST NOW CURRENT_TIMESTAMP CURRENT_DATE LOWER UPPER TRIM BTRIM LTRIM RTRIM LENGTH SUBSTRING CONCAT REPLACE SPLIT_PART TO_CHAR TO_DATE TO_TIMESTAMP DATE_TRUNC EXTRACT AGE ROW_NUMBER RANK DENSE_RANK JSON_AGG JSONB_BUILD_OBJECT STRING_AGG ARRAY_AGG")
)

var insertColsRe = regexp.MustCompile(`(?is)\bINSERT\s+INTO\s+([A-Za-z_][\w."]*)\s*\([^)]*$`)

func insertTargetTable(sql string, v int) string {
	if v < 0 || v > len(sql) {
		return ""
	}
	m := insertColsRe.FindStringSubmatch(sql[:v])
	if len(m) >= 2 {
		return m[1]
	}
	return ""
}

func colItem(t *indexer.Table, c *indexer.Column) map[string]any {
	return map[string]any{
		"label": c.Name, "kind": 5, "detail": colDetail(t, c),
		"documentation": map[string]any{"kind": "markdown", "value": colMD(t, c)},
	}
}

func modelDetail(t *indexer.Table) string {
	if t != nil && t.Model != "" {
		return "models." + t.Model
	}
	return "table"
}

func colDetail(t *indexer.Table, c *indexer.Column) string {
	typ := c.SQLType
	if typ == "" {
		typ = c.GoType
	}
	return typ
}

func colMD(t *indexer.Table, c *indexer.Column) string {
	var b strings.Builder
	fmt.Fprintf(&b, "**%s.%s**\n\n", t.Name, c.Name)
	if c.GoName != "" {
		fmt.Fprintf(&b, "- Go: `%s.%s` (`%s`)\n", t.Model, c.GoName, c.GoType)
	}
	if c.SQLType != "" {
		null := "NOT NULL"
		if c.Nullable {
			null = "NULL"
		}
		fmt.Fprintf(&b, "- SQL: `%s %s`\n", c.SQLType, null)
	}
	if c.ForeignKey != "" {
		fmt.Fprintf(&b, "- FK: `%s`\n", c.ForeignKey)
	}
	if c.Doc != "" {
		fmt.Fprintf(&b, "\n%s\n", c.Doc)
	}
	if c.Example != "" {
		fmt.Fprintf(&b, "\nExample: `%s`\n", c.Example)
	}
	return b.String()
}

func tableMD(t *indexer.Table) string {
	if t == nil {
		return ""
	}
	var b strings.Builder
	if t.Schema != "" {
		fmt.Fprintf(&b, "**%s.%s**", t.Schema, t.Name)
	} else {
		fmt.Fprintf(&b, "**%s**", t.Name)
	}
	if t.Model != "" {
		fmt.Fprintf(&b, " — `models.%s`", t.Model)
	}
	b.WriteString("\n\n| column | type |\n|---|---|\n")
	for _, c := range t.Columns {
		fmt.Fprintf(&b, "| %s | %s |\n", c.Name, colDetail(t, c))
	}
	return b.String()
}

func (s *Server) hover(raw json.RawMessage) any {
	_, path, text, _, off, inputOK := s.resolveInput(raw)
	if !inputOK {
		return nil
	}
	if framework.IsTemplateFile(path) {
		if h := framework.TemplateHover(path, text, s.cat, off); h != nil {
			return map[string]any{"contents": map[string]any{"kind": "markdown", "value": h.Markdown}}
		}
		return nil
	}
	// 1. Check ORM / Config / Validation spans first
	fa := orm.AnalyzeFile(path, []byte(text), s.cat)
	if md, st, en, ok := fa.Hover(off, s.cat); ok {
		return map[string]any{
			"contents": map[string]any{"kind": "markdown", "value": md},
			"range":    Range{offsetToPos(text, st), offsetToPos(text, en)},
		}
	}

	// 1b. Framework hover (Routes/Controllers/Middleware, Views, Schedule Cron)
	if h := framework.RouteAndControllerHover(path, text, s.cat, off); h != nil {
		return map[string]any{"contents": map[string]any{"kind": "markdown", "value": h.Markdown}}
	}
	if h := framework.ViewHover(path, text, s.cat, off); h != nil {
		return map[string]any{"contents": map[string]any{"kind": "markdown", "value": h.Markdown}}
	}
	if h := framework.ScheduleHover(path, text, off); h != nil {
		return map[string]any{"contents": map[string]any{"kind": "markdown", "value": h.Markdown}}
	}

	// 1c. OriginalTableName constant symbol hover
	if word, st, en := identAtOffset(text, off); word != "" {
		if tc := s.cat.TableConsts[word]; tc != nil {
			var b strings.Builder
			fmt.Fprintf(&b, "**`%s`** = `\"%s\"`\n\n", tc.Name, tc.TableName)
			if tc.Model != "" {
				fmt.Fprintf(&b, "- Model: `models.%s`\n", tc.Model)
			}
			if t := s.cat.Table(tc.TableName); t != nil {
				fmt.Fprintf(&b, "- Columns: %d\n", len(t.Columns))
			}
			return map[string]any{
				"contents": map[string]any{"kind": "markdown", "value": b.String()},
				"range":    Range{offsetToPos(text, st), offsetToPos(text, en)},
			}
		}
	}

	// 2. Raw SQL hover
	_, _, _, fi, d, v, sc, ok := s.resolve(raw)
	if !ok {
		if fi != nil {
			if vr := fi.VarRefAt(off); vr != nil {
				if t := s.cat.Table(vr.Table); t != nil {
					return map[string]any{
						"contents": map[string]any{"kind": "markdown", "value": tableMD(t)},
						"range":    Range{offsetToPos(text, vr.GoStart), offsetToPos(text, vr.GoEnd)},
					}
				}
			}
		}
		return nil
	}
	ref := d.RefAt(v, sc)
	if ref == nil {
		return nil
	}
	md := ""
	switch {
	case ref.ColAlias != nil:
		md = fmt.Sprintf("**Column Alias `%s`**\n\n", ref.ColAlias.Name)
		if ref.Table != nil && ref.Column != nil {
			md += colMD(ref.Table, ref.Column)
		} else if ref.ColAlias.Expr != "" {
			md += fmt.Sprintf("- Expression: `%s`\n", ref.ColAlias.Expr)
		}
	case ref.Column != nil:
		md = colMD(ref.Table, ref.Column)
	case ref.TableAlias != "" && ref.Table != nil:
		md = fmt.Sprintf("**Table Alias `%s`** → %s", ref.TableAlias, tableMD(ref.Table))
	default:
		md = tableMD(ref.Table)
	}
	res := map[string]any{"contents": map[string]any{"kind": "markdown", "value": md}}
	if g1, ok1 := d.ToGo(ref.VStart); ok1 {
		if g2, ok2 := d.ToGo(ref.VEnd); ok2 {
			res["range"] = Range{offsetToPos(text, g1), offsetToPos(text, g2)}
		}
	}
	return res
}

func (s *Server) definition(raw json.RawMessage) any {
	uri, path, text, _, off, inputOK := s.resolveInput(raw)
	if !inputOK {
		return nil
	}
	var locs []LocationT
	add := func(l indexer.Location, n int) {
		if l.Valid() {
			locs = append(locs, LocationT{pathToURI(l.File), Range{Position{l.Line, l.Col}, Position{l.Line, l.Col + n}}})
		}
	}
	if framework.IsTemplateFile(path) {
		for _, l := range framework.TemplateDefinition(path, text, s.cat, off) {
			add(l, 0)
		}
		if len(locs) > 0 {
			return locs
		}
		return nil
	}

	// 1. Check ORM / Config / Validation spans first
	fa := orm.AnalyzeFile(path, []byte(text), s.cat)
	for _, l := range fa.Definition(off, s.cat) {
		add(l, 0)
	}
	if d := framework.ViewDefinition(path, text, s.cat, off); d != nil {
		add(d.Loc, 0)
	}
	if d := framework.RouteMiddlewareDefinition(path, text, s.cat, off); d != nil {
		add(d.Loc, 0)
	}
	if word, _, _ := identAtOffset(text, off); word != "" {
		if tc := s.cat.TableConsts[word]; tc != nil {
			pos := offsetToPos(text, off)
			if tc.Loc.Valid() && !(filepath.Clean(tc.Loc.File) == filepath.Clean(path) && tc.Loc.Line == pos.Line) {
				add(tc.Loc, len(tc.Name))
			} else if t := s.cat.Table(tc.TableName); t != nil {
				add(t.Loc, len(t.Model))
				add(t.MigLoc, 0)
			}
		}
	}
	if len(locs) > 0 {
		return locs
	}

	// 2. Raw SQL definition
	_, _, _, fi, d, v, sc, ok := s.resolve(raw)
	if !ok {
		if fi != nil {
			if vr := fi.VarRefAt(off); vr != nil {
				if t := s.cat.Table(vr.Table); t != nil {
					add(t.Loc, len(t.Model))
					add(t.MigLoc, 0)
					if len(locs) > 0 {
						return locs
					}
				}
			}
		}
		return nil
	}
	ref := d.RefAt(v, sc)
	if ref == nil {
		return nil
	}
	addVirtual := func(vs, ve int) {
		if g1, ok1 := d.ToGo(vs); ok1 {
			if g2, ok2 := d.ToGo(ve); ok2 {
				locs = append(locs, LocationT{uri, Range{offsetToPos(text, g1), offsetToPos(text, g2)}})
			}
		}
	}
	if ref.ColAlias != nil && ref.VStart != ref.ColAlias.VStart {
		addVirtual(ref.ColAlias.VStart, ref.ColAlias.VEnd)
	}
	if ref.Column != nil {
		add(ref.Column.Loc, len(ref.Column.GoName))
		add(ref.Column.MigLoc, len(ref.Column.Name))
	} else if ref.Table != nil {
		if ref.TableAlias != "" {
			if vs, ve, ok := d.TableAliasDef(ref.TableAlias); ok && vs != ref.VStart {
				addVirtual(vs, ve)
			}
		}
		add(ref.Table.Loc, len(ref.Table.Model))
		add(ref.Table.MigLoc, 0)
	}
	if len(locs) == 0 {
		return nil
	}
	return locs
}

func (s *Server) diagnose(uri string) {
	text := s.docs[uri]
	diags := []map[string]any{}
	if strings.HasSuffix(uri, ".go") {
		path := uriToPath(uri)
		fi := rawsql.Extract(path, []byte(text), s.cat)
		for _, d := range rawsql.Analyze(fi, s.cat) {
			diags = append(diags, map[string]any{
				"range":    Range{offsetToPos(text, d.Start), offsetToPos(text, d.End)},
				"severity": d.Severity, "source": "goravel-sql", "message": d.Message,
			})
		}
		fa := orm.AnalyzeFile(path, []byte(text), s.cat)
		for _, d := range fa.Diagnostics(s.cat) {
			diags = append(diags, map[string]any{
				"range":    Range{offsetToPos(text, d.Start), offsetToPos(text, d.End)},
				"severity": d.Severity, "source": "goravel", "message": d.Message,
			})
		}
		var fDiags []framework.Diag
		fDiags = append(fDiags, framework.RequestDiagnostics(path, text, s.cat)...)
		fDiags = append(fDiags, framework.ControllerDiagnostics(path, text, s.cat)...)
		fDiags = append(fDiags, framework.ViewDiagnostics(path, text, s.cat)...)
		fDiags = append(fDiags, framework.DriftDiagnostics(path, text, s.cat)...)
		for _, d := range fDiags {
			diags = append(diags, map[string]any{
				"range":    Range{offsetToPos(text, d.Start), offsetToPos(text, d.End)},
				"severity": d.Severity, "source": "goravel-framework", "message": d.Message,
			})
		}
	}
	s.notify("textDocument/publishDiagnostics", map[string]any{"uri": uri, "diagnostics": diags})
}

func (s *Server) codeAction(raw json.RawMessage) any {
	var p struct {
		TextDocument struct {
			URI string `json:"uri"`
		} `json:"textDocument"`
		Range Range `json:"range"`
	}
	_ = json.Unmarshal(raw, &p)
	uri := p.TextDocument.URI
	text, ok := s.docs[uri]
	if !ok || !strings.HasSuffix(uri, ".go") {
		return nil
	}
	off := posToOffset(text, p.Range.Start)
	fi := rawsql.Extract(uriToPath(uri), []byte(text), s.cat)
	actions := []map[string]any{}
	edit := func(title, kind string, start, end int, newText string) {
		actions = append(actions, map[string]any{
			"title": title, "kind": kind,
			"edit": map[string]any{"changes": map[string]any{uri: []map[string]any{{
				"range": Range{offsetToPos(text, start), offsetToPos(text, end)}, "newText": newText,
			}}}},
		})
	}
	if st, en, repl, ok := rawsql.ExpandStar(fi, s.cat, off); ok {
		edit("Expand * to explicit columns", "refactor.rewrite", st, en, repl)
	}
	if at, code, name, ok := rawsql.GenerateRowStruct(fi, s.cat, off); ok {
		edit("Generate row struct "+name, "refactor.extract", at, at, code)
	}
	if targetFile, insertAt, newText, title, ok := rawsql.AddMissingScanFields(fi, s.cat, off); ok {
		targetURI := pathToURI(targetFile)
		targetText := text
		if filepath.Clean(targetFile) != filepath.Clean(uriToPath(uri)) {
			if b, err := os.ReadFile(targetFile); err == nil {
				targetText = string(b)
			}
		}
		actions = append(actions, map[string]any{
			"title": title, "kind": "quickfix",
			"edit": map[string]any{"changes": map[string]any{targetURI: []map[string]any{{
				"range": Range{offsetToPos(targetText, insertAt), offsetToPos(targetText, insertAt)}, "newText": newText,
			}}}},
		})
	}
	if d := fi.DocAt(off); d != nil {
		cfg := indexer.ResolveDBConfig(s.cat)
		if cfg.Database != "" && (cfg.Host != "" || cfg.Driver == "sqlite" || cfg.Driver == "sqlite3") {
			actions = append(actions,
				map[string]any{
					"title": "Goravel: Explain SQL Query against DB",
					"kind":  "quickfix",
					"command": map[string]any{
						"title":     "Goravel: Explain SQL Query against DB",
						"command":   "goravel.explainQuery",
						"arguments": []any{d.Text},
					},
				},
				map[string]any{
					"title": "Goravel: Preview Query Results (Live DB)",
					"kind":  "quickfix",
					"command": map[string]any{
						"title":     "Goravel: Preview Query Results (Live DB)",
						"command":   "goravel.previewQuery",
						"arguments": []any{d.Text},
					},
				},
			)
		}
	}
	path := uriToPath(uri)
	var fActions []framework.CodeAction
	fActions = append(fActions, framework.RequestCodeActions(path, text, s.cat, off)...)
	fActions = append(fActions, framework.ControllerCodeActions(path, text, s.cat, off)...)
	fActions = append(fActions, framework.DriftCodeActions(path, text, s.cat, off)...)
	fActions = append(fActions, framework.EnvAndLangCodeActions(path, text, s.cat, off)...)
	for _, a := range fActions {
		changesByURI := map[string][]map[string]any{}
		for _, ed := range a.Edits {
			targetURI := uri
			targetText := text
			if ed.File != "" && filepath.Clean(ed.File) != filepath.Clean(path) {
				targetURI = pathToURI(ed.File)
				if doc, ok := s.docs[targetURI]; ok {
					targetText = doc
				} else if b, err := os.ReadFile(ed.File); err == nil {
					targetText = string(b)
				}
			}
			changesByURI[targetURI] = append(changesByURI[targetURI], map[string]any{
				"range":   Range{offsetToPos(targetText, ed.Start), offsetToPos(targetText, ed.End)},
				"newText": ed.NewText,
			})
		}
		actions = append(actions, map[string]any{
			"title": a.Title,
			"kind":  a.Kind,
			"edit":  map[string]any{"changes": changesByURI},
		})
	}
	return actions
}

func (s *Server) codeLens(raw json.RawMessage) any {
	var p struct {
		TextDocument struct {
			URI string `json:"uri"`
		} `json:"textDocument"`
	}
	_ = json.Unmarshal(raw, &p)
	path := filepath.Clean(uriToPath(p.TextDocument.URI))
	var lenses []map[string]any
	for _, r := range s.cat.Routes {
		if !r.CtrlLoc.Valid() || filepath.Clean(r.CtrlLoc.File) != path {
			continue
		}
		pos := Position{r.CtrlLoc.Line, r.CtrlLoc.Col}
		lenses = append(lenses,
			map[string]any{
				"range": Range{pos, pos},
				"command": map[string]any{
					"title":     fmt.Sprintf("$(globe) %s %s", r.Method, r.Path),
					"command":   "goravel.jumpToRoute",
					"arguments": []any{pathToURI(r.Loc.File), r.Loc.Line, r.Loc.Col},
				},
			},
			map[string]any{
				"range": Range{pos, pos},
				"command": map[string]any{
					"title":   "$(play) Test Request",
					"command": "goravel.route.testRequest",
					"arguments": []any{map[string]any{
						"method":     r.Method,
						"path":       r.Path,
						"controller": r.Controller,
						"action":     r.Action,
					}},
				},
			},
		)
	}
	for _, cmd := range s.cat.Commands {
		if !cmd.Loc.Valid() || filepath.Clean(cmd.Loc.File) != path {
			continue
		}
		pos := Position{cmd.Loc.Line, cmd.Loc.Col}
		lenses = append(lenses, map[string]any{
			"range": Range{pos, pos},
			"command": map[string]any{
				"title":     fmt.Sprintf("$(terminal) Run artisan %s", cmd.Signature),
				"command":   "goravel.artisan.runCommand",
				"arguments": []any{cmd.Signature},
			},
		})
	}
	for _, ev := range s.cat.Events {
		if !ev.Loc.Valid() || filepath.Clean(ev.Loc.File) != path || len(ev.Listeners) == 0 {
			continue
		}
		pos := Position{ev.Loc.Line, ev.Loc.Col}
		var args []any
		if len(ev.ListLocs) > 0 && ev.ListLocs[0].Valid() {
			args = []any{ev.ListLocs[0].File, ev.ListLocs[0].Line, ev.ListLocs[0].Col}
		}
		lenses = append(lenses, map[string]any{
			"range": Range{pos, pos},
			"command": map[string]any{
				"title":     fmt.Sprintf("$(broadcast) %d Listener(s): %s", len(ev.Listeners), strings.Join(ev.Listeners, ", ")),
				"command":   "goravel.openLocation",
				"arguments": args,
			},
		})
	}
	for _, obs := range s.cat.Observers {
		if obs.Model == "" {
			continue
		}
		t := s.cat.Model(obs.Model)
		if t == nil || !t.Loc.Valid() || filepath.Clean(t.Loc.File) != path {
			continue
		}
		pos := Position{t.Loc.Line, t.Loc.Col}
		hookSuffix := ""
		if len(obs.Hooks) > 0 {
			hookSuffix = " (" + strings.Join(obs.Hooks, ", ") + ")"
		}
		lenses = append(lenses, map[string]any{
			"range": Range{pos, pos},
			"command": map[string]any{
				"title":     fmt.Sprintf("$(eye) Observer: %s%s", obs.Name, hookSuffix),
				"command":   "goravel.openLocation",
				"arguments": []any{obs.Loc.File, obs.Loc.Line, obs.Loc.Col},
			},
		})
	}
	if strings.Contains(filepath.ToSlash(path), "/app/models/") {
		scopesByModel := map[string][]string{}
		firstLocByModel := map[string]indexer.Location{}
		for _, sc := range s.cat.Scopes {
			if sc.Model == "" {
				continue
			}
			scopesByModel[sc.Model] = append(scopesByModel[sc.Model], sc.Name)
			if _, ok := firstLocByModel[sc.Model]; !ok {
				firstLocByModel[sc.Model] = sc.Loc
			}
		}
		for mName, scNames := range scopesByModel {
			t := s.cat.Model(mName)
			if t == nil || !t.Loc.Valid() || filepath.Clean(t.Loc.File) != path {
				continue
			}
			pos := Position{t.Loc.Line, t.Loc.Col}
			fl := firstLocByModel[mName]
			lenses = append(lenses, map[string]any{
				"range": Range{pos, pos},
				"command": map[string]any{
					"title":     fmt.Sprintf("$(filter) %d Scope(s): %s", len(scNames), strings.Join(scNames, ", ")),
					"command":   "goravel.openLocation",
					"arguments": []any{fl.File, fl.Line, fl.Col},
				},
			})
		}
	}
	if strings.HasSuffix(path, "_test.go") {
		if text, ok := s.docs[p.TextDocument.URI]; ok {
			fset := token.NewFileSet()
			if f, err := parser.ParseFile(fset, path, text, 0); err == nil && f != nil {
				dir := filepath.Dir(path)
				for _, d := range f.Decls {
					fd, ok := d.(*ast.FuncDecl)
					if !ok || fd.Name == nil || !strings.HasPrefix(fd.Name.Name, "Test") {
						continue
					}
					p0 := fset.Position(fd.Name.Pos())
					pos := Position{p0.Line - 1, p0.Column - 1}
					runName := fd.Name.Name
					if fd.Recv != nil {
						runName = "/" + runName
					}
					lenses = append(lenses, map[string]any{
						"range": Range{pos, pos},
						"command": map[string]any{
							"title":     "$(beaker) Run Goravel Test",
							"command":   "goravel.test.run",
							"arguments": []any{dir, runName},
						},
					})
				}
			}
		}
	}
	return lenses
}

func (s *Server) inlayHint(raw json.RawMessage) any {
	var p struct {
		TextDocument struct {
			URI string `json:"uri"`
		} `json:"textDocument"`
	}
	_ = json.Unmarshal(raw, &p)
	uri := p.TextDocument.URI
	text, ok := s.docs[uri]
	if !ok || !strings.HasSuffix(uri, ".go") {
		return nil
	}
	path := uriToPath(uri)
	fa := orm.AnalyzeFile(path, []byte(text), s.cat)
	var hints []map[string]any
	for _, h := range fa.InlayHints(s.cat) {
		hints = append(hints, map[string]any{
			"position":    offsetToPos(text, h.Offset),
			"label":       h.Label,
			"kind":        2,
			"paddingLeft": true,
		})
	}
	for _, h := range framework.ScheduleInlayHints(path, text) {
		hints = append(hints, map[string]any{
			"position":    offsetToPos(text, h.Offset),
			"label":       h.Label,
			"kind":        2,
			"paddingLeft": true,
		})
	}
	for _, h := range framework.RouteInlayHints(path, text, s.cat) {
		hints = append(hints, map[string]any{
			"position":    offsetToPos(text, h.Offset),
			"label":       h.Label,
			"kind":        2,
			"paddingLeft": true,
		})
	}
	return hints
}

func (s *Server) semanticTokens(raw json.RawMessage) any {
	var p struct {
		TextDocument struct {
			URI string `json:"uri"`
		} `json:"textDocument"`
	}
	_ = json.Unmarshal(raw, &p)
	uri := p.TextDocument.URI
	text, ok := s.docs[uri]
	if !ok || !strings.HasSuffix(uri, ".go") {
		return map[string]any{"data": []int{}}
	}
	path := uriToPath(uri)
	fi := rawsql.Extract(path, []byte(text), s.cat)
	fa := orm.AnalyzeFile(path, []byte(text), s.cat)

	toks := rawsql.SemanticTokens(fi, s.cat)
	seen := map[int]bool{}
	for _, t := range toks {
		seen[t.GoStart] = true
	}
	for _, t := range fa.SemanticTokens() {
		if !seen[t.GoStart] {
			toks = append(toks, t)
		}
	}
	sort.Slice(toks, func(i, j int) bool { return toks[i].GoStart < toks[j].GoStart })

	data := make([]int, 0, len(toks)*5)
	prevLine, prevChar := 0, 0
	for _, t := range toks {
		pos := offsetToPos(text, t.GoStart)
		endPos := offsetToPos(text, t.GoStart+t.Length)
		if pos.Line != endPos.Line || endPos.Character <= pos.Character {
			continue
		}
		dLine := pos.Line - prevLine
		dStart := pos.Character - prevChar
		if dLine > 0 {
			dStart = pos.Character
		}
		if dLine < 0 || dStart < 0 {
			continue
		}
		data = append(data, dLine, dStart, endPos.Character-pos.Character, t.Type, 0)
		prevLine = pos.Line
		prevChar = pos.Character
	}
	return map[string]any{"data": data}
}

func (s *Server) overview() any {
	var tables []*indexer.Table
	for _, n := range s.cat.TableNames() {
		tables = append(tables, s.cat.Table(n))
	}
	var configs []*indexer.ConfigItem
	for _, k := range s.cat.ConfigKeys() {
		configs = append(configs, s.cat.Config(k))
	}
	var rules []*indexer.RuleItem
	for _, r := range s.cat.RuleNames() {
		rules = append(rules, s.cat.Rule(r))
	}
	return map[string]any{
		"tables":       tables,
		"routes":       s.cat.Routes,
		"migrations":   s.cat.Migrations,
		"configs":      configs,
		"rules":        rules,
		"commands":     s.cat.Commands,
		"seeders":      s.cat.Seeders,
		"schedules":    s.cat.Schedules,
		"events":       s.cat.Events,
		"jobs":         s.cat.Jobs,
		"observers":    s.cat.Observers,
		"scopes":       s.cat.Scopes,
		"unregistered": s.cat.Unregistered,
	}
}

func (s *Server) registerBootstrap(raw json.RawMessage) any {
	var p struct {
		File string `json:"file"`
	}
	_ = json.Unmarshal(raw, &p)
	clean := filepath.Clean(p.File)
	unreg := s.cat.UnregisteredByFile[clean]
	if unreg == nil {
		return map[string]any{"ok": false, "error": "Component is already registered or not found"}
	}
	act, ok := framework.ResolveBootstrapRegistrationAction(s.cat, unreg)
	if !ok || len(act.Edits) == 0 {
		return map[string]any{"ok": false, "error": "Could not locate matching bootstrap registration slice"}
	}
	ed := act.Edits[0]
	b, err := os.ReadFile(ed.File)
	if err != nil {
		return map[string]any{"ok": false, "error": err.Error()}
	}
	content := string(b)
	if ed.Start < 0 || ed.End > len(content) {
		return map[string]any{"ok": false, "error": "Invalid edit offset"}
	}
	updated := content[:ed.Start] + ed.NewText + content[ed.End:]
	if err := os.WriteFile(ed.File, []byte(updated), 0644); err != nil {
		return map[string]any{"ok": false, "error": err.Error()}
	}
	s.reindex()
	for uri := range s.docs {
		s.diagnose(uri)
	}
	return map[string]any{
		"ok":         true,
		"title":      act.Title,
		"targetFile": ed.File,
	}
}

func (s *Server) erDiagram() any {
	var b strings.Builder
	b.WriteString("erDiagram\n")
	names := s.cat.TableNames()
	seenRel := map[string]bool{}
	for _, n := range names {
		t := s.cat.Table(n)
		if t == nil {
			continue
		}
		fmt.Fprintf(&b, "  %s {\n", sanitizeMermaidIdent(t.Name))
		for _, c := range t.Columns {
			typ := sanitizeMermaidIdent(colDetail(t, c))
			if typ == "" {
				typ = "string"
			}
			pkFk := ""
			if strings.EqualFold(c.Name, "id") {
				pkFk = " PK"
			} else if c.ForeignKey != "" {
				pkFk = " FK"
			}
			fmt.Fprintf(&b, "    %s %s%s\n", typ, sanitizeMermaidIdent(c.Name), pkFk)
		}
		b.WriteString("  }\n")
		for _, r := range t.Relations {
			if r.TargetTable == "" {
				continue
			}
			relKey := t.Name + "->" + r.TargetTable + ":" + r.ForeignKey
			if seenRel[relKey] {
				continue
			}
			seenRel[relKey] = true
			lbl := r.ForeignKey
			if lbl == "" {
				lbl = r.Name
			}
			fmt.Fprintf(&b, "  %s }o--|| %s : %q\n",
				sanitizeMermaidIdent(t.Name),
				sanitizeMermaidIdent(r.TargetTable),
				lbl,
			)
		}
	}
	return map[string]any{
		"mermaid": b.String(),
		"tables":  len(names),
	}
}

func sanitizeMermaidIdent(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		ch := s[i]
		if (ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') || (ch >= '0' && ch <= '9') || ch == '_' {
			b.WriteByte(ch)
		} else {
			b.WriteByte('_')
		}
	}
	res := strings.Trim(b.String(), "_")
	if res == "" {
		return "unknown"
	}
	return res
}

func (s *Server) explainQuery(raw json.RawMessage) any {
	var p struct {
		SQL string `json:"sql"`
	}
	_ = json.Unmarshal(raw, &p)
	out, err := indexer.ExplainQuery(s.cat, p.SQL)
	if err != nil {
		return map[string]any{"ok": false, "output": out, "error": err.Error()}
	}
	return map[string]any{"ok": true, "output": out}
}

func (s *Server) previewQuery(raw json.RawMessage) any {
	var p struct {
		SQL string `json:"sql"`
	}
	_ = json.Unmarshal(raw, &p)
	out, err := indexer.PreviewQuery(s.cat, p.SQL)
	if err != nil {
		return map[string]any{"ok": false, "output": out, "error": err.Error()}
	}
	return map[string]any{"ok": true, "output": out}
}

func (s *Server) routeSample(raw json.RawMessage) any {
	var p struct {
		Controller string `json:"controller"`
		Action     string `json:"action"`
	}
	_ = json.Unmarshal(raw, &p)
	return map[string]any{
		"body": framework.SampleBodyForRoute(s.cat, p.Controller, p.Action),
	}
}

// ---- position helpers (LSP uses UTF-16 columns) ----

func posToOffset(text string, p Position) int {
	line, i := 0, 0
	for line < p.Line {
		j := strings.IndexByte(text[i:], '\n')
		if j < 0 {
			return len(text)
		}
		i += j + 1
		line++
	}
	units := 0
	for i < len(text) && text[i] != '\n' && units < p.Character {
		r, n := utf8.DecodeRuneInString(text[i:])
		units += len(utf16.Encode([]rune{r}))
		i += n
	}
	return i
}

func offsetToPos(text string, off int) Position {
	if off > len(text) {
		off = len(text)
	}
	line := strings.Count(text[:off], "\n")
	start := strings.LastIndexByte(text[:off], '\n') + 1
	units := 0
	for _, r := range text[start:off] {
		units += len(utf16.Encode([]rune{r}))
	}
	return Position{line, units}
}

func uriToPath(uri string) string {
	u, err := url.Parse(uri)
	if err != nil || u.Scheme != "file" {
		return uri
	}
	return filepath.FromSlash(u.Path)
}

func pathToURI(p string) string {
	return (&url.URL{Scheme: "file", Path: filepath.ToSlash(p)}).String()
}

func identAtOffset(text string, off int) (string, int, int) {
	if off < 0 || off > len(text) {
		return "", 0, 0
	}
	isIdent := func(b byte) bool {
		return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9') || b == '_'
	}
	start := off
	for start > 0 && isIdent(text[start-1]) {
		start--
	}
	end := off
	for end < len(text) && isIdent(text[end]) {
		end++
	}
	if start == end {
		return "", 0, 0
	}
	return text[start:end], start, end
}

func extractLiveIntrospectionSetting(settings map[string]any) (bool, bool) {
	if settings == nil {
		return false, false
	}
	if g, ok := settings["goravel"].(map[string]any); ok {
		settings = g
	}
	if sqlCfg, ok := settings["sql"].(map[string]any); ok {
		if v, ok := sqlCfg["liveIntrospection"].(bool); ok {
			return v, true
		}
	}
	if v, ok := settings["liveIntrospection"].(bool); ok {
		return v, true
	}
	return false, false
}
