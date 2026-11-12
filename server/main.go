package main

import (
	"fmt"
	"os"

	"github.com/rusmanplatd/goravel-lsp/internal/indexer"
	"github.com/rusmanplatd/goravel-lsp/internal/lsp"
)

func main() {
	if len(os.Args) > 2 && os.Args[1] == "--dump" {
		cat := indexer.Build(os.Args[2])
		for _, n := range cat.TableNames() {
			t := cat.Table(n)
			fmt.Printf("%s (%s) cols=%d rels=%d\n", t.Name, t.Model, len(t.Columns), len(t.Relations))
		}
		fmt.Printf("\nSummary: %d tables, %d routes, %d configs, %d rules, %d migrations\n",
			len(cat.Tables), len(cat.Routes), len(cat.Configs), len(cat.Rules), len(cat.Migrations))
		return
	}
	if len(os.Args) > 2 && os.Args[1] == "--check" {
		os.Exit(lsp.Check(os.Args[2]))
	}
	if err := lsp.New(os.Stdin, os.Stdout).Run(); err != nil {
		fmt.Fprintln(os.Stderr, "goravel-lsp:", err)
		os.Exit(1)
	}
}
