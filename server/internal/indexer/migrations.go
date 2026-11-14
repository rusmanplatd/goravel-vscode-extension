package indexer

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

var (
	createRe       = regexp.MustCompile("(?is)CREATE\\s+TABLE\\s+(?:IF\\s+NOT\\s+EXISTS\\s+)?(?:`\\s*\\+\\s*(\\w+)\\s*\\+\\s*`|([\\w.\"]+))\\s*\\(")
	alterAddRe     = regexp.MustCompile("(?im)ALTER\\s+TABLE\\s+(?:IF\\s+EXISTS\\s+)?(?:`\\s*\\+\\s*(\\w+)\\s*\\+\\s*`|([\\w.\"]+))\\s+ADD\\s+(?:COLUMN\\s+)?(?:IF\\s+NOT\\s+EXISTS\\s+)?\"?(\\w+)\"?\\s+([A-Za-z_]+(?:\\s*\\([^)]*\\))?(?:\\s+(?:WITH|WITHOUT)\\s+TIME\\s+ZONE)?[^;,\\n]*)")
	alterDropRe    = regexp.MustCompile("(?im)ALTER\\s+TABLE\\s+(?:IF\\s+EXISTS\\s+)?(?:`\\s*\\+\\s*(\\w+)\\s*\\+\\s*`|([\\w.\"]+))\\s+DROP\\s+(?:COLUMN\\s+)?(?:IF\\s+EXISTS\\s+)?\"?(\\w+)\"?")
	alterRenameRe  = regexp.MustCompile("(?im)ALTER\\s+TABLE\\s+(?:IF\\s+EXISTS\\s+)?(?:`\\s*\\+\\s*(\\w+)\\s*\\+\\s*`|([\\w.\"]+))\\s+RENAME\\s+(?:COLUMN\\s+)?\"?(\\w+)\"?\\s+TO\\s+\"?(\\w+)\"?")
	varRe          = regexp.MustCompile(`(\w+)\s*:=\s*\(?models\.(\w+)\{\}\)?\.TableName`)
	colRe          = regexp.MustCompile(`^\s*"?(\w+)"?\s+([A-Za-z_]+(?:\s*\([^)]*\))?(?:\s+(?:WITH|WITHOUT)\s+TIME\s+ZONE)?)`)
	fkRe           = regexp.MustCompile("(?i)FOREIGN\\s+KEY\\s*\\(\\s*\"?(\\w+)\"?\\s*\\)\\s*REFERENCES\\s+(?:`\\s*\\+\\s*(\\w+)\\s*\\+\\s*`|([\\w.\"]+))\\s*\\(\\s*\"?(\\w+)\"?\\s*\\)")
	inlineRefRe    = regexp.MustCompile("(?i)REFERENCES\\s+(?:`\\s*\\+\\s*(\\w+)\\s*\\+\\s*`|([\\w.\"]+))\\s*\\(\\s*\"?(\\w+)\"?\\s*\\)")
	schemaCreateRe = regexp.MustCompile(`(?s)Schema\(\)\.(?:Connection\([^)]*\)\.)?(Create|Table)\(\s*(?:"([^"]+)"|(\w+))\s*,\s*func\s*\(\s*(\w+)\s+[^)]+\)\s*\{`)
	bpColRe        = regexp.MustCompile(`\b(?:table|blueprint)\.(ID|BigIncrements|Increments|Uuid|Ulid|String|Char|Text|MediumText|LongText|Integer|TinyInteger|SmallInteger|MediumInteger|BigInteger|UnsignedInteger|UnsignedBigInteger|Boolean|Decimal|Float|Double|Date|DateTime|DateTimeTz|Time|TimeTz|Timestamp|TimestampTz|Timestamps|TimestampsTz|SoftDeletes|SoftDeletesTz|Json|Jsonb|Binary|Enum|Morphs|NullableMorphs|UuidMorphs|ForeignId|ForeignUuid|ForeignUlid)\s*\(\s*(?:"([^"]+)")?`)
	bpDropColRe    = regexp.MustCompile(`\b(?:table|blueprint)\.DropColumn\(\s*([^)]+)\)`)
	bpRenameColRe  = regexp.MustCompile(`\b(?:table|blueprint)\.RenameColumn\(\s*"([^"]+)"\s*,\s*"([^"]+)"\s*\)`)
	bpFkRe         = regexp.MustCompile(`\b(?:table|blueprint)\.Foreign\(\s*"([^"]+)"\s*\)\.References\(\s*"([^"]+)"\s*\)\.On\(\s*"([^"]+)"\s*\)`)
	bpIdxRe        = regexp.MustCompile(`\b(?:table|blueprint)\.(Index|Unique|Primary)\(\s*([^)]+)\)`)
	createIdxRe    = regexp.MustCompile("(?is)CREATE\\s+(UNIQUE\\s+)?INDEX\\s+(?:IF\\s+NOT\\s+EXISTS\\s+)?[\\w.\"]+\\s+ON\\s+(?:`\\s*\\+\\s*(\\w+)\\s*\\+\\s*`|([\\w.\"]+))\\s*\\(([^)]+)\\)")
	migSigRe       = regexp.MustCompile(`Signature\(\)\s*string\s*\{\s*return\s+"([^"]+)"`)
	quotedStrRe    = regexp.MustCompile(`"([^"]+)"`)
	skipWord       = map[string]bool{"constraint": true, "primary": true, "unique": true, "foreign": true, "check": true, "index": true, "key": true, "like": true, "exclude": true}
)

func indexMigrations(c *Catalog, root string) {
	dbDir := filepath.Join(root, "database")
	var migPaths []string
	_ = filepath.WalkDir(dbDir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return nil
		}
		rel, _ := filepath.Rel(dbDir, p)
		relSlash := filepath.ToSlash(rel)
		if !strings.HasPrefix(relSlash, "migrations") {
			return nil
		}
		migPaths = append(migPaths, p)
		return nil
	})
	sort.Strings(migPaths)
	for _, p := range migPaths {
		rel, _ := filepath.Rel(dbDir, p)
		relSlash := filepath.ToSlash(rel)
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		src := string(data)
		folder := strings.Split(relSlash, "/")[0]
		tname := parseMigration(c, p, src)
		name := strings.TrimSuffix(filepath.Base(p), ".go")
		sig := name
		if sm := migSigRe.FindStringSubmatch(src); len(sm) >= 2 && sm[1] != "" {
			sig = sm[1]
		}
		c.Migrations = append(c.Migrations, &MigrationItem{
			Name:      name,
			Signature: sig,
			Folder:    folder,
			Table:     tname,
			Loc:       Location{File: p, Line: 0, Col: 0},
		})
	}
}

func parseMigration(c *Catalog, path, src string) string {
	vars := map[string]string{} // go var -> table name
	for _, m := range varRe.FindAllStringSubmatch(src, -1) {
		if t := c.Models[m[2]]; t != nil {
			vars[m[1]] = t.Name
		} else {
			vars[m[1]] = Snake(m[2]) + "s"
		}
	}
	firstTable := ""

	// 1. Raw SQL CREATE TABLE
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
		if firstTable == "" {
			firstTable = t.Name
		}
		t.MigLoc = offsetLoc(path, src, m[0])
		bodyStart := open + 1
		off := bodyStart
		for _, line := range strings.Split(src[bodyStart:end], "\n") {
			lineOff := off
			off += len(line) + 1
			if fkm := fkRe.FindStringSubmatch(line); fkm != nil {
				colName := fkm[1]
				targetTbl := fkm[3]
				if fkm[2] != "" {
					targetTbl = vars[fkm[2]]
				}
				targetTbl = StripSchema(targetTbl)
				refCol := fkm[4]
				if targetTbl != "" {
					if col := t.Column(colName); col != nil {
						col.ForeignKey = targetTbl + "." + refCol
					}
					t.addRelation(&Relation{
						TargetTable: targetTbl,
						ForeignKey:  colName,
						References:  refCol,
						Loc:         offsetLoc(path, src, lineOff),
					})
				}
				continue
			}
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
			fkTarget := ""
			if irm := inlineRefRe.FindStringSubmatch(line); irm != nil {
				targetTbl := irm[2]
				if irm[1] != "" {
					targetTbl = vars[irm[1]]
				}
				targetTbl = StripSchema(targetTbl)
				if targetTbl != "" {
					fkTarget = targetTbl + "." + irm[3]
					t.addRelation(&Relation{
						TargetTable: targetTbl,
						ForeignKey:  name,
						References:  irm[3],
						Loc:         offsetLoc(path, src, lineOff+mm[2]),
					})
				}
			}
			t.add(&Column{
				Name:       name,
				GoName:     "",
				SQLType:    strings.ToUpper(typ),
				Nullable:   !strings.Contains(up, "NOT NULL") && !strings.Contains(up, "PRIMARY KEY"),
				ForeignKey: fkTarget,
				MigLoc:     offsetLoc(path, src, lineOff+mm[2]),
			})
		}
	}

	// 1b. Raw SQL ALTER TABLE ADD / DROP / RENAME COLUMN
	upOnly := src
	if downIdx := strings.Index(src, "Down() error"); downIdx > 0 {
		upOnly = src[:downIdx]
	}
	for _, m := range alterAddRe.FindAllStringSubmatchIndex(upOnly, -1) {
		var tname string
		if m[2] >= 0 {
			tname = vars[upOnly[m[2]:m[3]]]
		} else if m[4] >= 0 {
			tname = upOnly[m[4]:m[5]]
		}
		if tname == "" {
			continue
		}
		colName := upOnly[m[6]:m[7]]
		rawDef := strings.TrimSpace(upOnly[m[8]:m[9]])
		typ := strings.Fields(rawDef)[0]
		t := c.ensure(tname)
		if firstTable == "" {
			firstTable = t.Name
		}
		upDef := strings.ToUpper(rawDef)
		t.add(&Column{
			Name:     colName,
			SQLType:  strings.ToUpper(typ),
			Nullable: !strings.Contains(upDef, "NOT NULL"),
			MigLoc:   offsetLoc(path, src, m[6]),
		})
	}
	for _, m := range alterDropRe.FindAllStringSubmatch(upOnly, -1) {
		tname := m[2]
		if m[1] != "" {
			tname = vars[m[1]]
		}
		if tname != "" {
			c.ensure(tname).removeColumn(m[3])
		}
	}
	for _, m := range alterRenameRe.FindAllStringSubmatch(upOnly, -1) {
		tname := m[2]
		if m[1] != "" {
			tname = vars[m[1]]
		}
		if tname != "" {
			c.ensure(tname).renameColumn(m[3], m[4])
		}
	}

	// 2. facades.Schema().Create(...) and facades.Schema().Table(...) Blueprints
	for _, m := range schemaCreateRe.FindAllStringSubmatchIndex(upOnly, -1) {
		op := upOnly[m[2]:m[3]]
		var tname string
		if m[4] >= 0 {
			tname = upOnly[m[4]:m[5]]
		} else if m[6] >= 0 {
			v := upOnly[m[6]:m[7]]
			tname = vars[v]
		}
		if tname == "" {
			continue
		}
		openBrace := m[1] - 1
		endBrace := matchBrace(upOnly, openBrace)
		if endBrace < 0 {
			continue
		}
		t := c.ensure(tname)
		if firstTable == "" {
			firstTable = t.Name
		}
		if op == "Create" || !t.MigLoc.Valid() {
			t.MigLoc = offsetLoc(path, src, m[0])
		}
		bodyStart := openBrace + 1
		off := bodyStart
		for _, line := range strings.Split(upOnly[bodyStart:endBrace], "\n") {
			lineOff := off
			off += len(line) + 1
			if dm := bpDropColRe.FindStringSubmatch(line); dm != nil {
				for _, q := range quotedStrRe.FindAllStringSubmatch(dm[1], -1) {
					t.removeColumn(q[1])
				}
				continue
			}
			if rm := bpRenameColRe.FindStringSubmatch(line); rm != nil {
				t.renameColumn(rm[1], rm[2])
				continue
			}
			if fkm := bpFkRe.FindStringSubmatch(line); fkm != nil {
				targetTbl := StripSchema(fkm[3])
				if col := t.Column(fkm[1]); col != nil {
					col.ForeignKey = targetTbl + "." + fkm[2]
				}
				t.addRelation(&Relation{
					TargetTable: targetTbl,
					ForeignKey:  fkm[1],
					References:  fkm[2],
					Loc:         offsetLoc(path, src, lineOff),
				})
				continue
			}
			if im := bpIdxRe.FindStringSubmatch(line); im != nil {
				var cols []string
				for _, q := range quotedStrRe.FindAllStringSubmatch(im[2], -1) {
					cols = append(cols, q[1])
				}
				if len(cols) > 0 {
					t.Indexes = append(t.Indexes, IndexItem{
						Columns: cols,
						Unique:  im[1] == "Unique" || im[1] == "Primary",
						Loc:     offsetLoc(path, src, lineOff),
					})
				}
				continue
			}
			cm := bpColRe.FindStringSubmatchIndex(line)
			if cm == nil {
				continue
			}
			method := line[cm[2]:cm[3]]
			colName := ""
			if cm[4] >= 0 {
				colName = line[cm[4]:cm[5]]
			}
			nullable := strings.Contains(line, ".Nullable()")
			loc := offsetLoc(path, src, lineOff+cm[0])
			switch method {
			case "ID", "BigIncrements", "Increments":
				if colName == "" {
					colName = "id"
				}
				t.add(&Column{Name: colName, SQLType: "BIGINT", Nullable: false, MigLoc: loc})
			case "Timestamps", "TimestampsTz":
				t.add(&Column{Name: "created_at", SQLType: "TIMESTAMP", Nullable: true, MigLoc: loc})
				t.add(&Column{Name: "updated_at", SQLType: "TIMESTAMP", Nullable: true, MigLoc: loc})
			case "SoftDeletes", "SoftDeletesTz":
				if colName == "" {
					colName = "deleted_at"
				}
				t.add(&Column{Name: colName, SQLType: "TIMESTAMP", Nullable: true, MigLoc: loc})
			case "Morphs", "NullableMorphs", "UuidMorphs":
				if colName != "" {
					n := strings.HasPrefix(method, "Nullable")
					t.add(&Column{Name: colName + "_id", SQLType: "VARCHAR(255)", Nullable: n, MigLoc: loc})
					t.add(&Column{Name: colName + "_type", SQLType: "VARCHAR(255)", Nullable: n, MigLoc: loc})
				}
			default:
				if colName != "" {
					t.add(&Column{
						Name:     colName,
						SQLType:  blueprintSQLType(method),
						Nullable: nullable,
						MigLoc:   loc,
					})
				}
			}
		}
	}

	// 3. Raw SQL CREATE [UNIQUE] INDEX
	for _, m := range createIdxRe.FindAllStringSubmatchIndex(src, -1) {
		unique := m[2] >= 0
		var tname string
		if m[4] >= 0 {
			tname = vars[src[m[4]:m[5]]]
		} else if m[6] >= 0 {
			tname = src[m[6]:m[7]]
		}
		if tname == "" || m[8] < 0 {
			continue
		}
		var cols []string
		for _, part := range strings.Split(src[m[8]:m[9]], ",") {
			col := strings.TrimSpace(part)
			if sp := strings.Fields(col); len(sp) > 0 {
				col = strings.Trim(sp[0], "\"`")
			}
			if col != "" {
				cols = append(cols, col)
			}
		}
		if len(cols) > 0 {
			t := c.ensure(tname)
			t.Indexes = append(t.Indexes, IndexItem{
				Columns: cols,
				Unique:  unique,
				Loc:     offsetLoc(path, src, m[0]),
			})
		}
	}

	return firstTable
}

func blueprintSQLType(method string) string {
	switch method {
	case "Uuid", "ForeignUuid":
		return "UUID"
	case "Ulid", "ForeignUlid":
		return "CHAR(26)"
	case "String":
		return "VARCHAR(255)"
	case "Char":
		return "CHAR"
	case "Text", "MediumText", "LongText":
		return "TEXT"
	case "Integer", "MediumInteger", "UnsignedInteger":
		return "INTEGER"
	case "TinyInteger", "SmallInteger":
		return "SMALLINT"
	case "BigInteger", "UnsignedBigInteger", "ForeignId":
		return "BIGINT"
	case "Boolean":
		return "BOOLEAN"
	case "Decimal":
		return "DECIMAL"
	case "Float", "Double":
		return "DOUBLE PRECISION"
	case "Date":
		return "DATE"
	case "DateTime", "DateTimeTz", "Timestamp", "TimestampTz":
		return "TIMESTAMP"
	case "Time", "TimeTz":
		return "TIME"
	case "Json":
		return "JSON"
	case "Jsonb":
		return "JSONB"
	case "Binary":
		return "BYTEA"
	default:
		return "VARCHAR(255)"
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

func matchBrace(s string, open int) int {
	depth := 0
	for i := open; i < len(s); i++ {
		switch s[i] {
		case '{':
			depth++
		case '}':
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
