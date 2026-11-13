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

// BuildScope collects aliases from target first, then inherits non-conflicting
// aliases from other docs in the same function. It also synthesizes virtual
// tables for CTEs (WITH cte AS (...)) and derived subqueries (FROM (...) sub).
func (fi *FileInfo) BuildScope(target *Doc, cat *indexer.Catalog) Scope {
	sc := Scope{}
	// 1. Collect from target doc first (never overridden by other docs in same func)
	target.collectScope(sc, cat)

	// 2. Inherit non-conflicting aliases from other docs in the same function
	other := Scope{}
	ambiguous := map[string]bool{}
	for _, d := range fi.Docs {
		if d.Func != target.Func || d == target {
			continue
		}
		// `fn() t(col)` defines a derived alias that is not a model table
		for _, m := range funcAliasRe.FindAllStringSubmatch(d.Text, -1) {
			ambiguous[strings.ToLower(m[1])] = true
		}
		tmp := Scope{}
		d.collectScope(tmp, cat)
		for k, t := range tmp {
			if old, ok := other[k]; ok && old != t {
				ambiguous[k] = true
			}
			other[k] = t
		}
	}
	for k, t := range other {
		if !ambiguous[k] && sc[k] == nil {
			sc[k] = t
		}
	}
	return sc
}

func (d *Doc) collectScope(sc Scope, cat *indexer.Catalog) {
	// Skip derived function aliases like `fn() t(col)`
	derivedSkip := map[string]bool{}
	for _, m := range funcAliasRe.FindAllStringSubmatch(d.Text, -1) {
		derivedSkip[strings.ToLower(m[1])] = true
	}

	// 1. Extract CTEs (WITH cte AS (...)) so FROM/JOIN can reference them
	ctes := extractCTEs(d.Text, sc, cat)
	for k, t := range ctes {
		sc[k] = t
	}

	// 2. Physical tables & CTEs referenced in FROM/JOIN/UPDATE/INTO
	for _, m := range aliasRe.FindAllStringSubmatch(d.Text, -1) {
		t := cat.Table(m[1])
		if t == nil {
			t = ctes[strings.ToLower(indexer.StripSchema(m[1]))]
		}
		if t == nil {
			continue
		}
		k := strings.ToLower(indexer.StripSchema(m[1]))
		if !derivedSkip[k] {
			sc[k] = t
		}
		if m[2] != "" {
			ak := strings.ToLower(m[2])
			if !notAlias[ak] && !derivedSkip[ak] {
				sc[ak] = t
			}
		}
	}

	// 3. Derived subqueries: FROM (SELECT ...) [AS] sub
	for k, t := range extractSubqueries(d.Text, sc, cat) {
		if !derivedSkip[k] {
			sc[k] = t
		}
	}
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

// Check finds unknown `alias.column` references and SQL syntax errors.
func (d *Doc) Check(sc Scope) []Issue {
	return d.CheckWithCatalog(sc, nil)
}

// CheckWithCatalog validates SQL syntax, table names, qualified `alias.column`,
// and unqualified column references against the schema catalog.
func (d *Doc) CheckWithCatalog(sc Scope, cat *indexer.Catalog) []Issue {
	var out []Issue
	if ddl.MatchString(d.Text) {
		return nil // DDL/policy statements reuse aliases; too ambiguous to validate
	}

	out = append(out, checkSQLSyntax(d.Text, d.Dynamic)...)

	hasUnknownTable := false
	if cat != nil && len(cat.Tables) > 0 {
		ctes := extractCTEs(d.Text, sc, cat)
		for _, m := range aliasRe.FindAllStringSubmatchIndex(d.Text, -1) {
			tbl := d.Text[m[2]:m[3]]
			low := strings.ToLower(indexer.StripSchema(tbl))
			if strings.HasPrefix(tbl, "__") || strings.Contains(tbl, "__") || notAlias[low] || ctes[low] != nil {
				continue
			}
			// Skip table-valued functions like FROM unnest(...)
			after := strings.TrimSpace(d.Text[m[3]:])
			if strings.HasPrefix(after, "(") {
				continue
			}
			if cat.Table(tbl) == nil {
				hasUnknownTable = true
				out = append(out, Issue{m[2], m[3], 1, fmt.Sprintf("Unknown table '%s'", tbl)})
			}
		}
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

	if !hasUnknownTable && len(sc.Tables()) > 0 {
		out = append(out, checkUnqualifiedColumns(d.Text, sc)...)
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
	Table      *indexer.Table
	Column     *indexer.Column
	ColAlias   *OutCol // non-nil when on a column alias definition or usage
	TableAlias string  // non-empty when on a table alias
	VStart     int
	VEnd       int
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

	// Check column alias definitions (`SELECT ... AS alias`) and usages (`ORDER BY alias`, `GROUP BY alias`, `HAVING alias`)
	if oc := d.colAliasAt(s, e, word); oc != nil {
		t, c := resolveOutCol(oc, sc)
		return &Ref{Table: t, Column: c, ColAlias: oc, VStart: s, VEnd: e}
	}

	if t := sc[lw]; t != nil {
		ta := ""
		if lw != strings.ToLower(t.Name) {
			ta = lw
		}
		return &Ref{Table: t, TableAlias: ta, VStart: s, VEnd: e}
	}
	for _, t := range sc.Tables() {
		if c := t.Column(word); c != nil {
			return &Ref{Table: t, Column: c, VStart: s, VEnd: e}
		}
	}
	return nil
}

func (d *Doc) colAliasAt(s, e int, word string) *OutCol {
	cols, _ := ParseSelectList(d.Text)
	for i := range cols {
		oc := &cols[i]
		if !oc.IsAlias {
			continue
		}
		if s >= oc.VStart && e <= oc.VEnd {
			return oc
		}
	}
	if isAliasUsageContext(d.Text, s) {
		for i := range cols {
			oc := &cols[i]
			if oc.IsAlias && strings.EqualFold(oc.Name, word) {
				return oc
			}
		}
	}
	return nil
}

// SelectAliases returns all explicitly aliased output columns in the top-level SELECT.
func (d *Doc) SelectAliases() []OutCol {
	cols, _ := ParseSelectList(d.Text)
	var out []OutCol
	for _, c := range cols {
		if c.IsAlias {
			out = append(out, c)
		}
	}
	return out
}

func resolveOutCol(oc *OutCol, sc Scope) (*indexer.Table, *indexer.Column) {
	if oc.SrcCol == "" {
		return nil, nil
	}
	if oc.Qualifier != "" {
		if t := sc[oc.Qualifier]; t != nil {
			return t, t.Column(oc.SrcCol)
		}
		return nil, nil
	}
	for _, t := range sc.Tables() {
		if c := t.Column(oc.SrcCol); c != nil {
			return t, c
		}
	}
	return nil, nil
}

func isAliasUsageContext(sql string, pos int) bool {
	up := strings.ToUpper(sql[:pos])
	lastOrder := strings.LastIndex(up, "ORDER BY")
	lastGroup := strings.LastIndex(up, "GROUP BY")
	lastHaving := strings.LastIndex(up, "HAVING")
	ctxPos := lastOrder
	if lastGroup > ctxPos {
		ctxPos = lastGroup
	}
	if lastHaving > ctxPos {
		ctxPos = lastHaving
	}
	if ctxPos < 0 {
		return false
	}
	lastSel := strings.LastIndex(up, "SELECT")
	lastWhere := strings.LastIndex(up, "WHERE")
	lastFrom := strings.LastIndex(up, "FROM")
	return ctxPos > lastSel && ctxPos > lastWhere && ctxPos > lastFrom
}

func isWord(b byte) bool {
	return b == '_' || (b >= '0' && b <= '9') || (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}

// CompletionCtx describes what to complete at virtual offset v.
type CompletionCtx struct {
	Qualifier    string // alias before '.'
	Prefix       string
	WantTable    bool
	WantJoinOn   bool
	WantColAlias bool
	JoinTable    string
	JoinAlias    string
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
	return CompletionCtx{
		Prefix:       prefRe.FindString(before),
		WantColAlias: isAliasUsageContext(d.Text, v),
	}
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

// TableAliasDef returns the virtual byte range where `alias` is declared in d.
func (d *Doc) TableAliasDef(alias string) (vStart, vEnd int, ok bool) {
	if alias == "" {
		return 0, 0, false
	}
	la := strings.ToLower(alias)
	for _, m := range aliasRe.FindAllStringSubmatchIndex(d.Text, -1) {
		if m[4] >= 0 && strings.EqualFold(d.Text[m[4]:m[5]], la) {
			return m[4], m[5], true
		}
	}
	cteHeadRe := regexp.MustCompile(`(?i)(?:\bWITH(?:\s+RECURSIVE)?\s+|,\s*)([A-Za-z_]\w*)\s+AS\s*\(`)
	for _, m := range cteHeadRe.FindAllStringSubmatchIndex(d.Text, -1) {
		if strings.EqualFold(d.Text[m[2]:m[3]], la) {
			return m[2], m[3], true
		}
	}
	subTailRe := regexp.MustCompile(`(?i)\)\s+(?:AS\s+)?([A-Za-z_]\w*)`)
	for _, m := range subTailRe.FindAllStringSubmatchIndex(d.Text, -1) {
		if strings.EqualFold(d.Text[m[2]:m[3]], la) && !notAlias[la] {
			return m[2], m[3], true
		}
	}
	return 0, 0, false
}

var (
	withStartRe = regexp.MustCompile(`(?is)\bWITH(?:\s+RECURSIVE)?\s+`)
	cteItemRe   = regexp.MustCompile(`(?is)^\s*([A-Za-z_]\w*)\s*(?:\([^)]*\))?\s*AS\s*\(`)
	subFromRe   = regexp.MustCompile(`(?is)\b(?:FROM|JOIN)\s*\(`)
	subAliasRe  = regexp.MustCompile(`(?is)^\s*(?:AS\s+)?([A-Za-z_]\w*)`)
)

func matchParen(s string, openIdx int) int {
	depth, inStr := 0, false
	for i := openIdx; i < len(s); i++ {
		c := s[i]
		if inStr {
			if c == '\'' {
				inStr = false
			}
			continue
		}
		switch c {
		case '\'':
			inStr = true
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

func extractCTEs(sql string, parent Scope, cat *indexer.Catalog) map[string]*indexer.Table {
	out := map[string]*indexer.Table{}
	wm := withStartRe.FindStringIndex(sql)
	if wm == nil {
		return out
	}
	pos := wm[1]
	for pos < len(sql) {
		cm := cteItemRe.FindStringSubmatchIndex(sql[pos:])
		if cm == nil {
			break
		}
		name := sql[pos+cm[2] : pos+cm[3]]
		openParen := pos + cm[1] - 1
		closeParen := matchParen(sql, openParen)
		if closeParen < 0 {
			break
		}
		body := sql[openParen+1 : closeParen]
		tbl := buildVirtualTable(name, "CTE "+name, body, parent, out, cat)
		out[strings.ToLower(name)] = tbl
		pos = closeParen + 1
		rest := strings.TrimLeft(sql[pos:], " \t\r\n")
		if !strings.HasPrefix(rest, ",") {
			break
		}
		pos = len(sql) - len(rest) + 1
	}
	return out
}

func extractSubqueries(sql string, parent Scope, cat *indexer.Catalog) map[string]*indexer.Table {
	out := map[string]*indexer.Table{}
	for _, fm := range subFromRe.FindAllStringIndex(sql, -1) {
		openParen := fm[1] - 1
		closeParen := matchParen(sql, openParen)
		if closeParen < 0 {
			continue
		}
		am := subAliasRe.FindStringSubmatch(sql[closeParen+1:])
		if am == nil || notAlias[strings.ToLower(am[1])] {
			continue
		}
		alias := am[1]
		body := sql[openParen+1 : closeParen]
		tbl := buildVirtualTable(alias, "Subquery "+alias, body, parent, out, cat)
		out[strings.ToLower(alias)] = tbl
	}
	return out
}

func buildVirtualTable(name, modelLabel, bodySQL string, parent Scope, extra map[string]*indexer.Table, cat *indexer.Catalog) *indexer.Table {
	subScope := Scope{}
	for k, t := range parent {
		subScope[k] = t
	}
	for k, t := range extra {
		subScope[k] = t
	}
	for _, m := range aliasRe.FindAllStringSubmatch(bodySQL, -1) {
		t := cat.Table(m[1])
		if t == nil {
			t = subScope[strings.ToLower(indexer.StripSchema(m[1]))]
		}
		if t == nil {
			continue
		}
		subScope[strings.ToLower(indexer.StripSchema(m[1]))] = t
		if m[2] != "" && !notAlias[strings.ToLower(m[2])] {
			subScope[strings.ToLower(m[2])] = t
		}
	}

	cols, star := ParseSelectList(bodySQL)
	var vcols []*indexer.Column
	seen := map[string]bool{}
	addCol := func(c *indexer.Column) {
		lc := strings.ToLower(c.Name)
		if lc == "" || seen[lc] {
			return
		}
		seen[lc] = true
		vcols = append(vcols, c)
	}
	for _, oc := range cols {
		srcTbl, srcCol := resolveOutCol(&oc, subScope)
		if srcCol != nil {
			cp := *srcCol
			cp.Name = oc.Name
			if oc.IsAlias && oc.Expr != "" {
				if cp.Doc != "" {
					cp.Doc = fmt.Sprintf("Alias for `%s.%s` — %s", srcTbl.Name, srcCol.Name, cp.Doc)
				} else {
					cp.Doc = fmt.Sprintf("Alias for `%s.%s`", srcTbl.Name, srcCol.Name)
				}
			}
			addCol(&cp)
		} else {
			doc := ""
			if oc.Expr != "" {
				doc = fmt.Sprintf("Expression: `%s`", oc.Expr)
			}
			addCol(&indexer.Column{Name: oc.Name, Doc: doc})
		}
	}
	if star {
		for _, t := range subScope.Tables() {
			for _, c := range t.Columns {
				addCol(c)
			}
		}
	}
	return &indexer.Table{
		Name:    name,
		Model:   modelLabel,
		Columns: vcols,
	}
}
