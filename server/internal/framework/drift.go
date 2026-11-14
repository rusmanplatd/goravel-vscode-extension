package framework

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
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

// DriftCodeActions offers:
// 1. Registering an unregistered component in bootstrap/
// 2. Adding missing migration columns to a Go model struct
// 3. Adding missing Go model fields to its migration file
func DriftCodeActions(path, src string, cat *indexer.Catalog, goOff int) []CodeAction {
	var actions []CodeAction
	clean := filepath.Clean(path)

	// 1. Auto-register unregistered component in bootstrap/
	if unreg, ok := cat.UnregisteredByFile[clean]; ok {
		if act, ok := ResolveBootstrapRegistrationAction(cat, unreg); ok {
			actions = append(actions, act)
		}
	}

	if !strings.Contains(filepath.ToSlash(path), "/app/models/") {
		return actions
	}
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, src, 0)
	if err != nil || f == nil {
		return actions
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
			// 2. Migration -> Model quickfix
			var lines []string
			var missingInMig []*indexer.Column
			for _, col := range t.Columns {
				if col.MigLoc.Valid() && !col.Loc.Valid() {
					goName := pascalCase(col.Name)
					goType := goTypeFromSQL(col.SQLType, col.Nullable)
					lines = append(lines, fmt.Sprintf("\t%s %s `gorm:\"column:%s\" json:\"%s\"`", goName, goType, col.Name, col.Name))
				} else if col.Loc.Valid() && !col.MigLoc.Valid() && !isStandardTimestampOrID(col.Name) {
					missingInMig = append(missingInMig, col)
				}
			}
			if len(lines) > 0 {
				rbrace := fset.Position(st.Fields.Closing).Offset
				actions = append(actions, CodeAction{
					Title: fmt.Sprintf("Add %d missing migration column(s) to %s", len(lines), t.Model),
					Kind:  "quickfix",
					Edits: []TextEdit{{
						Start:   rbrace,
						End:     rbrace,
						NewText: strings.Join(lines, "\n") + "\n",
					}},
				})
			}
			// 3. Model -> Migration quickfix (cross-file edit into t.MigLoc.File)
			if len(missingInMig) > 0 {
				if act, ok := buildModelToMigrationAction(t, missingInMig); ok {
					actions = append(actions, act)
				}
			}
		}
	}
	return actions
}

// ResolveBootstrapRegistrationAction constructs a cross-file CodeAction to wire an UnregisteredItem in bootstrap/.
func ResolveBootstrapRegistrationAction(cat *indexer.Catalog, u *indexer.UnregisteredItem) (CodeAction, bool) {
	if cat == nil || u == nil || cat.Root == "" || u.StructName == "" {
		return CodeAction{}, false
	}
	targetFile, sliceMarker, defaultPkg := findBootstrapTargetFile(cat.Root, u)
	if targetFile == "" {
		return CodeAction{}, false
	}
	b, err := os.ReadFile(targetFile)
	if err != nil {
		return CodeAction{}, false
	}
	content := string(b)
	pkg := u.PkgName
	if pkg == "" {
		pkg = defaultPkg
	}
	idx := strings.Index(content, sliceMarker)
	if idx < 0 {
		return CodeAction{}, false
	}
	openBrace := strings.IndexByte(content[idx:], '{')
	if openBrace < 0 {
		return CodeAction{}, false
	}
	openPos := idx + openBrace
	closePos := matchBraceOffset(content, openPos)
	if closePos < 0 {
		return CodeAction{}, false
	}
	entry := fmt.Sprintf("\t\t&%s.%s{},\n\t", pkg, u.StructName)
	if closePos > 0 && content[closePos-1] == '\t' {
		entry = fmt.Sprintf("\t&%s.%s{},\n\t", pkg, u.StructName)
	}
	relTarget, err := filepath.Rel(cat.Root, targetFile)
	if err != nil {
		relTarget = filepath.Base(targetFile)
	}
	return CodeAction{
		Title: fmt.Sprintf("Register %s in %s", u.StructName, filepath.ToSlash(relTarget)),
		Kind:  "quickfix",
		Edits: []TextEdit{{
			File:    targetFile,
			Start:   closePos,
			End:     closePos,
			NewText: entry,
		}},
	}, true
}

func findBootstrapTargetFile(root string, u *indexer.UnregisteredItem) (targetFile, sliceMarker, defaultPkg string) {
	bootDir := filepath.Join(root, "bootstrap")
	switch u.Kind {
	case "migration":
		sliceMarker = "[]schema.Migration"
		defaultPkg = "migrations"
		if strings.HasPrefix(u.PkgName, "migrations_") {
			suffix := strings.TrimPrefix(u.PkgName, "migrations_")
			cand := filepath.Join(bootDir, "bootstrap_"+suffix, "migrations.go")
			if fileHasMarker(cand, sliceMarker) {
				return cand, sliceMarker, u.PkgName
			}
		}
		cand := filepath.Join(bootDir, "migrations.go")
		if fileHasMarker(cand, sliceMarker) {
			return cand, sliceMarker, defaultPkg
		}
		candPusat := filepath.Join(bootDir, "bootstrap_pusat", "migrations.go")
		if fileHasMarker(candPusat, sliceMarker) {
			return candPusat, sliceMarker, u.PkgName
		}
	case "seeder":
		sliceMarker = "[]seeder.Seeder"
		defaultPkg = "seeders"
		cand := filepath.Join(bootDir, "seeders.go")
		if fileHasNonEmptyMarker(cand, sliceMarker) {
			return cand, sliceMarker, defaultPkg
		}
		candPusat := filepath.Join(bootDir, "bootstrap_pusat", "seeders.go")
		if fileHasMarker(candPusat, sliceMarker) {
			return candPusat, sliceMarker, defaultPkg
		}
		if fileHasMarker(cand, sliceMarker) {
			return cand, sliceMarker, defaultPkg
		}
	case "rule":
		sliceMarker = "[]validation.Rule"
		defaultPkg = "rules"
		cand := filepath.Join(bootDir, "rules.go")
		if fileHasMarker(cand, sliceMarker) {
			return cand, sliceMarker, defaultPkg
		}
	case "command":
		sliceMarker = "[]console.Command"
		defaultPkg = "commands"
		cand := filepath.Join(bootDir, "commands.go")
		if fileHasMarker(cand, sliceMarker) {
			return cand, sliceMarker, defaultPkg
		}
	}
	return "", "", ""
}

func fileHasMarker(path, marker string) bool {
	b, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	return strings.Contains(string(b), marker)
}

func fileHasNonEmptyMarker(path, marker string) bool {
	b, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	s := string(b)
	return strings.Contains(s, marker+"{\n")
}

func matchBraceOffset(s string, open int) int {
	depth := 0
	for i := open; i < len(s); i++ {
		switch s[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}

func buildModelToMigrationAction(t *indexer.Table, cols []*indexer.Column) (CodeAction, bool) {
	migFile := t.MigLoc.File
	b, err := os.ReadFile(migFile)
	if err != nil {
		return CodeAction{}, false
	}
	migSrc := string(b)
	upEnd := len(migSrc)
	if dIdx := strings.Index(migSrc, "Down() error"); dIdx > 0 {
		upEnd = dIdx
	}
	upPart := migSrc[:upEnd]

	// Case A: Schema().Create blueprint
	if scIdx := strings.Index(upPart, "Schema()."); scIdx >= 0 {
		braceOpen := strings.IndexByte(upPart[scIdx:], '{')
		if braceOpen >= 0 {
			openPos := scIdx + braceOpen
			closePos := matchBraceOffset(upPart, openPos)
			if closePos > openPos {
				var lines []string
				for _, c := range cols {
					lines = append(lines, "\t\t\t"+blueprintLineForCol(c))
				}
				return CodeAction{
					Title: fmt.Sprintf("Add %d missing column(s) to migration %s", len(cols), filepath.Base(migFile)),
					Kind:  "quickfix",
					Edits: []TextEdit{{
						File:    migFile,
						Start:   closePos,
						End:     closePos,
						NewText: strings.Join(lines, "\n") + "\n\t\t",
					}},
				}, true
			}
		}
	}

	// Case B: Raw SQL CREATE TABLE (...)
	if ctIdx := strings.Index(strings.ToUpper(upPart), "CREATE TABLE"); ctIdx >= 0 {
		parenOpen := strings.IndexByte(upPart[ctIdx:], '(')
		if parenOpen >= 0 {
			openPos := ctIdx + parenOpen
			closePos := matchParenOffset(upPart, openPos)
			if closePos > openPos {
				var lines []string
				for _, c := range cols {
					sqlType := sqlTypeFromGo(c.GoType)
					nullStr := " NOT NULL"
					if c.Nullable {
						nullStr = ""
					}
					lines = append(lines, fmt.Sprintf("\t\t\t%s %s%s", c.Name, sqlType, nullStr))
				}
				prefix := ",\n"
				trimmedBefore := strings.TrimRight(upPart[:closePos], " \t\r\n")
				insertPos := len(trimmedBefore)
				if strings.HasSuffix(trimmedBefore, ",") {
					prefix = "\n"
				}
				return CodeAction{
					Title: fmt.Sprintf("Add %d missing column(s) to migration %s", len(cols), filepath.Base(migFile)),
					Kind:  "quickfix",
					Edits: []TextEdit{{
						File:    migFile,
						Start:   insertPos,
						End:     insertPos,
						NewText: prefix + strings.Join(lines, ",\n"),
					}},
				}, true
			}
		}
	}
	return CodeAction{}, false
}

func matchParenOffset(s string, open int) int {
	depth := 0
	for i := open; i < len(s); i++ {
		switch s[i] {
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}

func blueprintLineForCol(c *indexer.Column) string {
	gt := strings.TrimPrefix(c.GoType, "*")
	method := "String"
	switch gt {
	case "int", "int32":
		method = "Integer"
	case "int64", "uint", "uint64":
		method = "BigInteger"
	case "bool":
		method = "Boolean"
	case "float32", "float64":
		method = "Decimal"
	case "time.Time", "carbon.DateTime":
		method = "TimestampTz"
	case "uuid.UUID":
		method = "Uuid"
	}
	line := fmt.Sprintf("table.%s(%q)", method, c.Name)
	if c.Nullable {
		line += ".Nullable()"
	}
	return line
}

func sqlTypeFromGo(goType string) string {
	gt := strings.TrimPrefix(goType, "*")
	switch gt {
	case "int", "int32":
		return "INTEGER"
	case "int64", "uint", "uint64":
		return "BIGINT"
	case "bool":
		return "BOOLEAN"
	case "float32", "float64":
		return "NUMERIC(18,2)"
	case "time.Time", "carbon.DateTime":
		return "TIMESTAMPTZ"
	case "uuid.UUID":
		return "UUID"
	default:
		return "VARCHAR(255)"
	}
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

