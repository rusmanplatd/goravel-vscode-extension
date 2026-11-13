package framework

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
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

