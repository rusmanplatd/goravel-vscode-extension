package indexer

import (
	"fmt"
	"go/ast"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

func indexViews(c *Catalog, root string) {
	viewDir := filepath.Join(root, "resources", "views")
	_ = filepath.WalkDir(viewDir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		ext := strings.ToLower(filepath.Ext(d.Name()))
		if ext != ".tmpl" && ext != ".html" && ext != ".gohtml" {
			return nil
		}
		rel, err := filepath.Rel(viewDir, p)
		if err != nil {
			return nil
		}
		c.Views[filepath.ToSlash(rel)] = Location{File: p, Line: 0, Col: 0}
		return nil
	})
}

func indexMiddleware(c *Catalog, root string) {
	files := walkGoFiles(filepath.Join(root, "app", "http", "middleware"))
	for _, pf := range files {
		for _, d := range pf.file.Decls {
			switch decl := d.(type) {
			case *ast.FuncDecl:
				if decl.Recv == nil && decl.Name != nil && decl.Name.IsExported() {
					c.Middleware[decl.Name.Name] = loc(pf, decl.Name.Pos())
				}
			case *ast.GenDecl:
				for _, sp := range decl.Specs {
					if ts, ok := sp.(*ast.TypeSpec); ok && ts.Name != nil && ts.Name.IsExported() {
						c.Middleware[ts.Name.Name] = loc(pf, ts.Name.Pos())
					}
				}
			}
		}
	}
}

func indexSeeders(c *Catalog, root string) {
	files := walkGoFiles(filepath.Join(root, "database", "seeders"))
	for _, pf := range files {
		sigs := map[string]struct {
			sig string
			loc Location
		}{}
		for _, d := range pf.file.Decls {
			fd, ok := d.(*ast.FuncDecl)
			if !ok || fd.Recv == nil || fd.Body == nil {
				continue
			}
			recv := recvName(fd.Recv)
			if recv == "" {
				continue
			}
			if fd.Name.Name == "Signature" {
				if s := returnString(fd.Body); s != "" {
					sigs[recv] = struct {
						sig string
						loc Location
					}{s, loc(pf, fd.Name.Pos())}
				}
			}
		}
		for recv, s := range sigs {
			c.Seeders = append(c.Seeders, &SeederItem{
				Name:      recv,
				Signature: s.sig,
				Loc:       s.loc,
			})
		}
	}
	sort.Slice(c.Seeders, func(i, j int) bool {
		return c.Seeders[i].Signature < c.Seeders[j].Signature
	})
}

func indexSchedules(c *Catalog, root string) {
	var files []*parsedFile
	files = append(files, walkGoFiles(filepath.Join(root, "app", "console"))...)
	files = append(files, walkGoFiles(filepath.Join(root, "bootstrap"))...)
	for _, pf := range files {
		ast.Inspect(pf.file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			switch sel.Sel.Name {
			case "Cron":
				if len(call.Args) > 0 {
					if s := stringLitValue(call.Args[0]); s != "" {
						c.Schedules = append(c.Schedules, &ScheduleItem{
							Expr:   s,
							Human:  HumanCron(s),
							Target: extractScheduleTarget(sel.X),
							Loc:    loc(pf, call.Args[0].Pos()),
						})
					}
				}
			case "EveryMinute", "EveryFiveMinutes", "EveryTenMinutes", "EveryFifteenMinutes",
				"EveryThirtyMinutes", "Hourly", "Daily", "Weekly", "Monthly":
				c.Schedules = append(c.Schedules, &ScheduleItem{
					Expr:   sel.Sel.Name,
					Human:  HumanScheduleMethod(sel.Sel.Name, ""),
					Target: extractScheduleTarget(sel.X),
					Loc:    loc(pf, sel.Sel.Pos()),
				})
			case "DailyAt", "HourlyAt":
				arg := ""
				if len(call.Args) > 0 {
					arg = stringLitValue(call.Args[0])
				}
				c.Schedules = append(c.Schedules, &ScheduleItem{
					Expr:   fmt.Sprintf("%s(%s)", sel.Sel.Name, arg),
					Human:  HumanScheduleMethod(sel.Sel.Name, arg),
					Target: extractScheduleTarget(sel.X),
					Loc:    loc(pf, sel.Sel.Pos()),
				})
			}
			return true
		})
	}
}

func extractScheduleTarget(e ast.Expr) string {
	for e != nil {
		call, ok := e.(*ast.CallExpr)
		if !ok {
			return ""
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return ""
		}
		if sel.Sel.Name == "Command" && len(call.Args) > 0 {
			if s := stringLitValue(call.Args[0]); s != "" {
				return s
			}
		}
		if sel.Sel.Name == "Call" {
			return "closure"
		}
		e = sel.X
	}
	return ""
}

// HumanScheduleMethod formats Goravel Schedule helper methods as readable text.
func HumanScheduleMethod(method, arg string) string {
	switch method {
	case "EveryMinute":
		return "Every minute"
	case "EveryFiveMinutes":
		return "Every 5 minutes"
	case "EveryTenMinutes":
		return "Every 10 minutes"
	case "EveryFifteenMinutes":
		return "Every 15 minutes"
	case "EveryThirtyMinutes":
		return "Every 30 minutes"
	case "Hourly":
		return "Every hour (at :00)"
	case "HourlyAt":
		if arg != "" {
			return "Every hour at :" + arg
		}
		return "Every hour"
	case "Daily":
		return "Every day at 00:00"
	case "DailyAt":
		if arg != "" {
			return "Every day at " + arg
		}
		return "Every day"
	case "Weekly":
		return "Every week (Sunday 00:00)"
	case "Monthly":
		return "Every month (1st 00:00)"
	}
	return method
}

// HumanCron converts a standard 5-field cron expression into a concise human description.
func HumanCron(expr string) string {
	fields := strings.Fields(strings.TrimSpace(expr))
	if len(fields) < 5 {
		return expr
	}
	min, hour, dom, mon, dow := fields[0], fields[1], fields[2], fields[3], fields[4]
	if min == "*" && hour == "*" && dom == "*" && mon == "*" && dow == "*" {
		return "Every minute"
	}
	if strings.HasPrefix(min, "*/") && hour == "*" && dom == "*" && mon == "*" && dow == "*" {
		return "Every " + strings.TrimPrefix(min, "*/") + " minutes"
	}
	if min == "0" && strings.HasPrefix(hour, "*/") && dom == "*" && mon == "*" && dow == "*" {
		return "Every " + strings.TrimPrefix(hour, "*/") + " hours"
	}
	if hour == "*" && dom == "*" && mon == "*" && dow == "*" {
		if m, err := strconv.Atoi(min); err == nil {
			return fmt.Sprintf("Every hour at :%02d", m)
		}
	}
	if dom == "*" && mon == "*" && dow == "*" {
		if h, err := strconv.Atoi(hour); err == nil {
			if m, err2 := strconv.Atoi(min); err2 == nil {
				return fmt.Sprintf("Daily at %02d:%02d", h, m)
			}
		}
	}
	return fmt.Sprintf("Cron (%s %s %s %s %s)", min, hour, dom, mon, dow)
}

func indexBootstrapRegistrations(c *Catalog, root string) {
	bootDir := filepath.Join(root, "bootstrap")
	if _, err := os.Stat(bootDir); err != nil {
		return
	}
	var bootSrc strings.Builder
	_ = filepath.WalkDir(bootDir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".go") {
			return nil
		}
		if b, err := os.ReadFile(p); err == nil {
			bootSrc.Write(b)
			bootSrc.WriteByte('\n')
		}
		return nil
	})
	allBoot := bootSrc.String()
	if allBoot == "" {
		return
	}

	addUnreg := func(kind, name string, l Location) {
		if l.File == "" || name == "" {
			return
		}
		it := &UnregisteredItem{Kind: kind, Name: name, File: l.File, Loc: l}
		c.Unregistered = append(c.Unregistered, it)
		c.UnregisteredByFile[filepath.Clean(l.File)] = it
	}

	// Check migrations: struct name &M2025...{}
	for _, m := range c.Migrations {
		if m.Loc.File == "" {
			continue
		}
		// Find struct name in migration file (typically M<timestamp><Name>)
		if b, err := os.ReadFile(m.Loc.File); err == nil {
			s := string(b)
			if !strings.Contains(allBoot, m.Name) && !containsMigrationStruct(allBoot, s) {
				addUnreg("migration", m.Name, m.Loc)
			}
		}
	}
	// Check seeders
	for _, s := range c.Seeders {
		if !strings.Contains(allBoot, s.Name) {
			addUnreg("seeder", s.Name, s.Loc)
		}
	}
	// Check custom rules
	for _, r := range c.Rules {
		if !r.Custom || !r.Loc.Valid() {
			continue
		}
		// Match struct name in the rule file
		if b, err := os.ReadFile(r.Loc.File); err == nil {
			if !containsAnyExportedType(allBoot, string(b)) {
				addUnreg("rule", r.Name, r.Loc)
			}
		}
	}
	// Check custom commands
	for _, cmd := range c.Commands {
		if !strings.Contains(allBoot, cmd.StructName) {
			addUnreg("command", cmd.Signature, cmd.Loc)
		}
	}
}

func containsMigrationStruct(bootSrc, migSrc string) bool {
	return containsAnyExportedType(bootSrc, migSrc)
}

func stringLitValue(e ast.Expr) string {
	bl, ok := e.(*ast.BasicLit)
	if !ok || bl.Kind != token.STRING {
		return ""
	}
	s, err := strconv.Unquote(bl.Value)
	if err != nil {
		return ""
	}
	return s
}

func containsAnyExportedType(bootSrc, fileSrc string) bool {
	for _, line := range strings.Split(fileSrc, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "type ") {
			fields := strings.Fields(line)
			if len(fields) >= 3 && fields[2] == "struct" {
				if strings.Contains(bootSrc, fields[1]) {
					return true
				}
			}
		}
	}
	return false
}
