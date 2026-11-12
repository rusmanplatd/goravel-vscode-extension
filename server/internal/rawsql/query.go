package rawsql

import (
	"go/ast"
	"go/token"
	"regexp"
	"strings"

	"github.com/rusmanplatd/goravel-lsp/internal/indexer"
)

// OutCol is one output column of a SELECT list (virtual offsets into Doc.Text).
type OutCol struct {
	Name      string // output name (alias or column)
	Qualifier string // table alias, if `q.col`
	SrcCol    string // source column, if a plain column reference
	VStart    int
	VEnd      int
}

// QueryCall is a facades.DB().Select/Insert/Update/... call with its assembled query.
type QueryCall struct {
	Method     string
	FuncName   string
	FuncStart  int
	CallStart  int
	CallEnd    int
	ArgStart   int // query argument range
	ArgEnd     int
	Doc        *Doc // query with variables inlined
	DestStruct string
	Cols       []OutCol
	Star       bool
	NArgs      int
	ArgsOpaque bool // slices / spread: argument count unknown
}

var queryMethods = map[string]int{"Select": 1, "Insert": 0, "Update": 0, "Delete": 0, "Statement": 0, "Exec": 0}

func typeString(e ast.Expr) string {
	switch t := e.(type) {
	case *ast.Ident:
		return t.Name
	case *ast.StarExpr:
		return "*" + typeString(t.X)
	case *ast.SelectorExpr:
		return typeString(t.X) + "." + t.Sel.Name
	case *ast.ArrayType:
		return "[]" + typeString(t.Elt)
	case *ast.InterfaceType:
		return "any"
	}
	return ""
}

func analyzeQueries(fi *FileInfo, fd *ast.FuncDecl, cat *indexer.Catalog, off func(token.Pos) int,
	build func(ast.Expr, int, map[string][]ast.Expr) *Doc) {
	env := map[string][]ast.Expr{}
	types := map[string]string{}
	ast.Inspect(fd.Body, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.AssignStmt:
			if len(x.Lhs) != 1 || len(x.Rhs) != 1 {
				return true
			}
			id, ok := x.Lhs[0].(*ast.Ident)
			if !ok {
				return true
			}
			switch x.Tok {
			case token.ADD_ASSIGN:
				env[id.Name] = append(env[id.Name], x.Rhs[0])
			case token.DEFINE, token.ASSIGN:
				env[id.Name] = []ast.Expr{x.Rhs[0]}
				rhs := x.Rhs[0]
				if u, ok := rhs.(*ast.UnaryExpr); ok && u.Op == token.AND {
					rhs = u.X
				}
				switch r := rhs.(type) {
				case *ast.CompositeLit:
					if r.Type != nil {
						types[id.Name] = typeString(r.Type)
					}
				case *ast.CallExpr:
					if f, ok := r.Fun.(*ast.Ident); ok && f.Name == "make" && len(r.Args) > 0 {
						types[id.Name] = typeString(r.Args[0])
					}
				}
			}
		case *ast.ValueSpec:
			if x.Type != nil {
				for _, n := range x.Names {
					types[n.Name] = typeString(x.Type)
				}
			}
		}
		return true
	})
	// parameters
	if fd.Type.Params != nil {
		for _, p := range fd.Type.Params.List {
			for _, n := range p.Names {
				types[n.Name] = typeString(p.Type)
			}
		}
	}

	fnStart := off(fd.Pos())
	ast.Inspect(fd.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		qi, ok := queryMethods[sel.Sel.Name]
		if !ok || len(call.Args) <= qi {
			return true
		}
		recv, ok := sel.X.(*ast.CallExpr)
		if !ok {
			return true
		}
		rs, ok := recv.Fun.(*ast.SelectorExpr)
		if !ok || rs.Sel.Name != "DB" {
			return true
		}
		doc := build(call.Args[qi], fnStart, env)
		if len(doc.Segs) == 0 {
			return true
		}
		qc := &QueryCall{
			Method: sel.Sel.Name, FuncName: fd.Name.Name, FuncStart: fnStart,
			CallStart: off(call.Pos()), CallEnd: off(call.End()),
			ArgStart: off(call.Args[qi].Pos()), ArgEnd: off(call.Args[qi].End()),
			Doc: doc, NArgs: len(call.Args) - qi - 1,
		}
		if call.Ellipsis.IsValid() {
			qc.ArgsOpaque = true
		}
		for _, a := range call.Args[qi+1:] {
			if id, ok := a.(*ast.Ident); ok && strings.HasPrefix(types[id.Name], "[]") {
				qc.ArgsOpaque = true
			}
		}
		if qi == 1 {
			if u, ok := call.Args[0].(*ast.UnaryExpr); ok && u.Op == token.AND {
				if id, ok := u.X.(*ast.Ident); ok {
					t := strings.TrimLeft(types[id.Name], "[]*")
					if i := strings.LastIndex(t, "."); i >= 0 {
						t = t[i+1:]
					}
					if cat.Struct(t) != nil {
						qc.DestStruct = t
					}
				}
			}
			qc.Cols, qc.Star = ParseSelectList(doc.Text)
		}
		fi.Queries = append(fi.Queries, qc)
		return true
	})
}

var (
	selectRe = regexp.MustCompile(`(?i)^\s*SELECT\b(\s+DISTINCT\b)?`)
	itemRe   = regexp.MustCompile(`(?is)^(?:(\w+)\.)?(\w+)(?:\s+(?:AS\s+)?"?(\w+)"?)?$`)
	asRe     = regexp.MustCompile(`(?is)\sAS\s+"?(\w+)"?$`)
)

// ParseSelectList extracts output columns from a top-level SELECT.
func ParseSelectList(text string) (cols []OutCol, star bool) {
	m := selectRe.FindStringIndex(text)
	if m == nil {
		return nil, false
	}
	depth, start, inStr := 0, m[1], false
	var items [][2]int
	i := start
	for ; i < len(text); i++ {
		c := text[i]
		if inStr {
			if c == '\'' {
				inStr = false
			}
			continue
		}
		switch c {
		case '\'':
			inStr = true
		case '(':
			depth++
		case ')':
			depth--
		case ',':
			if depth == 0 {
				items = append(items, [2]int{start, i})
				start = i + 1
			}
		}
		if depth == 0 && (c == 'F' || c == 'f') && i+4 <= len(text) && strings.EqualFold(text[i:i+4], "FROM") &&
			(i == 0 || !isWord(text[i-1])) && (i+4 == len(text) || !isWord(text[i+4])) {
			break
		}
	}
	items = append(items, [2]int{start, i})
	for _, it := range items {
		raw := text[it[0]:it[1]]
		trim := strings.TrimSpace(raw)
		lead := it[0] + strings.Index(raw, trim)
		if trim == "" {
			continue
		}
		if trim == "*" || strings.HasSuffix(trim, ".*") {
			star = true
			continue
		}
		if mm := itemRe.FindStringSubmatchIndex(trim); mm != nil && !strings.Contains(trim, "__") {
			q, src := sub(trim, mm, 1), sub(trim, mm, 2)
			name := src
			ns, ne := mm[4], mm[5]
			if a := sub(trim, mm, 3); a != "" {
				name = a
				ns, ne = mm[6], mm[7]
			}
			cols = append(cols, OutCol{Name: name, Qualifier: strings.ToLower(q), SrcCol: src, VStart: lead + ns, VEnd: lead + ne})
			continue
		}
		if am := asRe.FindStringSubmatchIndex(trim); am != nil {
			cols = append(cols, OutCol{Name: trim[am[2]:am[3]], VStart: lead + am[2], VEnd: lead + am[3]})
		}
	}
	return cols, star
}

func sub(s string, m []int, g int) string {
	if m[2*g] < 0 {
		return ""
	}
	return s[m[2*g]:m[2*g+1]]
}

var (
	numPh    = regexp.MustCompile(`\$(\d+)`)
	strLit   = regexp.MustCompile(`'[^']*'`)
	leftJoin = regexp.MustCompile(`(?i)\b(?:LEFT|RIGHT|FULL)\s+(?:OUTER\s+)?JOIN\s+[\w."]+\s+(?:AS\s+)?([A-Za-z_]\w*)`)
)

// Placeholders returns the number of arguments the query expects, or -1 if unknown.
func (q *QueryCall) Placeholders() int {
	if q.Doc.Dynamic || ddl.MatchString(q.Doc.Text) {
		return -1
	}
	t := strLit.ReplaceAllString(q.Doc.Text, "''")
	max := 0
	for _, m := range numPh.FindAllStringSubmatch(t, -1) {
		n := 0
		for _, ch := range m[1] {
			n = n*10 + int(ch-'0')
		}
		if n > max {
			max = n
		}
	}
	if max > 0 {
		return max
	}
	return strings.Count(t, "?")
}

// NullableAliases returns aliases introduced by outer joins.
func (q *QueryCall) NullableAliases() map[string]bool {
	out := map[string]bool{}
	for _, m := range leftJoin.FindAllStringSubmatch(q.Doc.Text, -1) {
		out[strings.ToLower(m[1])] = true
	}
	return out
}
