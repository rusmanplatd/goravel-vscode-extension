// Package lsp implements a minimal stdio JSON-RPC Language Server.
package lsp

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/rusmanplatd/goravel-lsp/internal/indexer"
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
		}
		_ = json.Unmarshal(m.Params, &p)
		uri := p.RootURI
		if uri == "" && len(p.Folders) > 0 {
			uri = p.Folders[0].URI
		}
		s.root = uriToPath(uri)
		s.reindex()
		s.reply(m.ID, map[string]any{
			"capabilities": map[string]any{
				"textDocumentSync":   1,
				"completionProvider": map[string]any{"triggerCharacters": []string{".", " "}},
				"hoverProvider":      true,
				"definitionProvider": true,
			},
			"serverInfo": map[string]any{"name": "goravel-lsp", "version": "0.1.0"},
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
		if strings.Contains(path, "/app/models/") || strings.Contains(path, "/database/migrations") {
			s.reindex()
			for uri := range s.docs {
				s.diagnose(uri)
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
	case "textDocument/definition":
		s.reply(m.ID, s.definition(m.Params))
	default:
		if len(m.ID) > 0 && m.Method != "" {
			s.send(map[string]any{"jsonrpc": "2.0", "id": m.ID, "error": &rpcError{-32601, "method not found: " + m.Method}})
		}
	}
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

// resolve finds file, doc, scope and virtual offset for a request.
func (s *Server) resolve(raw json.RawMessage) (text string, p posParams, off int, fi *rawsql.FileInfo, d *rawsql.Doc, v int, sc rawsql.Scope, ok bool) {
	_ = json.Unmarshal(raw, &p)
	text, found := s.docs[p.TextDocument.URI]
	if !found || !strings.HasSuffix(p.TextDocument.URI, ".go") {
		return
	}
	off = posToOffset(text, p.Position)
	fi = rawsql.Extract(uriToPath(p.TextDocument.URI), []byte(text), s.cat)
	d = fi.DocAt(off)
	if d == nil {
		return
	}
	var vok bool
	v, vok = d.ToVirtual(off)
	if !vok {
		return
	}
	sc = fi.BuildScope(d, s.cat)
	ok = true
	return
}

func (s *Server) completion(raw json.RawMessage) any {
	_, _, _, _, d, v, sc, ok := s.resolve(raw)
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
	case cc.WantTable:
		names := s.cat.TableNames()
		sort.Strings(names)
		for _, n := range names {
			t := s.cat.Table(n)
			items = append(items, map[string]any{
				"label": n, "kind": 7, "detail": modelDetail(t),
				"documentation": map[string]any{"kind": "markdown", "value": tableMD(t)},
			})
		}
	default:
		for _, t := range sc.Tables() {
			for _, c := range t.Columns {
				items = append(items, colItem(t, c))
			}
		}
		for _, kw := range keywords {
			items = append(items, map[string]any{"label": kw, "kind": 14})
		}
	}
	return map[string]any{"isIncomplete": false, "items": items}
}

var keywords = strings.Fields("SELECT FROM WHERE JOIN LEFT INNER ON AND OR ORDER GROUP BY LIMIT OFFSET INSERT INTO VALUES UPDATE SET DELETE RETURNING ILIKE LIKE IN IS NULL NOT COUNT COALESCE NOW")

func colItem(t *indexer.Table, c *indexer.Column) map[string]any {
	return map[string]any{
		"label": c.Name, "kind": 5, "detail": colDetail(t, c),
		"documentation": map[string]any{"kind": "markdown", "value": colMD(t, c)},
	}
}

func modelDetail(t *indexer.Table) string {
	if t.Model != "" {
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
	if c.Doc != "" {
		fmt.Fprintf(&b, "\n%s\n", c.Doc)
	}
	return b.String()
}

func tableMD(t *indexer.Table) string {
	var b strings.Builder
	fmt.Fprintf(&b, "**%s**", t.Name)
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
	text, _, _, _, d, v, sc, ok := s.resolve(raw)
	if !ok {
		return nil
	}
	ref := d.RefAt(v, sc)
	if ref == nil {
		return nil
	}
	md := tableMD(ref.Table)
	if ref.Column != nil {
		md = colMD(ref.Table, ref.Column)
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
	_, _, _, _, d, v, sc, ok := s.resolve(raw)
	if !ok {
		return nil
	}
	ref := d.RefAt(v, sc)
	if ref == nil {
		return nil
	}
	var locs []LocationT
	add := func(l indexer.Location, n int) {
		if l.Valid() {
			locs = append(locs, LocationT{pathToURI(l.File), Range{Position{l.Line, l.Col}, Position{l.Line, l.Col + n}}})
		}
	}
	if ref.Column != nil {
		add(ref.Column.Loc, len(ref.Column.GoName))
		add(ref.Column.MigLoc, len(ref.Column.Name))
	} else {
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
		fi := rawsql.Extract(uriToPath(uri), []byte(text), s.cat)
		for _, d := range fi.Docs {
			sc := fi.BuildScope(d, s.cat)
			for _, is := range d.Check(sc) {
				g1, ok1 := d.ToGo(is.VStart)
				g2, ok2 := d.ToGo(is.VEnd)
				if !ok1 || !ok2 {
					continue
				}
				diags = append(diags, map[string]any{
					"range":    Range{offsetToPos(text, g1), offsetToPos(text, g2)},
					"severity": 1, "source": "goravel-sql", "message": is.Message,
				})
			}
		}
	}
	s.notify("textDocument/publishDiagnostics", map[string]any{"uri": uri, "diagnostics": diags})
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

