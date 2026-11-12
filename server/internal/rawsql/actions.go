package rawsql

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/rusmanplatd/goravel-lsp/internal/indexer"
)

var starRe = regexp.MustCompile(`(?:(\w+)\.)?\*`)

// ExpandStar rewrites `*` / `alias.*` under goOff into explicit columns.
func ExpandStar(fi *FileInfo, cat *indexer.Catalog, goOff int) (start, end int, text string, ok bool) {
	d := fi.DocAt(goOff)
	if d == nil {
		return
	}
	v, vok := d.ToVirtual(goOff)
	if !vok {
		return
	}
	sc := fi.BuildScope(d, cat)
	for _, m := range starRe.FindAllStringSubmatchIndex(d.Text, -1) {
		if v < m[0] || v > m[1] {
			continue
		}
		// must be a select-list star, not COUNT(*) or multiplication
		prev := strings.TrimSpace(d.Text[:m[0]])
		if strings.HasSuffix(prev, "(") || !(strings.HasSuffix(strings.ToUpper(prev), "SELECT") || strings.HasSuffix(prev, ",") || strings.HasSuffix(strings.ToUpper(prev), "DISTINCT")) {
			return
		}
		var t *indexer.Table
		qual := ""
		if m[2] >= 0 {
			qual = d.Text[m[2]:m[3]]
			t = sc[strings.ToLower(qual)]
		} else if am := aliasRe.FindStringSubmatch(d.Text); am != nil {
			t = cat.Table(am[1])
			if am[2] != "" && !notAlias[strings.ToLower(am[2])] {
				qual = am[2]
			}
		}
		if t == nil {
			return
		}
		g1, ok1 := d.ToGo(m[0])
		g2, ok2 := d.ToGo(m[1])
		if !ok1 || !ok2 {
			return
		}
		var cols []string
		for _, c := range t.Columns {
			if qual != "" {
				cols = append(cols, qual+"."+c.Name)
			} else {
				cols = append(cols, c.Name)
			}
		}
		return g1, g2, strings.Join(cols, ", "), true
	}
	return
}

// GenerateRowStruct builds a scan struct for the Select call containing goOff.
func GenerateRowStruct(fi *FileInfo, cat *indexer.Catalog, goOff int) (insertAt int, text, name string, ok bool) {
	for _, q := range fi.Queries {
		if q.Method != "Select" || goOff < q.CallStart || goOff > q.CallEnd || q.Star || len(q.Cols) == 0 {
			continue
		}
		sc := fi.BuildScope(q.Doc, cat)
		outer := q.NullableAliases()
		name = q.FuncName + "Row"
		var b strings.Builder
		var needSQL, needUUID, needTime bool
		fmt.Fprintf(&b, "// %s is the scan target for the query in %s.\ntype %s struct {\n", name, q.FuncName, name)
		for _, c := range q.Cols {
			goType, null := "any", false
			var col *indexer.Column
			if c.SrcCol != "" {
				if c.Qualifier != "" {
					col = sc[c.Qualifier].Column(c.SrcCol)
				} else {
					for _, t := range sc.Tables() {
						if col = t.Column(c.SrcCol); col != nil {
							break
						}
					}
				}
			}
			if col != nil {
				goType = strings.TrimPrefix(col.GoType, "*")
				if goType == "" {
					goType = sqlToGo(col.SQLType)
				}
				null = col.Nullable || outer[c.Qualifier]
			}
			goType = wrapNull(goType, null)
			needSQL = needSQL || strings.HasPrefix(goType, "sql.")
			needUUID = needUUID || strings.Contains(goType, "uuid.")
			needTime = needTime || strings.Contains(goType, "time.")
			fmt.Fprintf(&b, "\t%s %s `json:\"%s\" db:\"%s\"`\n", Pascal(c.Name), goType, c.Name, c.Name)
		}
		b.WriteString("}\n\n")
		var imports []string
		if needSQL {
			imports = append(imports, `"database/sql"`)
		}
		if needTime {
			imports = append(imports, `"time"`)
		}
		if needUUID {
			imports = append(imports, `"github.com/google/uuid"`)
		}
		text = b.String()
		if len(imports) > 0 {
			text = "// NOTE: ensure imports: " + strings.Join(imports, ", ") + "\n" + text
		}
		return q.FuncStart, text, name, true
	}
	return
}

func wrapNull(t string, null bool) string {
	if !null {
		return t
	}
	switch t {
	case "string":
		return "sql.NullString"
	case "float64":
		return "sql.NullFloat64"
	case "int", "int32":
		return "sql.NullInt32"
	case "int64":
		return "sql.NullInt64"
	case "bool":
		return "sql.NullBool"
	case "time.Time":
		return "sql.NullTime"
	case "uuid.UUID":
		return "uuid.NullUUID"
	case "any":
		return t
	}
	return "*" + t
}

func sqlToGo(t string) string {
	u := strings.ToUpper(t)
	switch {
	case strings.HasPrefix(u, "UUID"):
		return "uuid.UUID"
	case strings.HasPrefix(u, "TIMESTAMP"), u == "DATE":
		return "time.Time"
	case strings.HasPrefix(u, "BOOL"):
		return "bool"
	case strings.HasPrefix(u, "BIGINT"):
		return "int64"
	case strings.HasPrefix(u, "INT"), strings.HasPrefix(u, "SMALLINT"), strings.HasPrefix(u, "SERIAL"):
		return "int"
	case strings.HasPrefix(u, "DECIMAL"), strings.HasPrefix(u, "NUMERIC"), strings.HasPrefix(u, "FLOAT"), strings.HasPrefix(u, "DOUBLE"), strings.HasPrefix(u, "REAL"):
		return "float64"
	case strings.HasPrefix(u, "VARCHAR"), strings.HasPrefix(u, "TEXT"), strings.HasPrefix(u, "CHAR"):
		return "string"
	}
	return "any"
}

// Pascal converts snake_case to GoName (id -> ID, province_id -> ProvinceID).
func Pascal(s string) string {
	var b strings.Builder
	for _, p := range strings.Split(s, "_") {
		switch strings.ToLower(p) {
		case "id", "url", "uuid", "ip", "api":
			b.WriteString(strings.ToUpper(p))
		case "":
		default:
			b.WriteString(strings.ToUpper(p[:1]) + p[1:])
		}
	}
	return b.String()
}
