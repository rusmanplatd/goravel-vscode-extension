package framework

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"

	"github.com/rusmanplatd/goravel-lsp/internal/indexer"
)

// DriftDiagnostics reports Model <-> Migration drift and unregistered bootstrap components.
func DriftDiagnostics(path, src string, cat *indexer.Catalog) []Diag {
	var out []Diag
	clean := filepath.Clean(path)

	// 1. Unregistered bootstrap component check
	if unreg, ok := cat.UnregisteredByFile[clean]; ok {
		off := lineColToOffset(src, unreg.Loc.Line, unreg.Loc.Col)
		end := off + 10
		if end > len(src) {
			end = len(src)
		}
		out = append(out, Diag{
			Start:    off,
			End:      end,
			Severity: 3, // Info
			Message:  fmt.Sprintf("%s '%s' does not appear to be registered in bootstrap/", titleWord(unreg.Kind), unreg.Name),
		})
	}

	// 2. Model <-> Migration drift check
	if !strings.Contains(filepath.ToSlash(clean), "/app/models/") {
		return out
	}
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, src, 0)
	if err != nil || f == nil {
		return out
	}
	for _, d := range f.Decls {
		gd, ok := d.(*ast.GenDecl)
		if !ok {
			continue
		}
		for _, sp := range gd.Specs {
			ts, ok := sp.(*ast.TypeSpec)
			if !ok || ts.Name == nil {
				continue
			}
			t := cat.Model(ts.Name.Name)
			if t == nil || !t.MigLoc.Valid() {
				continue
			}
			var missingInModel []string
			for _, col := range t.Columns {
				if col.MigLoc.Valid() && !col.Loc.Valid() {
					missingInModel = append(missingInModel, col.Name)
				} else if col.Loc.Valid() && !col.MigLoc.Valid() && !isStandardTimestampOrID(col.Name) {
					off := lineColToOffset(src, col.Loc.Line, col.Loc.Col)
					end := off + len(col.GoName)
					if end > len(src) {
						end = len(src)
					}
					out = append(out, Diag{
						Start:    off,
						End:      end,
						Severity: 3,
						Message:  fmt.Sprintf("Model field '%s' (column '%s') was not found in migration for '%s'", col.GoName, col.Name, t.Name),
					})
				}
			}
			if len(missingInModel) > 0 {
				s := fset.Position(ts.Name.Pos()).Offset
				e := fset.Position(ts.Name.End()).Offset
				out = append(out, Diag{
					Start:    s,
					End:      e,
					Severity: 2,
					Message:  fmt.Sprintf("Model '%s' is missing migration column(s) for '%s': %s", t.Model, t.Name, strings.Join(missingInModel, ", ")),
				})
			}
		}
	}
	return out
}

// DriftCodeActions offers adding missing migration columns to a Go model struct.
func DriftCodeActions(path, src string, cat *indexer.Catalog, goOff int) []CodeAction {
	if !strings.Contains(filepath.ToSlash(path), "/app/models/") {
		return nil
	}
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, src, 0)
	if err != nil || f == nil {
		return nil
	}
	for _, d := range f.Decls {
		gd, ok := d.(*ast.GenDecl)
		if !ok {
			continue
		}
		for _, sp := range gd.Specs {
			ts, ok := sp.(*ast.TypeSpec)
			if !ok || ts.Name == nil {
				continue
			}
			st, ok := ts.Type.(*ast.StructType)
			if !ok || st.Fields == nil {
				continue
			}
			s := fset.Position(ts.Pos()).Offset
			e := fset.Position(ts.End()).Offset
			if goOff < s || goOff > e {
				continue
			}
			t := cat.Model(ts.Name.Name)
			if t == nil || !t.MigLoc.Valid() {
				continue
			}
			var lines []string
			for _, col := range t.Columns {
				if col.MigLoc.Valid() && !col.Loc.Valid() {
					goName := pascalCase(col.Name)
					goType := goTypeFromSQL(col.SQLType, col.Nullable)
					lines = append(lines, fmt.Sprintf("\t%s %s `gorm:\"column:%s\" json:\"%s\"`", goName, goType, col.Name, col.Name))
				}
			}
			if len(lines) == 0 {
				continue
			}
			rbrace := fset.Position(st.Fields.Closing).Offset
			return []CodeAction{{
				Title: fmt.Sprintf("Add %d missing migration column(s) to %s", len(lines), t.Model),
				Kind:  "quickfix",
				Edits: []TextEdit{{
					Start:   rbrace,
					End:     rbrace,
					NewText: strings.Join(lines, "\n") + "\n",
				}},
			}}
		}
	}
	return nil
}

func isStandardTimestampOrID(col string) bool {
	switch strings.ToLower(col) {
	case "id", "created_at", "updated_at", "deleted_at":
		return true
	}
	return false
}

func titleWord(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

func pascalCase(s string) string {
	parts := strings.Split(s, "_")
	var b strings.Builder
	for _, p := range parts {
		if p == "" {
			continue
		}
		if strings.EqualFold(p, "id") {
			b.WriteString("ID")
			continue
		}
		b.WriteString(strings.ToUpper(p[:1]) + p[1:])
	}
	return b.String()
}

func goTypeFromSQL(sqlType string, nullable bool) string {
	u := strings.ToUpper(sqlType)
	base := "string"
	switch {
	case strings.Contains(u, "UUID"):
		base = "string"
	case strings.Contains(u, "BIGINT"), strings.Contains(u, "INT8"):
		base = "int64"
	case strings.Contains(u, "INT"):
		base = "int"
	case strings.Contains(u, "BOOL"):
		base = "bool"
	case strings.Contains(u, "DECIMAL"), strings.Contains(u, "NUMERIC"), strings.Contains(u, "FLOAT"), strings.Contains(u, "DOUBLE"), strings.Contains(u, "REAL"):
		base = "float64"
	case strings.Contains(u, "TIMESTAMP"), strings.Contains(u, "DATE"), strings.Contains(u, "TIME"):
		base = "time.Time"
	}
	if nullable {
		return "*" + base
	}
	return base
}

func lineColToOffset(src string, line, col int) int {
	if line <= 0 {
		if col < 0 {
			return 0
		}
		if col > len(src) {
			return len(src)
		}
		return col
	}
	cur := 0
	for i := 0; i < len(src) && cur < line; i++ {
		if src[i] == '\n' {
			cur++
		}
		if cur == line {
			off := i + 1 + col
			if off > len(src) {
				return len(src)
			}
			return off
		}
	}
	return 0
}

