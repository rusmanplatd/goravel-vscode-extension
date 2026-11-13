package framework

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/rusmanplatd/goravel-lsp/internal/indexer"
)

func isRequestMethod(name string) bool {
	return name == "Rules" || name == "Messages" || name == "Attributes"
}

func receiverTypeName(recv *ast.FieldList) string {
	if recv == nil || len(recv.List) == 0 {
		return ""
	}
	e := recv.List[0].Type
	if se, ok := e.(*ast.StarExpr); ok {
		e = se.X
	}
	if id, ok := e.(*ast.Ident); ok {
		return id.Name
	}
	return ""
}

func fieldTagKey(f indexer.StructField) string {
	if len(f.Alt) > 0 && f.Alt[0] != "" {
		return f.Alt[0]
	}
	if f.Col != "" {
		return f.Col
	}
	return indexer.Snake(f.GoName)
}

func baseRuleFieldKey(method, key string) string {
	if method == "Messages" {
		// Messages keys look like "name.required" or "items.*.name.required"
		if i := strings.IndexByte(key, '.'); i > 0 {
			return key[:i]
		}
		return key
	}
	// Rules/Attributes keys can be "items.*" or "user.email"
	if i := strings.IndexByte(key, '.'); i > 0 {
		return key[:i]
	}
	return key
}

// RequestDiagnostics checks Rules/Messages/Attributes keys against the receiver struct fields.
func RequestDiagnostics(path, src string, cat *indexer.Catalog) []Diag {
	if !strings.Contains(filepath.ToSlash(path), "/requests/") {
		return nil
	}
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, src, 0)
	if err != nil || f == nil {
		return nil
	}
	var out []Diag
	for _, d := range f.Decls {
		fd, ok := d.(*ast.FuncDecl)
		if !ok || fd.Recv == nil || fd.Body == nil || !isRequestMethod(fd.Name.Name) {
			continue
		}
		recv := receiverTypeName(fd.Recv)
		st := cat.Struct(recv)
		if st == nil || len(st.Fields) == 0 {
			continue
		}
		ast.Inspect(fd.Body, func(n ast.Node) bool {
			kv, ok := n.(*ast.KeyValueExpr)
			if !ok {
				return true
			}
			bl, ok := kv.Key.(*ast.BasicLit)
			if !ok || bl.Kind != token.STRING {
				return true
			}
			key, err := strconv.Unquote(bl.Value)
			if err != nil || key == "" {
				return true
			}
			base := baseRuleFieldKey(fd.Name.Name, key)
			if st.Has(base) == nil {
				s := fset.Position(bl.Pos()).Offset
				e := fset.Position(bl.End()).Offset
				out = append(out, Diag{
					Start:    s,
					End:      e,
					Severity: 2,
					Message:  fmt.Sprintf("Key '%s' does not match any field on %s", base, recv),
				})
			}
			return true
		})
	}
	return out
}

// RequestCompletions suggests struct field keys inside Rules/Messages/Attributes map keys.
func RequestCompletions(path, src string, cat *indexer.Catalog, goOff int) []CompItem {
	if !strings.Contains(filepath.ToSlash(path), "/requests/") {
		return nil
	}
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, src, 0)
	if err != nil || f == nil {
		return nil
	}
	for _, d := range f.Decls {
		fd, ok := d.(*ast.FuncDecl)
		if !ok || fd.Recv == nil || fd.Body == nil || !isRequestMethod(fd.Name.Name) {
			continue
		}
		bStart := fset.Position(fd.Body.Pos()).Offset
		bEnd := fset.Position(fd.Body.End()).Offset
		if goOff < bStart || goOff > bEnd {
			continue
		}
		recv := receiverTypeName(fd.Recv)
		st := cat.Struct(recv)
		if st == nil {
			continue
		}
		inKey := false
		ast.Inspect(fd.Body, func(n ast.Node) bool {
			kv, ok := n.(*ast.KeyValueExpr)
			if !ok {
				return true
			}
			ks := fset.Position(kv.Key.Pos()).Offset
			ke := fset.Position(kv.Key.End()).Offset
			if goOff >= ks && goOff <= ke {
				inKey = true
				return false
			}
			return true
		})
		if !inKey {
			continue
		}
		var out []CompItem
		for _, fld := range st.Fields {
			k := fieldTagKey(fld)
			out = append(out, CompItem{
				Label:  k,
				Detail: fmt.Sprintf("%s.%s (%s)", recv, fld.GoName, fld.GoType),
				Kind:   5,
			})
		}
		return out
	}
	return nil
}

// RequestCodeActions offers generating missing Rules(ctx) entries from the request struct.
func RequestCodeActions(path, src string, cat *indexer.Catalog, goOff int) []CodeAction {
	if !strings.Contains(filepath.ToSlash(path), "/requests/") {
		return nil
	}
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, src, 0)
	if err != nil || f == nil {
		return nil
	}
	for _, d := range f.Decls {
		fd, ok := d.(*ast.FuncDecl)
		if !ok || fd.Recv == nil || fd.Body == nil || fd.Name.Name != "Rules" {
			continue
		}
		bStart := fset.Position(fd.Body.Pos()).Offset
		bEnd := fset.Position(fd.Body.End()).Offset
		if goOff < bStart || goOff > bEnd {
			continue
		}
		recv := receiverTypeName(fd.Recv)
		st := cat.Struct(recv)
		if st == nil || len(st.Fields) == 0 {
			continue
		}
		var targetMap *ast.CompositeLit
		existing := map[string]bool{}
		ast.Inspect(fd.Body, func(n ast.Node) bool {
			cl, ok := n.(*ast.CompositeLit)
			if !ok {
				return true
			}
			if _, ok := cl.Type.(*ast.MapType); ok {
				targetMap = cl
				for _, elt := range cl.Elts {
					if kv, ok := elt.(*ast.KeyValueExpr); ok {
						if bl, ok := kv.Key.(*ast.BasicLit); ok && bl.Kind == token.STRING {
							if s, err := strconv.Unquote(bl.Value); err == nil {
								existing[baseRuleFieldKey("Rules", s)] = true
							}
						}
					}
				}
				return false
			}
			return true
		})
		if targetMap == nil {
			continue
		}
		var missing []string
		for _, fld := range st.Fields {
			k := fieldTagKey(fld)
			if existing[k] {
				continue
			}
			rule := inferRuleForField(fld)
			missing = append(missing, fmt.Sprintf("\t\t%q: %q,", k, rule))
		}
		if len(missing) == 0 {
			continue
		}
		rbrace := fset.Position(targetMap.Rbrace).Offset
		insert := strings.Join(missing, "\n") + "\n\t"
		if len(targetMap.Elts) == 0 {
			insert = "\n" + insert
		}
		return []CodeAction{{
			Title: fmt.Sprintf("Generate Rules(ctx) entries from %s fields", recv),
			Kind:  "quickfix",
			Edits: []TextEdit{{Start: rbrace, End: rbrace, NewText: insert}},
		}}
	}
	return nil
}

func inferRuleForField(f indexer.StructField) string {
	var parts []string
	if !strings.HasPrefix(f.GoType, "*") && !strings.HasPrefix(f.GoType, "[]") {
		parts = append(parts, "required")
	} else {
		parts = append(parts, "nullable")
	}
	switch strings.TrimPrefix(f.GoType, "*") {
	case "string":
		parts = append(parts, "string")
	case "int", "int32", "int64", "uint", "uint32", "uint64":
		parts = append(parts, "integer")
	case "float32", "float64":
		parts = append(parts, "numeric")
	case "bool":
		parts = append(parts, "boolean")
	case "uuid.UUID":
		parts = append(parts, "uuid")
	}
	return strings.Join(parts, "|")
}

