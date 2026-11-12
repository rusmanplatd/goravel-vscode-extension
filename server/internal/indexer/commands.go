package indexer

import (
	"go/ast"
	"path/filepath"
	"sort"
	"strings"
)

func indexCommands(c *Catalog, root string) {
	files := walkGoFiles(filepath.Join(root, "app", "console", "commands"))
	for _, pf := range files {
		sigs := map[string]struct {
			sig string
			loc Location
		}{}
		descs := map[string]string{}
		for _, d := range pf.file.Decls {
			fd, ok := d.(*ast.FuncDecl)
			if !ok || fd.Recv == nil || fd.Body == nil {
				continue
			}
			recv := recvName(fd.Recv)
			if recv == "" {
				continue
			}
			switch fd.Name.Name {
			case "Signature":
				if s := returnString(fd.Body); s != "" {
					sigs[recv] = struct {
						sig string
						loc Location
					}{s, loc(pf, fd.Name.Pos())}
				}
			case "Description":
				if s := returnString(fd.Body); s != "" {
					descs[recv] = s
				}
			}
		}
		for recv, s := range sigs {
			sig := strings.TrimSpace(s.sig)
			if sig == "" {
				continue
			}
			c.Commands = append(c.Commands, &CommandItem{
				Signature:   sig,
				Description: descs[recv],
				StructName:  recv,
				Loc:         s.loc,
			})
		}
	}
	sort.Slice(c.Commands, func(i, j int) bool {
		return c.Commands[i].Signature < c.Commands[j].Signature
	})
}

