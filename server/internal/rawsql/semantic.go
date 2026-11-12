package rawsql

import (
	"regexp"
	"sort"
	"strings"

	"github.com/rusmanplatd/goravel-lsp/internal/indexer"
)

// Semantic token types (indices into SemanticTokenTypes legend).
const (
	SemKeyword   = 0
	SemFunction  = 1
	SemType      = 2 // table or table alias
	SemProperty  = 3 // column or column alias
	SemParameter = 4 // $1, ?
	SemString    = 5 // '...'
	SemNumber    = 6
	SemOperator  = 7
)

var SemanticTokenTypes = []string{
	"keyword",
	"function",
	"type",
	"property",
	"parameter",
	"string",
	"number",
	"operator",
}

// SemToken is a semantic token in Go byte coordinates.
type SemToken struct {
	GoStart int
	Length  int
	Type    int
}

var (
	sqlKeywords  = map[string]bool{}
	sqlFunctions = map[string]bool{}
	sqlTypes     = map[string]bool{}
	semTokRe     = regexp.MustCompile(`'(?:''|[^'])*'|\$__|\$\d+|\$%[dsv]|\?|\b\d+(?:\.\d+)?\b|[A-Za-z_]\w*|!=|<>|<=|>=|=|<|>|\*`)
)

func init() {
	for _, w := range strings.Fields("select distinct from where left right inner outer full cross natural join on using as and or not is in exists between ilike like similar to order group by having limit offset fetch first next rows only insert into values update set delete returning with recursive union intersect except all any some case when then else end asc desc nulls last create alter drop truncate rename add column table temp temporary view materialized index unique concurrently policy schema extension trigger function procedure if cascade restrict primary foreign key references check default constraint conflict do nothing for each row statement enable disable level security begin commit rollback transaction grant revoke public over partition window filter within lateral null true false") {
		sqlKeywords[w] = true
	}
	for _, w := range strings.Fields("count sum avg min max coalesce nullif now current_timestamp current_date current_time lower upper trim ltrim rtrim substring concat length char_length cast extract date_trunc date_part to_char to_date to_timestamp age row_number rank dense_rank lag lead first_value last_value array_agg string_agg json_build_object jsonb_build_object json_agg jsonb_agg to_jsonb gen_random_uuid uuid_generate_v4 round ceil floor abs greatest least") {
		sqlFunctions[w] = true
	}
	for _, w := range strings.Fields("uuid varchar char text int integer int2 int4 int8 bigint smallint serial bigserial smallserial decimal numeric float float4 float8 double precision real boolean bool date time timetz timestamp timestamptz interval json jsonb bytea inet cidr macaddr geography geometry point linestring polygon tsvector tsquery") {
		sqlTypes[w] = true
	}
}

// SemanticTokens extracts semantic tokens across all SQL docs in fi.
func SemanticTokens(fi *FileInfo, cat *indexer.Catalog) []SemToken {
	seen := map[int]bool{}
	var out []SemToken
	add := func(vStart, vEnd int, d *Doc, typ int) {
		g1, ok1 := d.ToGo(vStart)
		g2, ok2 := d.ToGo(vEnd)
		if !ok1 || !ok2 || g2 <= g1 || seen[g1] {
			return
		}
		// Ensure both endpoints belong to the same Go literal segment
		for _, s := range d.Segs {
			if vStart >= s.VStart && vEnd <= s.VEnd {
				seen[g1] = true
				out = append(out, SemToken{GoStart: g1, Length: g2 - g1, Type: typ})
				return
			}
		}
	}

	for _, d := range fi.Docs {
		sc := fi.BuildScope(d, cat)
		for _, m := range semTokRe.FindAllStringIndex(d.Text, -1) {
			tok := d.Text[m[0]:m[1]]
			if strings.HasPrefix(tok, "__") {
				continue
			}
			switch {
			case strings.HasPrefix(tok, "'"):
				add(m[0], m[1], d, SemString)
			case strings.HasPrefix(tok, "$") || tok == "?":
				add(m[0], m[1], d, SemParameter)
			case tok[0] >= '0' && tok[0] <= '9':
				add(m[0], m[1], d, SemNumber)
			case !isWord(tok[0]):
				add(m[0], m[1], d, SemOperator)
			default:
				lw := strings.ToLower(tok)
				// Check if followed by '(' -> function
				rest := strings.TrimLeft(d.Text[m[1]:], " \t")
				if strings.HasPrefix(rest, "(") && sqlFunctions[lw] {
					add(m[0], m[1], d, SemFunction)
					continue
				}
				// Qualified `alias.col`: if preceded by '.', it's a column property
				if m[0] > 0 && d.Text[m[0]-1] == '.' {
					add(m[0], m[1], d, SemProperty)
					continue
				}
				// If followed by '.', it's a table/alias qualifier
				if m[1] < len(d.Text) && d.Text[m[1]] == '.' {
					add(m[0], m[1], d, SemType)
					continue
				}
				if sqlKeywords[lw] {
					add(m[0], m[1], d, SemKeyword)
					continue
				}
				if sqlTypes[lw] {
					add(m[0], m[1], d, SemType)
					continue
				}
				if sc[lw] != nil || cat.Table(lw) != nil {
					add(m[0], m[1], d, SemType)
					continue
				}
				// Column in scope or alias
				add(m[0], m[1], d, SemProperty)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].GoStart < out[j].GoStart })
	return out
}

