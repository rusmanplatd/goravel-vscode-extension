package lsp

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/rusmanplatd/goravel-lsp/internal/orm"
	"github.com/rusmanplatd/goravel-lsp/internal/rawsql"
)

func (s *Server) rename(raw json.RawMessage) any {
	var params struct {
		TextDocument struct {
			URI string `json:"uri"`
		} `json:"textDocument"`
		Position Position `json:"position"`
		NewName  string   `json:"newName"`
	}
	_ = json.Unmarshal(raw, &params)
	newName := strings.TrimSpace(params.NewName)
	if newName == "" {
		return nil
	}
	uri, path, text, _, off, ok := s.resolveInput(raw)
	if !ok {
		return nil
	}

	changes := map[string][]map[string]any{}
	addEdit := func(targetURI, content string, start, end int, repl string) {
		if start < 0 || end < start || end > len(content) {
			return
		}
		changes[targetURI] = append(changes[targetURI], map[string]any{
			"range":   Range{offsetToPos(content, start), offsetToPos(content, end)},
			"newText": repl,
		})
	}

	var targetConfigKey string
	var targetRuleName string
	var targetGateAbility string
	var targetLangKey string
	var targetTable, targetColumn string

	// 1. Check ORM / Config / Validation / Lang / Gate spans
	fa := orm.AnalyzeFile(path, []byte(text), s.cat)
	if sp := fa.SpanAt(off); sp != nil {
		relOff := off - sp.Start
		switch sp.Kind {
		case orm.SpanConfigKey:
			targetConfigKey = sp.Text
		case orm.SpanLangKey:
			targetLangKey = sp.Text
		case orm.SpanGateAbility:
			targetGateAbility = sp.Text
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

	// 2. Check Raw SQL alias or column
	if targetConfigKey == "" && targetRuleName == "" && targetGateAbility == "" && targetLangKey == "" && targetTable == "" {
		if _, _, _, _, d, v, sc, rok := s.resolve(raw); rok {
			if ref := d.RefAt(v, sc); ref != nil {
				// Local SQL column alias rename within current query
				if ref.ColAlias != nil && ref.Table == nil {
					for _, r := range d.Refs(sc) {
						if r.ColAlias != nil && strings.EqualFold(r.ColAlias.Name, ref.ColAlias.Name) {
							if g1, ok1 := d.ToGo(r.VStart); ok1 {
								if g2, ok2 := d.ToGo(r.VEnd); ok2 {
									addEdit(uri, text, g1, g2, newName)
								}
							}
						}
					}
					if len(changes) > 0 {
						return map[string]any{"changes": changes}
					}
				}
				// Local SQL table alias rename within current query
				if ref.TableAlias != "" && ref.Column == nil {
					if vs, ve, ok := d.TableAliasDef(ref.TableAlias); ok {
						if g1, ok1 := d.ToGo(vs); ok1 {
							if g2, ok2 := d.ToGo(ve); ok2 {
								addEdit(uri, text, g1, g2, newName)
							}
						}
					}
					for _, r := range d.Refs(sc) {
						if strings.EqualFold(r.TableAlias, ref.TableAlias) && r.Column == nil {
							if g1, ok1 := d.ToGo(r.VStart); ok1 {
								if g2, ok2 := d.ToGo(r.VEnd); ok2 {
									addEdit(uri, text, g1, g2, newName)
								}
							}
						}
					}
					if len(changes) > 0 {
						return map[string]any{"changes": changes}
					}
				}
				if ref.Table != nil && ref.Column != nil {
					targetTable = ref.Table.Name
					targetColumn = ref.Column.Name
				}
			}
		}
	}

	if targetConfigKey == "" && targetRuleName == "" && targetGateAbility == "" && targetLangKey == "" && targetColumn == "" {
		return nil
	}

	scanFile := func(fpath, content string) {
		furi := pathToURI(fpath)
		ffa := orm.AnalyzeFile(fpath, []byte(content), s.cat)
		for _, sp := range ffa.Spans {
			if targetConfigKey != "" && sp.Kind == orm.SpanConfigKey && sp.Text == targetConfigKey {
				addEdit(furi, content, sp.Start, sp.End, newName)
			}
			if targetLangKey != "" && sp.Kind == orm.SpanLangKey && sp.Text == targetLangKey {
				addEdit(furi, content, sp.Start, sp.End, newName)
			}
			if targetGateAbility != "" && sp.Kind == orm.SpanGateAbility && sp.Text == targetGateAbility {
				addEdit(furi, content, sp.Start, sp.End, newName)
			}
			if targetRuleName != "" && sp.Kind == orm.SpanRuleValue {
				offIn := 0
				for _, part := range strings.Split(sp.Text, "|") {
					rn := part
					if i := strings.IndexByte(part, ':'); i >= 0 {
						rn = part[:i]
					}
					trimmed := strings.TrimSpace(rn)
					if trimmed == targetRuleName {
						lead := strings.Index(part, trimmed)
						addEdit(furi, content, sp.Start+offIn+lead, sp.Start+offIn+lead+len(trimmed), newName)
					}
					offIn += len(part) + 1
				}
			}
			if targetColumn != "" && sp.Table != nil && strings.EqualFold(sp.Table.Name, targetTable) {
				if sp.Kind == orm.SpanORMColumn || sp.Kind == orm.SpanPatchKey || sp.Kind == orm.SpanORMWhere || sp.Kind == orm.SpanORMOrder {
					for _, idx := range findWordOccurrences(sp.Text, targetColumn) {
						addEdit(furi, content, sp.Start+idx[0], sp.Start+idx[1], newName)
					}
				}
			}
		}
		if targetTable != "" && targetColumn != "" {
			fi := rawsql.Extract(fpath, []byte(content), s.cat)
			for _, d := range fi.Docs {
				sc := fi.BuildScope(d, s.cat)
				for _, ref := range d.Refs(sc) {
					if ref.Table != nil && strings.EqualFold(ref.Table.Name, targetTable) &&
						ref.Column != nil && strings.EqualFold(ref.Column.Name, targetColumn) {
						if g1, ok1 := d.ToGo(ref.VStart); ok1 {
							if g2, ok2 := d.ToGo(ref.VEnd); ok2 {
								addEdit(furi, content, g1, g2, newName)
							}
						}
					}
				}
			}
		}
	}

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

	// Also update definition sites for config key, custom rule signature, or gate ability
	if targetConfigKey != "" {
		if ci := s.cat.Config(targetConfigKey); ci != nil && ci.Loc.Valid() {
			oldLeaf := targetConfigKey
			if i := strings.LastIndexByte(targetConfigKey, '.'); i >= 0 {
				oldLeaf = targetConfigKey[i+1:]
			}
			newLeaf := newName
			if i := strings.LastIndexByte(newName, '.'); i >= 0 {
				newLeaf = newName[i+1:]
			}
			if b, err := os.ReadFile(ci.Loc.File); err == nil {
				content := string(b)
				lineOff := posToOffset(content, Position{ci.Loc.Line, ci.Loc.Col})
				target := `"` + oldLeaf + `"`
				if idx := strings.Index(content[lineOff:], target); idx >= 0 && idx < 40 {
					st := lineOff + idx + 1
					addEdit(pathToURI(ci.Loc.File), content, st, st+len(oldLeaf), newLeaf)
				}
			}
		}
	}
	if targetRuleName != "" {
		if ri := s.cat.Rule(targetRuleName); ri != nil && ri.Custom && ri.Loc.Valid() {
			if b, err := os.ReadFile(ri.Loc.File); err == nil {
				content := string(b)
				target := `"` + targetRuleName + `"`
				if idx := strings.Index(content, target); idx >= 0 {
					addEdit(pathToURI(ri.Loc.File), content, idx+1, idx+1+len(targetRuleName), newName)
				}
			}
		}
	}
	if targetGateAbility != "" {
		if gi := s.cat.Gates[targetGateAbility]; gi != nil && gi.Loc.Valid() {
			if b, err := os.ReadFile(gi.Loc.File); err == nil {
				content := string(b)
				lineOff := posToOffset(content, Position{gi.Loc.Line, gi.Loc.Col})
				target := `"` + targetGateAbility + `"`
				if idx := strings.Index(content[lineOff:], target); idx >= 0 && idx < 40 {
					st := lineOff + idx + 1
					addEdit(pathToURI(gi.Loc.File), content, st, st+len(targetGateAbility), newName)
				}
			}
		}
	}

	if len(changes) == 0 {
		return nil
	}
	return map[string]any{"changes": changes}
}

