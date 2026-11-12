package lsp

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// run feeds requests to a server and returns the decoded responses/notifications.
func run(t *testing.T, msgs ...map[string]any) []map[string]any {
	t.Helper()
	var in bytes.Buffer
	for _, m := range msgs {
		m["jsonrpc"] = "2.0"
		b, _ := json.Marshal(m)
		fmt.Fprintf(&in, "Content-Length: %d\r\n\r\n%s", len(b), b)
	}
	var out bytes.Buffer
	if err := New(&in, &out).Run(); err != nil {
		t.Fatal(err)
	}
	var res []map[string]any
	rest := out.String()
	for rest != "" {
		i := strings.Index(rest, "\r\n\r\n")
		var n int
		fmt.Sscanf(rest[:i], "Content-Length: %d", &n)
		body := rest[i+4 : i+4+n]
		rest = rest[i+4+n:]
		var m map[string]any
		_ = json.Unmarshal([]byte(body), &m)
		res = append(res, m)
	}
	return res
}

func fixture(t *testing.T) (root, uri, text string) {
	root, _ = filepath.Abs("../../testdata/project")
	p := filepath.Join(root, "app/repositories/city_repository.go")
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return root, "file://" + p, string(b)
}

func posAfter(text, needle string, extra int) map[string]any {
	i := strings.Index(text, needle)
	line := strings.Count(text[:i], "\n")
	col := i - (strings.LastIndex(text[:i], "\n") + 1) + len(needle) + extra
	return map[string]any{"line": line, "character": col}
}

func init1(root string) map[string]any {
	return map[string]any{"id": 1, "method": "initialize", "params": map[string]any{"rootUri": "file://" + root}}
}

func open(uri, text string) map[string]any {
	return map[string]any{"method": "textDocument/didOpen", "params": map[string]any{"textDocument": map[string]any{"uri": uri, "text": text}}}
}

func req(id int, method, uri string, pos map[string]any) map[string]any {
	return map[string]any{"id": id, "method": method, "params": map[string]any{"textDocument": map[string]any{"uri": uri}, "position": pos}}
}

func find(res []map[string]any, id float64) map[string]any {
	for _, r := range res {
		if r["id"] == id {
			return r
		}
	}
	return nil
}

func TestCompletionAfterAlias(t *testing.T) {
	root, uri, text := fixture(t)
	res := run(t, init1(root), open(uri, text),
		req(2, "textDocument/completion", uri, posAfter(text, "AND ct.", 0)),
		map[string]any{"method": "exit"})
	r := find(res, 2)
	b, _ := json.Marshal(r["result"])
	for _, want := range []string{`"label":"province_id"`, `"label":"latitude"`, `"label":"deleted_at"`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("missing %s in %s", want, b)
		}
	}
	if strings.Contains(string(b), `"label":"country_id"`) {
		t.Error("ct. must only list city columns")
	}
}

func TestCompletionJoinedAlias(t *testing.T) {
	root, uri, text := fixture(t)
	// "p." inside "p.name as province_name"
	res := run(t, init1(root), open(uri, text),
		req(2, "textDocument/completion", uri, posAfter(text, "p.name as province_name", -len("name as province_name"))),
		map[string]any{"method": "exit"})
	b, _ := json.Marshal(find(res, 2)["result"])
	if !strings.Contains(string(b), `"label":"country_id"`) {
		t.Errorf("p. should list province columns: %s", b)
	}
}

func TestHoverAndDefinition(t *testing.T) {
	root, uri, text := fixture(t)
	pos := posAfter(text, "ct.province_id = $", -len("province_id = $")+3)
	res := run(t, init1(root), open(uri, text),
		req(2, "textDocument/hover", uri, pos),
		req(3, "textDocument/definition", uri, pos),
		map[string]any{"method": "exit"})
	h, _ := json.Marshal(find(res, 2)["result"])
	if !strings.Contains(string(h), "RefGeoCity.ProvinceID") || !strings.Contains(string(h), "UUID") {
		t.Errorf("hover: %s", h)
	}
	d, _ := json.Marshal(find(res, 3)["result"])
	if !strings.Contains(string(d), "ref_geo_city.go") || !strings.Contains(string(d), "create_ref_geo_cities_table.go") {
		t.Errorf("definition: %s", d)
	}
}

func TestDiagnosticsUnknownColumn(t *testing.T) {
	root, uri, text := fixture(t)
	bad := strings.Replace(text, "ct.name ILIKE", "ct.nme ILIKE", 1)
	res := run(t, init1(root), open(uri, bad), map[string]any{"method": "exit"})
	var found bool
	for _, r := range res {
		if r["method"] == "textDocument/publishDiagnostics" {
			b, _ := json.Marshal(r["params"])
			if strings.Contains(string(b), "Column 'nme' does not exist on table 'ref_geo_cities'") {
				found = true
			}
		}
	}
	if !found {
		t.Error("expected unknown column diagnostic")
	}
}

func TestNoFalsePositives(t *testing.T) {
	root, uri, text := fixture(t)
	res := run(t, init1(root), open(uri, text), map[string]any{"method": "exit"})
	for _, r := range res {
		if r["method"] == "textDocument/publishDiagnostics" {
			b, _ := json.Marshal(r["params"])
			if !strings.Contains(string(b), `"diagnostics":[]`) {
				t.Errorf("unexpected diagnostics: %s", b)
			}
		}
	}
}

func diagsOf(res []map[string]any) string {
	var sb strings.Builder
	for _, r := range res {
		if r["method"] == "textDocument/publishDiagnostics" {
			b, _ := json.Marshal(r["params"])
			sb.Write(b)
		}
	}
	return sb.String()
}

func TestScanMismatchDiagnostic(t *testing.T) {
	root, uri, text := fixture(t)
	bad := strings.Replace(text, "SELECT id, province_id, name, code,", "SELECT id, province_id, name, code, deleted_by_id,", 1)
	res := run(t, init1(root), open(uri, bad), map[string]any{"method": "exit"})
	if d := diagsOf(res); !strings.Contains(d, "Column 'deleted_by_id' has no matching field in CityRow") {
		t.Errorf("expected scan mismatch, got %s", d)
	}
}

func TestPlaceholderCountDiagnostic(t *testing.T) {
	root, uri, text := fixture(t)
	bad := strings.Replace(text, "WHERE id = $1 AND deleted_at IS NULL", "WHERE id = $1 AND name = $2 AND deleted_at IS NULL", 1)
	res := run(t, init1(root), open(uri, bad), map[string]any{"method": "exit"})
	if d := diagsOf(res); !strings.Contains(d, "Query has 2 placeholder(s) but 1 argument(s) passed") {
		t.Errorf("expected placeholder diagnostic, got %s", d)
	}
}

func codeActions(t *testing.T, root, uri, text string, pos map[string]any) string {
	res := run(t, init1(root), open(uri, text),
		map[string]any{"id": 2, "method": "textDocument/codeAction", "params": map[string]any{
			"textDocument": map[string]any{"uri": uri}, "range": map[string]any{"start": pos, "end": pos},
			"context": map[string]any{"diagnostics": []any{}}}},
		map[string]any{"method": "exit"})
	b, _ := json.Marshal(find(res, 2)["result"])
	return string(b)
}

func TestExpandStarAction(t *testing.T) {
	root, uri, text := fixture(t)
	star := strings.Replace(text, "SELECT ct.id, ct.province_id,", "SELECT ct.*, ct.province_id,", 1)
	got := codeActions(t, root, uri, star, posAfter(star, "SELECT ct.*", 0))
	if !strings.Contains(got, "Expand * to explicit columns") || !strings.Contains(got, "ct.province_id, ct.name") {
		t.Errorf("expand action: %s", got)
	}
}

func TestGenerateRowStructAction(t *testing.T) {
	root, uri, text := fixture(t)
	got := codeActions(t, root, uri, text, posAfter(text, "facades.DB().Select(&row, query", 0))
	for _, want := range []string{"Generate row struct FindByIDRow", "ProvinceID uuid.UUID", "Code sql.NullString", "Latitude sql.NullFloat64"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in %s", want, got)
		}
	}
}

func TestJoinOnAndTableVarCompletion(t *testing.T) {
	root, uri, text := fixture(t)
	res := run(t, init1(root), open(uri, text),
		req(2, "textDocument/completion", uri, posAfter(text, "` p ON ", 0)),
		req(3, "textDocument/completion", uri, posAfter(text, "SELECT COUNT(*) FROM ", 0)),
		map[string]any{"method": "exit"})
	joinOn, _ := json.Marshal(find(res, 2)["result"])
	if !strings.Contains(string(joinOn), "ct.province_id = p.id") {
		t.Errorf("expected FK join condition, got %s", joinOn)
	}
	tblComp, _ := json.Marshal(find(res, 3)["result"])
	if !strings.Contains(string(tblComp), "refGeoCityTableName") || !strings.Contains(string(tblComp), "ref_geo_cities") {
		t.Errorf("expected Go table var and table name, got %s", tblComp)
	}
}

func TestORMIntelligence(t *testing.T) {
	root, _, _ := fixture(t)
	uri := "file://" + filepath.Join(root, "app/repositories/orm_repo.go")
	src := `package repositories
import (
	"github.com/rusmanplatd/basegoravel/app/facades"
	"github.com/rusmanplatd/basegoravel/app/models"
)
func QueryCities() {
	refGeoCityTableName := (models.RefGeoCity{}).TableName()
	facades.Orm().Query().Model(&models.RefGeoCity{}).With("Province").Where("province_id = ?", 1).Order("bad_col DESC")
	patches := map[string]any{"name": "A", "unknown_patch": 1}
	BuildPatchQueryWithWhere(refGeoCityTableName, 1, patches, false)
}
`
	res := run(t, init1(root), open(uri, src),
		req(2, "textDocument/completion", uri, posAfter(src, `With("`, 0)),
		req(3, "textDocument/hover", uri, posAfter(src, `Where("province`, 0)),
		map[string]any{"method": "exit"})
	withComp, _ := json.Marshal(find(res, 2)["result"])
	if !strings.Contains(string(withComp), `"label":"Province"`) {
		t.Errorf("expected Province relation completion, got %s", withComp)
	}
	whHover, _ := json.Marshal(find(res, 3)["result"])
	if !strings.Contains(string(whHover), "RefGeoCity.ProvinceID") {
		t.Errorf("expected hover on Where column, got %s", whHover)
	}
	diags := diagsOf(res)
	if !strings.Contains(diags, "Column 'bad_col' does not exist on table 'ref_geo_cities'") {
		t.Errorf("expected Order unknown column diagnostic, got %s", diags)
	}
	if !strings.Contains(diags, "Column 'unknown_patch' does not exist on table 'ref_geo_cities'") {
		t.Errorf("expected BuildPatchQuery unknown column diagnostic, got %s", diags)
	}
}

func TestConfigRouteAndValidationIntelligence(t *testing.T) {
	root, _, _ := fixture(t)
	ctrlPath := filepath.Join(root, "app/http/controllers/city_controller.go")
	ctrlBytes, _ := os.ReadFile(ctrlPath)
	ctrlURI := "file://" + ctrlPath
	ctrlText := string(ctrlBytes)

	reqPath := filepath.Join(root, "app/http/requests/city_request.go")
	reqBytes, _ := os.ReadFile(reqPath)
	reqURI := "file://" + reqPath
	reqText := string(reqBytes)

	res := run(t, init1(root),
		open(ctrlURI, ctrlText),
		open(reqURI, reqText),
		req(2, "textDocument/hover", ctrlURI, posAfter(ctrlText, `"jwt.`, 2)),
		req(3, "textDocument/definition", ctrlURI, posAfter(ctrlText, `"jwt.`, 2)),
		map[string]any{"id": 4, "method": "textDocument/codeLens", "params": map[string]any{"textDocument": map[string]any{"uri": ctrlURI}}},
		req(5, "textDocument/hover", reqURI, posAfter(reqText, "alpha_dash_", 0)),
		req(6, "textDocument/definition", reqURI, posAfter(reqText, "alpha_dash_", 0)),
		map[string]any{"id": 7, "method": "goravel/overview", "params": map[string]any{}},
		map[string]any{"method": "exit"})

	cfgHov, _ := json.Marshal(find(res, 2)["result"])
	if !strings.Contains(string(cfgHov), "JWT_SECRET") || !strings.Contains(string(cfgHov), "default-secret") {
		t.Errorf("config hover: %s", cfgHov)
	}
	cfgDef, _ := json.Marshal(find(res, 3)["result"])
	if !strings.Contains(string(cfgDef), "config/jwt.go") {
		t.Errorf("config definition: %s", cfgDef)
	}
	lenses, _ := json.Marshal(find(res, 4)["result"])
	if !strings.Contains(string(lenses), "GET /api/v1/geo/cities") {
		t.Errorf("controller codeLens: %s", lenses)
	}
	ruleHov, _ := json.Marshal(find(res, 5)["result"])
	if !strings.Contains(string(ruleHov), "alpha_dash_custom") || !strings.Contains(string(ruleHov), "letters, numbers, dashes") {
		t.Errorf("rule hover: %s", ruleHov)
	}
	ruleDef, _ := json.Marshal(find(res, 6)["result"])
	if !strings.Contains(string(ruleDef), "app/rules/alpha_dash.go") {
		t.Errorf("rule definition: %s", ruleDef)
	}
	ov, _ := json.Marshal(find(res, 7)["result"])
	if !strings.Contains(string(ov), "/api/v1/geo/cities") || !strings.Contains(string(ov), "jwt.secret") {
		t.Errorf("overview: %s", ov)
	}
}

func TestSemanticTokens(t *testing.T) {
	root, uri, text := fixture(t)
	res := run(t, init1(root), open(uri, text),
		map[string]any{"id": 2, "method": "textDocument/semanticTokens/full", "params": map[string]any{"textDocument": map[string]any{"uri": uri}}},
		map[string]any{"method": "exit"})
	b, _ := json.Marshal(find(res, 2)["result"])
	if !strings.Contains(string(b), `"data":[`) || len(b) < 50 {
		t.Errorf("expected non-empty semantic tokens, got %s", b)
	}
}

