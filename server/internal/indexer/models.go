package indexer

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"unicode"
)

type parsedFile struct {
	path string
	fset *token.FileSet
	file *ast.File
}

func indexModels(c *Catalog, root string) {
	dir := filepath.Join(root, "app", "models")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	var files []*parsedFile
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		p := filepath.Join(dir, e.Name())
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, p, nil, parser.ParseComments)
		if err != nil || f == nil {
			continue
		}
		files = append(files, &parsedFile{p, fset, f})
	}

	// 1. string constants (OriginalTableName values etc.)
	consts := map[string]string{}
	for _, pf := range files {
		for _, d := range pf.file.Decls {
			gd, ok := d.(*ast.GenDecl)
			if !ok || gd.Tok != token.CONST {
				continue
			}
			for _, s := range gd.Specs {
				vs := s.(*ast.ValueSpec)
				for i, n := range vs.Names {
					if i < len(vs.Values) {
						if bl, ok := vs.Values[i].(*ast.BasicLit); ok && bl.Kind == token.STRING {
							if v, err := strconv.Unquote(bl.Value); err == nil {
								consts[n.Name] = v
							}
						}
					}
				}
			}
		}
	}

	// 2. TableName() methods -> table name per model
	tableOf := map[string]string{}
	for _, pf := range files {
		for _, d := range pf.file.Decls {
			fd, ok := d.(*ast.FuncDecl)
			if !ok || fd.Recv == nil || fd.Name.Name != "TableName" || fd.Body == nil {
				continue
			}
			model := recvName(fd.Recv)
			ast.Inspect(fd.Body, func(n ast.Node) bool {
				if id, ok := n.(*ast.Ident); ok && strings.HasSuffix(id.Name, "OriginalTableName") {
					if v, ok := consts[id.Name]; ok {
						tableOf[model] = v
					}
				}
				if bl, ok := n.(*ast.BasicLit); ok && bl.Kind == token.STRING {
					if v, err := strconv.Unquote(bl.Value); err == nil && v != "." && tableOf[model] == "" {
						tableOf[model] = v
					}
				}
				return true
			})
		}
	}

	// 3. structs -> tables + columns
	for _, pf := range files {
		for _, d := range pf.file.Decls {
			gd, ok := d.(*ast.GenDecl)
			if !ok || gd.Tok != token.TYPE {
				continue
			}
			for _, s := range gd.Specs {
				ts := s.(*ast.TypeSpec)
				st, ok := ts.Type.(*ast.StructType)
				if !ok {
					continue
				}
				c.Structs[ts.Name.Name] = structInfo(ts.Name.Name, st)
				tname, has := tableOf[ts.Name.Name]
				if !has {
					continue // not a table model (e.g. *Row scan structs are handled below)
				}
				t := c.ensure(tname)
				t.Model = ts.Name.Name
				t.Loc = loc(pf, ts.Name.Pos())
				c.Models[ts.Name.Name] = t
				for _, f := range st.Fields.List {
					addFields(t, pf, f)
				}
			}
		}
	}
}

func addFields(t *Table, pf *parsedFile, f *ast.Field) {
	if len(f.Names) == 0 {
		return
	}
	tag := reflect.StructTag("")
	if f.Tag != nil {
		if u, err := strconv.Unquote(f.Tag.Value); err == nil {
			tag = reflect.StructTag(u)
		}
	}
	gormTag := tag.Get("gorm")
	if gormTag == "-" || strings.Contains(gormTag, "foreignKey") || strings.Contains(gormTag, "many2many") || strings.Contains(gormTag, "polymorphic") {
		return
	}
	jsonName := strings.Split(tag.Get("json"), ",")[0]
	if jsonName == "-" {
		return
	}
	for _, n := range f.Names {
		if !n.IsExported() {
			continue
		}
		col := ""
		for _, part := range strings.Split(gormTag, ";") {
			if strings.HasPrefix(part, "column:") {
				col = strings.TrimPrefix(part, "column:")
			}
		}
		if dbt := tag.Get("db"); col == "" && dbt != "" && dbt != "-" {
			col = strings.Split(dbt, ",")[0]
		}
		if col == "" {
			col = Snake(n.Name)
		}
		_ = jsonName
		goType := exprString(f.Type)
		doc := ""
		if f.Doc != nil {
			doc = cleanDoc(f.Doc.Text())
		}
		t.add(&Column{
			Name:     col,
			GoName:   n.Name,
			GoType:   goType,
			Doc:      doc,
			Nullable: strings.HasPrefix(goType, "*"),
			Loc:      loc(pf, n.Pos()),
		})
	}
}

var docRe = regexp.MustCompile(`@Description\s+(.*)`)

func cleanDoc(s string) string {
	if m := docRe.FindStringSubmatch(s); m != nil {
		return strings.TrimSpace(m[1])
	}
	return strings.TrimSpace(s)
}

func recvName(fl *ast.FieldList) string {
	if len(fl.List) == 0 {
		return ""
	}
	switch t := fl.List[0].Type.(type) {
	case *ast.Ident:
		return t.Name
	case *ast.StarExpr:
		if id, ok := t.X.(*ast.Ident); ok {
			return id.Name
		}
	}
	return ""
}

func exprString(e ast.Expr) string {
	switch t := e.(type) {
	case *ast.Ident:
		return t.Name
	case *ast.StarExpr:
		return "*" + exprString(t.X)
	case *ast.SelectorExpr:
		return exprString(t.X) + "." + t.Sel.Name
	case *ast.ArrayType:
		return "[]" + exprString(t.Elt)
	case *ast.MapType:
		return "map[" + exprString(t.Key) + "]" + exprString(t.Value)
	case *ast.InterfaceType:
		return "any"
	}
	return "?"
}

func loc(pf *parsedFile, p token.Pos) Location {
	pos := pf.fset.Position(p)
	return Location{File: pf.path, Line: pos.Line - 1, Col: pos.Column - 1}
}

// Snake converts GoName to go_name (ID -> id, ProvinceID -> province_id).
func Snake(s string) string {
	r := []rune(s)
	var b strings.Builder
	for i, ch := range r {
		if unicode.IsUpper(ch) {
			if i > 0 && (unicode.IsLower(r[i-1]) || unicode.IsDigit(r[i-1]) || (i+1 < len(r) && unicode.IsLower(r[i+1]) && unicode.IsUpper(r[i-1]))) {
				b.WriteByte('_')
			}
			b.WriteRune(unicode.ToLower(ch))
		} else {
			b.WriteRune(ch)
		}
	}
	return b.String()
}

func structInfo(name string, st *ast.StructType) *StructInfo {
	si := &StructInfo{Name: name}
	for _, f := range st.Fields.List {
		tag := reflect.StructTag("")
		if f.Tag != nil {
			if u, err := strconv.Unquote(f.Tag.Value); err == nil {
				tag = reflect.StructTag(u)
			}
		}
		if tag.Get("db") == "-" {
			continue
		}
		for _, n := range f.Names {
			if !n.IsExported() {
				continue
			}
			col := strings.Split(tag.Get("db"), ",")[0]
			if col == "" {
				col = Snake(n.Name)
			}
			alts := []string{strings.ToLower(n.Name), Snake(n.Name)}
			if j := strings.Split(tag.Get("json"), ",")[0]; j != "" && j != "-" {
				alts = append(alts, j)
			}
			si.Fields = append(si.Fields, StructField{Col: col, Alt: alts, GoName: n.Name, GoType: exprString(f.Type)})
		}
	}
	return si
}
