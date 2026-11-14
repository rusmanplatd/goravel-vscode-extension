package framework

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/rusmanplatd/goravel-lsp/internal/indexer"
)

type viewCall struct {
	Name      string
	NameStart int
	NameEnd   int
	DataMap   *ast.CompositeLit
}

func findViewCalls(path, src string) ([]viewCall, *token.FileSet) {
	if !strings.Contains(src, "View(") && !strings.Contains(src, ".Make(") {
		return nil, nil
	}
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, src, 0)
	if err != nil || f == nil {
		return nil, nil
	}
	var out []viewCall
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || len(call.Args) == 0 {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "Make" {
			return true
		}
		// Check receiver looks like ...View()
		if !strings.Contains(src[fset.Position(sel.X.Pos()).Offset:fset.Position(sel.X.End()).Offset], "View") {
			return true
		}
		bl, ok := call.Args[0].(*ast.BasicLit)
		if !ok || bl.Kind != token.STRING {
			return true
		}
		name, err := strconv.Unquote(bl.Value)
		if err != nil {
			return true
		}
		vc := viewCall{
			Name:      name,
			NameStart: fset.Position(bl.Pos()).Offset,
			NameEnd:   fset.Position(bl.End()).Offset,
		}
		if len(call.Args) > 1 {
			if cl, ok := call.Args[1].(*ast.CompositeLit); ok {
				vc.DataMap = cl
			}
		}
		out = append(out, vc)
		return true
	})
	return out, fset
}

// ViewCompletions suggests template paths inside View().Make("...").
func ViewCompletions(path, src string, cat *indexer.Catalog, goOff int) []CompItem {
	calls, _ := findViewCalls(path, src)
	for _, vc := range calls {
		if goOff >= vc.NameStart && goOff <= vc.NameEnd {
			var names []string
			for k := range cat.Views {
				names = append(names, k)
			}
			sort.Strings(names)
			var out []CompItem
			for _, n := range names {
				out = append(out, CompItem{
					Label:  n,
					Detail: "resources/views/" + n,
					Kind:   17, // File
				})
			}
			return out
		}
	}
	return nil
}

// ViewHover shows info about the referenced view template.
func ViewHover(path, src string, cat *indexer.Catalog, goOff int) *HoverResult {
	calls, _ := findViewCalls(path, src)
	for _, vc := range calls {
		if goOff >= vc.NameStart && goOff <= vc.NameEnd {
			loc, ok := cat.Views[vc.Name]
			if !ok {
				return &HoverResult{Markdown: fmt.Sprintf("**View `%s`** *(not found in `resources/views/`)*", vc.Name)}
			}
			return &HoverResult{Markdown: fmt.Sprintf("**View `%s`**\n\nFile: `resources/views/%s` (`%s`)", vc.Name, vc.Name, loc.File)}
		}
	}
	return nil
}

// ViewDefinition jumps from View().Make("...") to resources/views/<name>.
func ViewDefinition(path, src string, cat *indexer.Catalog, goOff int) *DefResult {
	calls, _ := findViewCalls(path, src)
	for _, vc := range calls {
		if goOff >= vc.NameStart && goOff <= vc.NameEnd {
			if loc, ok := cat.Views[vc.Name]; ok {
				return &DefResult{Loc: loc}
			}
		}
	}
	return nil
}

// ViewDiagnostics warns on missing templates and unused data keys in View().Make(...).
func ViewDiagnostics(path, src string, cat *indexer.Catalog) []Diag {
	if len(cat.Views) == 0 {
		return nil
	}
	calls, fset := findViewCalls(path, src)
	var out []Diag
	for _, vc := range calls {
		loc, ok := cat.Views[vc.Name]
		if !ok {
			out = append(out, Diag{
				Start:    vc.NameStart,
				End:      vc.NameEnd,
				Severity: 2,
				Message:  fmt.Sprintf("View template '%s' not found in resources/views/", vc.Name),
			})
			continue
		}
		if vc.DataMap != nil && fset != nil {
			if b, err := os.ReadFile(loc.File); err == nil {
				tplSrc := string(b)
				for _, elt := range vc.DataMap.Elts {
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
					if !strings.Contains(tplSrc, "."+k) {
						out = append(out, Diag{
							Start:    fset.Position(bl.Pos()).Offset,
							End:      fset.Position(bl.End()).Offset,
							Severity: 4, // Hint
							Message:  fmt.Sprintf("View data key '%s' is not referenced in %s", k, vc.Name),
						})
					}
				}
			}
		}
	}
	return out
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
	if idx := strings.Index(slash, "/resources/views/"); idx >= 0 {
		rel := slash[idx+len("/resources/views/"):]
		keys = append(keys, rel, strings.TrimSuffix(rel, filepath.Ext(rel)))
	}
	base := filepath.Base(slash)
	keys = append(keys, base, strings.TrimSuffix(base, filepath.Ext(base)))
	seen := map[string]bool{}
	var out []indexer.ViewVar
	for _, k := range keys {
		for _, vv := range cat.ViewVars[k] {
			id := vv.Key + "@" + vv.Controller
			if !seen[id] {
				seen[id] = true
				out = append(out, vv)
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

// TemplateCompletions suggests context variables passed via View().Make(...) inside {{ ... }} blocks of resources/views/ templates.
func TemplateCompletions(path, src string, cat *indexer.Catalog, off int) []CompItem {
	if !IsTemplateFile(path) || !isInsideTemplateAction(src, off) {
		return nil
	}
	vars := resolveViewVarsForTemplate(path, cat)
	if len(vars) == 0 {
		return nil
	}
	hasDotPrefix := off > 0 && src[off-1] == '.'
	seen := map[string]bool{}
	var out []CompItem
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
		if vv.Controller != "" {
			detail = fmt.Sprintf("%s (from %s)", vv.ValueExpr, vv.Controller)
		}
		out = append(out, CompItem{
			Label:  lbl,
			Detail: detail,
			Kind:   6, // Variable
		})
	}
	return out
}

// TemplateHover displays controller origin and value expression when hovering over a .varName inside a template.
func TemplateHover(path, src string, cat *indexer.Catalog, off int) *HoverResult {
	if !IsTemplateFile(path) || !isInsideTemplateAction(src, off) {
		return nil
	}
	word := templateVarAtOffset(src, off)
	if word == "" {
		return nil
	}
	vars := resolveViewVarsForTemplate(path, cat)
	var lines []string
	for _, vv := range vars {
		if vv.Key == word {
			srcInfo := vv.ValueExpr
			if vv.Controller != "" {
				srcInfo = fmt.Sprintf("`%s` in `%s` (`%s:%d`)", vv.ValueExpr, vv.Controller, filepath.Base(vv.Loc.File), vv.Loc.Line+1)
			}
			lines = append(lines, "- Passed as "+srcInfo)
		}
	}
	if len(lines) == 0 {
		return nil
	}
	return &HoverResult{
		Markdown: fmt.Sprintf("**Template Variable `.%s`**\n\n%s", word, strings.Join(lines, "\n")),
	}
}

// TemplateDefinition jumps from .varName in a template file to the View().Make(...) map key in the Go controller.
func TemplateDefinition(path, src string, cat *indexer.Catalog, off int) []indexer.Location {
	if !IsTemplateFile(path) || !isInsideTemplateAction(src, off) {
		return nil
	}
	word := templateVarAtOffset(src, off)
	if word == "" {
		return nil
	}
	vars := resolveViewVarsForTemplate(path, cat)
	var locs []indexer.Location
	for _, vv := range vars {
		if vv.Key == word && vv.Loc.Valid() {
			locs = append(locs, vv.Loc)
		}
	}
	return locs
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
	if l > 0 && src[l-1] == '.' {
		return src[l:r]
	}
	return src[l:r]
}


