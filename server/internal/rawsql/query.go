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
	Expr      string // expression text (e.g., "p.name", "COUNT(*)")
	IsAlias   bool   // true when explicitly aliased
	VStart    int    // virtual start of output name (alias if IsAlias, else column)
	VEnd      int    // virtual end of output name
	SrcVStart int    // virtual start of source column when IsAlias
	SrcVEnd   int    // virtual end of source column when IsAlias
}

// QueryCall is a facades.DB().Select/Insert/Update/... or facades.Orm().Query().Raw/... call.
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

var queryMethods = map[string]int{"Select": 1, "Insert": 0, "Update": 0, "Delete": 0, "Statement": 0, "Exec": 0, "Raw": 0}

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

	// Track .Raw(...).Scan(&dest) chains
	scanDestForRaw := map[*ast.CallExpr]ast.Expr{}
	ast.Inspect(fd.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || len(call.Args) == 0 {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || (sel.Sel.Name != "Scan" && sel.Sel.Name != "Find" && sel.Sel.Name != "First") {
			return true
		}
		if inner, ok := sel.X.(*ast.CallExpr); ok {
			if isel, ok := inner.Fun.(*ast.SelectorExpr); ok && isel.Sel.Name == "Raw" {
				scanDestForRaw[inner] = call.Args[0]
			}
		}
		return true
	})

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
		if !isDBOrOrmReceiver(sel.X) {
			return true
		}
		doc := build(call.Args[qi], fnStart, env)
		if len(doc.Segs) == 0 {
			return true
		}
		method := sel.Sel.Name
		if method == "Raw" {
			method = "Select"
		}
		qc := &QueryCall{
			Method: method, FuncName: fd.Name.Name, FuncStart: fnStart,
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
		var destArg ast.Expr
		if qi == 1 {
			destArg = call.Args[0]
		} else if da, ok := scanDestForRaw[call]; ok {
			destArg = da
		}
		if method == "Select" {
			if destArg != nil {
				if u, ok := destArg.(*ast.UnaryExpr); ok && u.Op == token.AND {
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
			}
			qc.Cols, qc.Star = ParseSelectList(doc.Text)
		}
		fi.Queries = append(fi.Queries, qc)
		return true
	})
}

func isDBOrOrmReceiver(e ast.Expr) bool {
	call, ok := e.(*ast.CallExpr)
	if !ok {
		return false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	if sel.Sel.Name == "DB" || sel.Sel.Name == "Query" {
		return true
	}
	return isDBOrOrmReceiver(sel.X)
}

var (
	selectRe     = regexp.MustCompile(`(?i)^\s*SELECT\b(\s+DISTINCT\b)?`)
	itemRe       = regexp.MustCompile(`(?is)^(?:(\w+)\.)?(\w+)(?:\s+(?:AS\s+)?"?(\w+)"?)?$`)
	asRe         = regexp.MustCompile(`(?is)\sAS\s+"?(\w+)"?$`)
	implicitAsRe = regexp.MustCompile(`(?is)\)\s+"?([A-Za-z_]\w*)"?$`)
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
			isAlias := false
			srcS, srcE := 0, 0
			expr := trim
			if a := sub(trim, mm, 3); a != "" {
				name = a
				isAlias = true
				srcS, srcE = lead+mm[4], lead+mm[5]
				ns, ne = mm[6], mm[7]
				if q != "" {
					expr = q + "." + src
				} else {
					expr = src
				}
			}
			cols = append(cols, OutCol{
				Name: name, Qualifier: strings.ToLower(q), SrcCol: src,
				Expr: expr, IsAlias: isAlias,
				VStart: lead + ns, VEnd: lead + ne,
				SrcVStart: srcS, SrcVEnd: srcE,
			})
			continue
		}
		if am := asRe.FindStringSubmatchIndex(trim); am != nil {
			cols = append(cols, OutCol{
				Name: trim[am[2]:am[3]], Expr: strings.TrimSpace(trim[:am[0]]),
				IsAlias: true, VStart: lead + am[2], VEnd: lead + am[3],
			})
			continue
		}
		if im := implicitAsRe.FindStringSubmatchIndex(trim); im != nil {
			alias := trim[im[2]:im[3]]
			if !notAlias[strings.ToLower(alias)] {
				cols = append(cols, OutCol{
					Name: alias, Expr: strings.TrimSpace(trim[:im[0]+1]),
					IsAlias: true, VStart: lead + im[2], VEnd: lead + im[3],
				})
			}
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
