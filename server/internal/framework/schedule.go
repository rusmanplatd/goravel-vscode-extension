package framework

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"strconv"
	"strings"

	"github.com/rusmanplatd/goravel-lsp/internal/indexer"
)

// ScheduleInlayHints returns human-readable cron descriptions after .Cron("...") literals.
func ScheduleInlayHints(path, src string) []InlayHint {
	if !strings.Contains(src, ".Cron(") {
		return nil
	}
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, src, 0)
	if err != nil || f == nil {
		return nil
	}
	var out []InlayHint
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || len(call.Args) == 0 {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "Cron" {
			return true
		}
		bl, ok := call.Args[0].(*ast.BasicLit)
		if !ok || bl.Kind != token.STRING {
			return true
		}
		expr, err := strconv.Unquote(bl.Value)
		if err != nil || expr == "" {
			return true
		}
		out = append(out, InlayHint{
			Offset: fset.Position(bl.End()).Offset,
			Label:  " → " + indexer.HumanCron(expr),
		})
		return true
	})
	return out
}

// ScheduleHover returns a human-readable cron breakdown when hovering over .Cron("...").
func ScheduleHover(path, src string, goOff int) *HoverResult {
	if !strings.Contains(src, ".Cron(") {
		return nil
	}
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, src, 0)
	if err != nil || f == nil {
		return nil
	}
	var res *HoverResult
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || len(call.Args) == 0 {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "Cron" {
			return true
		}
		bl, ok := call.Args[0].(*ast.BasicLit)
		if !ok || bl.Kind != token.STRING {
			return true
		}
		s := fset.Position(bl.Pos()).Offset
		e := fset.Position(bl.End()).Offset
		if goOff < s || goOff > e {
			return true
		}
		expr, err := strconv.Unquote(bl.Value)
		if err != nil {
			return true
		}
		res = &HoverResult{
			Markdown: fmt.Sprintf("**Goravel Schedule Cron**\n\n- Expression: `%s`\n- Schedule: **%s**", expr, indexer.HumanCron(expr)),
		}
		return false
	})
	return res
}

