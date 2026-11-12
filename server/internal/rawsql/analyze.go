package rawsql

import (
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

// Issue is a diagnostic in virtual coordinates.
type Issue struct {
	VStart, VEnd int
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
		if t == nil || t.Column(col) != nil {
			continue
		}
		if strings.HasPrefix(col, "__") {
			continue
		}
		out = append(out, Issue{m[4], m[5], "Column '" + col + "' does not exist on table '" + t.Name + "'"})
	}
	return out
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
		word := d.Text[m[0]:m[1]]
		lw := strings.ToLower(word)
		if m[0] > 0 && d.Text[m[0]-1] == '.' {
			// qualified column: find qualifier
			qs := m[0] - 1
			qe := qs
			for qs > 0 && (isWord(d.Text[qs-1])) {
				qs--
			}
			if t := sc[strings.ToLower(d.Text[qs:qe])]; t != nil {
				if c := t.Column(word); c != nil {
					return &Ref{Table: t, Column: c, VStart: m[0], VEnd: m[1]}
				}
			}
			return nil
		}
		if t := sc[lw]; t != nil {
			return &Ref{Table: t, VStart: m[0], VEnd: m[1]}
		}
		for _, t := range sc.Tables() {
			if c := t.Column(word); c != nil {
				return &Ref{Table: t, Column: c, VStart: m[0], VEnd: m[1]}
			}
		}
		return nil
	}
	return nil
}

func isWord(b byte) bool {
	return b == '_' || (b >= '0' && b <= '9') || (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}

// CompletionCtx describes what to complete at virtual offset v.
type CompletionCtx struct {
	Qualifier string // alias before '.'
	Prefix    string
	WantTable bool
}

var (
	qualRe  = regexp.MustCompile(`([A-Za-z_]\w*)\.(\w*)$`)
	tableKw = regexp.MustCompile(`(?i)\b(FROM|JOIN|INTO|UPDATE|TABLE)\s+([\w."]*)$`)
	prefRe  = regexp.MustCompile(`\w*$`)
)

func (d *Doc) CompletionAt(v int) CompletionCtx {
	before := d.Text[:v]
	if m := qualRe.FindStringSubmatch(before); m != nil {
		return CompletionCtx{Qualifier: strings.ToLower(m[1]), Prefix: m[2]}
	}
	if m := tableKw.FindStringSubmatch(before); m != nil {
		return CompletionCtx{WantTable: true, Prefix: m[2]}
	}
	return CompletionCtx{Prefix: prefRe.FindString(before)}
}
