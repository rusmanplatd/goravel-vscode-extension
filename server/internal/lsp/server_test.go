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

