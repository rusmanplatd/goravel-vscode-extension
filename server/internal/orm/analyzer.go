// Package orm provides AST-based intelligence for Goravel ORM query builders
// (facades.Orm().Query(), facades.DB().Table(), BuildPatchQuery*), Config keys
// (facades.Config()), and Request Validation rules (Rules(ctx), Validate()).
package orm

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/rusmanplatd/goravel-lsp/internal/indexer"
	"github.com/rusmanplatd/goravel-lsp/internal/rawsql"
)

// SpanKind identifies the kind of string literal span analyzed in Go code.
type SpanKind int

const (
	SpanORMWhere    SpanKind = iota // .Where("col = ?", x), .OrWhere, .Having
	SpanORMColumn                   // .Select("col"), .Group("col"), .Update("col", v), .Pluck("col", &x)
	SpanORMOrder                    // .Order("col DESC")
	SpanORMRelation                 // .With("Province.Country")
	SpanORMJoin                     // .Join("LEFT JOIN tbl alias ON a.id = b.id")
	SpanPatchKey                    // patches["col"] or map[string]any{"col": v} passed to BuildPatchQuery*
	SpanConfigKey                   // facades.Config().GetString("jwt.secret")
	SpanEnvKey                      // config.Env("JWT_SECRET")
	SpanRuleField                   // map key in Rules(ctx)
	SpanRuleValue                   // map value in Rules(ctx) ("required|integer|min:1")
	SpanRouteParam                  // ctx.Request().Route("id")
	SpanRequestInput                // ctx.Request().Input("name"), Query("page")
	SpanLangKey                     // facades.Lang(ctx).Get("messages.welcome")
	SpanGateAbility                 // facades.Gate().Allows("update-post")
)

// Span represents a string literal in a Go file with Goravel framework semantics.
type Span struct {
	Kind       SpanKind
	Start      int // byte offset after opening quote
	End        int // byte offset before closing quote
	CallEnd    int // byte offset of enclosing call end (for inlay hints)
	Text       string
	Table      *indexer.Table      // resolved table for ORM / Patch spans
	Aliases    map[string]*indexer.Table // table aliases in ORM chain (lowercase -> Table)
	ReqStruct  *indexer.StructInfo // resolved request struct for Rule spans
	Routes     []*indexer.RouteItem // matched routes for controller method
}

func (sp *Span) resolveTable(qual string) *indexer.Table {
	if qual != "" {
		lq := strings.ToLower(qual)
		if sp.Aliases != nil {
			if t := sp.Aliases[lq]; t != nil {
				return t
			}
		}
		if sp.Table != nil && strings.EqualFold(qual, sp.Table.Name) {
			return sp.Table
		}
		return nil
	}
	return sp.Table
}

// FileAnalysis holds all framework spans extracted from a Go source file.
type FileAnalysis struct {
	Path       string
	Src        []byte
	Spans      []*Span
	ExtraDiags []rawsql.Diag
}

var (
	ormColMethods     = map[string]bool{"Select": true, "Group": true, "Update": true, "Updates": true, "Pluck": true, "Omit": true, "Distinct": true, "WhereColumn": true, "Increment": true, "Decrement": true}
	ormWhereMethods   = map[string]bool{"Where": true, "OrWhere": true, "WhereNot": true, "WhereIn": true, "WhereNotIn": true, "WhereBetween": true, "WhereNull": true, "WhereNotNull": true, "Having": true}
	ormJoinMethods    = map[string]bool{"Join": true, "InnerJoin": true, "LeftJoin": true, "RightJoin": true}
	cfgGetMethods     = map[string]bool{"Get": true, "GetString": true, "GetInt": true, "GetBool": true, "GetDuration": true}
	routeParamMethods = map[string]bool{"Route": true, "RouteInt": true, "RouteInt64": true}
	reqInputMethods   = map[string]bool{"Input": true, "InputInt": true, "InputInt64": true, "InputBool": true, "InputArray": true, "InputMap": true, "Query": true, "QueryInt": true, "QueryInt64": true, "QueryBool": true, "QueryArray": true, "QueryMap": true, "File": true}
	gateCheckMethods  = map[string]bool{"Allows": true, "Denies": true, "Inspect": true, "Check": true, "Any": true, "None": true}
	swaggerRouterRe   = regexp.MustCompile(`@Router\s+(\S+)\s+\[([a-zA-Z]+)\]`)
	identRe           = regexp.MustCompile(`[A-Za-z_]\w*`)
	ormQualRe         = regexp.MustCompile(`([A-Za-z_]\w*)\.(\w*)$`)
	whereSkipWords  = map[string]bool{
		"and": true, "or": true, "not": true, "is": true, "null": true, "in": true, "like": true, "ilike": true,
		"between": true, "exists": true, "true": true, "false": true, "asc": true, "desc": true, "as": true,
		"now": true, "count": true, "sum": true, "avg": true, "min": true, "max": true, "coalesce": true,
		"lower": true, "upper": true, "date": true, "extract": true, "cast": true,
		"join": true, "inner": true, "left": true, "right": true, "outer": true, "cross": true, "on": true,
	}
)

// AnalyzeFile extracts ORM, Config, and Validation spans from a Go file.
func AnalyzeFile(path string, src []byte, cat *indexer.Catalog) *FileAnalysis {
	fa := &FileAnalysis{Path: path, Src: src}
	fset := token.NewFileSet()
	f, _ := parser.ParseFile(fset, path, src, parser.ParseComments|parser.SkipObjectResolution)
	if f == nil {
		return fa
	}
	tf := fset.File(f.Pos())
	if tf == nil {
		return fa
	}
	off := func(p token.Pos) int { return tf.Offset(p) }

	// 1. Collect receiver helper methods returning a table name (e.g. func (r *Repo) tableName(ctx) string)
	recvTable := map[string]*indexer.Table{}
	for _, decl := range f.Decls {
		fd, ok := decl.(*ast.FuncDecl)
		if !ok || fd.Recv == nil || fd.Body == nil {
			continue
		}
		rname := recvTypeName(fd.Recv)
		if rname == "" {
			continue
		}
		ast.Inspect(fd.Body, func(n ast.Node) bool {
			if call, ok := n.(*ast.CallExpr); ok {
				if t := tableFromExpr(call, nil, nil, cat); t != nil {
					recvTable[rname] = t
					return false
				}
			}
			return true
		})
	}

	// 2. Analyze each function declaration
	for _, decl := range f.Decls {
		fd, ok := decl.(*ast.FuncDecl)
		if !ok || fd.Body == nil {
			continue
		}
		rname := ""
		if fd.Recv != nil {
			rname = recvTypeName(fd.Recv)
		}
		defaultTable := recvTable[rname]
		var ctrlRoutes []*indexer.RouteItem
		if rname != "" && fd.Name != nil {
			ctrlRoutes = cat.RoutesForHandler(rname, fd.Name.Name)
			if len(ctrlRoutes) > 0 && fd.Doc != nil {
				checkSwaggerRouter(fa, off, fd.Doc, ctrlRoutes)
			}
		}

		// Handle Form Request Rules(ctx) methods
		if fd.Name.Name == "Rules" && rname != "" {
			reqSt := cat.Struct(rname)
			ast.Inspect(fd.Body, func(n ast.Node) bool {
				cl, ok := n.(*ast.CompositeLit)
				if !ok {
					return true
				}
				for _, elt := range cl.Elts {
					kv, ok := elt.(*ast.KeyValueExpr)
					if !ok {
						continue
					}
					if kl, ok := kv.Key.(*ast.BasicLit); ok && kl.Kind == token.STRING {
						addLitSpan(fa, off, kl, SpanRuleField, 0, nil, reqSt)
					}
					if vl, ok := kv.Value.(*ast.BasicLit); ok && vl.Kind == token.STRING {
						addLitSpan(fa, off, vl, SpanRuleValue, 0, nil, reqSt)
					}
				}
				return true
			})
		}

		// Collect local table variables, model variables, query variables, bound request struct, and patch maps in fd.Body
		varTable := map[string]*indexer.Table{}
		patchMaps := map[string]*ast.CompositeLit{}
		patchAssigns := map[string][]*ast.BasicLit{}
		var boundReqStruct *indexer.StructInfo

		ast.Inspect(fd.Body, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.SelectorExpr:
				if id, ok := x.X.(*ast.Ident); ok && id.Name == "requests" && boundReqStruct == nil {
					boundReqStruct = cat.Struct(x.Sel.Name)
				}
			case *ast.CallExpr:
				// Bind nested .With("Rel", func(q orm.Query) orm.Query { ... }) callback parameter to relation's target table
				if sel, ok := x.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "With" && len(x.Args) >= 2 {
					if bl, ok := x.Args[0].(*ast.BasicLit); ok && bl.Kind == token.STRING {
						if fn, ok := x.Args[1].(*ast.FuncLit); ok && fn.Type != nil && fn.Type.Params != nil && len(fn.Type.Params.List) > 0 {
							paramField := fn.Type.Params.List[0]
							if len(paramField.Names) > 0 {
								baseTbl := tableFromExpr(sel.X, varTable, recvTable, cat)
								if baseTbl == nil {
									baseTbl = defaultTable
								}
								if relPath, err := strconv.Unquote(bl.Value); err == nil && baseTbl != nil {
									if relTbl := resolveFinalRelationTable(baseTbl, relPath, cat); relTbl != nil {
										varTable[paramField.Names[0].Name] = relTbl
									}
								}
							}
						}
					}
				}
			case *ast.AssignStmt:
				if len(x.Lhs) == 1 && len(x.Rhs) == 1 {
					if id, ok := x.Lhs[0].(*ast.Ident); ok {
						if t := tableFromExpr(x.Rhs[0], varTable, recvTable, cat); t != nil {
							varTable[id.Name] = t
						} else if defaultTable != nil && isQueryExpr(x.Rhs[0]) {
							varTable[id.Name] = defaultTable
						}
						if cl, ok := x.Rhs[0].(*ast.CompositeLit); ok {
							patchMaps[id.Name] = cl
						}
					}
					if idx, ok := x.Lhs[0].(*ast.IndexExpr); ok {
						if id, ok := idx.X.(*ast.Ident); ok {
							if bl, ok := idx.Index.(*ast.BasicLit); ok && bl.Kind == token.STRING {
								patchAssigns[id.Name] = append(patchAssigns[id.Name], bl)
							}
						}
					}
				}
			case *ast.ValueSpec:
				if x.Type != nil {
					if t := tableFromTypeExpr(x.Type, cat); t != nil {
						for _, n := range x.Names {
							varTable[n.Name] = t
						}
					}
					if sel, ok := x.Type.(*ast.SelectorExpr); ok {
						if id, ok := sel.X.(*ast.Ident); ok && id.Name == "requests" && boundReqStruct == nil {
							boundReqStruct = cat.Struct(sel.Sel.Name)
						}
					}
				}
			}
			return true
		})

		// Second pass on fd.Body: inspect CallExprs for ORM chains, BuildPatchQuery*, Config, and Validate
		ast.Inspect(fd.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}

			// Check BuildPatchQuery / BuildPatchQueryWithWhere(tableName, id, patches, ...)
			if id, ok := call.Fun.(*ast.Ident); ok && strings.HasPrefix(id.Name, "BuildPatchQuery") && len(call.Args) >= 3 {
				t := tableFromExpr(call.Args[0], varTable, recvTable, cat)
				if t == nil {
					t = defaultTable
				}
				if t != nil {
					switch p := call.Args[2].(type) {
					case *ast.Ident:
						if cl := patchMaps[p.Name]; cl != nil {
							addMapKeySpans(fa, off, cl, SpanPatchKey, t)
						}
						for _, bl := range patchAssigns[p.Name] {
							addLitSpan(fa, off, bl, SpanPatchKey, 0, t, nil)
						}
					case *ast.CompositeLit:
						addMapKeySpans(fa, off, p, SpanPatchKey, t)
					}
				}
			}

			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			mname := sel.Sel.Name

			// Check facades.Config().Get*("...") and config.Env("...")
			if cfgGetMethods[mname] && len(call.Args) >= 1 && isConfigReceiver(sel.X) {
				if bl, ok := call.Args[0].(*ast.BasicLit); ok && bl.Kind == token.STRING {
					addLitSpan(fa, off, bl, SpanConfigKey, off(call.End()), nil, nil)
				}
				return true
			}
			if mname == "Env" && len(call.Args) >= 1 && isConfigReceiver(sel.X) {
				if bl, ok := call.Args[0].(*ast.BasicLit); ok && bl.Kind == token.STRING {
					addLitSpan(fa, off, bl, SpanEnvKey, off(call.End()), nil, nil)
				}
				return true
			}

			// Check facades.Lang(ctx).Get("...")
			if mname == "Get" && len(call.Args) >= 1 && isLangReceiver(sel.X) {
				if bl, ok := call.Args[0].(*ast.BasicLit); ok && bl.Kind == token.STRING {
					addLitSpan(fa, off, bl, SpanLangKey, off(call.End()), nil, nil)
				}
				return true
			}

			// Check facades.Gate().Allows("..."), Denies("..."), Inspect("..."), Check("..."), Any([]string{"..."}), None(...)
			if gateCheckMethods[mname] && len(call.Args) >= 1 && isGateReceiver(sel.X) {
				switch a0 := call.Args[0].(type) {
				case *ast.BasicLit:
					if a0.Kind == token.STRING {
						addLitSpan(fa, off, a0, SpanGateAbility, off(call.End()), nil, nil)
					}
				case *ast.CompositeLit:
					for _, elt := range a0.Elts {
						if bl, ok := elt.(*ast.BasicLit); ok && bl.Kind == token.STRING {
							addLitSpan(fa, off, bl, SpanGateAbility, 0, nil, nil)
						}
					}
				}
				return true
			}

			// Check ctx.Request().Route("id"), RouteInt("id"), RouteInt64("id")
			if routeParamMethods[mname] && len(call.Args) >= 1 && len(ctrlRoutes) > 0 && isRequestReceiver(sel.X) {
				if bl, ok := call.Args[0].(*ast.BasicLit); ok && bl.Kind == token.STRING {
					if raw, err := strconv.Unquote(bl.Value); err == nil {
						s := off(bl.Pos()) + 1
						fa.Spans = append(fa.Spans, &Span{
							Kind: SpanRouteParam, Start: s, End: s + len(raw), Text: raw, Routes: ctrlRoutes,
						})
					}
				}
				return true
			}

			// Check ctx.Request().Input("name"), Query("page"), etc.
			if reqInputMethods[mname] && len(call.Args) >= 1 && (boundReqStruct != nil || len(ctrlRoutes) > 0) && isRequestReceiver(sel.X) {
				if bl, ok := call.Args[0].(*ast.BasicLit); ok && bl.Kind == token.STRING {
					if raw, err := strconv.Unquote(bl.Value); err == nil {
						s := off(bl.Pos()) + 1
						fa.Spans = append(fa.Spans, &Span{
							Kind: SpanRequestInput, Start: s, End: s + len(raw), Text: raw, ReqStruct: boundReqStruct, Routes: ctrlRoutes,
						})
					}
				}
				return true
			}

			// Check ctx.Request().Validate(map[string]string{...}) or facades.Validation().Make(data, rules)
			if (mname == "Validate" && len(call.Args) >= 1) || (mname == "Make" && len(call.Args) >= 2 && isValidationReceiver(sel.X)) {
				argIdx := 0
				if mname == "Make" {
					argIdx = 1
				}
				if cl, ok := call.Args[argIdx].(*ast.CompositeLit); ok {
					for _, elt := range cl.Elts {
						if kv, ok := elt.(*ast.KeyValueExpr); ok {
							if vl, ok := kv.Value.(*ast.BasicLit); ok && vl.Kind == token.STRING {
								addLitSpan(fa, off, vl, SpanRuleValue, 0, nil, nil)
							}
						}
					}
				}
			}

			// Check ORM methods: .Where, .Order, .Select, .With, .Join, etc. (exclude direct facades.DB().Select/Update)
			if isDirectDBCall(sel.X) {
				return true
			}
			if ormWhereMethods[mname] || ormColMethods[mname] || ormJoinMethods[mname] || mname == "Order" || mname == "With" {
				t := tableFromExpr(call, varTable, recvTable, cat)
				if t == nil && isQueryChain(sel.X) {
					t = defaultTable
				}
				if t == nil {
					return true
				}
				aliases := aliasesFromExpr(call, varTable, recvTable, cat)
				prevLen := len(fa.Spans)
				switch {
				case ormJoinMethods[mname] && len(call.Args) >= 1:
					if bl, ok := call.Args[0].(*ast.BasicLit); ok && bl.Kind == token.STRING {
						addLitSpan(fa, off, bl, SpanORMJoin, 0, t, nil)
					}
				case ormWhereMethods[mname] && len(call.Args) >= 1:
					switch a0 := call.Args[0].(type) {
					case *ast.BasicLit:
						if a0.Kind == token.STRING {
							addLitSpan(fa, off, a0, SpanORMWhere, 0, t, nil)
						}
					case *ast.CompositeLit:
						addMapKeySpans(fa, off, a0, SpanPatchKey, t)
					}
				case mname == "Order" && len(call.Args) >= 1:
					if bl, ok := call.Args[0].(*ast.BasicLit); ok && bl.Kind == token.STRING {
						addLitSpan(fa, off, bl, SpanORMOrder, 0, t, nil)
					}
				case mname == "With" && len(call.Args) >= 1:
					if bl, ok := call.Args[0].(*ast.BasicLit); ok && bl.Kind == token.STRING {
						addLitSpan(fa, off, bl, SpanORMRelation, 0, t, nil)
					}
				case ormColMethods[mname]:
					for i, arg := range call.Args {
						if (mname == "Update" || mname == "Pluck" || mname == "Increment" || mname == "Decrement") && i > 0 {
							break
						}
						switch ax := arg.(type) {
						case *ast.BasicLit:
							if ax.Kind == token.STRING {
								addLitSpan(fa, off, ax, SpanORMColumn, 0, t, nil)
							}
						case *ast.CompositeLit:
							addMapKeySpans(fa, off, ax, SpanPatchKey, t)
						}
					}
				}
				if len(aliases) > 0 {
					for i := prevLen; i < len(fa.Spans); i++ {
						fa.Spans[i].Aliases = aliases
					}
				}
			}
			return true
		})
	}
	return fa
}

func addMapKeySpans(fa *FileAnalysis, off func(token.Pos) int, cl *ast.CompositeLit, kind SpanKind, t *indexer.Table) {
	for _, elt := range cl.Elts {
		if kv, ok := elt.(*ast.KeyValueExpr); ok {
			if kl, ok := kv.Key.(*ast.BasicLit); ok && kl.Kind == token.STRING {
				addLitSpan(fa, off, kl, kind, 0, t, nil)
			}
		}
	}
}

func addLitSpan(fa *FileAnalysis, off func(token.Pos) int, bl *ast.BasicLit, kind SpanKind, callEnd int, t *indexer.Table, req *indexer.StructInfo) {
	s := off(bl.Pos()) + 1
	e := off(bl.End()) - 1
	if e < s {
		return
	}
	val, err := strconv.Unquote(bl.Value)
	if err != nil {
		val = string(fa.Src[s:e])
	}
	fa.Spans = append(fa.Spans, &Span{
		Kind:      kind,
		Start:     s,
		End:       e,
		CallEnd:   callEnd,
		Text:      val,
		Table:     t,
		ReqStruct: req,
	})
}

func isConfigReceiver(e ast.Expr) bool {
	switch x := e.(type) {
	case *ast.Ident:
		return x.Name == "config" || x.Name == "cfg"
	case *ast.CallExpr:
		if sel, ok := x.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "Config" {
			return true
		}
	}
	return false
}

func isValidationReceiver(e ast.Expr) bool {
	call, ok := e.(*ast.CallExpr)
	if !ok {
		return false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	return ok && sel.Sel.Name == "Validation"
}

func isLangReceiver(e ast.Expr) bool {
	call, ok := e.(*ast.CallExpr)
	if !ok {
		return false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	return ok && sel.Sel.Name == "Lang"
}

func isGateReceiver(e ast.Expr) bool {
	call, ok := e.(*ast.CallExpr)
	if !ok {
		return false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	return ok && sel.Sel.Name == "Gate"
}

func resolveFinalRelationTable(base *indexer.Table, relPath string, cat *indexer.Catalog) *indexer.Table {
	curr := base
	for _, part := range strings.Split(relPath, ".") {
		if curr == nil {
			return nil
		}
		r := curr.Relation(part)
		if r == nil {
			return nil
		}
		curr = cat.Model(r.TargetModel)
		if curr == nil && r.TargetTable != "" {
			curr = cat.Table(r.TargetTable)
		}
	}
	return curr
}

func isDirectDBCall(e ast.Expr) bool {
	call, ok := e.(*ast.CallExpr)
	if !ok {
		return false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	return ok && sel.Sel.Name == "DB"
}

func isQueryChain(e ast.Expr) bool {
	switch x := e.(type) {
	case *ast.Ident:
		return x.Name == "query" || x.Name == "q" || x.Name == "tx" || x.Name == "db"
	case *ast.CallExpr:
		return isQueryExpr(x)
	}
	return false
}

func isQueryExpr(e ast.Expr) bool {
	call, ok := e.(*ast.CallExpr)
	if !ok {
		return false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	switch sel.Sel.Name {
	case "Query", "Table", "Model", "Where", "OrWhere", "Select", "Order", "With", "scoped", "build":
		return true
	}
	return isQueryExpr(sel.X)
}

func tableFromExpr(e ast.Expr, varTable map[string]*indexer.Table, recvTable map[string]*indexer.Table, cat *indexer.Catalog) *indexer.Table {
	switch x := e.(type) {
	case *ast.Ident:
		if varTable != nil {
			if t := varTable[x.Name]; t != nil {
				return t
			}
		}
		if strings.HasSuffix(x.Name, "OriginalTableName") {
			// Look up via Models
			modelName := strings.TrimSuffix(x.Name, "OriginalTableName")
			if t := cat.Model(modelName); t != nil {
				return t
			}
		}
	case *ast.BasicLit:
		if x.Kind == token.STRING {
			if s, err := strconv.Unquote(x.Value); err == nil {
				tblName, _ := parseTableAndAlias(s)
				return cat.Table(tblName)
			}
		}
	case *ast.BinaryExpr:
		if x.Op == token.ADD {
			if t := tableFromExpr(x.X, varTable, recvTable, cat); t != nil {
				return t
			}
		}
	case *ast.UnaryExpr:
		return tableFromExpr(x.X, varTable, recvTable, cat)
	case *ast.ParenExpr:
		return tableFromExpr(x.X, varTable, recvTable, cat)
	case *ast.CompositeLit:
		return tableFromTypeExpr(x.Type, cat)
	case *ast.CallExpr:
		sel, ok := x.Fun.(*ast.SelectorExpr)
		if !ok {
			return nil
		}
		switch sel.Sel.Name {
		case "TableName":
			return tableFromExpr(sel.X, varTable, recvTable, cat)
		case "Table":
			if len(x.Args) > 0 {
				if t := tableFromExpr(x.Args[0], varTable, recvTable, cat); t != nil {
					return t
				}
			}
		case "Model":
			if len(x.Args) > 0 {
				if t := tableFromExpr(x.Args[0], varTable, recvTable, cat); t != nil {
					return t
				}
			}
		case "Find", "First", "Get", "Scan":
			if len(x.Args) > 0 {
				if t := tableFromExpr(x.Args[0], varTable, recvTable, cat); t != nil {
					return t
				}
			}
		}
		if t := tableFromExpr(sel.X, varTable, recvTable, cat); t != nil {
			return t
		}
	}
	return nil
}

func parseTableAndAlias(s string) (tableName, alias string) {
	fields := strings.Fields(strings.TrimSpace(s))
	if len(fields) == 0 {
		return "", ""
	}
	tableName = fields[0]
	if len(fields) >= 3 && strings.EqualFold(fields[1], "AS") {
		alias = strings.ToLower(fields[2])
	} else if len(fields) >= 2 && !strings.EqualFold(fields[1], "ON") && !strings.EqualFold(fields[1], "WHERE") {
		alias = strings.ToLower(fields[1])
	}
	return tableName, alias
}

var ormJoinAliasRe = regexp.MustCompile(`(?i)\b(?:JOIN\s+)?([A-Za-z_][\w."]*)(?:\s+(?:AS\s+)?([A-Za-z_]\w*))?(?:\s+ON\b|$)`)

func aliasesFromExpr(e ast.Expr, varTable map[string]*indexer.Table, recvTable map[string]*indexer.Table, cat *indexer.Catalog) map[string]*indexer.Table {
	out := map[string]*indexer.Table{}
	var walk func(ex ast.Expr)
	walk = func(ex ast.Expr) {
		call, ok := ex.(*ast.CallExpr)
		if !ok {
			return
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return
		}
		walk(sel.X)
		switch sel.Sel.Name {
		case "Table":
			if len(call.Args) > 0 {
				if bl, ok := call.Args[0].(*ast.BasicLit); ok && bl.Kind == token.STRING {
					if s, err := strconv.Unquote(bl.Value); err == nil {
						tn, al := parseTableAndAlias(s)
						if t := cat.Table(tn); t != nil {
							out[strings.ToLower(indexer.StripSchema(tn))] = t
							if al != "" {
								out[al] = t
							}
						}
					}
				} else if bin, ok := call.Args[0].(*ast.BinaryExpr); ok && bin.Op == token.ADD {
					if t := tableFromExpr(bin.X, varTable, recvTable, cat); t != nil {
						if bl, ok := bin.Y.(*ast.BasicLit); ok && bl.Kind == token.STRING {
							if s, err := strconv.Unquote(bl.Value); err == nil {
								_, al := parseTableAndAlias("tbl " + strings.TrimSpace(s))
								if strings.HasPrefix(strings.ToUpper(strings.TrimSpace(s)), "AS ") {
									_, al = parseTableAndAlias("tbl " + s)
								}
								if al != "" {
									out[al] = t
								}
							}
						}
					}
				}
			}
		case "Join", "InnerJoin", "LeftJoin", "RightJoin":
			if len(call.Args) > 0 {
				if bl, ok := call.Args[0].(*ast.BasicLit); ok && bl.Kind == token.STRING {
					if s, err := strconv.Unquote(bl.Value); err == nil {
						if m := ormJoinAliasRe.FindStringSubmatch(s); m != nil {
							if t := cat.Table(m[1]); t != nil {
								out[strings.ToLower(indexer.StripSchema(m[1]))] = t
								if m[2] != "" && !strings.EqualFold(m[2], "ON") {
									out[strings.ToLower(m[2])] = t
								}
							}
						}
					}
				}
			}
		}
	}
	walk(e)
	return out
}

func tableFromTypeExpr(e ast.Expr, cat *indexer.Catalog) *indexer.Table {
	if e == nil {
		return nil
	}
	switch t := e.(type) {
	case *ast.Ident:
		return cat.Model(t.Name)
	case *ast.SelectorExpr:
		return cat.Model(t.Sel.Name)
	case *ast.StarExpr:
		return tableFromTypeExpr(t.X, cat)
	case *ast.ArrayType:
		return tableFromTypeExpr(t.Elt, cat)
	}
	return nil
}

func recvTypeName(fl *ast.FieldList) string {
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

// SpanAt returns the Span containing Go byte offset goOff.
func (fa *FileAnalysis) SpanAt(goOff int) *Span {
	for _, s := range fa.Spans {
		if goOff >= s.Start && goOff <= s.End {
			return s
		}
	}
	return nil
}

// Completion returns LSP completion items at Go byte offset goOff if inside a framework Span.
func (fa *FileAnalysis) Completion(goOff int, cat *indexer.Catalog) ([]map[string]any, bool) {
	sp := fa.SpanAt(goOff)
	if sp == nil {
		return nil, false
	}
	var items []map[string]any
	switch sp.Kind {
	case SpanORMWhere, SpanORMColumn, SpanORMOrder, SpanPatchKey:
		if sp.Table == nil {
			return nil, false
		}
		targetTable := sp.Table
		relOff := goOff - sp.Start
		if relOff >= 0 && relOff <= len(sp.Text) {
			if qm := ormQualRe.FindStringSubmatch(sp.Text[:relOff]); qm != nil {
				if qt := sp.resolveTable(qm[1]); qt != nil {
					targetTable = qt
				} else {
					return nil, false
				}
			}
		}
		for _, c := range targetTable.Columns {
			items = append(items, map[string]any{
				"label": c.Name, "kind": 5,
				"detail":        colTypeDetail(c),
				"documentation": map[string]any{"kind": "markdown", "value": colDocMD(targetTable, c)},
			})
		}
		if sp.Kind == SpanORMOrder {
			for _, kw := range []string{"ASC", "DESC", "NULLS FIRST", "NULLS LAST"} {
				items = append(items, map[string]any{"label": kw, "kind": 14})
			}
		}
		return items, true

	case SpanORMJoin:
		relOff := goOff - sp.Start
		before := ""
		if relOff >= 0 && relOff <= len(sp.Text) {
			before = sp.Text[:relOff]
		}
		if qm := ormQualRe.FindStringSubmatch(before); qm != nil {
			if qt := sp.resolveTable(qm[1]); qt != nil {
				for _, c := range qt.Columns {
					items = append(items, map[string]any{
						"label": c.Name, "kind": 5, "detail": colTypeDetail(c),
						"documentation": map[string]any{"kind": "markdown", "value": colDocMD(qt, c)},
					})
				}
				return items, true
			}
		}
		if strings.Contains(strings.ToUpper(before), " ON ") || strings.HasSuffix(strings.ToUpper(strings.TrimSpace(before)), "ON") {
			if m := ormJoinAliasRe.FindStringSubmatch(sp.Text); m != nil {
				if jt := cat.Table(m[1]); jt != nil {
					jAlias := m[2]
					if jAlias == "" || strings.EqualFold(jAlias, "ON") {
						jAlias = jt.Name
					}
					sc := rawsql.Scope{}
					if sp.Table != nil {
						sc[strings.ToLower(sp.Table.Name)] = sp.Table
					}
					for k, v := range sp.Aliases {
						sc[k] = v
					}
					for _, cond := range rawsql.JoinConditions(jt, jAlias, sc) {
						items = append(items, map[string]any{
							"label": cond, "kind": 15, "detail": "Foreign Key Join Condition", "sortText": "0_" + cond,
						})
					}
					for _, c := range jt.Columns {
						items = append(items, map[string]any{
							"label": c.Name, "kind": 5, "detail": colTypeDetail(c),
						})
					}
				}
			}
			if sp.Table != nil {
				for _, c := range sp.Table.Columns {
					items = append(items, map[string]any{
						"label": c.Name, "kind": 5, "detail": colTypeDetail(c),
					})
				}
			}
			return items, true
		}
		for _, tn := range cat.TableNames() {
			t := cat.Table(tn)
			detail := "table"
			if t != nil && t.Model != "" {
				detail = "models." + t.Model
			}
			items = append(items, map[string]any{"label": tn, "kind": 7, "detail": detail})
		}
		return items, true

	case SpanORMRelation:
		t := resolveRelationChainTable(sp.Table, sp.Text[:goOff-sp.Start], cat)
		if t == nil {
			return nil, false
		}
		for _, r := range t.Relations {
			if r.Name == "" {
				continue
			}
			items = append(items, map[string]any{
				"label": r.Name, "kind": 8,
				"detail": fmt.Sprintf("-> models.%s (%s.%s)", r.TargetModel, r.TargetTable, r.ForeignKey),
			})
		}
		return items, true

	case SpanConfigKey:
		for _, k := range cat.ConfigKeys() {
			ci := cat.Config(k)
			detail := ci.Default
			if ci.EnvKey != "" {
				detail = fmt.Sprintf("Env(%s, %s)", ci.EnvKey, ci.Default)
			}
			items = append(items, map[string]any{
				"label": k, "kind": 21, "detail": detail,
				"documentation": map[string]any{"kind": "markdown", "value": configDocMD(ci)},
			})
		}
		return items, true

	case SpanEnvKey:
		var keys []string
		for k := range cat.EnvVars {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			items = append(items, map[string]any{
				"label": k, "kind": 21, "detail": cat.EnvVars[k],
			})
		}
		return items, true

	case SpanRuleField:
		if sp.ReqStruct == nil {
			return nil, false
		}
		for _, f := range sp.ReqStruct.Fields {
			items = append(items, map[string]any{
				"label": f.Col, "kind": 5, "detail": fmt.Sprintf("%s.%s (%s)", sp.ReqStruct.Name, f.GoName, f.GoType),
			})
		}
		return items, true

	case SpanRuleValue:
		before := sp.Text[:goOff-sp.Start]
		seg := before
		if i := strings.LastIndexByte(before, '|'); i >= 0 {
			seg = before[i+1:]
		}
		// Check `exists:table,col` or `unique:table,col`
		if strings.HasPrefix(seg, "exists:") || strings.HasPrefix(seg, "unique:") {
			arg := seg[strings.IndexByte(seg, ':')+1:]
			if comma := strings.IndexByte(arg, ','); comma >= 0 {
				tname := arg[:comma]
				if t := cat.Table(tname); t != nil {
					for _, c := range t.Columns {
						items = append(items, map[string]any{"label": c.Name, "kind": 5, "detail": colTypeDetail(c)})
					}
					return items, true
				}
			} else {
				for _, tn := range cat.TableNames() {
					items = append(items, map[string]any{"label": tn, "kind": 7, "detail": "table"})
				}
				return items, true
			}
		}
		for _, rn := range cat.RuleNames() {
			ri := cat.Rule(rn)
			detail := "built-in rule"
			if ri.Custom {
				detail = "custom rule (app/rules)"
			}
			items = append(items, map[string]any{
				"label": rn, "kind": 12, "detail": detail,
				"documentation": map[string]any{"kind": "markdown", "value": ruleDocMD(ri)},
			})
		}
		return items, true

	case SpanRouteParam:
		seen := map[string]bool{}
		for _, r := range sp.Routes {
			for _, p := range r.Params {
				if seen[p] {
					continue
				}
				seen[p] = true
				items = append(items, map[string]any{
					"label":  p,
					"kind":   6,
					"detail": fmt.Sprintf("Route param (%s %s)", r.Method, r.Path),
				})
			}
		}
		return items, true

	case SpanRequestInput:
		seen := map[string]bool{}
		if sp.ReqStruct != nil {
			for _, f := range sp.ReqStruct.Fields {
				seen[f.Col] = true
				items = append(items, map[string]any{
					"label":  f.Col,
					"kind":   5,
					"detail": fmt.Sprintf("%s.%s (%s)", sp.ReqStruct.Name, f.GoName, f.GoType),
				})
			}
		}
		for _, r := range sp.Routes {
			for _, p := range r.Params {
				if seen[p] {
					continue
				}
				seen[p] = true
				items = append(items, map[string]any{
					"label":  p,
					"kind":   6,
					"detail": fmt.Sprintf("Route param (%s %s)", r.Method, r.Path),
				})
			}
		}
		return items, len(items) > 0

	case SpanLangKey:
		for _, k := range cat.TranslationKeys() {
			locales := cat.Translations[k]
			preview := ""
			if en, ok := locales["en"]; ok {
				preview = en.Value
			} else {
				for _, v := range locales {
					preview = v.Value
					break
				}
			}
			items = append(items, map[string]any{
				"label":         k,
				"kind":          21,
				"detail":        preview,
				"documentation": map[string]any{"kind": "markdown", "value": langDocMD(k, locales)},
			})
		}
		return items, true

	case SpanGateAbility:
		for _, ab := range cat.GateAbilities() {
			gi := cat.Gates[ab]
			detail := "Gate ability"
			if gi != nil && gi.Loc.Valid() {
				detail = fmt.Sprintf("Gate (%s:%d)", gi.Loc.File, gi.Loc.Line+1)
			}
			items = append(items, map[string]any{
				"label":  ab,
				"kind":   12,
				"detail": detail,
			})
		}
		return items, true
	}
	return nil, false
}

// Hover returns Markdown hover contents and byte range [start, end] at goOff.
func (fa *FileAnalysis) Hover(goOff int, cat *indexer.Catalog) (md string, start, end int, ok bool) {
	sp := fa.SpanAt(goOff)
	if sp == nil {
		return "", 0, 0, false
	}
	relOff := goOff - sp.Start
	switch sp.Kind {
	case SpanORMWhere, SpanORMColumn, SpanORMOrder, SpanPatchKey:
		if sp.Table == nil {
			return "", 0, 0, false
		}
		w, ws, we := wordAt(sp.Text, relOff)
		if w == "" {
			return "", 0, 0, false
		}
		targetTable := sp.Table
		if ws > 0 && sp.Text[ws-1] == '.' {
			qw, _, _ := wordAt(sp.Text, ws-2)
			if qt := sp.resolveTable(qw); qt != nil {
				targetTable = qt
			}
		}
		if col := targetTable.Column(w); col != nil {
			return colDocMD(targetTable, col), sp.Start + ws, sp.Start + we, true
		}

	case SpanORMJoin:
		w, ws, we := wordAt(sp.Text, relOff)
		if w == "" {
			return "", 0, 0, false
		}
		if ws > 0 && sp.Text[ws-1] == '.' {
			qw, _, _ := wordAt(sp.Text, ws-2)
			if qt := sp.resolveTable(qw); qt != nil {
				if col := qt.Column(w); col != nil {
					return colDocMD(qt, col), sp.Start + ws, sp.Start + we, true
				}
			}
		}
		if t := cat.Table(w); t != nil {
			return fmt.Sprintf("**Table `%s`** (`models.%s`, %d columns)", t.Name, t.Model, len(t.Columns)), sp.Start + ws, sp.Start + we, true
		}
		if sp.Table != nil {
			if col := sp.Table.Column(w); col != nil {
				return colDocMD(sp.Table, col), sp.Start + ws, sp.Start + we, true
			}
		}

	case SpanORMRelation:
		r, rs, re := relationAt(sp.Table, sp.Text, relOff, cat)
		if r != nil {
			var b strings.Builder
			fmt.Fprintf(&b, "**Relation `%s`** → `models.%s` (`%s`)\n\n", r.Name, r.TargetModel, r.TargetTable)
			if r.ForeignKey != "" {
				fmt.Fprintf(&b, "- Foreign Key: `%s` → `%s.%s`\n", r.ForeignKey, r.TargetTable, r.References)
			}
			return b.String(), sp.Start + rs, sp.Start + re, true
		}

	case SpanConfigKey:
		if ci := cat.Config(sp.Text); ci != nil {
			return configDocMD(ci), sp.Start, sp.End, true
		}
		if val, exists := cat.EnvVars[sp.Text]; exists {
			return fmt.Sprintf("**Env `%s`**\n\n- Value in `.env*`: `%s`", sp.Text, val), sp.Start, sp.End, true
		}

	case SpanEnvKey:
		if val, exists := cat.EnvVars[sp.Text]; exists {
			return fmt.Sprintf("**Env `%s`**\n\n- Value in `.env*`: `%s`", sp.Text, val), sp.Start, sp.End, true
		}

	case SpanRuleField, SpanRequestInput:
		if sp.ReqStruct != nil {
			if f := sp.ReqStruct.Has(sp.Text); f != nil {
				return fmt.Sprintf("**`%s.%s`** (`%s`)", sp.ReqStruct.Name, f.GoName, f.GoType), sp.Start, sp.End, true
			}
		}
		if sp.Kind == SpanRequestInput {
			for _, r := range sp.Routes {
				for _, p := range r.Params {
					if p == sp.Text {
						return fmt.Sprintf("**Route Parameter `%s`** (`%s %s`)", sp.Text, r.Method, r.Path), sp.Start, sp.End, true
					}
				}
			}
		}

	case SpanRuleValue:
		if tbl, col, rs, re, ok := ruleDBRefAt(sp.Text, relOff, cat); ok {
			if col != nil {
				return colDocMD(tbl, col), sp.Start + rs, sp.Start + re, true
			}
			if tbl != nil {
				return fmt.Sprintf("**Table `%s`** (%d columns)", tbl.Name, len(tbl.Columns)), sp.Start + rs, sp.Start + re, true
			}
		}
		rname, rs, re := ruleSegmentAt(sp.Text, relOff)
		if ri := cat.Rule(rname); ri != nil {
			return ruleDocMD(ri), sp.Start + rs, sp.Start + re, true
		}

	case SpanRouteParam:
		var lines []string
		for _, r := range sp.Routes {
			lines = append(lines, fmt.Sprintf("- `%s %s`", r.Method, r.Path))
		}
		return fmt.Sprintf("**Route Parameter `%s`**\n\n%s", sp.Text, strings.Join(lines, "\n")), sp.Start, sp.End, true

	case SpanLangKey:
		if locales, exists := cat.Translations[sp.Text]; exists && len(locales) > 0 {
			return langDocMD(sp.Text, locales), sp.Start, sp.End, true
		}

	case SpanGateAbility:
		if gi, exists := cat.Gates[sp.Text]; exists && gi != nil {
			return fmt.Sprintf("**Gate Ability `%s`**\n\n- Defined at `%s:%d`", gi.Ability, gi.Loc.File, gi.Loc.Line+1), sp.Start, sp.End, true
		}
	}
	return "", 0, 0, false
}

// Definition returns target locations for Go-to-Definition at goOff.
func (fa *FileAnalysis) Definition(goOff int, cat *indexer.Catalog) []indexer.Location {
	sp := fa.SpanAt(goOff)
	if sp == nil {
		return nil
	}
	relOff := goOff - sp.Start
	var locs []indexer.Location
	add := func(l indexer.Location) {
		if l.Valid() {
			locs = append(locs, l)
		}
	}
	switch sp.Kind {
	case SpanORMWhere, SpanORMColumn, SpanORMOrder, SpanPatchKey:
		if sp.Table == nil {
			return nil
		}
		w, ws, _ := wordAt(sp.Text, relOff)
		targetTable := sp.Table
		if ws > 0 && sp.Text[ws-1] == '.' {
			qw, _, _ := wordAt(sp.Text, ws-2)
			if qt := sp.resolveTable(qw); qt != nil {
				targetTable = qt
			}
		}
		if col := targetTable.Column(w); col != nil {
			add(col.Loc)
			add(col.MigLoc)
		}

	case SpanORMJoin:
		w, ws, _ := wordAt(sp.Text, relOff)
		if ws > 0 && sp.Text[ws-1] == '.' {
			qw, _, _ := wordAt(sp.Text, ws-2)
			if qt := sp.resolveTable(qw); qt != nil {
				if col := qt.Column(w); col != nil {
					add(col.Loc)
					add(col.MigLoc)
					return locs
				}
			}
		}
		if t := cat.Table(w); t != nil {
			add(t.Loc)
			add(t.MigLoc)
		} else if sp.Table != nil {
			if col := sp.Table.Column(w); col != nil {
				add(col.Loc)
				add(col.MigLoc)
			}
		}

	case SpanORMRelation:
		r, _, _ := relationAt(sp.Table, sp.Text, relOff, cat)
		if r != nil {
			add(r.Loc)
			if target := cat.Model(r.TargetModel); target != nil {
				add(target.Loc)
			}
		}

	case SpanConfigKey:
		if ci := cat.Config(sp.Text); ci != nil {
			add(ci.Loc)
		}

	case SpanRuleField, SpanRequestInput:
		if sp.ReqStruct != nil {
			if f := sp.ReqStruct.Has(sp.Text); f != nil {
				add(f.Loc)
			}
		}
		if sp.Kind == SpanRequestInput && len(locs) == 0 {
			for _, r := range sp.Routes {
				for _, p := range r.Params {
					if p == sp.Text {
						add(r.Loc)
					}
				}
			}
		}

	case SpanRuleValue:
		if tbl, col, _, _, ok := ruleDBRefAt(sp.Text, relOff, cat); ok {
			if col != nil {
				add(col.Loc)
				add(col.MigLoc)
			} else if tbl != nil {
				add(tbl.Loc)
				add(tbl.MigLoc)
			}
			return locs
		}
		rname, _, _ := ruleSegmentAt(sp.Text, relOff)
		if ri := cat.Rule(rname); ri != nil && ri.Loc.Valid() {
			add(ri.Loc)
		}

	case SpanRouteParam:
		for _, r := range sp.Routes {
			add(r.Loc)
		}

	case SpanLangKey:
		if locales, exists := cat.Translations[sp.Text]; exists {
			var keys []string
			for l := range locales {
				keys = append(keys, l)
			}
			sort.Strings(keys)
			for _, l := range keys {
				add(locales[l].Loc)
			}
		}

	case SpanGateAbility:
		if gi, exists := cat.Gates[sp.Text]; exists && gi != nil {
			add(gi.Loc)
		}
	}
	return locs
}

// Diagnostics validates ORM columns, relations, joins, patch keys, config/env keys, lang keys, gates, and validation rules.
func (fa *FileAnalysis) Diagnostics(cat *indexer.Catalog) []rawsql.Diag {
	out := append([]rawsql.Diag(nil), fa.ExtraDiags...)
	for _, sp := range fa.Spans {
		switch sp.Kind {
		case SpanORMColumn, SpanPatchKey:
			if sp.Table == nil || strings.Contains(sp.Text, "(") || strings.Contains(sp.Text, "*") {
				continue
			}
			for _, part := range strings.Split(sp.Text, ",") {
				colExpr := strings.TrimSpace(part)
				if colExpr == "" {
					continue
				}
				// Strip column alias: `col AS alias` or `col alias`
				fields := strings.Fields(colExpr)
				col := fields[0]
				if len(fields) > 1 && !(len(fields) == 2 || (len(fields) == 3 && strings.EqualFold(fields[1], "AS"))) {
					continue
				}
				targetTable := sp.Table
				if i := strings.LastIndexByte(col, '.'); i >= 0 {
					qual := col[:i]
					col = col[i+1:]
					if qt := sp.resolveTable(qual); qt != nil {
						targetTable = qt
					}
				}
				if col != "" && targetTable.Column(col) == nil {
					out = append(out, rawsql.Diag{
						Start: sp.Start, End: sp.End, Severity: 1,
						Message: fmt.Sprintf("Column '%s' does not exist on table '%s'", col, targetTable.Name),
					})
				}
			}

		case SpanORMOrder:
			if sp.Table == nil {
				continue
			}
			fields := strings.Fields(sp.Text)
			if len(fields) > 0 {
				col := fields[0]
				if i := strings.LastIndexByte(col, '.'); i >= 0 {
					col = col[i+1:]
				}
				if !strings.Contains(col, "(") && sp.Table.Column(col) == nil {
					out = append(out, rawsql.Diag{
						Start: sp.Start, End: sp.Start + len(fields[0]), Severity: 1,
						Message: fmt.Sprintf("Column '%s' does not exist on table '%s'", col, sp.Table.Name),
					})
				}
			}

		case SpanORMWhere:
			if sp.Table == nil {
				continue
			}
			// Mask single-quoted strings before scanning identifiers
			masked := strMask(sp.Text)
			for _, m := range identRe.FindAllStringIndex(masked, -1) {
				w := masked[m[0]:m[1]]
				lw := strings.ToLower(w)
				if whereSkipWords[lw] || (w[0] >= '0' && w[0] <= '9') {
					continue
				}
				// Skip if followed by '(' (SQL function) or '.' (table qualifier)
				if m[1] < len(masked) && (masked[m[1]] == '(' || masked[m[1]] == '.') {
					continue
				}
				targetTable := sp.Table
				if m[0] > 0 && masked[m[0]-1] == '.' {
					qw, _, _ := wordAt(masked, m[0]-2)
					if qt := sp.resolveTable(qw); qt != nil {
						targetTable = qt
					}
				}
				if targetTable.Column(w) == nil {
					out = append(out, rawsql.Diag{
						Start: sp.Start + m[0], End: sp.Start + m[1], Severity: 1,
						Message: fmt.Sprintf("Column '%s' does not exist on table '%s'", w, targetTable.Name),
					})
				}
			}

		case SpanORMJoin:
			if len(cat.Tables) == 0 {
				continue
			}
			if m := ormJoinAliasRe.FindStringSubmatchIndex(sp.Text); m != nil && m[2] >= 0 {
				tname := sp.Text[m[2]:m[3]]
				if cat.Table(tname) == nil {
					out = append(out, rawsql.Diag{
						Start: sp.Start + m[2], End: sp.Start + m[3], Severity: 1,
						Message: fmt.Sprintf("Table '%s' not found in schema", tname),
					})
				}
			}

		case SpanORMRelation:
			if sp.Table == nil || len(sp.Table.Relations) == 0 {
				continue
			}
			curr := sp.Table
			off := 0
			for _, part := range strings.Split(sp.Text, ".") {
				plen := len(part)
				if curr == nil {
					break
				}
				r := curr.Relation(part)
				if r == nil {
					out = append(out, rawsql.Diag{
						Start: sp.Start + off, End: sp.Start + off + plen, Severity: 2,
						Message: fmt.Sprintf("Relation '%s' is not defined on model '%s'", part, curr.Model),
					})
					break
				}
				curr = cat.Model(r.TargetModel)
				off += plen + 1
			}

		case SpanConfigKey:
			if len(cat.Configs) > 0 && cat.Config(sp.Text) == nil {
				if _, ok := cat.EnvVars[sp.Text]; ok {
					continue
				}
				// Allow top-level namespace access like facades.Config().Get("app")
				hasPrefix := false
				prefix := sp.Text + "."
				for k := range cat.Configs {
					if strings.HasPrefix(k, prefix) {
						hasPrefix = true
						break
					}
				}
				if !hasPrefix {
					out = append(out, rawsql.Diag{
						Start: sp.Start, End: sp.End, Severity: 2,
						Message: fmt.Sprintf("Unknown config key '%s'", sp.Text),
					})
				}
			}

		case SpanEnvKey:
			if len(cat.EnvVars) > 0 && sp.Text != "" {
				if _, ok := cat.EnvVars[sp.Text]; !ok {
					out = append(out, rawsql.Diag{
						Start: sp.Start, End: sp.End, Severity: 3,
						Message: fmt.Sprintf("Environment variable '%s' is not defined in .env*", sp.Text),
					})
				}
			}

		case SpanLangKey:
			if len(cat.Translations) > 0 && sp.Text != "" {
				if _, ok := cat.Translations[sp.Text]; !ok {
					out = append(out, rawsql.Diag{
						Start: sp.Start, End: sp.End, Severity: 2,
						Message: fmt.Sprintf("Missing translation key '%s' in lang/", sp.Text),
					})
				}
			}

		case SpanGateAbility:
			if len(cat.Gates) > 0 && sp.Text != "" {
				if _, ok := cat.Gates[sp.Text]; !ok {
					out = append(out, rawsql.Diag{
						Start: sp.Start, End: sp.End, Severity: 2,
						Message: fmt.Sprintf("Unknown Gate ability '%s'", sp.Text),
					})
				}
			}

		case SpanRuleValue:
			off := 0
			for _, part := range strings.Split(sp.Text, "|") {
				plen := len(part)
				rname := part
				if i := strings.IndexByte(part, ':'); i >= 0 {
					rname = part[:i]
				}
				rname = strings.TrimSpace(rname)
				if rname != "" && cat.Rule(rname) == nil {
					out = append(out, rawsql.Diag{
						Start: sp.Start + off, End: sp.Start + off + len(rname), Severity: 2,
						Message: fmt.Sprintf("Unknown validation rule '%s'", rname),
					})
				} else if (rname == "exists" || rname == "unique") && strings.Contains(part, ":") && len(cat.Tables) > 0 {
					colon := strings.IndexByte(part, ':')
					argsStr := part[colon+1:]
					args := strings.Split(argsStr, ",")
					tname := strings.TrimSpace(args[0])
					if tname != "" && !strings.Contains(tname, "{") {
						tbl := cat.Table(tname)
						if tbl == nil {
							tStart := sp.Start + off + colon + 1
							out = append(out, rawsql.Diag{
								Start: tStart, End: tStart + len(args[0]), Severity: 2,
								Message: fmt.Sprintf("Table '%s' not found in schema for '%s' rule", tname, rname),
							})
						} else if len(args) >= 2 {
							cname := strings.TrimSpace(args[1])
							if cname != "" && !strings.EqualFold(cname, "NULL") && !strings.Contains(cname, "{") && tbl.Column(cname) == nil {
								cStart := sp.Start + off + colon + 1 + len(args[0]) + 1
								out = append(out, rawsql.Diag{
									Start: cStart, End: cStart + len(args[1]), Severity: 2,
									Message: fmt.Sprintf("Column '%s' does not exist on table '%s'", cname, tbl.Name),
								})
							}
						}
					}
				}
				off += plen + 1
			}

		case SpanRouteParam:
			found := false
			var avail []string
			seen := map[string]bool{}
			for _, r := range sp.Routes {
				for _, p := range r.Params {
					if !seen[p] {
						seen[p] = true
						avail = append(avail, p)
					}
					if p == sp.Text {
						found = true
					}
				}
			}
			if !found && sp.Text != "" {
				msg := fmt.Sprintf("Route parameter '%s' is not defined in matched route(s)", sp.Text)
				if len(avail) > 0 {
					msg += fmt.Sprintf(" (available: %s)", strings.Join(avail, ", "))
				}
				out = append(out, rawsql.Diag{
					Start: sp.Start, End: sp.End, Severity: 2, Message: msg,
				})
			}
		}
	}
	return out
}

// SemanticTokens returns semantic tokens for ORM Where/Order/Select/Join/Relation strings.
func (fa *FileAnalysis) SemanticTokens() []rawsql.SemToken {
	var out []rawsql.SemToken
	for _, sp := range fa.Spans {
		switch sp.Kind {
		case SpanORMWhere, SpanORMOrder, SpanORMColumn, SpanORMJoin:
			masked := strMask(sp.Text)
			for _, m := range identRe.FindAllStringIndex(masked, -1) {
				w := masked[m[0]:m[1]]
				lw := strings.ToLower(w)
				if whereSkipWords[lw] {
					out = append(out, rawsql.SemToken{GoStart: sp.Start + m[0], Length: m[1] - m[0], Type: rawsql.SemKeyword})
				} else if sp.Table != nil && sp.Table.Column(w) != nil {
					out = append(out, rawsql.SemToken{GoStart: sp.Start + m[0], Length: m[1] - m[0], Type: rawsql.SemProperty})
				}
			}
			for i := 0; i < len(sp.Text); i++ {
				if sp.Text[i] == '?' {
					out = append(out, rawsql.SemToken{GoStart: sp.Start + i, Length: 1, Type: rawsql.SemParameter})
				}
			}
		}
	}
	return out
}

// InlayHint describes a virtual hint displayed next to Config / Lang / Env calls.
type InlayHint struct {
	Offset int
	Label  string
}

func (fa *FileAnalysis) InlayHints(cat *indexer.Catalog) []InlayHint {
	var out []InlayHint
	for _, sp := range fa.Spans {
		if sp.CallEnd <= 0 {
			continue
		}
		switch sp.Kind {
		case SpanConfigKey:
			if ci := cat.Config(sp.Text); ci != nil {
				lbl := ci.Default
				if ci.EnvKey != "" {
					lbl = fmt.Sprintf("→ %s (default: %s)", ci.EnvKey, ci.Default)
				}
				if lbl != "" {
					out = append(out, InlayHint{Offset: sp.CallEnd, Label: " " + lbl})
				}
			}
		case SpanLangKey:
			if locales, ok := cat.Translations[sp.Text]; ok && len(locales) > 0 {
				val := ""
				if en, ok := locales["en"]; ok {
					val = en.Value
				} else {
					for _, v := range locales {
						val = v.Value
						break
					}
				}
				if val != "" {
					if len(val) > 36 {
						val = val[:33] + "..."
					}
					out = append(out, InlayHint{Offset: sp.CallEnd, Label: fmt.Sprintf(" → %q", val)})
				}
			}
		}
	}
	return out
}

func langDocMD(key string, locales map[string]indexer.TranslationEntry) string {
	var b strings.Builder
	fmt.Fprintf(&b, "**Translation `%s`**\n\n| Locale | Value |\n|---|---|\n", key)
	var keys []string
	for k := range locales {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Fprintf(&b, "| `%s` | %s |\n", k, locales[k].Value)
	}
	return b.String()
}

func strMask(s string) string {
	b := []byte(s)
	inStr := false
	for i := range b {
		if b[i] == '\'' {
			inStr = !inStr
			b[i] = ' '
		} else if inStr {
			b[i] = ' '
		}
	}
	return string(b)
}

func resolveRelationChainTable(base *indexer.Table, before string, cat *indexer.Catalog) *indexer.Table {
	if base == nil {
		return nil
	}
	parts := strings.Split(before, ".")
	curr := base
	for i := 0; i < len(parts)-1; i++ {
		if curr == nil {
			return nil
		}
		r := curr.Relation(parts[i])
		if r == nil {
			return nil
		}
		curr = cat.Model(r.TargetModel)
	}
	return curr
}

func relationAt(base *indexer.Table, text string, relOff int, cat *indexer.Catalog) (r *indexer.Relation, start, end int) {
	if base == nil {
		return nil, 0, 0
	}
	curr := base
	off := 0
	for _, part := range strings.Split(text, ".") {
		plen := len(part)
		if curr == nil {
			return nil, 0, 0
		}
		rel := curr.Relation(part)
		if rel == nil {
			return nil, 0, 0
		}
		if relOff >= off && relOff <= off+plen {
			return rel, off, off + plen
		}
		curr = cat.Model(rel.TargetModel)
		off += plen + 1
	}
	return nil, 0, 0
}

func wordAt(s string, off int) (string, int, int) {
	for _, m := range identRe.FindAllStringIndex(s, -1) {
		if off >= m[0] && off <= m[1] {
			return s[m[0]:m[1]], m[0], m[1]
		}
	}
	return "", 0, 0
}

func ruleSegmentAt(s string, off int) (name string, start, end int) {
	pos := 0
	for _, part := range strings.Split(s, "|") {
		plen := len(part)
		if off >= pos && off <= pos+plen {
			rname := part
			if i := strings.IndexByte(part, ':'); i >= 0 {
				rname = part[:i]
			}
			return strings.TrimSpace(rname), pos, pos + len(rname)
		}
		pos += plen + 1
	}
	return "", 0, 0
}

func colTypeDetail(c *indexer.Column) string {
	if c.SQLType != "" {
		return c.SQLType
	}
	return c.GoType
}

func colDocMD(t *indexer.Table, c *indexer.Column) string {
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
	if c.ForeignKey != "" {
		fmt.Fprintf(&b, "- FK: `%s`\n", c.ForeignKey)
	}
	if c.Doc != "" {
		fmt.Fprintf(&b, "\n%s\n", c.Doc)
	}
	return b.String()
}

func configDocMD(ci *indexer.ConfigItem) string {
	var b strings.Builder
	fmt.Fprintf(&b, "**Config `%s`**\n\n", ci.Key)
	if ci.EnvKey != "" {
		fmt.Fprintf(&b, "- Env Variable: `%s`\n", ci.EnvKey)
	}
	if ci.Default != "" {
		fmt.Fprintf(&b, "- Default Value: `%s`\n", ci.Default)
	}
	if ci.EnvVal != "" {
		fmt.Fprintf(&b, "- `.env` Value: `%s`\n", ci.EnvVal)
	}
	if ci.Loc.Valid() {
		fmt.Fprintf(&b, "- File: `%s:%d`\n", ci.Loc.File, ci.Loc.Line+1)
	}
	return b.String()
}

func ruleDocMD(ri *indexer.RuleItem) string {
	var b strings.Builder
	kind := "Built-in Validation Rule"
	if ri.Custom {
		kind = "Custom Validation Rule (`app/rules`)"
	}
	fmt.Fprintf(&b, "**`%s`** — *%s*\n\n", ri.Name, kind)
	if ri.Doc != "" {
		fmt.Fprintf(&b, "%s\n\n", ri.Doc)
	}
	if ri.Message != "" {
		fmt.Fprintf(&b, "- Message: *\"%s\"*\n", ri.Message)
	}
	return b.String()
}

func isRequestReceiver(e ast.Expr) bool {
	call, ok := e.(*ast.CallExpr)
	if !ok {
		return false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	return ok && sel.Sel.Name == "Request"
}

func normalizeRoutePath(p string) string {
	parts := strings.Split(strings.TrimSpace(p), "/")
	for i, seg := range parts {
		if (strings.HasPrefix(seg, "{") && strings.HasSuffix(seg, "}")) || strings.HasPrefix(seg, ":") {
			parts[i] = "{}"
		}
	}
	return "/" + strings.Trim(strings.Join(parts, "/"), "/")
}

func checkSwaggerRouter(fa *FileAnalysis, off func(token.Pos) int, doc *ast.CommentGroup, routes []*indexer.RouteItem) {
	for _, c := range doc.List {
		m := swaggerRouterRe.FindStringSubmatchIndex(c.Text)
		if m == nil {
			continue
		}
		swPath := c.Text[m[2]:m[3]]
		swMethod := strings.ToUpper(c.Text[m[4]:m[5]])
		normSw := normalizeRoutePath(swPath)
		matched := false
		for _, r := range routes {
			if (r.Method == swMethod || r.Method == "ANY") && normalizeRoutePath(r.Path) == normSw {
				matched = true
				break
			}
		}
		if !matched {
			first := routes[0]
			cOff := off(c.Pos())
			fa.ExtraDiags = append(fa.ExtraDiags, rawsql.Diag{
				Start:    cOff + m[0],
				End:      cOff + m[1],
				Severity: 3, // Information / Hint-level warning
				Message:  fmt.Sprintf("@Router %s [%s] does not match registered route %s %s", swPath, strings.ToLower(swMethod), first.Method, first.Path),
			})
		}
	}
}

func ruleDBRefAt(text string, relOff int, cat *indexer.Catalog) (tbl *indexer.Table, col *indexer.Column, start, end int, ok bool) {
	if relOff < 0 || relOff > len(text) {
		return nil, nil, 0, 0, false
	}
	segStart := strings.LastIndexByte(text[:relOff], '|') + 1
	segEnd := len(text)
	if i := strings.IndexByte(text[relOff:], '|'); i >= 0 {
		segEnd = relOff + i
	}
	seg := text[segStart:segEnd]
	colon := strings.IndexByte(seg, ':')
	if colon < 0 {
		return nil, nil, 0, 0, false
	}
	rname := strings.TrimSpace(seg[:colon])
	if rname != "exists" && rname != "unique" {
		return nil, nil, 0, 0, false
	}
	argsStart := segStart + colon + 1
	if relOff < argsStart {
		return nil, nil, 0, 0, false
	}
	args := seg[colon+1:]
	comma := strings.IndexByte(args, ',')
	tRaw := args
	if comma >= 0 {
		tRaw = args[:comma]
	}
	tname := strings.TrimSpace(tRaw)
	tbl = cat.Table(tname)
	if comma < 0 || relOff <= argsStart+comma {
		if tbl != nil {
			return tbl, nil, argsStart, argsStart + len(tRaw), true
		}
		return nil, nil, 0, 0, false
	}
	colStart := argsStart + comma + 1
	rest := args[comma+1:]
	cEndRel := len(rest)
	if c2 := strings.IndexByte(rest, ','); c2 >= 0 {
		cEndRel = c2
	}
	if relOff > colStart+cEndRel {
		return nil, nil, 0, 0, false
	}
	cname := strings.TrimSpace(rest[:cEndRel])
	if tbl != nil && cname != "" {
		if col = tbl.Column(cname); col != nil {
			return tbl, col, colStart, colStart + cEndRel, true
		}
	}
	return nil, nil, 0, 0, false
}
