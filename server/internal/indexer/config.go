package indexer

import (
	"bufio"
	"go/ast"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

func indexConfigs(c *Catalog, root string) {
	// 1. Parse .env* files for environment variables
	for _, name := range []string{".env.example", ".env.local.example", ".env.docker.example", ".env"} {
		parseEnvFile(c, filepath.Join(root, name))
	}

	// 2. Parse config/*.go files
	files := walkGoFiles(filepath.Join(root, "config"))
	for _, pf := range files {
		ast.Inspect(pf.file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || len(call.Args) < 2 {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "Add" {
				return true
			}
			nsLit, ok := call.Args[0].(*ast.BasicLit)
			if !ok || nsLit.Kind != token.STRING {
				return true
			}
			ns, err := strconv.Unquote(nsLit.Value)
			if err != nil || ns == "" {
				return true
			}
			if cl, ok := call.Args[1].(*ast.CompositeLit); ok {
				walkConfigMap(c, pf, ns, cl)
			}
			return false
		})
	}
}

func walkConfigMap(c *Catalog, pf *parsedFile, prefix string, cl *ast.CompositeLit) {
	for _, elt := range cl.Elts {
		kv, ok := elt.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		kl, ok := kv.Key.(*ast.BasicLit)
		if !ok || kl.Kind != token.STRING {
			continue
		}
		k, err := strconv.Unquote(kl.Value)
		if err != nil || k == "" {
			continue
		}
		fullKey := prefix + "." + k
		if subMap, ok := kv.Value.(*ast.CompositeLit); ok && isMapLit(subMap) {
			walkConfigMap(c, pf, fullKey, subMap)
			continue
		}
		envKey, defVal := extractConfigValue(kv.Value)
		c.Configs[fullKey] = &ConfigItem{
			Key:     fullKey,
			EnvKey:  envKey,
			Default: defVal,
			EnvVal:  c.EnvVars[envKey],
			Loc:     loc(pf, kl.Pos()),
		}
	}
}

func isMapLit(cl *ast.CompositeLit) bool {
	if _, ok := cl.Type.(*ast.MapType); ok {
		return true
	}
	for _, e := range cl.Elts {
		if _, ok := e.(*ast.KeyValueExpr); ok {
			return true
		}
	}
	return false
}

func extractConfigValue(e ast.Expr) (envKey, defVal string) {
	switch x := e.(type) {
	case *ast.CallExpr:
		if sel, ok := x.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "Env" && len(x.Args) >= 1 {
			if bl, ok := x.Args[0].(*ast.BasicLit); ok && bl.Kind == token.STRING {
				envKey, _ = strconv.Unquote(bl.Value)
			}
			if len(x.Args) >= 2 {
				defVal = literalOrExprString(x.Args[1])
			}
			return
		}
		// e.g. cast.ToInt(config.Env(...))
		for _, a := range x.Args {
			if ek, dv := extractConfigValue(a); ek != "" {
				return ek, dv
			}
		}
		return "", exprString(x.Fun) + "(...)"
	default:
		return "", literalOrExprString(e)
	}
}

func literalOrExprString(e ast.Expr) string {
	switch x := e.(type) {
	case *ast.BasicLit:
		return x.Value
	case *ast.Ident:
		return x.Name
	case *ast.SelectorExpr:
		return exprString(x)
	}
	return exprString(e)
}

func parseEnvFile(c *Catalog, path string) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		i := strings.IndexByte(line, '=')
		if i <= 0 {
			continue
		}
		k := strings.TrimSpace(line[:i])
		v := strings.TrimSpace(line[i+1:])
		v = strings.Trim(v, "\"'")
		c.EnvVars[k] = v
	}
}

