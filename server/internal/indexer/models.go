package indexer

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
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

func walkGoFiles(dir string) []*parsedFile {
	var files []*parsedFile
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if !strings.HasSuffix(d.Name(), ".go") || strings.HasSuffix(d.Name(), "_test.go") {
			return nil
		}
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, p, nil, parser.ParseComments)
		if err != nil || f == nil {
			return nil
		}
		files = append(files, &parsedFile{p, fset, f})
		return nil
	})
	return files
}

func indexModels(c *Catalog, root string) {
	files := walkGoFiles(filepath.Join(root, "app", "models"))
	if len(files) == 0 {
		return
	}

	// 1. string constants (OriginalTableName, OriginalSchemaName values etc.)
	consts := map[string]string{}
	constLocs := map[string]Location{}
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
								constLocs[n.Name] = loc(pf, n.Pos())
								if strings.HasSuffix(n.Name, "OriginalTableName") {
									c.TableConsts[n.Name] = &TableConstInfo{
										Name:      n.Name,
										TableName: StripSchema(v),
										Loc:       constLocs[n.Name],
									}
								}
							}
						}
					}
				}
			}
		}
	}

	// 2. TableName() and OriginalSchemaName() methods -> table/schema name per model
	tableOf := map[string]string{}
	schemaOf := map[string]string{}
	constOfModel := map[string]string{}
	for _, pf := range files {
		for _, d := range pf.file.Decls {
			fd, ok := d.(*ast.FuncDecl)
			if !ok || fd.Recv == nil || fd.Body == nil {
				continue
			}
			model := recvName(fd.Recv)
			switch fd.Name.Name {
			case "TableName":
				ast.Inspect(fd.Body, func(n ast.Node) bool {
					if id, ok := n.(*ast.Ident); ok && strings.HasSuffix(id.Name, "OriginalTableName") {
						if v, ok := consts[id.Name]; ok {
							tableOf[model] = v
							constOfModel[model] = id.Name
						}
					}
					if bl, ok := n.(*ast.BasicLit); ok && bl.Kind == token.STRING {
						if v, err := strconv.Unquote(bl.Value); err == nil && v != "." && tableOf[model] == "" {
							tableOf[model] = v
						}
					}
					return true
				})
			case "OriginalSchemaName":
				ast.Inspect(fd.Body, func(n ast.Node) bool {
					if id, ok := n.(*ast.Ident); ok && strings.HasSuffix(id.Name, "OriginalSchemaName") {
						if v, ok := consts[id.Name]; ok {
							schemaOf[model] = v
						}
					}
					if bl, ok := n.(*ast.BasicLit); ok && bl.Kind == token.STRING {
						if v, err := strconv.Unquote(bl.Value); err == nil && v != "" && schemaOf[model] == "" {
							schemaOf[model] = v
						}
					}
					return true
				})
			}
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
				c.Structs[ts.Name.Name] = structInfo(pf, ts.Name.Name, st, ts.Name.Pos())
				tname, has := tableOf[ts.Name.Name]
				if !has {
					continue // not a table model (e.g. *Row scan structs or base structs)
				}
				t := c.ensure(tname)
				t.Model = ts.Name.Name
				if sc := schemaOf[ts.Name.Name]; sc != "" {
					t.Schema = sc
				}
				t.Loc = loc(pf, ts.Name.Pos())
				c.Models[ts.Name.Name] = t
				if cn := constOfModel[ts.Name.Name]; cn != "" {
					if tc := c.TableConsts[cn]; tc != nil {
						tc.Model = ts.Name.Name
					}
				}
				for _, f := range st.Fields.List {
					addFields(t, pf, f)
				}
			}
		}
	}
}

func indexRequests(c *Catalog, root string) {
	files := walkGoFiles(filepath.Join(root, "app", "http", "requests"))
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
				c.Structs[ts.Name.Name] = structInfo(pf, ts.Name.Name, st, ts.Name.Pos())
			}
		}
	}
}

func addEmbeddedColumns(t *Table, pf *parsedFile, pos token.Pos, embedType string) {
	l := loc(pf, pos)
	switch embedType {
	case "orm.Model", "Model":
		t.add(&Column{Name: "id", GoName: "ID", GoType: "uint", SQLType: "BIGINT", Nullable: false, Loc: l})
		t.add(&Column{Name: "created_at", GoName: "CreatedAt", GoType: "carbon.DateTime", SQLType: "TIMESTAMP", Nullable: true, Loc: l})
		t.add(&Column{Name: "updated_at", GoName: "UpdatedAt", GoType: "carbon.DateTime", SQLType: "TIMESTAMP", Nullable: true, Loc: l})
	case "orm.SoftDeletes", "SoftDeletes", "gorm.DeletedAt":
		t.add(&Column{Name: "deleted_at", GoName: "DeletedAt", GoType: "*time.Time", SQLType: "TIMESTAMP", Nullable: true, Loc: l})
	case "orm.Timestamps", "Timestamps":
		t.add(&Column{Name: "created_at", GoName: "CreatedAt", GoType: "time.Time", SQLType: "TIMESTAMP", Nullable: true, Loc: l})
		t.add(&Column{Name: "updated_at", GoName: "UpdatedAt", GoType: "time.Time", SQLType: "TIMESTAMP", Nullable: true, Loc: l})
	}
}

func addFields(t *Table, pf *parsedFile, f *ast.Field) {
	if len(f.Names) == 0 {
		addEmbeddedColumns(t, pf, f.Pos(), strings.TrimPrefix(exprString(f.Type), "*"))
		return
	}
	tag := reflect.StructTag("")
	if f.Tag != nil {
		if u, err := strconv.Unquote(f.Tag.Value); err == nil {
			tag = reflect.StructTag(u)
		}
	}
	gormTag := tag.Get("gorm")
	if gormTag == "-" {
		return
	}
	fk, refs := parseGormRelTags(gormTag)
	goType := exprString(f.Type)
	baseType := cleanModelType(goType)

	if strings.Contains(gormTag, "foreignKey") || strings.Contains(gormTag, "many2many") || strings.Contains(gormTag, "polymorphic") {
		for _, n := range f.Names {
			if n.IsExported() {
				t.addRelation(&Relation{
					Name:        n.Name,
					TargetModel: baseType,
					ForeignKey:  Snake(fk),
					References:  Snake(refs),
					Loc:         loc(pf, n.Pos()),
				})
			}
		}
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
		doc, example := "", ""
		if f.Doc != nil {
			doc, example = parseFieldDoc(f.Doc.Text())
		}
		t.add(&Column{
			Name:     col,
			GoName:   n.Name,
			GoType:   goType,
			Doc:      doc,
			Example:  example,
			Nullable: strings.HasPrefix(goType, "*") || strings.Contains(goType, "Null"),
			Loc:      loc(pf, n.Pos()),
		})
	}
}

func parseGormRelTags(gormTag string) (fk, refs string) {
	for _, part := range strings.Split(gormTag, ";") {
		part = strings.TrimSpace(part)
		switch {
		case strings.HasPrefix(part, "foreignKey:"):
			fk = strings.TrimPrefix(part, "foreignKey:")
		case strings.HasPrefix(part, "references:"):
			refs = strings.TrimPrefix(part, "references:")
		}
	}
	return
}

func cleanModelType(goType string) string {
	t := strings.TrimLeft(goType, "[]*")
	if i := strings.LastIndex(t, "."); i >= 0 {
		t = t[i+1:]
	}
	return t
}

// resolveModelRelations links relation fields on models (including implicit struct/slice fields)
// to their target tables and foreign keys, and merges embedded struct fields.
func resolveModelRelations(c *Catalog) {
	for modelName, t := range c.Models {
		si := c.Structs[modelName]
		if si != nil {
			for _, emb := range si.Embeds {
				if esi := c.Structs[emb]; esi != nil {
					for _, ef := range esi.Fields {
						t.add(&Column{
							Name:     ef.Col,
							GoName:   ef.GoName,
							GoType:   ef.GoType,
							Nullable: strings.HasPrefix(ef.GoType, "*"),
							Loc:      ef.Loc,
						})
					}
				}
			}
			for _, sf := range si.Fields {
				target := c.Models[sf.Relation]
				if target == nil {
					continue
				}
				fk := Snake(sf.FK)
				if fk == "" {
					cand := Snake(sf.GoName) + "_id"
					if t.Column(cand) != nil {
						fk = cand
					} else if target.Column(Snake(modelName)+"_id") != nil {
						fk = Snake(modelName) + "_id"
					}
				}
				refs := Snake(sf.Refs)
				if refs == "" {
					refs = "id"
				}
				t.addRelation(&Relation{
					Name:        sf.GoName,
					TargetModel: target.Model,
					TargetTable: target.Name,
					ForeignKey:  fk,
					References:  refs,
					Loc:         sf.Loc,
				})
			}
		}
		for _, r := range t.Relations {
			if r.TargetTable == "" && r.TargetModel != "" {
				if target := c.Models[r.TargetModel]; target != nil {
					r.TargetTable = target.Name
				}
			}
			if r.References == "" {
				r.References = "id"
			}
			if r.ForeignKey == "" && r.Name != "" {
				cand := Snake(r.Name) + "_id"
				if t.Column(cand) != nil {
					r.ForeignKey = cand
				}
			}
			if r.ForeignKey != "" && r.TargetTable != "" {
				if col := t.Column(r.ForeignKey); col != nil && col.ForeignKey == "" {
					col.ForeignKey = r.TargetTable + "." + r.References
				}
			}
		}
		// Infer FK relations from *_id columns when matching a table name or model suffix
		for _, col := range t.Columns {
			if !strings.HasSuffix(col.Name, "_id") || col.ForeignKey != "" {
				continue
			}
			stem := strings.TrimSuffix(col.Name, "_id") // e.g. "province"
			for _, cand := range c.Tables {
				if cand == t {
					continue
				}
				if cand.Name == stem+"s" || cand.Name == stem+"es" || strings.HasSuffix(cand.Name, "_"+stem+"s") ||
					strings.HasSuffix(cand.Name, "_"+stem+"es") || strings.HasSuffix(cand.Name, "_"+ strings.TrimSuffix(stem, "y")+"ies") ||
					strings.EqualFold(cand.Model, stem) || strings.HasSuffix(strings.ToLower(cand.Model), stem) {
					col.ForeignKey = cand.Name + ".id"
					t.addRelation(&Relation{
						Name:        strings.TrimSuffix(col.GoName, "ID"),
						TargetModel: cand.Model,
						TargetTable: cand.Name,
						ForeignKey:  col.Name,
						References:  "id",
						Loc:         col.Loc,
					})
					break
				}
			}
		}
	}
}

var (
	docRe     = regexp.MustCompile(`@Description\s+(.*)`)
	exampleRe = regexp.MustCompile(`@Example\s+(.*)`)
)

func parseFieldDoc(s string) (doc, example string) {
	if m := exampleRe.FindStringSubmatch(s); m != nil {
		example = strings.TrimSpace(m[1])
		s = exampleRe.ReplaceAllString(s, "")
	}
	if m := docRe.FindStringSubmatch(s); m != nil {
		doc = strings.TrimSpace(m[1])
		return doc, example
	}
	return strings.TrimSpace(s), example
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

func structInfo(pf *parsedFile, name string, st *ast.StructType, pos token.Pos) *StructInfo {
	si := &StructInfo{Name: name, Loc: loc(pf, pos)}
	for _, f := range st.Fields.List {
		if len(f.Names) == 0 {
			si.Embeds = append(si.Embeds, cleanModelType(exprString(f.Type)))
			continue
		}
		tag := reflect.StructTag("")
		if f.Tag != nil {
			if u, err := strconv.Unquote(f.Tag.Value); err == nil {
				tag = reflect.StructTag(u)
			}
		}
		if tag.Get("db") == "-" {
			continue
		}
		gormTag := tag.Get("gorm")
		fk, refs := parseGormRelTags(gormTag)
		goType := exprString(f.Type)
		baseType := cleanModelType(goType)
		for _, n := range f.Names {
			if !n.IsExported() {
				continue
			}
			col := strings.Split(tag.Get("db"), ",")[0]
			if col == "" {
				if j := strings.Split(tag.Get("json"), ",")[0]; j != "" && j != "-" {
					col = j
				} else if fm := strings.Split(tag.Get("form"), ",")[0]; fm != "" && fm != "-" {
					col = fm
				} else {
					col = Snake(n.Name)
				}
			}
			alts := []string{strings.ToLower(n.Name), Snake(n.Name)}
			if j := strings.Split(tag.Get("json"), ",")[0]; j != "" && j != "-" {
				alts = append(alts, j)
			}
			if fm := strings.Split(tag.Get("form"), ",")[0]; fm != "" && fm != "-" {
				alts = append(alts, fm)
			}
			sf := StructField{
				Col:      col,
				Alt:      alts,
				GoName:   n.Name,
				GoType:   goType,
				Relation: baseType,
				FK:       fk,
				Refs:     refs,
				Loc:      loc(pf, n.Pos()),
			}
			si.Fields = append(si.Fields, sf)
		}
	}
	return si
}
