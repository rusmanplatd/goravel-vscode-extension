// Package rawsql extracts raw SQL from Go source (including string concatenation
// and fmt.Sprintf) into virtual SQL documents with a bidirectional offset map.
package rawsql

import (
	"go/ast"
	"go/parser"
	"go/token"
	"regexp"
	"strings"

	"github.com/rusmanplatd/goravel-lsp/internal/indexer"
)

// Seg maps a run of virtual SQL bytes back to Go source bytes (literal text only).
type Seg struct {
	VStart, VEnd int
	GoStart      int
}

// Doc is one reconstructed SQL statement/fragment.
type Doc struct {
	Text    string
	Segs    []Seg
	Func    int // offset of enclosing func (or -1)
	GoStart int
	GoEnd   int
	Dynamic bool // contains values only known at runtime (format verbs, unknown vars)
}

// FileInfo is the extraction result for one Go file.
type FileInfo struct {
	Src       []byte
	Docs      []*Doc
	Queries   []*QueryCall
	TableVars map[string]string // go var name -> table name
}

var sqlish = regexp.MustCompile(`(?is)^\s*(SELECT|INSERT\s+INTO|UPDATE|DELETE\s+FROM|WITH|CREATE|ALTER|DROP|TRUNCATE|AND|OR|WHERE|ORDER\s+BY|GROUP\s+BY|HAVING|LIMIT|OFFSET|LEFT\s+JOIN|INNER\s+JOIN|RIGHT\s+JOIN|JOIN|SET|FROM|ON)\s`)

// Extract parses Go source and returns its SQL docs. Table-name variables are
// resolved via the catalog (x := (models.T{}).TableName(ctx)).
func Extract(path string, src []byte, cat *indexer.Catalog) *FileInfo {
	fset := token.NewFileSet()
	f, _ := parser.ParseFile(fset, path, src, parser.SkipObjectResolution)
	fi := &FileInfo{Src: src, TableVars: map[string]string{}}
	if f == nil {
		return fi
	}
	tf := fset.File(f.Pos())
	if tf == nil {
		return fi
	}
	off := func(p token.Pos) int { return tf.Offset(p) }

	// variable -> table name
	vars := fi.TableVars
	ast.Inspect(f, func(n ast.Node) bool {
		as, ok := n.(*ast.AssignStmt)
		if !ok || len(as.Lhs) != 1 || len(as.Rhs) != 1 {
			return true
		}
		id, ok := as.Lhs[0].(*ast.Ident)
		if !ok {
			return true
		}
		if tname := extractTableNameExpr(as.Rhs[0], cat); tname != "" {
			vars[id.Name] = tname
		}
		return true
	})

	build := func(e ast.Expr, fn int, env map[string][]ast.Expr) *Doc {
		d := &Doc{Func: fn, GoStart: off(e.Pos()), GoEnd: off(e.End())}
		var sb strings.Builder
		var flat func(e ast.Expr, depth int)
		flat = func(e ast.Expr, depth int) {
			switch x := e.(type) {
			case *ast.BasicLit:
				if x.Kind != token.STRING {
					sb.WriteString("__")
					return
				}
				start := off(x.Pos()) + 1
				end := off(x.End()) - 1
				if end < start {
					return
				}
				raw := string(src[start:end])
				if strings.Contains(raw, "%") {
					d.Dynamic = true
				}
				vs := sb.Len()
				sb.WriteString(neutralize(raw))
				d.Segs = append(d.Segs, Seg{vs, sb.Len(), start})
			case *ast.ParenExpr:
				flat(x.X, depth)
			case *ast.BinaryExpr:
				if x.Op == token.ADD {
					flat(x.X, depth)
					flat(x.Y, depth)
				} else {
					d.Dynamic = true
					sb.WriteString("__")
				}
			case *ast.Ident:
				if t, ok := vars[x.Name]; ok {
					sb.WriteString(t)
				} else if exprs := env[x.Name]; len(exprs) > 0 && depth < 8 {
					for _, ex := range exprs {
						flat(ex, depth+1)
					}
				} else {
					d.Dynamic = true
					sb.WriteString("__" + x.Name + "__")
				}
			case *ast.CallExpr:
				if isSprintf(x) && len(x.Args) > 0 {
					d.Dynamic = true
					flat(x.Args[0], depth)
				} else if tname := extractTableNameExpr(x, cat); tname != "" {
					sb.WriteString(tname)
				} else {
					d.Dynamic = true
					sb.WriteString("__call__")
				}
			default:
				d.Dynamic = true
				sb.WriteString("__")
			}
		}
		flat(e, 0)
		d.Text = sb.String()
		return d
	}

	handle := func(root ast.Node, fn int) {
		ast.Inspect(root, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.BinaryExpr:
				if x.Op != token.ADD {
					return true
				}
				if d := build(x, fn, nil); len(d.Segs) > 0 && anySQL(d, src) {
					fi.Docs = append(fi.Docs, d)
					return false
				}
			case *ast.CallExpr:
				if isSprintf(x) && len(x.Args) > 0 {
					if d := build(x, fn, nil); len(d.Segs) > 0 && sqlish.MatchString(d.Text) {
						fi.Docs = append(fi.Docs, d)
						return false
					}
				}
			case *ast.BasicLit:
				if x.Kind == token.STRING {
					if d := build(x, fn, nil); sqlish.MatchString(d.Text) {
						fi.Docs = append(fi.Docs, d)
					}
				}
			}
			return true
		})
	}
	for _, decl := range f.Decls {
		if fd, ok := decl.(*ast.FuncDecl); ok && fd.Body != nil {
			handle(fd.Body, off(fd.Pos()))
			analyzeQueries(fi, fd, cat, off, build)
		} else {
			handle(decl, -1)
		}
	}
	return fi
}

func extractTableNameExpr(e ast.Expr, cat *indexer.Catalog) string {
	call, ok := e.(*ast.CallExpr)
	if !ok {
		return ""
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "TableName" {
		return ""
	}
	x := sel.X
	for {
		if p, ok := x.(*ast.ParenExpr); ok {
			x = p.X
			continue
		}
		break
	}
	cl, ok := x.(*ast.CompositeLit)
	if !ok {
		return ""
	}
	name := ""
	switch t := cl.Type.(type) {
	case *ast.Ident:
		name = t.Name
	case *ast.SelectorExpr:
		name = t.Sel.Name
	}
	if tbl := cat.Model(name); tbl != nil {
		return tbl.Name
	}
	return ""
}

func anySQL(d *Doc, src []byte) bool {
	for _, s := range d.Segs {
		if sqlish.MatchString(string(neutralize(string(src[s.GoStart:s.GoStart+s.VEnd-s.VStart]))) + " ") {
			return true
		}
	}
	return false
}

func isSprintf(c *ast.CallExpr) bool {
	sel, ok := c.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	id, ok := sel.X.(*ast.Ident)
	return ok && id.Name == "fmt" && sel.Sel.Name == "Sprintf"
}

// neutralize keeps byte length but removes escapes/format verbs so SQL scanning works.
func neutralize(s string) string {
	b := []byte(s)
	for i := 0; i+1 < len(b); i++ {
		switch {
		case b[i] == '\\' && (b[i+1] == 'n' || b[i+1] == 't' || b[i+1] == 'r'):
			b[i], b[i+1] = ' ', ' '
			i++
		case b[i] == '\\' && b[i+1] == '"':
			b[i] = ' '
			i++
		case b[i] == '%' && (b[i+1] == 'd' || b[i+1] == 's' || b[i+1] == 'v'):
			b[i], b[i+1] = '_', '_'
			i++
		}
	}
	return string(b)
}

// ToVirtual maps a Go byte offset to a virtual offset.
func (d *Doc) ToVirtual(goOff int) (int, bool) {
	for _, s := range d.Segs {
		if goOff >= s.GoStart && goOff <= s.GoStart+(s.VEnd-s.VStart) {
			return s.VStart + goOff - s.GoStart, true
		}
	}
	return 0, false
}

// ToGo maps a virtual offset to a Go offset (only if inside a literal segment).
func (d *Doc) ToGo(v int) (int, bool) {
	for _, s := range d.Segs {
		if v >= s.VStart && v <= s.VEnd {
			return s.GoStart + v - s.VStart, true
		}
	}
	return 0, false
}

// DocAt returns the doc whose Go range contains offset.
func (fi *FileInfo) DocAt(goOff int) *Doc {
	var best *Doc
	for _, d := range fi.Docs {
		if goOff >= d.GoStart && goOff <= d.GoEnd {
			if _, ok := d.ToVirtual(goOff); !ok {
				continue
			}
			if best == nil || d.GoEnd-d.GoStart < best.GoEnd-best.GoStart {
				best = d
			}
		}
	}
	return best
}
