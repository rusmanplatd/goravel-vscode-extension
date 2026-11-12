package lsp

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/rusmanplatd/goravel-lsp/internal/indexer"
	"github.com/rusmanplatd/goravel-lsp/internal/rawsql"
)

// Check prints SQL diagnostics for every Go file in a project (CLI: --check <root>).
// It returns a non-zero exit code when issues are found.
func Check(root string) int {
	cat := indexer.Build(root)
	issues, docs := 0, 0
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() && (d.Name() == "vendor" || d.Name() == "node_modules" || d.Name() == ".git") {
			return filepath.SkipDir
		}
		if d.IsDir() || !strings.HasSuffix(p, ".go") {
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return nil
		}
		fi := rawsql.Extract(p, b, cat)
		docs += len(fi.Docs)
		for _, dg := range rawsql.Analyze(fi, cat) {
			pos := offsetToPos(string(b), dg.Start)
			fmt.Printf("%s:%d:%d: %s\n", p, pos.Line+1, pos.Character+1, dg.Message)
			issues++
		}
		return nil
	})
	fmt.Printf("%d sql docs, %d issues\n", docs, issues)
	if issues > 0 {
		return 1
	}
	return 0
}
