package framework

import (
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/rusmanplatd/goravel-lsp/internal/indexer"
)

var (
	swagRouterRe = regexp.MustCompile(`@Router\s+(\S+)\s+\[([a-zA-Z]+)\]`)
	swagTypeRe   = regexp.MustCompile(`(?:@Param|@Success|@Failure)\s+[^\n]*?\b(?:requests|models|responses)\.(\w+)`)
	reqRefRe     = regexp.MustCompile(`\brequests\.(\w+)`)
)

func isControllerFile(path string) bool {
	return strings.Contains(filepath.ToSlash(path), "/app/http/controllers/")
}

func isRouteFile(path string) bool {
	return strings.Contains(filepath.ToSlash(path), "/routes/")
}

// ControllerDiagnostics checks route params and Swagger annotations in controller methods.
func ControllerDiagnostics(path, src string, cat *indexer.Catalog) []Diag {
	if !isControllerFile(path) {
		return nil
	}
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, src, parser.ParseComments)
	if err != nil || f == nil {
		return nil
	}
	var out []Diag
	for _, d := range f.Decls {
		fd, ok := d.(*ast.FuncDecl)
		if !ok || fd.Recv == nil || fd.Name == nil {
			continue
		}
		ctrl := receiverTypeName(fd.Recv)
		if ctrl == "" {
			continue
		}
		routes := cat.RoutesForHandler(ctrl, fd.Name.Name)

		// 1. Route parameter check: ctx.Request().Route("id")
		if fd.Body != nil && len(routes) > 0 {
			validParams := map[string]bool{}
			var paramList []string
			for _, r := range routes {
				for _, p := range r.Params {
					if !validParams[p] {
						validParams[p] = true
						paramList = append(paramList, p)
					}
				}
			}
			ast.Inspect(fd.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok || len(call.Args) == 0 {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				switch sel.Sel.Name {
				case "Route", "RouteInt", "RouteInt64":
					bl, ok := call.Args[0].(*ast.BasicLit)
					if !ok || bl.Kind != token.STRING {
						return true
					}
					pName, err := strconv.Unquote(bl.Value)
					if err != nil || pName == "" {
						return true
					}
					if !validParams[pName] {
						s := fset.Position(bl.Pos()).Offset
						e := fset.Position(bl.End()).Offset
						hint := "route has no path parameters"
						if len(paramList) > 0 {
							hint = "route defines: " + strings.Join(paramList, ", ")
						}
						out = append(out, Diag{
							Start:    s,
							End:      e,
							Severity: 2,
							Message:  fmt.Sprintf("Route parameter '%s' not found on %s.%s (%s)", pName, ctrl, fd.Name.Name, hint),
						})
					}
				}
				return true
			})
		}

		// 2. Swagger @Router / @Param / @Success sync check
		if fd.Doc != nil {
			for _, cmt := range fd.Doc.List {
				cStart := fset.Position(cmt.Pos()).Offset
				cEnd := fset.Position(cmt.End()).Offset
				if len(routes) > 0 {
					if m := swagRouterRe.FindStringSubmatch(cmt.Text); m != nil {
						swPath, swMethod := m[1], strings.ToUpper(m[2])
						matched := false
						for _, r := range routes {
							if normalizeSwagPath(r.Path) == normalizeSwagPath(swPath) && (r.Method == "ANY" || r.Method == swMethod) {
								matched = true
								break
							}
						}
						if !matched {
							out = append(out, Diag{
								Start:    cStart,
								End:      cEnd,
								Severity: 2,
								Message:  fmt.Sprintf("Swagger @Router [%s] %s does not match registered route (%s %s)", swMethod, swPath, routes[0].Method, routes[0].Path),
							})
						}
					}
				}
				if len(cat.Structs) > 0 {
					for _, tm := range swagTypeRe.FindAllStringSubmatch(cmt.Text, -1) {
						typeName := tm[1]
						if cat.Struct(typeName) == nil && !strings.HasSuffix(typeName, "Response") {
							out = append(out, Diag{
								Start:    cStart,
								End:      cEnd,
								Severity: 2,
								Message:  fmt.Sprintf("Swagger type '%s' not found in indexed models or requests", typeName),
							})
						}
					}
				}
			}
		}
	}
	return out
}

func normalizeSwagPath(p string) string {
	p = strings.TrimSpace(p)
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	return strings.TrimRight(p, "/")
}

// ControllerCodeActions offers generating Swagger annotations from a RouteItem.
func ControllerCodeActions(path, src string, cat *indexer.Catalog, goOff int) []CodeAction {
	if !isControllerFile(path) {
		return nil
	}
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, src, parser.ParseComments)
	if err != nil || f == nil {
		return nil
	}
	for _, d := range f.Decls {
		fd, ok := d.(*ast.FuncDecl)
		if !ok || fd.Recv == nil || fd.Name == nil {
			continue
		}
		fStart := fset.Position(fd.Pos()).Offset
		fEnd := fset.Position(fd.End()).Offset
		docStart := fStart
		if fd.Doc != nil {
			docStart = fset.Position(fd.Doc.Pos()).Offset
		}
		if goOff < docStart || goOff > fEnd {
			continue
		}
		ctrl := receiverTypeName(fd.Recv)
		routes := cat.RoutesForHandler(ctrl, fd.Name.Name)
		if len(routes) == 0 {
			continue
		}
		r := routes[0]
		if fd.Doc != nil && strings.Contains(fd.Doc.Text(), "@Router") {
			for _, cmt := range fd.Doc.List {
				if m := swagRouterRe.FindStringSubmatchIndex(cmt.Text); m != nil {
					swPath, swMethod := cmt.Text[m[2]:m[3]], strings.ToUpper(cmt.Text[m[4]:m[5]])
					matched := false
					for _, rt := range routes {
						if normalizeSwagPath(rt.Path) == normalizeSwagPath(swPath) && (rt.Method == "ANY" || rt.Method == swMethod) {
							matched = true
							break
						}
					}
					if !matched {
						cOff := fset.Position(cmt.Pos()).Offset
						newRouter := fmt.Sprintf("@Router %s [%s]", r.Path, strings.ToLower(r.Method))
						return []CodeAction{{
							Title: fmt.Sprintf("Update Swagger @Router to %s [%s]", r.Path, strings.ToLower(r.Method)),
							Kind:  "quickfix",
							Edits: []TextEdit{{
								Start:   cOff + m[0],
								End:     cOff + m[1],
								NewText: newRouter,
							}},
						}}
					}
				}
			}
			continue
		}
		var b strings.Builder
		fmt.Fprintf(&b, "// %s godoc\n", fd.Name.Name)
		fmt.Fprintf(&b, "// @Summary %s\n", fd.Name.Name)
		for _, p := range r.Params {
			fmt.Fprintf(&b, "// @Param %s path string true %q\n", p, p)
		}
		if reqName := findRequestStructInFunc(src, fset, fd); reqName != "" {
			fmt.Fprintf(&b, "// @Param request body requests.%s true \"Request payload\"\n", reqName)
		}
		fmt.Fprintf(&b, "// @Success 200 {object} map[string]any\n")
		fmt.Fprintf(&b, "// @Router %s [%s]\n", r.Path, strings.ToLower(r.Method))
		return []CodeAction{{
			Title: fmt.Sprintf("Generate Swagger comments for %s (%s %s)", fd.Name.Name, r.Method, r.Path),
			Kind:  "refactor",
			Edits: []TextEdit{{Start: fStart, End: fStart, NewText: b.String()}},
		}}
	}
	return nil
}

func findRequestStructInFunc(src string, fset *token.FileSet, fd *ast.FuncDecl) string {
	if fd.Body == nil {
		return ""
	}
	s := fset.Position(fd.Body.Pos()).Offset
	e := fset.Position(fd.Body.End()).Offset
	if s < 0 || e > len(src) || s >= e {
		return ""
	}
	if m := reqRefRe.FindStringSubmatch(src[s:e]); m != nil {
		return m[1]
	}
	return ""
}

// RouteAndControllerHover returns rich hover info for controller methods and route middleware.
func RouteAndControllerHover(path, src string, cat *indexer.Catalog, goOff int) *HoverResult {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, src, 0)
	if err != nil || f == nil {
		return nil
	}
	if isControllerFile(path) {
		for _, d := range f.Decls {
			fd, ok := d.(*ast.FuncDecl)
			if !ok || fd.Recv == nil || fd.Name == nil {
				continue
			}
			ns := fset.Position(fd.Name.Pos()).Offset
			ne := fset.Position(fd.Name.End()).Offset
			if goOff < ns || goOff > ne {
				continue
			}
			ctrl := receiverTypeName(fd.Recv)
			routes := cat.RoutesForHandler(ctrl, fd.Name.Name)
			if len(routes) == 0 {
				return nil
			}
			var lines []string
			for _, r := range routes {
				line := fmt.Sprintf("**`%s %s`** *(module: `%s`)*", r.Method, r.Path, r.Module)
				if len(r.Middlewares) > 0 {
					line += "\n- Middlewares: `" + strings.Join(r.Middlewares, "`, `") + "`"
				}
				if len(r.Params) > 0 {
					line += "\n- Path params: `" + strings.Join(r.Params, "`, `") + "`"
				}
				lines = append(lines, line)
			}
			return &HoverResult{Markdown: strings.Join(lines, "\n\n")}
		}
	}
	if isRouteFile(path) {
		word := wordAtOffset(src, goOff)
		if word != "" {
			if loc, ok := cat.Middleware[word]; ok {
				return &HoverResult{Markdown: fmt.Sprintf("**Middleware `%s`**\n\nDefined at `%s:%d`", word, filepath.Base(loc.File), loc.Line+1)}
			}
		}
		clean := filepath.Clean(path)
		line := strings.Count(src[:goOff], "\n")
		for _, r := range cat.Routes {
			if filepath.Clean(r.Loc.File) == clean && r.Loc.Line == line {
				md := fmt.Sprintf("**Route `%s %s`**\n\n- Handler: `%s`", r.Method, r.Path, r.Handler)
				if len(r.Middlewares) > 0 {
					md += "\n- Middlewares: `" + strings.Join(r.Middlewares, "`, `") + "`"
				}
				if r.CtrlLoc.Valid() {
					md += fmt.Sprintf("\n- Controller: `%s:%d`", filepath.Base(r.CtrlLoc.File), r.CtrlLoc.Line+1)
				}
				return &HoverResult{Markdown: md}
			}
		}
	}
	return nil
}

// RouteInlayHints displays resolved full route paths and middleware chains inline in routes/*.go.
func RouteInlayHints(path, src string, cat *indexer.Catalog) []InlayHint {
	if !isRouteFile(path) || cat == nil {
		return nil
	}
	clean := filepath.Clean(path)
	lines := strings.Split(src, "\n")
	lineStartOff := make([]int, len(lines))
	off := 0
	for i, l := range lines {
		lineStartOff[i] = off
		off += len(l) + 1
	}
	var out []InlayHint
	seenLine := map[int]bool{}
	for _, r := range cat.Routes {
		if filepath.Clean(r.Loc.File) != clean || r.Loc.Line < 0 || r.Loc.Line >= len(lines) {
			continue
		}
		if seenLine[r.Loc.Line] {
			continue
		}
		seenLine[r.Loc.Line] = true
		lineText := strings.TrimRight(lines[r.Loc.Line], "\r")
		endOff := lineStartOff[r.Loc.Line] + len(lineText)
		mwSuffix := ""
		if len(r.Middlewares) > 0 {
			mwSuffix = " [" + strings.Join(r.Middlewares, ", ") + "]"
		}
		out = append(out, InlayHint{
			Offset: endOff,
			Label:  fmt.Sprintf(" → %s %s%s", r.Method, r.Path, mwSuffix),
		})
	}
	return out
}

var (
	envCallRe  = regexp.MustCompile(`\bEnv\(\s*"([^"]+)"`)
	langCallRe = regexp.MustCompile(`\bLang\([^)]*\)\.Get\(\s*"([^"]+)"`)
)

// EnvAndLangCodeActions offers adding missing .env variables to .env.example and missing translation keys to lang/*.json.
func EnvAndLangCodeActions(path, src string, cat *indexer.Catalog, goOff int) []CodeAction {
	if cat == nil || cat.Root == "" {
		return nil
	}
	var actions []CodeAction
	for _, m := range envCallRe.FindAllStringSubmatchIndex(src, -1) {
		if goOff < m[0] || goOff > m[1] {
			continue
		}
		key := src[m[2]:m[3]]
		if _, ok := cat.EnvVars[key]; !ok && key != "" {
			envFile := filepath.Join(cat.Root, ".env.example")
			if _, err := os.Stat(envFile); err != nil {
				envFile = filepath.Join(cat.Root, ".env")
			}
			if b, err := os.ReadFile(envFile); err == nil {
				endPos := len(b)
				prefix := ""
				if endPos > 0 && b[endPos-1] != '\n' {
					prefix = "\n"
				}
				actions = append(actions, CodeAction{
					Title: fmt.Sprintf("Add '%s=' to %s", key, filepath.Base(envFile)),
					Kind:  "quickfix",
					Edits: []TextEdit{{
						File:    envFile,
						Start:   endPos,
						End:     endPos,
						NewText: fmt.Sprintf("%s%s=\n", prefix, key),
					}},
				})
			}
		}
	}
	for _, m := range langCallRe.FindAllStringSubmatchIndex(src, -1) {
		if goOff < m[0] || goOff > m[1] {
			continue
		}
		key := src[m[2]:m[3]]
		if _, ok := cat.Translations[key]; !ok && key != "" {
			langFile := filepath.Join(cat.Root, "lang", "en.json")
			if b, err := os.ReadFile(langFile); err == nil {
				content := string(b)
				if rbrace := strings.LastIndexByte(content, '}'); rbrace > 0 {
					trimmed := strings.TrimRight(content[:rbrace], " \t\r\n")
					comma := ","
					if strings.HasSuffix(trimmed, "{") {
						comma = ""
					}
					actions = append(actions, CodeAction{
						Title: fmt.Sprintf("Add translation key %q to lang/en.json", key),
						Kind:  "quickfix",
						Edits: []TextEdit{{
							File:    langFile,
							Start:   len(trimmed),
							End:     rbrace,
							NewText: fmt.Sprintf("%s\n  %q: %q\n", comma, key, key),
						}},
					})
				}
			}
		}
	}
	return actions
}

// RouteMiddlewareDefinition jumps from a middleware name in routes/*.go to app/http/middleware/*.go.
func RouteMiddlewareDefinition(path, src string, cat *indexer.Catalog, goOff int) *DefResult {
	if !isRouteFile(path) {
		return nil
	}
	word := wordAtOffset(src, goOff)
	if word == "" {
		return nil
	}
	if loc, ok := cat.Middleware[word]; ok && loc.Valid() {
		return &DefResult{Loc: loc}
	}
	return nil
}

// SampleBodyForRoute inspects the controller method for a bound requests.* struct and returns a sample JSON body.
func SampleBodyForRoute(cat *indexer.Catalog, controller, action string) string {
	if controller == "" || action == "" {
		return ""
	}
	m, ok := cat.Controllers[controller]
	if !ok {
		return ""
	}
	loc, ok := m[action]
	if !ok || !loc.Valid() {
		return ""
	}
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, loc.File, nil, 0)
	if err != nil || f == nil {
		return ""
	}
	reqName := ""
	for _, d := range f.Decls {
		fd, ok := d.(*ast.FuncDecl)
		if !ok || fd.Name == nil || fd.Name.Name != action || fd.Body == nil {
			continue
		}
		ast.Inspect(fd.Body, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			if id, ok := sel.X.(*ast.Ident); ok && id.Name == "requests" {
				reqName = sel.Sel.Name
				return false
			}
			return true
		})
		if reqName != "" {
			break
		}
	}
	if reqName == "" {
		return ""
	}
	st := cat.Struct(reqName)
	if st == nil || len(st.Fields) == 0 {
		return ""
	}
	sample := map[string]any{}
	for _, fld := range st.Fields {
		k := fieldTagKey(fld)
		sample[k] = sampleValueForType(fld.GoType)
	}
	b, err := json.MarshalIndent(sample, "", "  ")
	if err != nil {
		return ""
	}
	return string(b)
}

func sampleValueForType(goType string) any {
	t := strings.TrimPrefix(goType, "*")
	if strings.HasPrefix(t, "[]") {
		return []any{}
	}
	switch t {
	case "int", "int32", "int64", "uint", "uint32", "uint64":
		return 0
	case "float32", "float64":
		return 0.0
	case "bool":
		return false
	case "uuid.UUID":
		return "00000000-0000-0000-0000-000000000000"
	default:
		return ""
	}
}

func wordAtOffset(s string, off int) string {
	if off < 0 || off >= len(s) {
		return ""
	}
	l, r := off, off
	for l > 0 && isIdentChar(s[l-1]) {
		l--
	}
	for r < len(s) && isIdentChar(s[r]) {
		r++
	}
	return s[l:r]
}

func isIdentChar(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9') || b == '_'
}

