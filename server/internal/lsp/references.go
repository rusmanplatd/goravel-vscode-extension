package lsp

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/rusmanplatd/goravel-lsp/internal/indexer"
	"github.com/rusmanplatd/goravel-lsp/internal/orm"
	"github.com/rusmanplatd/goravel-lsp/internal/rawsql"
)

func (s *Server) references(raw json.RawMessage) any {
	uri, path, text, p, off, ok := s.resolveInput(raw)
	if !ok {
		return nil
	}

	var targetConfigKey string
	var targetRuleName string
	var targetTable, targetColumn string

	// 1. Check ORM / Config / Validation spans
	fa := orm.AnalyzeFile(path, []byte(text), s.cat)
	if sp := fa.SpanAt(off); sp != nil {
		relOff := off - sp.Start
		switch sp.Kind {
		case orm.SpanConfigKey:
			targetConfigKey = sp.Text
		case orm.SpanRuleValue:
			rname, _, _ := ruleNameAt(sp.Text, relOff)
			if rname != "" {
				targetRuleName = rname
			}
		case orm.SpanORMWhere, orm.SpanORMColumn, orm.SpanORMOrder, orm.SpanPatchKey:
			if sp.Table != nil {
				w := identWordAt(sp.Text, relOff)
				if col := sp.Table.Column(w); col != nil {
					targetTable = sp.Table.Name
					targetColumn = col.Name
				}
			}
		}
	}

	// 2. Check Raw SQL reference
	if targetConfigKey == "" && targetRuleName == "" && targetTable == "" {
		if _, _, _, _, d, v, sc, rok := s.resolve(raw); rok {
			if ref := d.RefAt(v, sc); ref != nil {
				if ref.ColAlias != nil && ref.Table == nil {
					var aliasLocs []LocationT
					for _, r := range d.Refs(sc) {
						if r.ColAlias != nil && strings.EqualFold(r.ColAlias.Name, ref.ColAlias.Name) {
							if g1, ok1 := d.ToGo(r.VStart); ok1 {
								if g2, ok2 := d.ToGo(r.VEnd); ok2 {
									aliasLocs = append(aliasLocs, LocationT{uri, Range{offsetToPos(text, g1), offsetToPos(text, g2)}})
								}
							}
						}
					}
					return aliasLocs
				}
				if ref.Table != nil {
					targetTable = ref.Table.Name
					if ref.Column != nil {
						targetColumn = ref.Column.Name
					}
				}
			}
		}
	}

	// 3. Check definition locations (config/*.go, app/rules/*.go, app/models/*.go)
	cleanPath := filepath.Clean(path)
	if targetConfigKey == "" && strings.Contains(filepath.ToSlash(cleanPath), "/config/") {
		for _, k := range s.cat.ConfigKeys() {
			ci := s.cat.Config(k)
			if ci.Loc.Valid() && filepath.Clean(ci.Loc.File) == cleanPath && ci.Loc.Line == p.Position.Line {
				targetConfigKey = ci.Key
				break
			}
		}
	}
	if targetRuleName == "" && strings.Contains(filepath.ToSlash(cleanPath), "/app/rules/") {
		for _, rn := range s.cat.RuleNames() {
			ri := s.cat.Rule(rn)
			if ri.Custom && ri.Loc.Valid() && filepath.Clean(ri.Loc.File) == cleanPath {
				targetRuleName = ri.Name
				break
			}
		}
	}
	if targetTable == "" && strings.Contains(filepath.ToSlash(cleanPath), "/app/models/") {
		for _, tn := range s.cat.TableNames() {
			t := s.cat.Table(tn)
			if t.Loc.Valid() && filepath.Clean(t.Loc.File) == cleanPath && t.Loc.Line == p.Position.Line {
				targetTable = t.Name
				break
			}
			for _, c := range t.Columns {
				if c.Loc.Valid() && filepath.Clean(c.Loc.File) == cleanPath && c.Loc.Line == p.Position.Line {
					targetTable = t.Name
					targetColumn = c.Name
					break
				}
			}
			if targetTable != "" {
				break
			}
		}
	}

	if targetConfigKey == "" && targetRuleName == "" && targetTable == "" {
		return nil
	}

	var locs []LocationT
	scanFile := func(fpath, content string) {
		furi := pathToURI(fpath)
		if targetConfigKey != "" || targetRuleName != "" || targetColumn != "" {
			ffa := orm.AnalyzeFile(fpath, []byte(content), s.cat)
			for _, sp := range ffa.Spans {
				if targetConfigKey != "" && sp.Kind == orm.SpanConfigKey && sp.Text == targetConfigKey {
					locs = append(locs, LocationT{furi, Range{offsetToPos(content, sp.Start), offsetToPos(content, sp.End)}})
				}
				if targetRuleName != "" && sp.Kind == orm.SpanRuleValue {
					offIn := 0
					for _, part := range strings.Split(sp.Text, "|") {
						rn := part
						if i := strings.IndexByte(part, ':'); i >= 0 {
							rn = part[:i]
						}
						if strings.TrimSpace(rn) == targetRuleName {
							locs = append(locs, LocationT{furi, Range{
								offsetToPos(content, sp.Start+offIn),
								offsetToPos(content, sp.Start+offIn+len(strings.TrimSpace(rn))),
							}})
						}
						offIn += len(part) + 1
					}
				}
				if targetColumn != "" && sp.Table != nil && strings.EqualFold(sp.Table.Name, targetTable) {
					if sp.Kind == orm.SpanORMColumn || sp.Kind == orm.SpanPatchKey || sp.Kind == orm.SpanORMWhere || sp.Kind == orm.SpanORMOrder {
						for _, idx := range findWordOccurrences(sp.Text, targetColumn) {
							locs = append(locs, LocationT{furi, Range{
								offsetToPos(content, sp.Start+idx[0]),
								offsetToPos(content, sp.Start+idx[1]),
							}})
						}
					}
				}
			}
		}
		if targetTable != "" {
			fi := rawsql.Extract(fpath, []byte(content), s.cat)
			for _, d := range fi.Docs {
				sc := fi.BuildScope(d, s.cat)
				for _, ref := range d.Refs(sc) {
					if ref.Table == nil || !strings.EqualFold(ref.Table.Name, targetTable) {
						continue
					}
					if targetColumn != "" {
						if ref.Column == nil || !strings.EqualFold(ref.Column.Name, targetColumn) {
							continue
						}
					} else if ref.Column != nil {
						continue
					}
					if g1, ok1 := d.ToGo(ref.VStart); ok1 {
						if g2, ok2 := d.ToGo(ref.VEnd); ok2 {
							locs = append(locs, LocationT{furi, Range{offsetToPos(content, g1), offsetToPos(content, g2)}})
						}
					}
				}
			}
		}
	}

	// Scan open docs first, then walk workspace
	visited := map[string]bool{}
	for u, doc := range s.docs {
		if strings.HasSuffix(u, ".go") {
			fp := filepath.Clean(uriToPath(u))
			visited[fp] = true
			scanFile(fp, doc)
		}
	}
	if s.root != "" {
		_ = filepath.WalkDir(s.root, func(fp string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if d.IsDir() && (d.Name() == "vendor" || d.Name() == "node_modules" || d.Name() == ".git") {
				return filepath.SkipDir
			}
			if d.IsDir() || !strings.HasSuffix(fp, ".go") || strings.HasSuffix(fp, "_test.go") {
				return nil
			}
			cfp := filepath.Clean(fp)
			if visited[cfp] {
				return nil
			}
			if b, err := os.ReadFile(cfp); err == nil {
				scanFile(cfp, string(b))
			}
			return nil
		})
	}
	_ = uri
	return locs
}

func ruleNameAt(text string, relOff int) (string, int, int) {
	if relOff < 0 || relOff > len(text) {
		return "", 0, 0
	}
	s := strings.LastIndexByte(text[:relOff], '|') + 1
	e := len(text)
	if i := strings.IndexByte(text[relOff:], '|'); i >= 0 {
		e = relOff + i
	}
	seg := text[s:e]
	if i := strings.IndexByte(seg, ':'); i >= 0 {
		seg = seg[:i]
	}
	return strings.TrimSpace(seg), s, s + len(strings.TrimSpace(seg))
}

func identWordAt(s string, off int) string {
	if off < 0 || off > len(s) {
		return ""
	}
	isW := func(b byte) bool {
		return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9') || b == '_'
	}
	l, r := off, off
	for l > 0 && isW(s[l-1]) {
		l--
	}
	for r < len(s) && isW(s[r]) {
		r++
	}
	return s[l:r]
}

func findWordOccurrences(s, word string) [][2]int {
	var out [][2]int
	lowS := strings.ToLower(s)
	lowW := strings.ToLower(word)
	isW := func(b byte) bool {
		return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9') || b == '_'
	}
	for i := 0; i <= len(lowS)-len(lowW); {
		idx := strings.Index(lowS[i:], lowW)
		if idx < 0 {
			break
		}
		start := i + idx
		end := start + len(lowW)
		leftOK := start == 0 || !isW(lowS[start-1])
		rightOK := end == len(lowS) || !isW(lowS[end])
		if leftOK && rightOK {
			out = append(out, [2]int{start, end})
		}
		i = end
	}
	return out
}

// Keep unused import check happy if needed
var _ = indexer.Build

