package rawsql

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/rusmanplatd/goravel-lsp/internal/indexer"
)

// Scope maps aliases / table names (lowercase) to tables for one Go function.
type Scope map[string]*indexer.Table

var aliasRe = regexp.MustCompile(`(?i)\b(?:FROM|JOIN|UPDATE|INTO)\s+([A-Za-z_][\w."]*)(?:\s+(?:AS\s+)?([A-Za-z_]\w*))?`)

var funcAliasRe = regexp.MustCompile(`\)\s+(?:AS\s+)?([A-Za-z_]\w*)\s*\(`)

var notAlias = map[string]bool{}

func init() {
	for _, w := range strings.Fields("where left right inner outer join on set group order limit offset values using cross full returning union having select from and or as natural for") {
		notAlias[w] = true
	}
}

// BuildScope collects aliases from every doc in the same function as target.
func (fi *FileInfo) BuildScope(target *Doc, cat *indexer.Catalog) Scope {
	sc := Scope{}
	ambiguous := map[string]bool{}
	set := func(k string, t *indexer.Table) {
		if old, ok := sc[k]; ok && old != t {
			ambiguous[k] = true
		}
		sc[k] = t
	}
	defer func() {
		for k := range ambiguous {
			delete(sc, k)
		}
	}()
	for _, d := range fi.Docs {
		if d.Func != target.Func {
			continue
		}
		// `fn() t(col)` defines a derived alias that is not a model table
		for _, m := range funcAliasRe.FindAllStringSubmatch(d.Text, -1) {
			ambiguous[strings.ToLower(m[1])] = true
		}
		for _, m := range aliasRe.FindAllStringSubmatch(d.Text, -1) {
			t := cat.Table(m[1])
			if t == nil {
				continue
			}
			set(strings.ToLower(indexer.StripSchema(m[1])), t)
			if m[2] != "" && !notAlias[strings.ToLower(m[2])] {
				set(strings.ToLower(m[2]), t)
			}
		}
	}
	return sc
}

// Tables returns the distinct tables in scope.
func (s Scope) Tables() []*indexer.Table {
	seen := map[*indexer.Table]bool{}
	var out []*indexer.Table
	for _, t := range s {
		if !seen[t] {
			seen[t] = true
			out = append(out, t)
		}
	}
	return out
}

// Aliases returns a map of Table -> shortest alias in scope.
func (s Scope) Aliases() map[*indexer.Table]string {
	out := map[*indexer.Table]string{}
	for k, t := range s {
		if cur, ok := out[t]; !ok || len(k) < len(cur) {
			out[t] = k
		}
	}
	return out
}

// Issue is a diagnostic in virtual coordinates.
type Issue struct {
	VStart, VEnd int
	Severity     int
	Message      string
}

var ddl = regexp.MustCompile(`(?is)^\s*(CREATE|ALTER|DROP|DO|TRUNCATE|COMMENT|GRANT)\s`)

var dotRe = regexp.MustCompile(`([A-Za-z_]\w*)\.([A-Za-z_]\w*)`)

// Check finds unknown `alias.column` references.
func (d *Doc) Check(sc Scope) []Issue {
	var out []Issue
	if ddl.MatchString(d.Text) {
		return nil // DDL/policy statements reuse aliases; too ambiguous to validate
	}
	for _, m := range dotRe.FindAllStringSubmatchIndex(d.Text, -1) {
		if m[0] > 0 {
			if p := d.Text[m[0]-1]; p == '.' || p == '"' || p == '\'' || p == '_' {
				continue
			}
		}
		qual := strings.ToLower(d.Text[m[2]:m[3]])
		col := d.Text[m[4]:m[5]]
		t := sc[qual]
		if t == nil {
			continue
		}
		if t.Column(col) != nil {
			// Index-aware hint for WHERE / ON filters on tables with known indexes
			if len(t.Indexes) > 0 && !t.HasIndex(col) && !strings.EqualFold(col, "deleted_at") && isFilterContext(d.Text, m[0]) {
				out = append(out, Issue{m[4], m[5], 4, fmt.Sprintf("Column '%s' on '%s' is not the leading column of any indexed migration", col, t.Name)})
			}
			continue
		}
		if strings.HasPrefix(col, "__") {
			continue
		}
		msg := "Column '" + col + "' does not exist on table '" + t.Name + "'"
		if sug := closestColumn(t, col); sug != "" {
			msg += fmt.Sprintf(" (did you mean '%s'?)", sug)
		}
		out = append(out, Issue{m[4], m[5], 1, msg})
	}
	return out
}

func isFilterContext(sql string, pos int) bool {
	up := strings.ToUpper(sql[:pos])
	lastWhere := strings.LastIndex(up, "WHERE")
	lastOn := strings.LastIndex(up, " ON ")
	lastSel := strings.LastIndex(up, "SELECT")
	lastOrder := strings.LastIndex(up, "ORDER BY")
	lastGroup := strings.LastIndex(up, "GROUP BY")
	filterPos := lastWhere
	if lastOn > filterPos {
		filterPos = lastOn
	}
	return filterPos > lastSel && filterPos > lastOrder && filterPos > lastGroup
}

func closestColumn(t *indexer.Table, col string) string {
	best, bestDist := "", 3
	lc := strings.ToLower(col)
	for _, c := range t.Columns {
		d := editDistance(lc, strings.ToLower(c.Name))
		if d < bestDist {
			bestDist = d
			best = c.Name
		}
	}
	return best
}

func editDistance(a, b string) int {
	if len(a) == 0 {
		return len(b)
	}
	if len(b) == 0 {
		return len(a)
	}
	dp := make([]int, len(b)+1)
	for j := range dp {
		dp[j] = j
	}
	for i := 1; i <= len(a); i++ {
		prev := dp[0]
		dp[0] = i
		for j := 1; j <= len(b); j++ {
			temp := dp[j]
			cost := 0
			if a[i-1] != b[j-1] {
				cost = 1
			}
			v := dp[j] + 1
			if dp[j-1]+1 < v {
				v = dp[j-1] + 1
			}
			if prev+cost < v {
				v = prev + cost
			}
			dp[j] = v
			prev = temp
		}
	}
	return dp[len(b)]
}

// Ref describes what the cursor is on.
type Ref struct {
	Table  *indexer.Table
	Column *indexer.Column
	VStart int
	VEnd   int
}

var wordRe = regexp.MustCompile(`[A-Za-z_]\w*`)

// RefAt resolves the identifier at virtual offset v.
func (d *Doc) RefAt(v int, sc Scope) *Ref {
	for _, m := range wordRe.FindAllStringIndex(d.Text, -1) {
		if v < m[0] || v > m[1] {
			continue
		}
		return d.refForMatch(m[0], m[1], sc)
	}
	return nil
}

// Refs returns all resolved table and column references in the virtual SQL document.
func (d *Doc) Refs(sc Scope) []*Ref {
	var out []*Ref
	for _, m := range wordRe.FindAllStringIndex(d.Text, -1) {
		if r := d.refForMatch(m[0], m[1], sc); r != nil {
			out = append(out, r)
		}
	}
	return out
}

func (d *Doc) refForMatch(s, e int, sc Scope) *Ref {
	word := d.Text[s:e]
	lw := strings.ToLower(word)
	if s > 0 && d.Text[s-1] == '.' {
		qs := s - 1
		qe := qs
		for qs > 0 && isWord(d.Text[qs-1]) {
			qs--
		}
		if t := sc[strings.ToLower(d.Text[qs:qe])]; t != nil {
			if c := t.Column(word); c != nil {
				return &Ref{Table: t, Column: c, VStart: s, VEnd: e}
			}
		}
		return nil
	}
	if t := sc[lw]; t != nil {
		return &Ref{Table: t, VStart: s, VEnd: e}
	}
	for _, t := range sc.Tables() {
		if c := t.Column(word); c != nil {
			return &Ref{Table: t, Column: c, VStart: s, VEnd: e}
		}
	}
	return nil
}

func isWord(b byte) bool {
	return b == '_' || (b >= '0' && b <= '9') || (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}

// CompletionCtx describes what to complete at virtual offset v.
type CompletionCtx struct {
	Qualifier  string // alias before '.'
	Prefix     string
	WantTable  bool
	WantJoinOn bool
	JoinTable  string
	JoinAlias  string
}

var (
	qualRe   = regexp.MustCompile(`([A-Za-z_]\w*)\.(\w*)$`)
	tableKw  = regexp.MustCompile(`(?i)\b(FROM|JOIN|INTO|UPDATE|TABLE)\s+([\w."]*)$`)
	joinOnRe = regexp.MustCompile(`(?i)\bJOIN\s+([A-Za-z_][\w."]*)(?:\s+(?:AS\s+)?([A-Za-z_]\w*))?\s+ON\s+(\w*)$`)
	prefRe   = regexp.MustCompile(`\w*$`)
)

func (d *Doc) CompletionAt(v int) CompletionCtx {
	before := d.Text[:v]
	if m := qualRe.FindStringSubmatch(before); m != nil {
		return CompletionCtx{Qualifier: strings.ToLower(m[1]), Prefix: m[2]}
	}
	if m := joinOnRe.FindStringSubmatch(before); m != nil {
		alias := m[2]
		if alias == "" || strings.EqualFold(alias, "ON") {
			alias = indexer.StripSchema(m[1])
		}
		return CompletionCtx{WantJoinOn: true, JoinTable: m[1], JoinAlias: strings.ToLower(alias), Prefix: m[3]}
	}
	if m := tableKw.FindStringSubmatch(before); m != nil {
		return CompletionCtx{WantTable: true, Prefix: m[2]}
	}
	return CompletionCtx{Prefix: prefRe.FindString(before)}
}

// JoinConditions returns candidate `a.fk = b.pk` expressions for a JOIN ON clause.
func JoinConditions(joinTable *indexer.Table, joinAlias string, sc Scope) []string {
	if joinTable == nil {
		return nil
	}
	aliases := sc.Aliases()
	seen := map[string]bool{}
	var out []string
	add := func(s string) {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	for _, other := range sc.Tables() {
		if other == joinTable {
			continue
		}
		oa := aliases[other]
		if oa == "" {
			oa = other.Name
		}
		// Check if other has FK to joinTable
		for _, r := range other.Relations {
			if r.TargetTable == joinTable.Name && r.ForeignKey != "" {
				ref := r.References
				if ref == "" {
					ref = "id"
				}
				add(fmt.Sprintf("%s.%s = %s.%s", oa, r.ForeignKey, joinAlias, ref))
			}
		}
		// Check if joinTable has FK to other
		for _, r := range joinTable.Relations {
			if r.TargetTable == other.Name && r.ForeignKey != "" {
				ref := r.References
				if ref == "" {
					ref = "id"
				}
				add(fmt.Sprintf("%s.%s = %s.%s", joinAlias, r.ForeignKey, oa, ref))
			}
		}
	}
	return out
}
