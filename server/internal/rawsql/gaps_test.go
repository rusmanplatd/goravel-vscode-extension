package rawsql

import (
	"strings"
	"testing"

	"github.com/rusmanplatd/goravel-lsp/internal/indexer"
)

func testCatalog() *indexer.Catalog {
	cat := indexer.NewCatalog()
	users := &indexer.Table{
		Name:  "users",
		Model: "User",
		Columns: []*indexer.Column{
			{Name: "id", GoName: "ID", GoType: "uint", SQLType: "BIGINT"},
			{Name: "email", GoName: "Email", GoType: "string", SQLType: "VARCHAR"},
			{Name: "name", GoName: "Name", GoType: "string", SQLType: "VARCHAR"},
		},
	}
	cat.Tables["users"] = users
	cat.Models["User"] = users
	return cat
}

func TestSQLSyntaxAndUnknownTableAndUnqualifiedCols(t *testing.T) {
	cat := testCatalog()

	// 1. Dangling comma & unclosed paren
	d1 := &Doc{Text: "SELECT id, FROM users WHERE (email = $1"}
	issues1 := d1.CheckWithCatalog(Scope{"users": cat.Table("users")}, cat)
	var msgs1 []string
	for _, is := range issues1 {
		msgs1 = append(msgs1, is.Message)
	}
	joined1 := strings.Join(msgs1, " | ")
	if !strings.Contains(joined1, "dangling comma") {
		t.Fatalf("expected dangling comma diagnostic, got: %s", joined1)
	}
	if !strings.Contains(joined1, "unclosed parenthesis") {
		t.Fatalf("expected unclosed parenthesis diagnostic, got: %s", joined1)
	}

	// 2. Unknown table
	d2 := &Doc{Text: "SELECT id FROM nonexistent_tbl"}
	issues2 := d2.CheckWithCatalog(Scope{}, cat)
	if len(issues2) == 0 || !strings.Contains(issues2[0].Message, "Unknown table 'nonexistent_tbl'") {
		t.Fatalf("expected unknown table diagnostic, got: %+v", issues2)
	}

	// 3. Unknown unqualified column
	d3 := &Doc{Text: "SELECT id, bogus_col FROM users WHERE emial = $1"}
	sc3 := Scope{"users": cat.Table("users")}
	issues3 := d3.CheckWithCatalog(sc3, cat)
	var msgs3 []string
	for _, is := range issues3 {
		msgs3 = append(msgs3, is.Message)
	}
	joined3 := strings.Join(msgs3, " | ")
	if !strings.Contains(joined3, "bogus_col") || !strings.Contains(joined3, "emial") {
		t.Fatalf("expected unknown unqualified column diagnostics for bogus_col and emial, got: %s", joined3)
	}
}

func TestQueryVarHeuristicAndSprintfAndLikePercent(t *testing.T) {
	cat := testCatalog()
	src := `package repo

func demo() {
	userQuery := "id, name FROM users"
	_ = userQuery

	q := fmt.Sprintf("SELECT id FROM users WHERE id = %d AND name = '%s'", 1, "a")
	_ = q

	facades.DB().Select(&rows, "SELECT id FROM users WHERE name LIKE '%foo%' AND id = $1", 1, 2)
}
`
	fi := Extract("repo.go", []byte(src), cat)

	foundUserQuery := false
	foundSprintf := false
	for _, d := range fi.Docs {
		if strings.Contains(d.Text, "id, name FROM users") {
			foundUserQuery = true
		}
		if strings.Contains(d.Text, "WHERE id = $1 AND name = '$2'") {
			foundSprintf = true
		}
	}
	if !foundUserQuery {
		t.Fatalf("expected userQuery to be extracted via *Query* variable heuristic")
	}
	if !foundSprintf {
		t.Fatalf("expected fmt.Sprintf verbs to be normalized to $1/$2")
	}

	diags := Analyze(fi, cat)
	foundMismatch := false
	for _, dg := range diags {
		if strings.Contains(dg.Message, "1 placeholder(s) but 2 argument(s)") {
			foundMismatch = true
		}
	}
	if !foundMismatch {
		t.Fatalf("expected placeholder count mismatch despite LIKE '%%foo%%', got: %+v", diags)
	}
}

