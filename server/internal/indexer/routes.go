package indexer

import (
	"go/ast"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

var httpMethods = map[string]string{
	"Get":     "GET",
	"Post":    "POST",
	"Put":     "PUT",
	"Patch":   "PATCH",
	"Delete":  "DELETE",
	"Options": "OPTIONS",
	"Any":     "ANY",
}

func indexRoutes(c *Catalog, root string) {
	// 1. Index controller methods in app/http/controllers
	ctrlFiles := walkGoFiles(filepath.Join(root, "app", "http", "controllers"))
	for _, pf := range ctrlFiles {
		for _, d := range pf.file.Decls {
			fd, ok := d.(*ast.FuncDecl)
			if !ok || fd.Recv == nil || fd.Name == nil || !fd.Name.IsExported() {
				continue
			}
			ctrl := recvName(fd.Recv)
			if ctrl == "" {
				continue
			}
			if c.Controllers[ctrl] == nil {
				c.Controllers[ctrl] = map[string]Location{}
			}
			c.Controllers[ctrl][fd.Name.Name] = loc(pf, fd.Name.Pos())
		}
	}

	// 2. Walk routes/**/*.go
	routeDir := filepath.Join(root, "routes")
	routeFiles := walkGoFiles(routeDir)
	for _, pf := range routeFiles {
		rel, _ := filepath.Rel(routeDir, pf.path)
		relSlash := filepath.ToSlash(rel)
		module := "routes"
		if parts := strings.Split(relSlash, "/"); len(parts) > 1 {
			module = parts[0]
		} else {
			module = strings.TrimSuffix(parts[0], ".go")
		}
		for _, d := range pf.file.Decls {
			fd, ok := d.(*ast.FuncDecl)
			if !ok || fd.Body == nil {
				continue
			}
			ctrlVars := map[string]string{} // local var -> ControllerStructName
			ast.Inspect(fd.Body, func(n ast.Node) bool {
				as, ok := n.(*ast.AssignStmt)
				if !ok || len(as.Lhs) != 1 || len(as.Rhs) != 1 {
					return true
				}
				id, ok := as.Lhs[0].(*ast.Ident)
				if !ok {
					return true
				}
				if ctrl := extractControllerType(as.Rhs[0]); ctrl != "" {
					ctrlVars[id.Name] = ctrl
				}
				return true
			})
			walkRouteBlock(c, pf, module, "", nil, ctrlVars, fd.Body.List)
		}
	}
}

func extractControllerType(e ast.Expr) string {
	switch x := e.(type) {
	case *ast.CallExpr:
		name := ""
		switch f := x.Fun.(type) {
		case *ast.Ident:
			name = f.Name
		case *ast.SelectorExpr:
			name = f.Sel.Name
		}
		if strings.HasPrefix(name, "New") && strings.HasSuffix(name, "Controller") {
			return strings.TrimPrefix(name, "New")
		}
	case *ast.UnaryExpr:
		return extractControllerType(x.X)
	case *ast.CompositeLit:
		t := cleanModelType(exprString(x.Type))
		if strings.HasSuffix(t, "Controller") {
			return t
		}
	}
	return ""
}

func walkRouteBlock(c *Catalog, pf *parsedFile, module, parentPrefix string, parentMws []string, ctrlVars map[string]string, stmts []ast.Stmt) {
	for _, st := range stmts {
		ast.Inspect(st, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			// Handle .Group(func(router route.Router) { ... })
			if sel.Sel.Name == "Group" && len(call.Args) > 0 {
				if fn, ok := call.Args[len(call.Args)-1].(*ast.FuncLit); ok && fn.Body != nil {
					grpPrefix := joinPath(parentPrefix, extractChainPrefix(sel.X))
					grpMws := mergeMiddlewares(parentMws, extractChainMiddlewares(sel.X))
					walkRouteBlock(c, pf, module, grpPrefix, grpMws, ctrlVars, fn.Body.List)
					return false
				}
			}
			// Handle .Get("/path", handler), .Post(...), etc.
			if verb, ok := httpMethods[sel.Sel.Name]; ok && len(call.Args) >= 2 {
				pathLit, ok := call.Args[0].(*ast.BasicLit)
				if !ok || pathLit.Kind != token.STRING {
					return true
				}
				subPath, err := strconv.Unquote(pathLit.Value)
				if err != nil {
					return true
				}
				chainPrefix := extractChainPrefix(sel.X)
				fullPath := joinPath(joinPath(parentPrefix, chainPrefix), subPath)
				mws := mergeMiddlewares(parentMws, extractChainMiddlewares(sel.X))
				ctrl, action, handler := resolveRouteHandler(call.Args[1], ctrlVars)
				var ctrlLoc Location
				if ctrl != "" && action != "" {
					if m, ok := c.Controllers[ctrl]; ok {
						ctrlLoc = m[action]
					}
				}
				c.Routes = append(c.Routes, &RouteItem{
					Method:      verb,
					Path:        fullPath,
					Params:      extractRouteParams(fullPath),
					Middlewares: mws,
					Handler:     handler,
					Controller:  ctrl,
					Action:      action,
					Module:      module,
					Loc:         loc(pf, sel.Sel.Pos()),
					CtrlLoc:     ctrlLoc,
				})
				return false
			}
			// Handle .Resource("/path", ctrl)
			if sel.Sel.Name == "Resource" && len(call.Args) >= 2 {
				pathLit, ok := call.Args[0].(*ast.BasicLit)
				if !ok || pathLit.Kind != token.STRING {
					return true
				}
				subPath, err := strconv.Unquote(pathLit.Value)
				if err != nil {
					return true
				}
				chainPrefix := extractChainPrefix(sel.X)
				basePath := joinPath(joinPath(parentPrefix, chainPrefix), subPath)
				mws := mergeMiddlewares(parentMws, extractChainMiddlewares(sel.X))
				ctrl := ""
				if id, ok := call.Args[1].(*ast.Ident); ok {
					ctrl = ctrlVars[id.Name]
				} else {
					ctrl = extractControllerType(call.Args[1])
				}
				resRoutes := []struct{ verb, suffix, act string }{
					{"GET", "", "Index"},
					{"POST", "", "Store"},
					{"GET", "/{id}", "Show"},
					{"PUT", "/{id}", "Update"},
					{"DELETE", "/{id}", "Destroy"},
				}
				for _, rr := range resRoutes {
					var ctrlLoc Location
					if ctrl != "" {
						if m, ok := c.Controllers[ctrl]; ok {
							ctrlLoc = m[rr.act]
						}
					}
					handler := rr.act
					if ctrl != "" {
						handler = ctrl + "." + rr.act
					}
					fullResPath := basePath + rr.suffix
					c.Routes = append(c.Routes, &RouteItem{
						Method:      rr.verb,
						Path:        fullResPath,
						Params:      extractRouteParams(fullResPath),
						Middlewares: mws,
						Handler:     handler,
						Controller:  ctrl,
						Action:      rr.act,
						Module:      module,
						Loc:         loc(pf, sel.Sel.Pos()),
						CtrlLoc:     ctrlLoc,
					})
				}
				return false
			}
			return true
		})
	}
}

func extractRouteParams(p string) []string {
	var params []string
	seen := map[string]bool{}
	for _, seg := range strings.Split(p, "/") {
		name := ""
		if strings.HasPrefix(seg, "{") && strings.HasSuffix(seg, "}") && len(seg) > 2 {
			name = seg[1 : len(seg)-1]
			if i := strings.IndexByte(name, ':'); i >= 0 {
				name = name[:i]
			}
		} else if strings.HasPrefix(seg, ":") && len(seg) > 1 {
			name = seg[1:]
		}
		if name != "" && !seen[name] {
			seen[name] = true
			params = append(params, name)
		}
	}
	return params
}

func extractChainPrefix(e ast.Expr) string {
	call, ok := e.(*ast.CallExpr)
	if !ok {
		return ""
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return ""
	}
	parent := extractChainPrefix(sel.X)
	if sel.Sel.Name == "Prefix" && len(call.Args) > 0 {
		if bl, ok := call.Args[0].(*ast.BasicLit); ok && bl.Kind == token.STRING {
			if p, err := strconv.Unquote(bl.Value); err == nil {
				return joinPath(parent, p)
			}
		}
	}
	return parent
}

func extractChainMiddlewares(e ast.Expr) []string {
	call, ok := e.(*ast.CallExpr)
	if !ok {
		return nil
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return nil
	}
	parent := extractChainMiddlewares(sel.X)
	if sel.Sel.Name == "Middleware" {
		var cur []string
		for _, a := range call.Args {
			s := cleanMiddlewareExpr(a)
			if s != "" {
				cur = append(cur, s)
			}
		}
		return mergeMiddlewares(parent, cur)
	}
	return parent
}

func cleanMiddlewareExpr(e ast.Expr) string {
	switch x := e.(type) {
	case *ast.CallExpr:
		s := exprString(x.Fun)
		s = strings.TrimPrefix(s, "middleware.")
		return s
	case *ast.SelectorExpr:
		return strings.TrimPrefix(exprString(x), "middleware.")
	case *ast.Ident:
		return x.Name
	}
	return ""
}

func mergeMiddlewares(a, b []string) []string {
	if len(a) == 0 && len(b) == 0 {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	for _, m := range append(append([]string(nil), a...), b...) {
		if m != "" && !seen[m] {
			seen[m] = true
			out = append(out, m)
		}
	}
	return out
}

func resolveRouteHandler(e ast.Expr, ctrlVars map[string]string) (ctrl, action, handler string) {
	if sel, ok := e.(*ast.SelectorExpr); ok {
		action = sel.Sel.Name
		switch x := sel.X.(type) {
		case *ast.Ident:
			if c, ok := ctrlVars[x.Name]; ok {
				ctrl = c
			} else {
				ctrl = x.Name
			}
		default:
			ctrl = extractControllerType(x)
		}
		if ctrl != "" {
			handler = ctrl + "." + action
		} else {
			handler = action
		}
		return
	}
	return "", "", exprString(e)
}

func joinPath(a, b string) string {
	a = strings.TrimSpace(a)
	b = strings.TrimSpace(b)
	if a == "" && b == "" {
		return "/"
	}
	if a == "" {
		if !strings.HasPrefix(b, "/") {
			return "/" + b
		}
		return b
	}
	if b == "" {
		if !strings.HasPrefix(a, "/") {
			return "/" + a
		}
		return a
	}
	res := strings.TrimRight(a, "/") + "/" + strings.TrimLeft(b, "/")
	if !strings.HasPrefix(res, "/") {
		res = "/" + res
	}
	return res
}

var (
	protoServiceRe = regexp.MustCompile(`(?s)\bservice\s+(\w+)\s*\{([^}]*)\}`)
	protoRpcRe     = regexp.MustCompile(`\brpc\s+(\w+)\s*\(\s*(?:stream\s+)?([\w.]+)\s*\)\s*returns\s*\(\s*(?:stream\s+)?([\w.]+)\s*\)`)
)

func indexGrpcRoutes(c *Catalog, root string) {
	// 1. Parse .proto files in proto/, app/grpc/, contracts/proto/
	for _, sub := range []string{"proto", filepath.Join("app", "grpc"), filepath.Join("contracts", "proto")} {
		dir := filepath.Join(root, sub)
		if _, err := os.Stat(dir); err != nil {
			continue
		}
		_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(strings.ToLower(d.Name()), ".proto") {
				return nil
			}
			b, err := os.ReadFile(p)
			if err != nil {
				return nil
			}
			src := string(b)
			for _, sm := range protoServiceRe.FindAllStringSubmatchIndex(src, -1) {
				svc := src[sm[2]:sm[3]]
				body := src[sm[4]:sm[5]]
				bodyOff := sm[4]
				if c.ProtoMethods[svc] == nil {
					c.ProtoMethods[svc] = map[string]ProtoMethod{}
				}
				for _, rm := range protoRpcRe.FindAllStringSubmatchIndex(body, -1) {
					mName := body[rm[2]:rm[3]]
					reqT := body[rm[4]:rm[5]]
					resT := body[rm[6]:rm[7]]
					c.ProtoMethods[svc][mName] = ProtoMethod{
						Service: svc,
						Method:  mName,
						ReqType: reqT,
						ResType: resT,
						Loc:     offsetLoc(p, src, bodyOff+rm[2]),
					}
				}
			}
			return nil
		})
	}

	// 2. Index gRPC controller methods in app/grpc/controllers
	grpcCtrlMethods := map[string][]string{}
	grpcCtrlFiles := walkGoFiles(filepath.Join(root, "app", "grpc", "controllers"))
	for _, pf := range grpcCtrlFiles {
		for _, d := range pf.file.Decls {
			fd, ok := d.(*ast.FuncDecl)
			if !ok || fd.Recv == nil || fd.Name == nil || !fd.Name.IsExported() {
				continue
			}
			ctrl := recvName(fd.Recv)
			if ctrl == "" {
				continue
			}
			if c.Controllers[ctrl] == nil {
				c.Controllers[ctrl] = map[string]Location{}
			}
			c.Controllers[ctrl][fd.Name.Name] = loc(pf, fd.Name.Pos())
			grpcCtrlMethods[ctrl] = append(grpcCtrlMethods[ctrl], fd.Name.Name)
		}
	}

	// 3. Scan routes/ (e.g. routes/grpc.go) and app/providers/ for Register<Service>Server(..., <ctrl>)
	var routeFiles []*parsedFile
	routeFiles = append(routeFiles, walkGoFiles(filepath.Join(root, "routes"))...)
	routeFiles = append(routeFiles, walkGoFiles(filepath.Join(root, "app", "providers"))...)
	registeredCtrl := map[string]bool{}
	for _, pf := range routeFiles {
		for _, d := range pf.file.Decls {
			fd, ok := d.(*ast.FuncDecl)
			if !ok || fd.Body == nil {
				continue
			}
			ctrlVars := map[string]string{}
			ast.Inspect(fd.Body, func(n ast.Node) bool {
				as, ok := n.(*ast.AssignStmt)
				if !ok || len(as.Lhs) != 1 || len(as.Rhs) != 1 {
					return true
				}
				id, ok := as.Lhs[0].(*ast.Ident)
				if !ok {
					return true
				}
				if ctrl := extractControllerType(as.Rhs[0]); ctrl != "" {
					ctrlVars[id.Name] = ctrl
				}
				return true
			})
			ast.Inspect(fd.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok || len(call.Args) < 2 {
					return true
				}
				fnName := ""
				var fnPos token.Pos
				switch f := call.Fun.(type) {
				case *ast.Ident:
					fnName = f.Name
					fnPos = f.Pos()
				case *ast.SelectorExpr:
					fnName = f.Sel.Name
					fnPos = f.Sel.Pos()
				}
				if !strings.HasPrefix(fnName, "Register") || !strings.HasSuffix(fnName, "Server") {
					return true
				}
				svcName := strings.TrimSuffix(strings.TrimPrefix(fnName, "Register"), "Server")
				if svcName == "" {
					return true
				}
				ctrlArg := call.Args[len(call.Args)-1]
				ctrl := ""
				if id, ok := ctrlArg.(*ast.Ident); ok {
					ctrl = ctrlVars[id.Name]
				} else {
					ctrl = extractControllerType(ctrlArg)
				}
				if ctrl == "" {
					return true
				}
				registeredCtrl[ctrl] = true
				methods := collectGrpcServiceMethods(c, svcName, ctrl, grpcCtrlMethods[ctrl])
				regLoc := loc(pf, fnPos)
				for _, mName := range methods {
					var ctrlLoc Location
					if m, ok := c.Controllers[ctrl]; ok {
						ctrlLoc = m[mName]
					}
					routeLoc := regLoc
					if pm, ok := c.ProtoMethods[svcName][mName]; ok && pm.Loc.Valid() && !routeLoc.Valid() {
						routeLoc = pm.Loc
					}
					c.Routes = append(c.Routes, &RouteItem{
						Method:     "gRPC",
						Path:       "/" + svcName + "/" + mName,
						Handler:    ctrl + "." + mName,
						Controller: ctrl,
						Action:     mName,
						Module:     "grpc",
						Loc:        routeLoc,
						CtrlLoc:    ctrlLoc,
					})
				}
				return true
			})
		}
	}

	// 4. Also link any app/grpc/controllers not explicitly caught via Register*Server if they match a Proto service
	for ctrl, methods := range grpcCtrlMethods {
		if registeredCtrl[ctrl] {
			continue
		}
		stem := strings.TrimSuffix(ctrl, "Controller")
		svcName := stem + "Service"
		if _, ok := c.ProtoMethods[svcName]; !ok {
			if _, ok2 := c.ProtoMethods[stem]; ok2 {
				svcName = stem
			}
		}
		for _, mName := range methods {
			ctrlLoc := c.Controllers[ctrl][mName]
			routeLoc := ctrlLoc
			if pm, ok := c.ProtoMethods[svcName][mName]; ok && pm.Loc.Valid() {
				routeLoc = pm.Loc
			}
			c.Routes = append(c.Routes, &RouteItem{
				Method:     "gRPC",
				Path:       "/" + svcName + "/" + mName,
				Handler:    ctrl + "." + mName,
				Controller: ctrl,
				Action:     mName,
				Module:     "grpc",
				Loc:        routeLoc,
				CtrlLoc:    ctrlLoc,
			})
		}
	}
}

func collectGrpcServiceMethods(c *Catalog, svcName, ctrl string, ctrlMethods []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, m := range ctrlMethods {
		if !seen[m] {
			seen[m] = true
			out = append(out, m)
		}
	}
	if pmMap, ok := c.ProtoMethods[svcName]; ok {
		var protoNames []string
		for m := range pmMap {
			protoNames = append(protoNames, m)
		}
		sort.Strings(protoNames)
		for _, m := range protoNames {
			if !seen[m] {
				seen[m] = true
				out = append(out, m)
			}
		}
	}
	return out
}


