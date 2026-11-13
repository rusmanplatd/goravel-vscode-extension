package rawsql

import (
	"fmt"

	"github.com/rusmanplatd/goravel-lsp/internal/indexer"
)

// Diag is a diagnostic in Go source byte offsets. Severity: 1 error, 2 warning.
type Diag struct {
	Start, End int
	Severity   int
	Message    string
}

// Analyze runs all checks on a file.
func Analyze(fi *FileInfo, cat *indexer.Catalog) []Diag {
	var out []Diag
	for _, d := range fi.Docs {
		sc := fi.BuildScope(d, cat)
		for _, is := range d.Check(sc) {
			g1, ok1 := d.ToGo(is.VStart)
			g2, ok2 := d.ToGo(is.VEnd)
			if ok1 && ok2 {
				sev := is.Severity
				if sev == 0 {
					sev = 1
				}
				out = append(out, Diag{g1, g2, sev, is.Message})
			}
		}
	}
	for _, q := range fi.Queries {
		out = append(out, queryDiags(fi, q, cat)...)
	}
	return out
}

// plainScalar reports Go types that cannot hold NULL.
func plainScalar(t string) bool {
	switch t {
	case "string", "bool", "int", "int8", "int16", "int32", "int64", "uint", "uint8", "uint16", "uint32", "uint64",
		"float32", "float64", "time.Time", "uuid.UUID":
		return true
	}
	return false
}

func queryDiags(fi *FileInfo, q *QueryCall, cat *indexer.Catalog) []Diag {
	var out []Diag
	// placeholder / argument count
	if !q.ArgsOpaque {
		if n := q.Placeholders(); n >= 0 && n != q.NArgs {
			out = append(out, Diag{q.ArgStart, q.ArgEnd, 2,
				fmt.Sprintf("Query has %d placeholder(s) but %d argument(s) passed", n, q.NArgs)})
		}
	}
	// scan destination vs selected columns
	if q.DestStruct == "" || q.Star {
		return out
	}
	st := cat.Struct(q.DestStruct)
	if st == nil {
		return out
	}
	sc := fi.BuildScope(q.Doc, cat)
	outer := q.NullableAliases()
	at := func(c OutCol) (int, int) {
		g1, ok1 := q.Doc.ToGo(c.VStart)
		g2, ok2 := q.Doc.ToGo(c.VEnd)
		if ok1 && ok2 {
			return g1, g2
		}
		return q.ArgStart, q.ArgEnd
	}
	for _, c := range q.Cols {
		s, e := at(c)
		f := st.Has(c.Name)
		if f == nil {
			out = append(out, Diag{s, e, 2, fmt.Sprintf("Column '%s' has no matching field in %s (scan will drop or fail)", c.Name, q.DestStruct)})
			continue
		}
		if c.SrcCol == "" || !plainScalar(f.GoType) {
			continue
		}
		var t *indexer.Table
		if c.Qualifier != "" {
			t = sc[c.Qualifier]
		} else {
			for _, cand := range sc.Tables() {
				if cand.Column(c.SrcCol) != nil {
					t = cand
					break
				}
			}
		}
		col := t.Column(c.SrcCol)
		if col == nil {
			continue
		}
		if col.Nullable || outer[c.Qualifier] {
			out = append(out, Diag{s, e, 3, fmt.Sprintf("Column '%s' may be NULL but %s.%s is %s; use sql.Null* or a pointer", c.Name, q.DestStruct, f.GoName, f.GoType)})
		}
	}
	return out
}
