package framework

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/rusmanplatd/goravel-lsp/internal/indexer"
)

type viewNameSpan struct {
	Name  string
	Start int
	End   int
}

type viewCall struct {
	Method   string // Make | First | Exists | PathView | Share | Shared | LoadViewsFrom
	Names    []viewNameSpan
	ShareKey *viewNameSpan
	DataMap  *ast.CompositeLit
}

func findViewCalls(path, src string) ([]viewCall, *token.FileSet) {
	if !strings.Contains(src, "View") && !strings.Contains(src, ".Make(") && !strings.Contains(src, ".First(") && !strings.Contains(src, ".Exists(") {
		return nil, nil
	}
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, src, 0)
	if err != nil || f == nil {
		return nil, nil
	}

	localMaps := map[string]*ast.CompositeLit{}
	ast.Inspect(f, func(n ast.Node) bool {
		switch stmt := n.(type) {
		case *ast.AssignStmt:
			for i, lhs := range stmt.Lhs {
				if ident, ok := lhs.(*ast.Ident); ok && i < len(stmt.Rhs) {
					if cl, ok := stmt.Rhs[i].(*ast.CompositeLit); ok {
						localMaps[ident.Name] = cl
					}
				}
			}
		case *ast.ValueSpec:
			for i, ident := range stmt.Names {
				if i < len(stmt.Values) {
					if cl, ok := stmt.Values[i].(*ast.CompositeLit); ok {
						localMaps[ident.Name] = cl
					}
				}
			}
		}
		return true
	})

	var out []viewCall
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || len(call.Args) == 0 {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}

		// Check path.View("...")
		if ident, ok := sel.X.(*ast.Ident); ok && ident.Name == "path" && sel.Sel.Name == "View" {
			if sp, ok := extractStringSpan(fset, call.Args[0]); ok {
				out = append(out, viewCall{
					Method: "PathView",
					Names:  []viewNameSpan{sp},
				})
			}
			return true
		}

		recvStart := fset.Position(sel.X.Pos()).Offset
		recvEnd := fset.Position(sel.X.End()).Offset
		if recvStart < 0 || recvEnd > len(src) || recvStart >= recvEnd {
			return true
		}
		recvText := src[recvStart:recvEnd]
		if !strings.Contains(recvText, "View") && !strings.Contains( strings.ToLower(recvText), "view") {
			return true
		}

		switch sel.Sel.Name {
		case "Make":
			sp, ok := extractStringSpan(fset, call.Args[0])
			if !ok {
				return true
			}
			vc := viewCall{
				Method: "Make",
				Names:  []viewNameSpan{sp},
			}
			if len(call.Args) > 1 {
				vc.DataMap = resolveDataMapArg(call.Args[1], localMaps)
			}
			out = append(out, vc)
		case "First":
			sliceLit, ok := call.Args[0].(*ast.CompositeLit)
			if !ok {
				return true
			}
			vc := viewCall{Method: "First"}
			for _, elt := range sliceLit.Elts {
				if sp, ok := extractStringSpan(fset, elt); ok {
					vc.Names = append(vc.Names, sp)
				}
			}
			if len(call.Args) > 1 {
				vc.DataMap = resolveDataMapArg(call.Args[1], localMaps)
			}
			if len(vc.Names) > 0 {
				out = append(out, vc)
			}
		case "Exists":
			if sp, ok := extractStringSpan(fset, call.Args[0]); ok {
				out = append(out, viewCall{
					Method: "Exists",
					Names:  []viewNameSpan{sp},
				})
			}
		case "Share":
			if sp, ok := extractStringSpan(fset, call.Args[0]); ok {
				out = append(out, viewCall{
					Method:   "Share",
					ShareKey: &sp,
				})
			}
		case "Shared":
			if sp, ok := extractStringSpan(fset, call.Args[0]); ok {
				out = append(out, viewCall{
					Method:   "Shared",
					ShareKey: &sp,
				})
			}
		}
		return true
	})
	return out, fset
}

func extractStringSpan(fset *token.FileSet, expr ast.Expr) (viewNameSpan, bool) {
	bl, ok := expr.(*ast.BasicLit)
	if !ok || bl.Kind != token.STRING {
		return viewNameSpan{}, false
	}
	val, err := strconv.Unquote(bl.Value)
	if err != nil {
		return viewNameSpan{}, false
	}
	return viewNameSpan{
		Name:  val,
		Start: fset.Position(bl.Pos()).Offset,
		End:   fset.Position(bl.End()).Offset,
	}, true
}

func resolveDataMapArg(expr ast.Expr, localMaps map[string]*ast.CompositeLit) *ast.CompositeLit {
	if cl, ok := expr.(*ast.CompositeLit); ok {
		return cl
	}
	if ident, ok := expr.(*ast.Ident); ok && localMaps != nil {
		return localMaps[ident.Name]
	}
	return nil
}

// ViewCompletions suggests template paths inside View().Make/First/Exists/path.View and shared keys inside View().Shared/Share.
func ViewCompletions(path, src string, cat *indexer.Catalog, goOff int) []CompItem {
	if cat == nil {
		return nil
	}
	calls, _ := findViewCalls(path, src)
	for _, vc := range calls {
		for _, ns := range vc.Names {
			if goOff >= ns.Start && goOff <= ns.End {
				var names []string
				for k := range cat.Views {
					names = append(names, k)
				}
				sort.Strings(names)
				var out []CompItem
				for _, n := range names {
					detail := "resources/views/" + n
					doc := ""
					if vi := cat.ViewItems[n]; vi != nil {
						detail = vi.RelDisplay
						if len(vi.PackageLocs) > 0 {
							detail += " (overrides package view)"
						} else if vi.Source == "package" {
							detail += " (package view)"
						}
						if len(vi.UsedVars) > 0 {
							doc = "Variables: `." + strings.Join(vi.UsedVars, "`, `.`") + "`"
						}
					}
					out = append(out, CompItem{
						Label:  n,
						Detail: detail,
						Doc:    doc,
						Kind:   17, // File
					})
				}
				return out
			}
		}
		if vc.ShareKey != nil && goOff >= vc.ShareKey.Start && goOff <= vc.ShareKey.End {
			var keys []string
			for k := range cat.SharedViewVars {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			var out []CompItem
			for _, k := range keys {
				vvs := cat.SharedViewVars[k]
				detail := "shared view variable"
				if len(vvs) > 0 {
					detail = fmt.Sprintf("%s (from %s)", vvs[0].ValueExpr, vvs[0].Controller)
				}
				out = append(out, CompItem{
					Label:  k,
					Detail: detail,
					Kind:   6, // Variable
				})
			}
			return out
		}
	}
	return nil
}

// ViewHover shows info about the referenced view template or shared view key.
func ViewHover(path, src string, cat *indexer.Catalog, goOff int) *HoverResult {
	if cat == nil {
		return nil
	}
	calls, fset := findViewCalls(path, src)
	for _, vc := range calls {
		for _, ns := range vc.Names {
			if goOff >= ns.Start && goOff <= ns.End {
				vi := cat.LookupView(ns.Name)
				if vi == nil {
					if loc, ok := cat.Views[ns.Name]; ok {
						return &HoverResult{Markdown: fmt.Sprintf("**View `%s`**\n\nFile: `resources/views/%s` (`%s`)", ns.Name, ns.Name, loc.File)}
					}
					return &HoverResult{Markdown: fmt.Sprintf("**View `%s`** *(not found in `resources/views/` or package views)*", ns.Name)}
				}
				var b strings.Builder
				fmt.Fprintf(&b, "**View `%s`** (%s)\n\n", vi.Name, vi.Source)
				fmt.Fprintf(&b, "- File: `%s` (`%s`)\n", vi.RelDisplay, vi.Loc.File)
				if vi.DefineName != "" {
					fmt.Fprintf(&b, "- Define: `{{ define %q }}`\n", vi.DefineName)
				}
				if len(vi.PackageLocs) > 0 {
					var overrides []string
					for _, pl := range vi.PackageLocs {
						overrides = append(overrides, fmt.Sprintf("`%s`", pl.File))
					}
					fmt.Fprintf(&b, "- Overrides package view(s): %s\n", strings.Join(overrides, ", "))
				}
				if len(vi.Includes) > 0 {
					var incs []string
					for _, inc := range vi.Includes {
						incs = append(incs, fmt.Sprintf("`%s`", inc.Target))
					}
					fmt.Fprintf(&b, "- Includes: %s\n", strings.Join(incs, ", "))
				}
				if len(vi.UsedVars) > 0 {
					fmt.Fprintf(&b, "- Template variables: `.%s`\n", strings.Join(vi.UsedVars, "`, `."))
				}
				return &HoverResult{Markdown: b.String()}
			}
		}
		if vc.ShareKey != nil && goOff >= vc.ShareKey.Start && goOff <= vc.ShareKey.End {
			key := vc.ShareKey.Name
			vvs := cat.SharedViewVars[key]
			var b strings.Builder
			fmt.Fprintf(&b, "**Shared View Variable `.%s`**\n\n", key)
			for _, vv := range vvs {
				fmt.Fprintf(&b, "- Shared as `%s` in `%s` (`%s:%d`)\n", vv.ValueExpr, vv.Controller, filepath.Base(vv.Loc.File), vv.Loc.Line+1)
			}
			var usedIn []string
			for _, vi := range cat.ViewItems {
				for _, uv := range vi.UsedVars {
					if uv == key {
						usedIn = append(usedIn, fmt.Sprintf("`%s`", vi.Name))
						break
					}
				}
			}
			if len(usedIn) > 0 {
				sort.Strings(usedIn)
				fmt.Fprintf(&b, "- Referenced in templates: %s\n", strings.Join(usedIn, ", "))
			}
			return &HoverResult{Markdown: b.String()}
		}
		if vc.DataMap != nil && fset != nil && len(vc.Names) > 0 {
			for _, elt := range vc.DataMap.Elts {
				kv, ok := elt.(*ast.KeyValueExpr)
				if !ok {
					continue
				}
				sp, ok := extractStringSpan(fset, kv.Key)
				if !ok || goOff < sp.Start || goOff > sp.End {
					continue
				}
				vi := cat.LookupView(vc.Names[0].Name)
				if vi != nil {
					return &HoverResult{
						Markdown: fmt.Sprintf("**View Context Key `.%s`**\n\nPassed to template `%s` (`%s`)", sp.Name, vi.Name, vi.RelDisplay),
					}
				}
			}
		}
	}
	return nil
}

// ViewLocations returns all definition locations for a View call (active template + overridden package views, or template var occurrences).
func ViewLocations(path, src string, cat *indexer.Catalog, goOff int) []indexer.Location {
	if cat == nil {
		return nil
	}
	calls, fset := findViewCalls(path, src)
	var locs []indexer.Location
	for _, vc := range calls {
		for _, ns := range vc.Names {
			if goOff >= ns.Start && goOff <= ns.End {
				if vi := cat.LookupView(ns.Name); vi != nil {
					if vi.DefineLoc.Valid() {
						locs = append(locs, vi.DefineLoc)
					} else if vi.Loc.Valid() {
						locs = append(locs, vi.Loc)
					}
					for _, pl := range vi.PackageLocs {
						if pl.Valid() {
							locs = append(locs, pl)
						}
					}
					return locs
				}
				if loc, ok := cat.Views[ns.Name]; ok {
					return []indexer.Location{loc}
				}
			}
		}
		if vc.ShareKey != nil && goOff >= vc.ShareKey.Start && goOff <= vc.ShareKey.End {
			key := vc.ShareKey.Name
			if vc.Method == "Shared" {
				for _, vv := range cat.SharedViewVars[key] {
					if vv.Loc.Valid() {
						locs = append(locs, vv.Loc)
					}
				}
				if len(locs) > 0 {
					return locs
				}
			}
			// Jump to templates referencing .key
			for _, vi := range cat.ViewItems {
				locs = append(locs, findTemplateVarOccurrencesInFile(vi.Loc.File, key)...)
			}
			return locs
		}
		if vc.DataMap != nil && fset != nil {
			for _, elt := range vc.DataMap.Elts {
				kv, ok := elt.(*ast.KeyValueExpr)
				if !ok {
					continue
				}
				sp, ok := extractStringSpan(fset, kv.Key)
				if !ok || goOff < sp.Start || goOff > sp.End {
					continue
				}
				for _, ns := range vc.Names {
					if vi := cat.LookupView(ns.Name); vi != nil {
						locs = append(locs, findTemplateVarOccurrencesInFile(vi.Loc.File, sp.Name)...)
						for _, inc := range vi.Includes {
							if child := cat.LookupView(inc.Target); child != nil {
								locs = append(locs, findTemplateVarOccurrencesInFile(child.Loc.File, sp.Name)...)
							}
						}
					}
				}
				if len(locs) > 0 {
					return locs
				}
			}
		}
	}
	return nil
}

// ViewDefinition jumps from View().Make("...") to resources/views/<name> or package view.
func ViewDefinition(path, src string, cat *indexer.Catalog, goOff int) *DefResult {
	locs := ViewLocations(path, src, cat, goOff)
	if len(locs) > 0 {
		return &DefResult{Loc: locs[0]}
	}
	return nil
}

// ViewDiagnostics warns on missing templates and unused data keys in View().Make/First/Exists.
func ViewDiagnostics(path, src string, cat *indexer.Catalog) []Diag {
	if cat == nil || (len(cat.Views) == 0 && len(cat.ViewItems) == 0) {
		return nil
	}
	calls, fset := findViewCalls(path, src)
	var out []Diag
	for _, vc := range calls {
		switch vc.Method {
		case "Make", "PathView", "Exists":
			if len(vc.Names) == 0 {
				continue
			}
			ns := vc.Names[0]
			vi := cat.LookupView(ns.Name)
			if vi == nil {
				if _, ok := cat.Views[ns.Name]; !ok {
					sev := 2 // Warning
					if vc.Method == "Exists" {
						sev = 4 // Hint for Exists("...")
					}
					out = append(out, Diag{
						Start:    ns.Start,
						End:      ns.End,
						Severity: sev,
						Message:  fmt.Sprintf("View template '%s' not found in resources/views/ or registered package views", ns.Name),
					})
					continue
				}
			}
			if vc.Method == "Make" && vc.DataMap != nil && fset != nil {
				out = append(out, checkUnusedViewDataMap(vc.Names[0].Name, vi, vc.DataMap, fset, cat)...)
			}
		case "First":
			anyFound := false
			var firstValid *indexer.ViewItem
			for _, ns := range vc.Names {
				if vi := cat.LookupView(ns.Name); vi != nil {
					anyFound = true
					if firstValid == nil {
						firstValid = vi
					}
				} else if _, ok := cat.Views[ns.Name]; ok {
					anyFound = true
				}
			}
			if !anyFound && len(vc.Names) > 0 {
				out = append(out, Diag{
					Start:    vc.Names[0].Start,
					End:      vc.Names[len(vc.Names)-1].End,
					Severity: 2,
					Message:  "None of the fallback view templates in View().First(...) exist in resources/views/ or package views",
				})
			} else if firstValid != nil && vc.DataMap != nil && fset != nil {
				out = append(out, checkUnusedViewDataMap(firstValid.Name, firstValid, vc.DataMap, fset, cat)...)
			}
		case "Shared":
			if vc.ShareKey != nil && len(cat.SharedViewVars) > 0 {
				if _, ok := cat.SharedViewVars[vc.ShareKey.Name]; !ok {
					out = append(out, Diag{
						Start:    vc.ShareKey.Start,
						End:      vc.ShareKey.End,
						Severity: 4,
						Message:  fmt.Sprintf("Shared view key '%s' is not registered via facades.View().Share()", vc.ShareKey.Name),
					})
				}
			}
		}
	}
	return out
}

func checkUnusedViewDataMap(viewName string, vi *indexer.ViewItem, dataMap *ast.CompositeLit, fset *token.FileSet, cat *indexer.Catalog) []Diag {
	var filePath string
	usedSet := map[string]bool{}
	passesDot := false
	if vi != nil {
		filePath = vi.Loc.File
		passesDot = vi.PassesDot
		for _, uv := range vi.UsedVars {
			usedSet[uv] = true
		}
	} else if loc, ok := cat.Views[viewName]; ok {
		filePath = loc.File
	}
	if passesDot || filePath == "" {
		return nil
	}
	b, err := os.ReadFile(filePath)
	if err != nil {
		return nil
	}
	tplSrc := string(b)
	var out []Diag
	for _, elt := range dataMap.Elts {
		kv, ok := elt.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		bl, ok := kv.Key.(*ast.BasicLit)
		if !ok || bl.Kind != token.STRING {
			continue
		}
		k, err := strconv.Unquote(bl.Value)
		if err != nil || k == "" {
			continue
		}
		if !usedSet[k] && !strings.Contains(tplSrc, "."+k) {
			out = append(out, Diag{
				Start:    fset.Position(bl.Pos()).Offset,
				End:      fset.Position(bl.End()).Offset,
				Severity: 4, // Hint
				Message:  fmt.Sprintf("View data key '%s' is not referenced in %s", k, viewName),
			})
		}
	}
	return out
}

// ViewCodeActions offers a Quick Fix to scaffold a missing template in resources/views/.
func ViewCodeActions(path, src string, cat *indexer.Catalog, goOff int) []CodeAction {
	if cat == nil || cat.Root == "" {
		return nil
	}
	calls, _ := findViewCalls(path, src)
	var actions []CodeAction
	for _, vc := range calls {
		for _, ns := range vc.Names {
			if goOff < ns.Start || goOff > ns.End {
				continue
			}
			if cat.LookupView(ns.Name) != nil {
				continue
			}
			if _, ok := cat.Views[ns.Name]; ok {
				continue
			}
			relName := ns.Name
			if filepath.Ext(relName) == "" {
				relName += ".tmpl"
			}
			targetFile := filepath.Join(cat.Root, "resources", "views", filepath.FromSlash(relName))
			var bodyLines []string
			bodyLines = append(bodyLines, fmt.Sprintf("{{ define %q }}", relName))
			bodyLines = append(bodyLines, "<div>")
			if vc.DataMap != nil {
				for _, elt := range vc.DataMap.Elts {
					if kv, ok := elt.(*ast.KeyValueExpr); ok {
						if bl, ok := kv.Key.(*ast.BasicLit); ok && bl.Kind == token.STRING {
							if k, err := strconv.Unquote(bl.Value); err == nil && k != "" {
								bodyLines = append(bodyLines, fmt.Sprintf("  <p>{{ .%s }}</p>", k))
							}
						}
					}
				}
			}
			bodyLines = append(bodyLines, "</div>")
			bodyLines = append(bodyLines, "{{ end }}\n")
			actions = append(actions, CodeAction{
				Title: fmt.Sprintf("Create view template 'resources/views/%s'", relName),
				Kind:  "quickfix",
				Edits: []TextEdit{
					{
						File:    targetFile,
						Start:   0,
						End:     0,
						NewText: strings.Join(bodyLines, "\n"),
					},
				},
			})
		}
	}
	return actions
}

// IsTemplateFile reports whether path is an HTML/Go template file.
func IsTemplateFile(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	return ext == ".tmpl" || ext == ".html" || ext == ".gohtml"
}

func resolveViewVarsForTemplate(path string, cat *indexer.Catalog) []indexer.ViewVar {
	if cat == nil {
		return nil
	}
	slash := filepath.ToSlash(path)
	var keys []string
	if vi := cat.ViewForPath(path); vi != nil {
		keys = append(keys, vi.Name, strings.TrimSuffix(vi.Name, filepath.Ext(vi.Name)))
		if vi.DefineName != "" {
			keys = append(keys, vi.DefineName)
		}
	}
	if idx := strings.Index(slash, "/resources/views/"); idx >= 0 {
		rel := slash[idx+len("/resources/views/"):]
		keys = append(keys, rel, strings.TrimSuffix(rel, filepath.Ext(rel)))
	}
	for _, pkgDir := range cat.PackageViewDirs {
		pkgSlash := filepath.ToSlash(pkgDir) + "/"
		if strings.HasPrefix(slash, pkgSlash) {
			rel := strings.TrimPrefix(slash, pkgSlash)
			keys = append(keys, rel, strings.TrimSuffix(rel, filepath.Ext(rel)))
		}
	}
	base := filepath.Base(slash)
	keys = append(keys, base, strings.TrimSuffix(base, filepath.Ext(base)))

	seen := map[string]bool{}
	var out []indexer.ViewVar
	for _, k := range keys {
		for _, vv := range cat.ViewVars[k] {
			id := vv.Key + "@" + vv.Controller + "@" + vv.Inherited
			if !seen[id] {
				seen[id] = true
				out = append(out, vv)
			}
		}
	}
	// Include global shared variables from facades.View().Share(key, val)
	sharedKeys := make([]string, 0, len(cat.SharedViewVars))
	for sk := range cat.SharedViewVars {
		sharedKeys = append(sharedKeys, sk)
	}
	sort.Strings(sharedKeys)
	for _, sk := range sharedKeys {
		for _, sv := range cat.SharedViewVars[sk] {
			id := sv.Key + "@shared@" + sv.Controller
			if !seen[id] {
				seen[id] = true
				out = append(out, sv)
			}
		}
	}
	return out
}

func isInsideTemplateAction(src string, off int) bool {
	if off < 0 || off > len(src) {
		return false
	}
	openIdx := strings.LastIndex(src[:off], "{{")
	if openIdx < 0 {
		return false
	}
	closeBefore := strings.LastIndex(src[:off], "}}")
	return openIdx > closeBefore
}

func currentTemplateActionPrefix(src string, off int) string {
	if off < 0 || off > len(src) {
		return ""
	}
	openIdx := strings.LastIndex(src[:off], "{{")
	if openIdx < 0 {
		return ""
	}
	inner := src[openIdx+2 : off]
	return strings.TrimPrefix(inner, "-")
}

var (
	includeCompletionPrefixRe = regexp.MustCompile(`^\s*(?:template|block)\s+"([^"]*)$`)
	nestedFieldPrefixRe       = regexp.MustCompile(`(?:^|[\s(|,=])(\$?[A-Za-z_][A-Za-z0-9_]*|\.?[A-Za-z_][A-Za-z0-9_]*)\.([A-Za-z0-9_]*)$`)
	localVarDeclRe            = regexp.MustCompile(`\{\{-?\s*(?:range\s+|with\s+|if\s+)?(\$[A-Za-z_][A-Za-z0-9_]*)(?:\s*,\s*(\$[A-Za-z_][A-Za-z0-9_]*))?\s*:=\s*([^}\n]+?)\s*-?\}\}`)
)

type localTemplateVar struct {
	Name   string
	Expr   string
	GoType string
	Offset int
}

func extractLocalTemplateVars(src string, limitOff int, vars []indexer.ViewVar, cat *indexer.Catalog) []localTemplateVar {
	if limitOff > len(src) {
		limitOff = len(src)
	}
	var out []localTemplateVar
	seen := map[string]bool{}
	for _, m := range localVarDeclRe.FindAllStringSubmatchIndex(src[:limitOff], -1) {
		rhs := strings.TrimSpace(src[m[6]:m[7]])
		rhsField := strings.TrimPrefix(strings.TrimPrefix(rhs, "$."), ".")
		inferredType := ""
		for _, vv := range vars {
			if vv.Key == rhsField && vv.GoType != "" {
				inferredType = vv.GoType
				break
			}
		}
		if m[4] >= 0 && m[5] >= 0 {
			// range $idx, $item := .items
			idxName := src[m[2]:m[3]]
			itemName := src[m[4]:m[5]]
			if !seen[idxName] {
				seen[idxName] = true
				out = append(out, localTemplateVar{Name: idxName, Expr: "index of " + rhs, Offset: m[2]})
			}
			if !seen[itemName] {
				seen[itemName] = true
				out = append(out, localTemplateVar{Name: itemName, Expr: rhs, GoType: inferredType, Offset: m[4]})
			}
		} else if m[2] >= 0 && m[3] >= 0 {
			vName := src[m[2]:m[3]]
			if !seen[vName] {
				seen[vName] = true
				out = append(out, localTemplateVar{Name: vName, Expr: rhs, GoType: inferredType, Offset: m[2]})
			}
		}
	}
	_ = cat
	return out
}

var (
	tmplKeywords = []struct {
		Label  string
		Detail string
	}{
		{"define", "Define a named template block: {{ define \"name\" }}...{{ end }}"},
		{"template", "Include and execute a named template: {{ template \"name\" . }}"},
		{"block", "Define and execute a replaceable block: {{ block \"name\" . }}...{{ end }}"},
		{"if", "Conditional block: {{ if .condition }}...{{ end }}"},
		{"else", "Else branch in if/with/range block"},
		{"range", "Iterate over slice/map/channel: {{ range .items }}...{{ end }}"},
		{"with", "Set dot (.) to pipeline value if non-empty: {{ with .user }}...{{ end }}"},
		{"end", "Close define, if, range, with, or block action"},
		{"nil", "Untyped Go nil constant"},
		{"break", "Exit innermost range loop early"},
		{"continue", "Skip to next iteration of innermost range loop"},
	}
	tmplBuiltins = []struct {
		Label  string
		Detail string
	}{
		{"len", "built-in: len(item) -> int length of slice/map/string"},
		{"index", "built-in: index(collection, keyOrIdx...) -> element"},
		{"slice", "built-in: slice(item, low, high...) -> sub-slice"},
		{"printf", "built-in: fmt.Sprintf(format, args...)"},
		{"print", "built-in: fmt.Sprint(args...)"},
		{"println", "built-in: fmt.Sprintln(args...)"},
		{"and", "built-in: boolean AND (returns first empty arg or last arg)"},
		{"or", "built-in: boolean OR (returns first non-empty arg or last arg)"},
		{"not", "built-in: boolean negation of single argument"},
		{"eq", "built-in: arg1 == arg2 (or arg1 == arg2 || arg1 == arg3 ...)"},
		{"ne", "built-in: arg1 != arg2"},
		{"lt", "built-in: arg1 < arg2"},
		{"le", "built-in: arg1 <= arg2"},
		{"gt", "built-in: arg1 > arg2"},
		{"ge", "built-in: arg1 >= arg2"},
		{"html", "built-in: escape HTML special characters"},
		{"js", "built-in: escape JavaScript special characters"},
		{"urlquery", "built-in: escape URL query string"},
		{"call", "built-in: call a function value with arguments"},
	}
)

// TemplateCompletions suggests sub-templates, context/shared/inherited variables, nested struct fields, local $vars, and Go template keywords/functions inside {{ ... }}.
func TemplateCompletions(path, src string, cat *indexer.Catalog, off int) []CompItem {
	if !IsTemplateFile(path) || !isInsideTemplateAction(src, off) {
		return nil
	}
	prefix := currentTemplateActionPrefix(src, off)

	// 1. Inside {{ template "|" }} or {{ block "|" }}
	if includeCompletionPrefixRe.MatchString(prefix) {
		if cat == nil {
			return nil
		}
		seen := map[string]bool{}
		var names []string
		for k := range cat.Views {
			names = append(names, k)
			seen[k] = true
		}
		for _, vi := range cat.ViewItems {
			if vi.DefineName != "" && !seen[vi.DefineName] {
				names = append(names, vi.DefineName)
				seen[vi.DefineName] = true
			}
		}
		sort.Strings(names)
		var out []CompItem
		for _, n := range names {
			detail := "resources/views/" + n
			if vi := cat.LookupView(n); vi != nil {
				detail = vi.RelDisplay
			}
			out = append(out, CompItem{
				Label:  n,
				Detail: detail,
				Kind:   17, // File
			})
		}
		return out
	}

	vars := resolveViewVarsForTemplate(path, cat)
	locals := extractLocalTemplateVars(src, off, vars, cat)

	// 2. Nested struct/model field completion: {{ .user.| }} or {{ $item.| }}
	if m := nestedFieldPrefixRe.FindStringSubmatch(prefix); m != nil && cat != nil {
		holder := strings.TrimPrefix(strings.TrimPrefix(m[1], "$."), ".")
		goType := ""
		if strings.HasPrefix(m[1], "$") && !strings.HasPrefix(m[1], "$.") {
			for _, lv := range locals {
				if lv.Name == m[1] && lv.GoType != "" {
					goType = lv.GoType
					break
				}
			}
		} else {
			for _, vv := range vars {
				if vv.Key == holder && vv.GoType != "" {
					goType = vv.GoType
					break
				}
			}
		}
		if goType != "" {
			if items := structFieldCompletions(goType, cat); len(items) > 0 {
				return items
			}
		}
	}

	hasDotPrefix := off > 0 && src[off-1] == '.'
	hasDollarPrefix := off > 0 && src[off-1] == '$'

	// 3. Local template variable completion when typing $
	if hasDollarPrefix {
		var out []CompItem
		for _, lv := range locals {
			lbl := strings.TrimPrefix(lv.Name, "$")
			detail := lv.Expr
			if lv.GoType != "" {
				detail += " (" + lv.GoType + ")"
			}
			out = append(out, CompItem{
				Label:  lbl,
				Detail: detail,
				Kind:   6, // Variable
			})
		}
		return out
	}

	var out []CompItem
	seen := map[string]bool{}
	for _, vv := range vars {
		if seen[vv.Key] {
			continue
		}
		seen[vv.Key] = true
		lbl := "." + vv.Key
		if hasDotPrefix {
			lbl = vv.Key
		}
		detail := vv.ValueExpr
		if vv.GoType != "" {
			detail += " [" + vv.GoType + "]"
		}
		if vv.Shared {
			detail += " (shared via View().Share)"
		} else if vv.Inherited != "" {
			detail += fmt.Sprintf(" (via %s ← %s)", vv.Inherited, vv.Controller)
		} else if vv.Controller != "" {
			detail = fmt.Sprintf("%s (from %s)", detail, vv.Controller)
		}
		out = append(out, CompItem{
			Label:  lbl,
			Detail: detail,
			Kind:   6, // Variable
		})
	}
	if !seen["csrf_token"] {
		lbl := ".csrf_token"
		if hasDotPrefix {
			lbl = "csrf_token"
		}
		out = append(out, CompItem{
			Label:  lbl,
			Detail: "Goravel CSRF Token (VerifyCsrfToken middleware)",
			Kind:   6,
		})
	}

	// If not immediately after '.', also include local $vars, template keywords, and built-in functions
	if !hasDotPrefix {
		for _, lv := range locals {
			out = append(out, CompItem{
				Label:  lv.Name,
				Detail: "local: " + lv.Expr,
				Kind:   6,
			})
		}
		for _, kw := range tmplKeywords {
			out = append(out, CompItem{
				Label:  kw.Label,
				Detail: kw.Detail,
				Kind:   14, // Keyword
			})
		}
		for _, fn := range tmplBuiltins {
			out = append(out, CompItem{
				Label:  fn.Label,
				Detail: fn.Detail,
				Kind:   3, // Function
			})
		}
	}
	return out
}

func structFieldCompletions(goType string, cat *indexer.Catalog) []CompItem {
	var out []CompItem
	seen := map[string]bool{}
	if st := cat.Struct(goType); st != nil {
		for _, f := range st.Fields {
			if f.GoName == "" || seen[f.GoName] {
				continue
			}
			seen[f.GoName] = true
			detail := f.GoType
			if f.Col != "" {
				detail += " (column: " + f.Col + ")"
			}
			out = append(out, CompItem{
				Label:  f.GoName,
				Detail: detail,
				Kind:   5, // Field
			})
		}
	}
	if tbl := cat.Model(goType); tbl != nil {
		for _, c := range tbl.Columns {
			if c.GoName == "" || seen[c.GoName] {
				continue
			}
			seen[c.GoName] = true
			out = append(out, CompItem{
				Label:  c.GoName,
				Detail: fmt.Sprintf("%s (column: %s)", c.GoType, c.Name),
				Kind:   5,
			})
		}
		for _, r := range tbl.Relations {
			if r.Name == "" || seen[r.Name] {
				continue
			}
			seen[r.Name] = true
			out = append(out, CompItem{
				Label:  r.Name,
				Detail: fmt.Sprintf("relation -> models.%s", r.TargetModel),
				Kind:   5,
			})
		}
	}
	return out
}

// TemplateHover displays controller origin, value expression, sub-template info, or struct field info inside a template.
func TemplateHover(path, src string, cat *indexer.Catalog, off int) *HoverResult {
	if !IsTemplateFile(path) || !isInsideTemplateAction(src, off) {
		return nil
	}

	// 1. Check if hovering over {{ template "name" }} or {{ define "name" }} or {{ block "name" }}
	if directive, targetName := templateDirectiveAtOffset(src, off); directive != "" && targetName != "" && cat != nil {
		vi := cat.LookupView(targetName)
		if directive == "define" {
			if vi == nil {
				vi = cat.ViewForPath(path)
			}
			var b strings.Builder
			fmt.Fprintf(&b, "**Template Definition `%s`**\n\n", targetName)
			if vi != nil {
				fmt.Fprintf(&b, "- File: `%s`\n", vi.RelDisplay)
				if len(vi.Callers) > 0 {
					b.WriteString("- Rendered/Referenced by:\n")
					for _, c := range vi.Callers {
						lbl := c.Controller
						if c.Route != "" {
							lbl += " (`" + c.Route + "`)"
						}
						fmt.Fprintf(&b, "  - `%s` in `%s` (`%s:%d`)\n", c.Method, lbl, filepath.Base(c.Loc.File), c.Loc.Line+1)
					}
				}
			}
			return &HoverResult{Markdown: b.String()}
		}
		if vi == nil {
			return &HoverResult{Markdown: fmt.Sprintf("**Included Template `%s`** *(not found)*", targetName)}
		}
		var b strings.Builder
		fmt.Fprintf(&b, "**Included Template `%s`** (%s)\n\n", vi.Name, vi.Source)
		fmt.Fprintf(&b, "- File: `%s` (`%s`)\n", vi.RelDisplay, vi.Loc.File)
		if raw, err := os.ReadFile(vi.Loc.File); err == nil {
			preview := strings.TrimSpace(string(raw))
			if len(preview) > 400 {
				preview = preview[:400] + "\n..."
			}
			fmt.Fprintf(&b, "\n```html\n%s\n```", preview)
		}
		return &HoverResult{Markdown: b.String()}
	}

	vars := resolveViewVarsForTemplate(path, cat)
	locals := extractLocalTemplateVars(src, len(src), vars, cat)

	// 2. Check nested struct field hover: .user.Name or $item.Name
	if holder, field := nestedFieldAtOffset(src, off); holder != "" && field != "" && cat != nil {
		goType := resolveHolderGoType(holder, vars, locals)
		if goType != "" {
			if tbl := cat.Model(goType); tbl != nil {
				for _, c := range tbl.Columns {
					if c.GoName == field {
						return &HoverResult{
							Markdown: fmt.Sprintf("**`models.%s.%s`** (`%s`)\n\n- Table column: `%s.%s` (`%s`)", goType, c.GoName, c.GoType, tbl.Name, c.Name, c.SQLType),
						}
					}
				}
			}
			if st := cat.Struct(goType); st != nil {
				for _, f := range st.Fields {
					if f.GoName == field {
						return &HoverResult{
							Markdown: fmt.Sprintf("**`%s.%s`** (`%s`)", st.Name, f.GoName, f.GoType),
						}
					}
				}
			}
		}
	}

	// 3. Check local $var hover
	if lvar := dollarVarAtOffset(src, off); lvar != "" {
		for _, lv := range locals {
			if lv.Name == lvar {
				line, _ := offsetToLineCol(src, lv.Offset)
				return &HoverResult{
					Markdown: fmt.Sprintf("**Local Template Variable `%s`**\n\n- Assigned from: `%s` (line %d)", lv.Name, lv.Expr, line+1),
				}
			}
		}
	}

	// 4. Context / shared variable hover (.varName)
	word := templateVarAtOffset(src, off)
	if word == "" {
		return nil
	}
	var lines []string
	for _, vv := range vars {
		if vv.Key == word {
			srcInfo := fmt.Sprintf("`%s`", vv.ValueExpr)
			if vv.GoType != "" {
				srcInfo += fmt.Sprintf(" (type `%s`)", vv.GoType)
			}
			if vv.Shared {
				srcInfo += fmt.Sprintf(" shared via `View().Share` in `%s` (`%s:%d`)", vv.Controller, filepath.Base(vv.Loc.File), vv.Loc.Line+1)
			} else if vv.Inherited != "" {
				srcInfo += fmt.Sprintf(" via parent `%s` from `%s` (`%s:%d`)", vv.Inherited, vv.Controller, filepath.Base(vv.Loc.File), vv.Loc.Line+1)
			} else if vv.Controller != "" {
				srcInfo += fmt.Sprintf(" in `%s` (`%s:%d`)", vv.Controller, filepath.Base(vv.Loc.File), vv.Loc.Line+1)
			}
			lines = append(lines, "- Passed as "+srcInfo)
		}
	}
	if len(lines) > 0 {
		return &HoverResult{
			Markdown: fmt.Sprintf("**Template Variable `.%s`**\n\n%s", word, strings.Join(lines, "\n")),
		}
	}

	// 5. Built-in keyword / function hover
	for _, kw := range tmplKeywords {
		if kw.Label == word {
			return &HoverResult{Markdown: fmt.Sprintf("**Go Template Keyword `%s`**\n\n%s", kw.Label, kw.Detail)}
		}
	}
	for _, fn := range tmplBuiltins {
		if fn.Label == word {
			return &HoverResult{Markdown: fmt.Sprintf("**Go Template Function `%s`**\n\n%s", fn.Label, fn.Detail)}
		}
	}
	return nil
}

// TemplateDefinition jumps from .varName, .varName.Field, $local, or {{ template "name" }} in a template file to its definition.
func TemplateDefinition(path, src string, cat *indexer.Catalog, off int) []indexer.Location {
	if !IsTemplateFile(path) || !isInsideTemplateAction(src, off) {
		return nil
	}

	// 1. Jump from {{ template "name" }} or {{ block "name" }} to target template file (or from {{ define "name" }} to Go callers)
	if directive, targetName := templateDirectiveAtOffset(src, off); directive != "" && targetName != "" && cat != nil {
		var locs []indexer.Location
		if directive == "define" {
			if vi := cat.LookupView(targetName); vi != nil {
				for _, c := range vi.Callers {
					if c.Loc.Valid() {
						locs = append(locs, c.Loc)
					}
				}
			}
			return locs
		}
		if vi := cat.LookupView(targetName); vi != nil {
			if vi.DefineLoc.Valid() {
				locs = append(locs, vi.DefineLoc)
			} else if vi.Loc.Valid() {
				locs = append(locs, vi.Loc)
			}
			for _, pl := range vi.PackageLocs {
				if pl.Valid() {
					locs = append(locs, pl)
				}
			}
			return locs
		}
	}

	vars := resolveViewVarsForTemplate(path, cat)
	locals := extractLocalTemplateVars(src, len(src), vars, cat)

	// 2. Jump from .user.Name to Go struct field definition
	if holder, field := nestedFieldAtOffset(src, off); holder != "" && field != "" && cat != nil {
		goType := resolveHolderGoType(holder, vars, locals)
		if goType != "" {
			if tbl := cat.Model(goType); tbl != nil {
				for _, c := range tbl.Columns {
					if c.GoName == field && c.Loc.Valid() {
						return []indexer.Location{c.Loc}
					}
				}
				for _, r := range tbl.Relations {
					if r.Name == field && r.Loc.Valid() {
						return []indexer.Location{r.Loc}
					}
				}
			}
			if st := cat.Struct(goType); st != nil {
				for _, f := range st.Fields {
					if f.GoName == field && f.Loc.Valid() {
						return []indexer.Location{f.Loc}
					}
				}
			}
		}
	}

	// 3. Jump from $localVar to its := assignment in the current template
	if lvar := dollarVarAtOffset(src, off); lvar != "" {
		for _, lv := range locals {
			if lv.Name == lvar {
				line, col := offsetToLineCol(src, lv.Offset)
				return []indexer.Location{{File: path, Line: line, Col: col}}
			}
		}
	}

	// 4. Jump from .varName to controller View().Make/First map key or View().Share
	word := templateVarAtOffset(src, off)
	if word == "" {
		return nil
	}
	var locs []indexer.Location
	for _, vv := range vars {
		if vv.Key == word && vv.Loc.Valid() {
			locs = append(locs, vv.Loc)
		}
	}
	return locs
}

var (
	blockOpenKeywordRe = regexp.MustCompile(`^\s*(define|if|range|with|block)\b`)
	blockEndKeywordRe  = regexp.MustCompile(`^\s*end\b`)
)

// TemplateDiagnostics validates template syntax (unclosed {{, unbalanced block/end, missing included templates, and mismatched define names).
func TemplateDiagnostics(path, src string, cat *indexer.Catalog) []Diag {
	if !IsTemplateFile(path) {
		return nil
	}
	var diags []Diag

	// 1. Check for unclosed {{ action
	searchFrom := 0
	for searchFrom < len(src) {
		openIdx := strings.Index(src[searchFrom:], "{{")
		if openIdx < 0 {
			break
		}
		absOpen := searchFrom + openIdx
		closeIdx := strings.Index(src[absOpen+2:], "}}")
		if closeIdx < 0 {
			diags = append(diags, Diag{
				Start:    absOpen,
				End:      len(src),
				Severity: 1, // Error
				Message:  "Unclosed Go template action '{{'",
			})
			break
		}
		searchFrom = absOpen + 2 + closeIdx + 2
	}

	// 2. Check block balance (define/if/range/with/block vs end) and included templates
	type openBlock struct {
		kw    string
		start int
		end   int
	}
	var stack []openBlock
	localDefines := map[string]bool{}
	var defineSpans []struct {
		name  string
		start int
		end   int
	}
	var includeSpans []struct {
		target string
		start  int
		end    int
	}

	matches := regexp.MustCompile(`(?s)\{\{-?(.*?)-?\}\}`).FindAllStringSubmatchIndex(src, -1)
	for _, m := range matches {
		actStart, actEnd := m[0], m[1]
		bodyStart, bodyEnd := m[2], m[3]
		body := src[bodyStart:bodyEnd]
		trimmed := strings.TrimSpace(body)
		if strings.HasPrefix(trimmed, "/*") {
			continue
		}
		if dm := regexp.MustCompile(`^\s*define\s+"([^"]+)"`).FindStringSubmatchIndex(body); dm != nil {
			nStart, nEnd := bodyStart+dm[2], bodyStart+dm[3]
			dName := src[nStart:nEnd]
			localDefines[dName] = true
			defineSpans = append(defineSpans, struct {
				name  string
				start int
				end   int
			}{dName, nStart, nEnd})
		}
		if im := regexp.MustCompile(`^\s*(?:template|block)\s+"([^"]+)"`).FindStringSubmatchIndex(body); im != nil {
			nStart, nEnd := bodyStart+im[2], bodyStart+im[3]
			tName := src[nStart:nEnd]
			includeSpans = append(includeSpans, struct {
				target string
				start  int
				end    int
			}{tName, nStart, nEnd})
		}
		if bm := blockOpenKeywordRe.FindStringSubmatch(body); bm != nil {
			stack = append(stack, openBlock{kw: bm[1], start: actStart, end: actEnd})
		} else if blockEndKeywordRe.MatchString(body) {
			if len(stack) == 0 {
				diags = append(diags, Diag{
					Start:    actStart,
					End:      actEnd,
					Severity: 1,
					Message:  "Unexpected '{{ end }}' without matching '{{ if / range / with / define / block }}'",
				})
			} else {
				stack = stack[:len(stack)-1]
			}
		}
	}
	for _, unclosed := range stack {
		diags = append(diags, Diag{
			Start:    unclosed.start,
			End:      unclosed.end,
			Severity: 1,
			Message:  fmt.Sprintf("Unclosed '{{ %s }}' block (missing '{{ end }}')", unclosed.kw),
		})
	}

	if cat != nil && (len(cat.Views) > 0 || len(cat.ViewItems) > 0) {
		for _, inc := range includeSpans {
			if localDefines[inc.target] {
				continue
			}
			if cat.LookupView(inc.target) == nil {
				if _, ok := cat.Views[inc.target]; !ok {
					diags = append(diags, Diag{
						Start:    inc.start,
						End:      inc.end,
						Severity: 2, // Warning
						Message:  fmt.Sprintf("Included template '%s' not found in resources/views/ or registered package views", inc.target),
					})
				}
			}
		}
		if len(defineSpans) == 1 {
			if vi := cat.ViewForPath(path); vi != nil && vi.Source == "app" {
				d := defineSpans[0]
				noExt := strings.TrimSuffix(vi.Name, filepath.Ext(vi.Name))
				if d.name != vi.Name && d.name != noExt {
					diags = append(diags, Diag{
						Start:    d.start,
						End:      d.end,
						Severity: 4, // Hint
						Message:  fmt.Sprintf("Template define name '%s' does not match relative view path '%s'", d.name, vi.Name),
					})
				}
			}
		}
	}
	return diags
}

// TemplateCodeActions provides Quick Fixes inside .tmpl files (sync define name, create missing partial template).
func TemplateCodeActions(path, src string, cat *indexer.Catalog, off int) []CodeAction {
	if !IsTemplateFile(path) || cat == nil {
		return nil
	}
	var actions []CodeAction
	directive, targetName, nStart, nEnd := templateDirectiveSpanAtOffset(src, off)
	if directive == "define" && targetName != "" {
		if vi := cat.ViewForPath(path); vi != nil && vi.Name != targetName {
			actions = append(actions, CodeAction{
				Title: fmt.Sprintf("Sync {{ define }} name to '%s'", vi.Name),
				Kind:  "quickfix",
				Edits: []TextEdit{
					{
						File:    path,
						Start:   nStart,
						End:     nEnd,
						NewText: vi.Name,
					},
				},
			})
		}
	} else if (directive == "template" || directive == "block") && targetName != "" && cat.Root != "" {
		if cat.LookupView(targetName) == nil {
			relName := targetName
			if filepath.Ext(relName) == "" {
				relName += ".tmpl"
			}
			targetFile := filepath.Join(cat.Root, "resources", "views", filepath.FromSlash(relName))
			content := fmt.Sprintf("{{ define %q }}\n\n{{ end }}\n", targetName)
			actions = append(actions, CodeAction{
				Title: fmt.Sprintf("Create partial template 'resources/views/%s'", relName),
				Kind:  "quickfix",
				Edits: []TextEdit{
					{
						File:    targetFile,
						Start:   0,
						End:     0,
						NewText: content,
					},
				},
			})
		}
	}
	return actions
}

// TemplateLens describes a CodeLens item for a template file.
type TemplateLens struct {
	Line  int
	Col   int
	Title string
	File  string
	TLine int
	TCol  int
}

// TemplateCodeLenses returns CodeLens items for a template file (Go callers, parent template includes, and package overrides).
func TemplateCodeLenses(path string, cat *indexer.Catalog) []TemplateLens {
	if !IsTemplateFile(path) || cat == nil {
		return nil
	}
	vi := cat.ViewForPath(path)
	if vi == nil {
		return nil
	}
	line := 0
	if vi.DefineLoc.Valid() && filepath.Clean(vi.DefineLoc.File) == filepath.Clean(path) {
		line = vi.DefineLoc.Line
	}
	var lenses []TemplateLens
	seenCaller := map[string]bool{}
	for _, c := range vi.Callers {
		if !c.Loc.Valid() {
			continue
		}
		key := fmt.Sprintf("%s:%d:%s", c.Loc.File, c.Loc.Line, c.Method)
		if seenCaller[key] {
			continue
		}
		seenCaller[key] = true
		title := fmt.Sprintf("$(symbol-method) View().%s in %s (%s)", c.Method, c.Controller, filepath.Base(c.Loc.File))
		if c.Route != "" {
			title = fmt.Sprintf("$(globe) %s → %s (%s)", c.Route, c.Controller, filepath.Base(c.Loc.File))
		}
		lenses = append(lenses, TemplateLens{
			Line:  line,
			Col:   0,
			Title: title,
			File:  c.Loc.File,
			TLine: c.Loc.Line,
			TCol:  c.Loc.Col,
		})
	}

	// Check parent templates that include this template via {{ template "..." }}
	for _, other := range cat.ViewItems {
		for _, inc := range other.Includes {
			if target := cat.LookupView(inc.Target); target != nil && target.Name == vi.Name && inc.Loc.Valid() {
				lenses = append(lenses, TemplateLens{
					Line:  line,
					Col:   0,
					Title: fmt.Sprintf("$(references) Included by %s", other.Name),
					File:  inc.Loc.File,
					TLine: inc.Loc.Line,
					TCol:  inc.Loc.Col,
				})
			}
		}
	}

	// Check package override relationship
	cleanPath := filepath.Clean(path)
	if filepath.Clean(vi.Loc.File) == cleanPath && len(vi.PackageLocs) > 0 {
		pl := vi.PackageLocs[0]
		lenses = append(lenses, TemplateLens{
			Line:  line,
			Col:   0,
			Title: fmt.Sprintf("$(layers) Overrides package view (%s)", filepath.Base(pl.File)),
			File:  pl.File,
			TLine: pl.Line,
			TCol:  pl.Col,
		})
	} else if filepath.Clean(vi.Loc.File) != cleanPath {
		lenses = append(lenses, TemplateLens{
			Line:  line,
			Col:   0,
			Title: fmt.Sprintf("$(warning) Overridden by app view (resources/views/%s)", vi.Name),
			File:  vi.Loc.File,
			TLine: vi.Loc.Line,
			TCol:  vi.Loc.Col,
		})
	}
	return lenses
}

func templateDirectiveAtOffset(src string, off int) (directive, target string) {
	d, t, _, _ := templateDirectiveSpanAtOffset(src, off)
	return d, t
}

func templateDirectiveSpanAtOffset(src string, off int) (directive, target string, nameStart, nameEnd int) {
	if off < 0 || off > len(src) {
		return "", "", 0, 0
	}
	openIdx := strings.LastIndex(src[:off], "{{")
	if openIdx < 0 {
		return "", "", 0, 0
	}
	closeRel := strings.Index(src[openIdx:], "}}")
	if closeRel < 0 {
		return "", "", 0, 0
	}
	closeIdx := openIdx + closeRel + 2
	if off > closeIdx {
		return "", "", 0, 0
	}
	act := src[openIdx:closeIdx]
	re := regexp.MustCompile(`^\{\{-?\s*(define|template|block)\s+"([^"]+)"`)
	m := re.FindStringSubmatchIndex(act)
	if m == nil {
		return "", "", 0, 0
	}
	return act[m[2]:m[3]], act[m[4]:m[5]], openIdx + m[4], openIdx + m[5]
}

func nestedFieldAtOffset(src string, off int) (holder, field string) {
	if off < 0 || off > len(src) {
		return "", ""
	}
	l, r := off, off
	for l > 0 && isIdentChar(src[l-1]) {
		l--
	}
	for r < len(src) && isIdentChar(src[r]) {
		r++
	}
	if l == r || l == 0 || src[l-1] != '.' {
		return "", ""
	}
	field = src[l:r]
	hlEnd := l - 1
	hlStart := hlEnd
	for hlStart > 0 && (isIdentChar(src[hlStart-1]) || src[hlStart-1] == '$' || src[hlStart-1] == '.') {
		hlStart--
	}
	holder = strings.TrimPrefix(strings.TrimPrefix(src[hlStart:hlEnd], "$."), ".")
	if holder == "" {
		return "", ""
	}
	return holder, field
}

func resolveHolderGoType(holder string, vars []indexer.ViewVar, locals []localTemplateVar) string {
	if strings.HasPrefix(holder, "$") {
		for _, lv := range locals {
			if lv.Name == holder && lv.GoType != "" {
				return lv.GoType
			}
		}
		return ""
	}
	for _, vv := range vars {
		if vv.Key == holder && vv.GoType != "" {
			return vv.GoType
		}
	}
	return ""
}

func dollarVarAtOffset(src string, off int) string {
	if off < 0 || off > len(src) {
		return ""
	}
	l, r := off, off
	for l > 0 && isIdentChar(src[l-1]) {
		l--
	}
	for r < len(src) && isIdentChar(src[r]) {
		r++
	}
	if l > 0 && src[l-1] == '$' && l < r {
		return "$" + src[l:r]
	}
	if l < len(src) && src[l] == '$' && r > l+1 {
		return src[l:r]
	}
	return ""
}

func templateVarAtOffset(src string, off int) string {
	if off < 0 || off > len(src) {
		return ""
	}
	l, r := off, off
	for l > 0 && isIdentChar(src[l-1]) {
		l--
	}
	for r < len(src) && isIdentChar(src[r]) {
		r++
	}
	if l == r {
		return ""
	}
	return src[l:r]
}

// TemplateTargetAtOffset inspects a .go or .tmpl file at off and returns either a target view name or (viewName, varKey) for References/Rename.
func TemplateTargetAtOffset(path, src string, cat *indexer.Catalog, off int) (targetViewName, targetVarKey string, isSharedVar bool) {
	if IsTemplateFile(path) {
		if !isInsideTemplateAction(src, off) {
			return "", "", false
		}
		if _, tName := templateDirectiveAtOffset(src, off); tName != "" {
			return tName, "", false
		}
		word := templateVarAtOffset(src, off)
		if word == "" {
			return "", "", false
		}
		if cat != nil {
			if _, ok := cat.SharedViewVars[word]; ok {
				return "", word, true
			}
			if vi := cat.ViewForPath(path); vi != nil {
				return vi.Name, word, false
			}
		}
		return filepath.Base(path), word, false
	}
	calls, fset := findViewCalls(path, src)
	for _, vc := range calls {
		for _, ns := range vc.Names {
			if off >= ns.Start && off <= ns.End {
				return ns.Name, "", false
			}
		}
		if vc.ShareKey != nil && off >= vc.ShareKey.Start && off <= vc.ShareKey.End {
			return "", vc.ShareKey.Name, true
		}
		if vc.DataMap != nil && fset != nil && len(vc.Names) > 0 {
			for _, elt := range vc.DataMap.Elts {
				kv, ok := elt.(*ast.KeyValueExpr)
				if !ok {
					continue
				}
				if sp, ok := extractStringSpan(fset, kv.Key); ok && off >= sp.Start && off <= sp.End {
					return vc.Names[0].Name, sp.Name, false
				}
			}
		}
	}
	return "", "", false
}

// FindViewAndTemplateSpans returns all byte spans [start, end] in (fpath, content) matching targetViewName or (targetViewName, targetVarKey).
func FindViewAndTemplateSpans(fpath, content string, cat *indexer.Catalog, targetViewName, targetVarKey string, isSharedVar bool) [][2]int {
	var spans [][2]int
	if IsTemplateFile(fpath) {
		if targetViewName != "" && targetVarKey == "" {
			// Match {{ define "target" }} or {{ template "target" }} or {{ block "target" }}
			re := regexp.MustCompile(`\{\{-?\s*(?:define|template|block)\s+"([^"]+)"`)
			for _, m := range re.FindAllStringSubmatchIndex(content, -1) {
				name := content[m[2]:m[3]]
				if sameViewName(name, targetViewName, cat) {
					spans = append(spans, [2]int{m[2], m[3]})
				}
			}
			return spans
		}
		if targetVarKey != "" {
			if !isSharedVar && targetViewName != "" && cat != nil {
				vi := cat.ViewForPath(fpath)
				if vi != nil && !sameViewName(vi.Name, targetViewName, cat) {
					// Check if fpath is an included partial of targetViewName
					isRelated := false
					if parent := cat.LookupView(targetViewName); parent != nil {
						for _, inc := range parent.Includes {
							if sameViewName(inc.Target, vi.Name, cat) {
								isRelated = true
								break
							}
						}
					}
					if !isRelated {
						return nil
					}
				}
			}
			spans = append(spans, findTemplateVarSpansInContent(content, targetVarKey)...)
		}
		return spans
	}

	if !strings.HasSuffix(fpath, ".go") {
		return nil
	}
	calls, fset := findViewCalls(fpath, content)
	for _, vc := range calls {
		if targetViewName != "" && targetVarKey == "" {
			for _, ns := range vc.Names {
				if sameViewName(ns.Name, targetViewName, cat) {
					// Return inner string without quotes
					spans = append(spans, [2]int{ns.Start + 1, ns.End - 1})
				}
			}
		}
		if targetVarKey != "" {
			if isSharedVar && vc.ShareKey != nil && vc.ShareKey.Name == targetVarKey {
				spans = append(spans, [2]int{vc.ShareKey.Start + 1, vc.ShareKey.End - 1})
			}
			if vc.DataMap != nil && fset != nil {
				matchesView := isSharedVar || targetViewName == ""
				if !matchesView {
					for _, ns := range vc.Names {
						if sameViewName(ns.Name, targetViewName, cat) {
							matchesView = true
							break
						}
					}
				}
				if matchesView {
					for _, elt := range vc.DataMap.Elts {
						kv, ok := elt.(*ast.KeyValueExpr)
						if !ok {
							continue
						}
						if sp, ok := extractStringSpan(fset, kv.Key); ok && sp.Name == targetVarKey {
							spans = append(spans, [2]int{sp.Start + 1, sp.End - 1})
						}
					}
				}
			}
		}
	}
	return spans
}

func sameViewName(a, b string, cat *indexer.Catalog) bool {
	if a == b {
		return true
	}
	noExtA := strings.TrimSuffix(a, filepath.Ext(a))
	noExtB := strings.TrimSuffix(b, filepath.Ext(b))
	if noExtA == noExtB {
		return true
	}
	if cat != nil {
		va := cat.LookupView(a)
		vb := cat.LookupView(b)
		if va != nil && vb != nil && va.Name == vb.Name {
			return true
		}
	}
	return false
}

func findTemplateVarOccurrencesInFile(filePath, key string) []indexer.Location {
	b, err := os.ReadFile(filePath)
	if err != nil {
		return nil
	}
	src := string(b)
	var locs []indexer.Location
	for _, sp := range findTemplateVarSpansInContent(src, key) {
		line, col := offsetToLineCol(src, sp[0])
		locs = append(locs, indexer.Location{File: filePath, Line: line, Col: col})
	}
	return locs
}

func offsetToLineCol(src string, off int) (int, int) {
	if off < 0 {
		return 0, 0
	}
	if off > len(src) {
		off = len(src)
	}
	line := strings.Count(src[:off], "\n")
	lastNL := strings.LastIndexByte(src[:off], '\n')
	col := off - (lastNL + 1)
	return line, col
}

func findTemplateVarSpansInContent(src, key string) [][2]int {
	var spans [][2]int
	actionRe := regexp.MustCompile(`(?s)\{\{-?(.*?)-?\}\}`)
	varRe := regexp.MustCompile(`(?:^|[\s(|,=:$])(?:\$)?\.(` + regexp.QuoteMeta(key) + `)\b`)
	for _, m := range actionRe.FindAllStringSubmatchIndex(src, -1) {
		bStart, bEnd := m[2], m[3]
		body := src[bStart:bEnd]
		for _, vm := range varRe.FindAllStringSubmatchIndex(body, -1) {
			spans = append(spans, [2]int{bStart + vm[2], bStart + vm[3]})
		}
	}
	return spans
}
