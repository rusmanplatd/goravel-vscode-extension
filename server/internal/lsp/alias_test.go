package lsp

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestRawSQLColumnAliasHoverDefCompletionRefs(t *testing.T) {
	root, uri, text := fixture(t)
	// Add a query with both a column alias and an expression alias + ORDER BY
	extra := `
func aliasQueryTest(ctx any) {
	q := ` + "`" + `
		SELECT ct.id, p.name AS province_name, COUNT(*) AS total_cities
		FROM ref_geo_cities ct
		LEFT JOIN ref_geo_provinces p ON ct.province_id = p.id
		GROUP BY ct.id, province_name
		ORDER BY province_name, total_cities
	` + "`" + `
	_ = q
}
`
	doc := text + extra

	// Position on `province_name` in ORDER BY
	orderProvPos := posAfter(doc, "ORDER BY province_name", -len("province_name")+2)
	// Position on `total_cities` in ORDER BY
	orderTotalPos := posAfter(doc, "ORDER BY province_name, total_cities", -len("total_cities")+2)
	// Position after `ORDER BY ` in our test query for completion
	orderCompPos := posAfter(doc, "ORDER BY province_name, total_cities", -len("province_name, total_cities"))

	res := run(t, init1(root), open(uri, doc),
		req(2, "textDocument/hover", uri, orderProvPos),
		req(3, "textDocument/definition", uri, orderProvPos),
		req(4, "textDocument/hover", uri, orderTotalPos),
		req(5, "textDocument/completion", uri, orderCompPos),
		req(6, "textDocument/references", uri, orderTotalPos),
		map[string]any{"method": "exit"},
	)

	hProv, _ := json.Marshal(find(res, 2)["result"])
	if !strings.Contains(string(hProv), "Column Alias") || !strings.Contains(string(hProv), "RefGeoProvince.Name") {
		t.Errorf("expected column alias hover with RefGeoProvince.Name, got: %s", hProv)
	}

	defProv, _ := json.Marshal(find(res, 3)["result"])
	if !strings.Contains(string(defProv), "city_repository.go") || !strings.Contains(string(defProv), "ref_geo_province.go") {
		t.Errorf("expected definition to jump to SELECT alias and model, got: %s", defProv)
	}

	hTotal, _ := json.Marshal(find(res, 4)["result"])
	if !strings.Contains(string(hTotal), "Column Alias") || !strings.Contains(string(hTotal), "COUNT(*)") {
		t.Errorf("expected expression alias hover with COUNT(*), got: %s", hTotal)
	}

	comp, _ := json.Marshal(find(res, 5)["result"])
	if !strings.Contains(string(comp), `"label":"province_name"`) || !strings.Contains(string(comp), `"label":"total_cities"`) {
		t.Errorf("expected column aliases in ORDER BY completion, got: %s", comp)
	}

	refs, _ := json.Marshal(find(res, 6)["result"])
	// Should find both `AS total_cities` and `ORDER BY ..., total_cities`
	if strings.Count(string(refs), "city_repository.go") < 2 {
		t.Errorf("expected >= 2 references for total_cities, got: %s", refs)
	}
}

func TestRawSQLCTEAndSubqueryAliases(t *testing.T) {
	root, uri, text := fixture(t)
	extra := `
func cteAndSubqueryTest() {
	q1 := ` + "`" + `
		WITH city_cte AS (
			SELECT ct.id, ct.name AS city_name, p.name AS province_name
			FROM ref_geo_cities ct
			JOIN ref_geo_provinces p ON p.id = ct.province_id
		)
		SELECT cc.city_name, cc.bad_col
		FROM city_cte cc
		WHERE cc.
	` + "`" + `
	q2 := ` + "`" + `
		SELECT sub.city_name
		FROM (SELECT ct.id, ct.name AS city_name FROM ref_geo_cities ct) sub
		WHERE sub.
	` + "`" + `
	_, _ = q1, q2
}
`
	doc := text + extra

	ccCompPos := posAfter(doc, "WHERE cc.", 0)
	ccHoverPos := posAfter(doc, "SELECT cc.city_name", -len("city_name")+2)
	subCompPos := posAfter(doc, "WHERE sub.", 0)

	res := run(t, init1(root), open(uri, doc),
		req(2, "textDocument/completion", uri, ccCompPos),
		req(3, "textDocument/hover", uri, ccHoverPos),
		req(4, "textDocument/completion", uri, subCompPos),
		map[string]any{"method": "exit"},
	)

	compCC, _ := json.Marshal(find(res, 2)["result"])
	for _, want := range []string{`"label":"id"`, `"label":"city_name"`, `"label":"province_name"`} {
		if !strings.Contains(string(compCC), want) {
			t.Errorf("expected %s in CTE completion, got: %s", want, compCC)
		}
	}

	hoverCC, _ := json.Marshal(find(res, 3)["result"])
	if !strings.Contains(string(hoverCC), "Alias for") || !strings.Contains(string(hoverCC), "ref_geo_cities.name") {
		t.Errorf("expected CTE aliased column hover, got: %s", hoverCC)
	}

	compSub, _ := json.Marshal(find(res, 4)["result"])
	if !strings.Contains(string(compSub), `"label":"city_name"`) {
		t.Errorf("expected city_name in subquery completion, got: %s", compSub)
	}

	var foundDiag bool
	for _, r := range res {
		if r["method"] == "textDocument/publishDiagnostics" {
			b, _ := json.Marshal(r["params"])
			if strings.Contains(string(b), "Column 'bad_col' does not exist on table 'city_cte'") {
				foundDiag = true
			}
		}
	}
	if !foundDiag {
		t.Error("expected unknown column diagnostic on CTE alias cc.bad_col")
	}
}

func TestRawSQLTableAliasReuseAcrossQueriesInSameFunc(t *testing.T) {
	root, uri, text := fixture(t)
	extra := `
func reuseAliasTest() {
	q1 := ` + "`SELECT x.name FROM ref_geo_cities x`" + `
	q2 := ` + "`SELECT x.country_id FROM ref_geo_provinces x`" + `
	_, _ = q1, q2
}
`
	doc := text + extra
	pos := posAfter(doc, "SELECT x.country_id", -len("country_id")+2)
	res := run(t, init1(root), open(uri, doc),
		req(2, "textDocument/hover", uri, pos),
		map[string]any{"method": "exit"},
	)
	h, _ := json.Marshal(find(res, 2)["result"])
	if !strings.Contains(string(h), "RefGeoProvince.CountryID") {
		t.Errorf("expected x.country_id to resolve to RefGeoProvince even when x is reused in q1, got: %s", h)
	}
}

func TestORMAliasedTableAndColumns(t *testing.T) {
	root, uri, text := fixture(t)
	extra := `
func ormAliasTest() {
	var rows []any
	_ = facades.Orm().Query().
		Table("ref_geo_cities AS ct").
		Join("LEFT JOIN ref_geo_provinces p ON p.id = ct.province_id").
		Select("ct.id", "ct.name AS city_name", "p.name AS province_name", "p.bad_col AS oops").
		Where("p.country_id = ?", 1).
		Find(&rows)
}
`
	doc := text + extra
	hoverPos := posAfter(doc, `"p.name AS province_name"`, -len(` AS province_name"`)-2)
	compPos := posAfter(doc, `Where("p.country_id`, -len("country_id"))

	res := run(t, init1(root), open(uri, doc),
		req(2, "textDocument/hover", uri, hoverPos),
		req(3, "textDocument/completion", uri, compPos),
		map[string]any{"method": "exit"},
	)

	h, _ := json.Marshal(find(res, 2)["result"])
	if !strings.Contains(string(h), "RefGeoProvince.Name") {
		t.Errorf("expected ORM aliased column hover for p.name, got: %s", h)
	}

	comp, _ := json.Marshal(find(res, 3)["result"])
	if !strings.Contains(string(comp), `"label":"country_id"`) {
		t.Errorf("expected ORM joined alias p. completion, got: %s", comp)
	}

	var foundBad, falsePos bool
	for _, r := range res {
		if r["method"] == "textDocument/publishDiagnostics" {
			b, _ := json.Marshal(r["params"])
			if strings.Contains(string(b), "Column 'bad_col' does not exist on table 'ref_geo_provinces'") {
				foundBad = true
			}
			if strings.Contains(string(b), "city_name") || strings.Contains(string(b), "province_name") {
				falsePos = true
			}
		}
	}
	if !foundBad {
		t.Error("expected unknown column diagnostic for p.bad_col AS oops")
	}
	if falsePos {
		t.Error("unexpected diagnostic on valid aliased columns city_name/province_name")
	}
}
