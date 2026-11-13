package rawsql

import (
	"fmt"
	"regexp"
	"strings"
)

var (
	topStmtRe       = regexp.MustCompile(`(?is)^\s*(SELECT|INSERT|UPDATE|DELETE|WITH)\b`)
	danglingCommaRe = regexp.MustCompile(`(?i),\s*(?:FROM|WHERE|GROUP\s+BY|ORDER\s+BY|HAVING|LIMIT|OFFSET|SET|VALUES|RETURNING|UNION|EXCEPT|INTERSECT|\)|$)`)
	emptySelectRe   = regexp.MustCompile(`(?is)\bSELECT\s+(?:DISTINCT\s+)?FROM\b`)
	trailingClause  = regexp.MustCompile(`(?is)\b(SELECT|FROM|WHERE|JOIN|ON|SET|VALUES|GROUP\s+BY|ORDER\s+BY|HAVING|LIMIT|OFFSET)\s*$`)
	doubleCommaRe   = regexp.MustCompile(`,\s*,`)
	leadingCommaRe  = regexp.MustCompile(`(?is)(?:\bSELECT(?:\s+DISTINCT)?|\()\s*,`)
	dupOpRe         = regexp.MustCompile(`(?is)\b(AND\s+AND|OR\s+OR|AND\s+OR|OR\s+AND)\b`)
)

func checkSQLSyntax(sql string, dynamic bool) []Issue {
	var out []Issue
	if strings.TrimSpace(sql) == "" {
		return nil
	}

	// 1. Check balanced quotes and parentheses
	inSingle := false
	inDouble := false
	quoteStart := -1
	var parenStack []int

	for i := 0; i < len(sql); i++ {
		c := sql[i]
		if inSingle {
			if c == '\'' {
				if i+1 < len(sql) && sql[i+1] == '\'' {
					i++
					continue
				}
				inSingle = false
			}
			continue
		}
		if inDouble {
			if c == '"' {
				if i+1 < len(sql) && sql[i+1] == '"' {
					i++
					continue
				}
				inDouble = false
			}
			continue
		}
		switch c {
		case '\'':
			inSingle = true
			quoteStart = i
		case '"':
			inDouble = true
			quoteStart = i
		case '(':
			parenStack = append(parenStack, i)
		case ')':
			if len(parenStack) == 0 {
				out = append(out, Issue{i, i + 1, 1, "SQL syntax error: unexpected closing parenthesis ')'"})
			} else {
				parenStack = parenStack[:len(parenStack)-1]
			}
		}
	}
	if inSingle && quoteStart >= 0 {
		out = append(out, Issue{quoteStart, quoteStart + 1, 1, "SQL syntax error: unclosed string literal"})
	}
	if inDouble && quoteStart >= 0 {
		out = append(out, Issue{quoteStart, quoteStart + 1, 1, "SQL syntax error: unclosed quoted identifier"})
	}

	isFullStmt := topStmtRe.MatchString(sql)
	if isFullStmt && !dynamic && len(parenStack) > 0 {
		p := parenStack[len(parenStack)-1]
		out = append(out, Issue{p, p + 1, 1, "SQL syntax error: unclosed parenthesis '('"})
	}

	// Strip string literals for structural regex checks
	clean := maskStringLiterals(sql)

	if m := danglingCommaRe.FindStringIndex(clean); m != nil {
		out = append(out, Issue{m[0], m[0] + 1, 1, "SQL syntax error: dangling comma"})
	}
	if m := doubleCommaRe.FindStringIndex(clean); m != nil {
		out = append(out, Issue{m[0], m[1], 1, "SQL syntax error: consecutive commas"})
	}
	if m := leadingCommaRe.FindStringIndex(clean); m != nil {
		out = append(out, Issue{m[1] - 1, m[1], 1, "SQL syntax error: unexpected comma"})
	}
	if m := dupOpRe.FindStringIndex(clean); m != nil {
		out = append(out, Issue{m[0], m[1], 1, "SQL syntax error: consecutive logical operators"})
	}
	if isFullStmt {
		if m := emptySelectRe.FindStringIndex(clean); m != nil {
			out = append(out, Issue{m[0], m[1], 1, "SQL syntax error: empty SELECT list before FROM"})
		}
		if !dynamic {
			if m := trailingClause.FindStringSubmatchIndex(clean); m != nil {
				out = append(out, Issue{m[2], m[3], 1, fmt.Sprintf("SQL syntax error: incomplete %s clause", strings.ToUpper(clean[m[2]:m[3]]))})
			}
		}
	}

	return out
}

func maskStringLiterals(sql string) string {
	b := []byte(sql)
	inSingle := false
	for i := 0; i < len(b); i++ {
		if inSingle {
			if b[i] == '\'' {
				if i+1 < len(b) && b[i+1] == '\'' {
					b[i], b[i+1] = ' ', ' '
					i++
					continue
				}
				inSingle = false
				continue
			}
			b[i] = ' '
			continue
		}
		if b[i] == '\'' {
			inSingle = true
		}
	}
	return string(b)
}

var sqlNonColumnWords = map[string]bool{}

func init() {
	words := "select distinct from where join left right inner outer full cross natural on using " +
		"and or not in is null true false as into values update set delete returning " +
		"group by order asc desc nulls first last limit offset having union all except intersect " +
		"with recursive case when then else end cast like ilike similar to between exists any some " +
		"over partition filter within extract epoch from interval current_timestamp current_date " +
		"current_time localtimestamp default excluded conflict do nothing coalesce nullif " +
		"count sum avg min max now lower upper trim btrim ltrim rtrim length substr substring " +
		"concat replace split_part to_char to_date to_timestamp date_trunc age row_number rank dense_rank " +
		"int integer bigint smallint serial bigserial text varchar char boolean bool float double " +
		"numeric decimal real date time timestamp timestamptz uuid json jsonb bytea " +
		"for share update skip locked wait nowait fetch next row rows only"
	for _, w := range strings.Fields(words) {
		sqlNonColumnWords[w] = true
	}
}

var (
	identRe     = regexp.MustCompile(`[A-Za-z_]\w*`)
	asAliasRe   = regexp.MustCompile(`(?i)\bAS\s+"?([A-Za-z_]\w*)"?`)
	whereClause = regexp.MustCompile(`(?is)\b(?:WHERE|ORDER\s+BY|GROUP\s+BY)\b`)
)

func checkUnqualifiedColumns(sql string, sc Scope) []Issue {
	// Skip queries with CTEs or subqueries that might introduce opaque columns
	if strings.Contains(strings.ToUpper(sql), "WITH ") {
		return nil
	}
	clean := maskStringLiterals(sql)

	// Collect explicit AS aliases from SELECT so ORDER BY / GROUP BY alias isn't flagged
	aliases := map[string]bool{}
	for _, m := range asAliasRe.FindAllStringSubmatch(clean, -1) {
		aliases[strings.ToLower(m[1])] = true
	}
	// Also collect table names/aliases in scope
	tables := sc.Tables()
	hasCol := func(name string) bool {
		for _, t := range tables {
			if t.Column(name) != nil {
				return true
			}
		}
		return false
	}

	var out []Issue
	// Check SELECT list items (when ParseSelectList finds plain unqualified columns)
	cols, _ := ParseSelectList(sql)
	for _, c := range cols {
		if c.Qualifier == "" && c.SrcCol != "" && !strings.HasPrefix(c.SrcCol, "__") {
			low := strings.ToLower(c.SrcCol)
			if !sqlNonColumnWords[low] && sc[low] == nil && !hasCol(c.SrcCol) {
				vStart, vEnd := c.VStart, c.VEnd
				if c.IsAlias && c.SrcVStart > 0 {
					vStart, vEnd = c.SrcVStart, c.SrcVEnd
				}
				msg := fmt.Sprintf("Column '%s' does not exist on table(s) in scope", c.SrcCol)
				if len(tables) == 1 {
					if sug := closestColumn(tables[0], c.SrcCol); sug != "" {
						msg += fmt.Sprintf(" (did you mean '%s'?)", sug)
					}
				}
				out = append(out, Issue{vStart, vEnd, 2, msg})
			}
		}
	}

	// Check WHERE / ORDER BY / GROUP BY unqualified identifiers
	wm := whereClause.FindStringIndex(clean)
	if wm == nil {
		return out
	}
	tailStart := wm[0]
	tail := clean[tailStart:]
	for _, m := range identRe.FindAllStringIndex(tail, -1) {
		absStart := tailStart + m[0]
		absEnd := tailStart + m[1]
		word := tail[m[0]:m[1]]
		low := strings.ToLower(word)

		if strings.HasPrefix(word, "__") || sqlNonColumnWords[low] || aliases[low] || sc[low] != nil {
			continue
		}
		// Skip qualified identifiers (a.b), function calls (fn(), ::type casts, $1)
		if absStart > 0 {
			prev := clean[absStart-1]
			if prev == '.' || prev == '$' || prev == ':' || prev == '\'' || prev == '"' {
				continue
			}
		}
		if absEnd < len(clean) {
			next := clean[absEnd]
			if next == '.' || next == '(' {
				continue
			}
		}
		if hasCol(word) {
			continue
		}
		msg := fmt.Sprintf("Column '%s' does not exist on table(s) in scope", word)
		if len(tables) == 1 {
			if sug := closestColumn(tables[0], word); sug != "" {
				msg += fmt.Sprintf(" (did you mean '%s'?)", sug)
			}
		}
		out = append(out, Issue{absStart, absEnd, 2, msg})
	}
	return out
}

