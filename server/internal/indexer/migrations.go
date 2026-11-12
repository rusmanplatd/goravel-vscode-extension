package indexer

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var (
	createRe = regexp.MustCompile("(?is)CREATE\\s+TABLE\\s+(?:IF\\s+NOT\\s+EXISTS\\s+)?(?:`\\s*\\+\\s*(\\w+)\\s*\\+\\s*`|([\\w.\"]+))\\s*\\(")
	varRe    = regexp.MustCompile(`(\w+)\s*:=\s*\(?models\.(\w+)\{\}\)?\.TableName`)
	colRe    = regexp.MustCompile(`^\s*"?(\w+)"?\s+([A-Za-z_]+(?:\s*\([^)]*\))?(?:\s+(?:WITH|WITHOUT)\s+TIME\s+ZONE)?)`)
	skipWord = map[string]bool{"constraint": true, "primary": true, "unique": true, "foreign": true, "check": true, "index": true, "key": true, "like": true, "exclude": true}
)

func indexMigrations(c *Catalog, root string) {
	dbDir := filepath.Join(root, "database")
	_ = filepath.WalkDir(dbDir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".go") {
			return nil
		}
		rel, _ := filepath.Rel(dbDir, p)
		if !strings.HasPrefix(rel, "migrations") {
			return nil
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return nil
		}
		parseMigration(c, p, string(data))
		return nil
	})
}

func parseMigration(c *Catalog, path, src string) {
	vars := map[string]string{} // go var -> table name
	for _, m := range varRe.FindAllStringSubmatch(src, -1) {
		if t := c.Models[m[2]]; t != nil {
			vars[m[1]] = t.Name
		}
	}
	for _, m := range createRe.FindAllStringSubmatchIndex(src, -1) {
		var tname string
		if m[2] >= 0 {
			tname = vars[src[m[2]:m[3]]]
		} else {
			tname = src[m[4]:m[5]]
		}
		if tname == "" {
			continue
		}
		open := m[1] - 1
		end := matchParen(src, open)
		if end < 0 {
			continue
		}
		t := c.ensure(tname)
		t.MigLoc = offsetLoc(path, src, m[0])
		bodyStart := open + 1
		off := bodyStart
		for _, line := range strings.Split(src[bodyStart:end], "\n") {
			lineOff := off
			off += len(line) + 1
			mm := colRe.FindStringSubmatchIndex(line)
			if mm == nil {
				continue
			}
			name := line[mm[2]:mm[3]]
			if skipWord[strings.ToLower(name)] {
				continue
			}
			typ := strings.TrimSpace(line[mm[4]:mm[5]])
			up := strings.ToUpper(line)
			t.add(&Column{
				Name:     name,
				GoName:   "",
				SQLType:  strings.ToUpper(typ),
				Nullable: !strings.Contains(up, "NOT NULL") && !strings.Contains(up, "PRIMARY KEY"),
				MigLoc:   offsetLoc(path, src, lineOff+mm[2]),
			})
		}
	}
}

func matchParen(s string, open int) int {
	depth := 0
	for i := open; i < len(s); i++ {
		switch s[i] {
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}

func offsetLoc(path, src string, off int) Location {
	line := strings.Count(src[:off], "\n")
	col := off - (strings.LastIndex(src[:off], "\n") + 1)
	return Location{File: path, Line: line, Col: col}
}
