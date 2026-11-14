package lsp

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rusmanplatd/goravel-lsp/internal/framework"
	"github.com/rusmanplatd/goravel-lsp/internal/indexer"
	"github.com/rusmanplatd/goravel-lsp/internal/orm"
)

func writeTempFile(t *testing.T, root, rel, content string) string {
	t.Helper()
	full := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return full
}

func TestAlterMigrationAndTranslationsAndGates(t *testing.T) {
	root := t.TempDir()

	writeTempFile(t, root, "app/models/user.go", `package models
type User struct {
	ID    uint   `+"`gorm:\"column:id\"`"+`
	Email string `+"`gorm:\"column:email\"`"+`
	Bio   string `+"`gorm:\"column:bio\"`"+`
	Posts []Post `+"`gorm:\"foreignKey:user_id;references:id\"`"+`
}
func (*User) TableName() string { return "users" }
`)
	writeTempFile(t, root, "app/models/post.go", `package models
type Post struct {
	ID     uint   `+"`gorm:\"column:id\"`"+`
	UserID uint   `+"`gorm:\"column:user_id\"`"+`
	Title  string `+"`gorm:\"column:title\"`"+`
}
func (*Post) TableName() string { return "posts" }
`)

	// 1. Initial create migration + 2. Alter migration
	writeTempFile(t, root, "database/migrations/20260101000001_create_users.go", `package migrations
import "github.com/goravel/framework/contracts/database/schema"
import "github.com/goravel/framework/facades"
type M20260101000001CreateUsers struct{}
func (r *M20260101000001CreateUsers) Signature() string { return "20260101000001_create_users" }
func (r *M20260101000001CreateUsers) Up() error {
	return facades.Schema().Create("users", func(table schema.Blueprint) {
		table.ID("id")
		table.String("old_email")
		table.String("temp_col")
	})
}
`)
	writeTempFile(t, root, "database/migrations/20260101000002_alter_users.go", `package migrations
import "github.com/goravel/framework/contracts/database/schema"
import "github.com/goravel/framework/facades"
type M20260101000002AlterUsers struct{}
func (r *M20260101000002AlterUsers) Signature() string { return "20260101000002_alter_users" }
func (r *M20260101000002AlterUsers) Up() error {
	return facades.Schema().Table("users", func(table schema.Blueprint) {
		table.RenameColumn("old_email", "email")
		table.DropColumn("temp_col")
		table.String("phone").Nullable()
	})
}
`)
	writeTempFile(t, root, "bootstrap/migrations.go", `package bootstrap
import "github.com/goravel/framework/contracts/database/schema"
func Migrations() []schema.Migration {
	return []schema.Migration{
		&migrations.M20260101000001CreateUsers{},
	}
}
`)

	// Translations
	writeTempFile(t, root, "lang/en.json", `{
  "messages": {
    "welcome": "Welcome to Goravel"
  },
  "auth.failed": "Invalid credentials"
}`)

	// Gate ability
	writeTempFile(t, root, "app/providers/auth_service_provider.go", `package providers
import "github.com/goravel/framework/facades"
func boot() {
	facades.Gate().Define("update-post", nil)
}
`)

	// Events & Listeners
	writeTempFile(t, root, "app/events/user_registered.go", `package events
type UserRegistered struct{}
`)
	writeTempFile(t, root, "app/listeners/send_welcome_email.go", `package listeners
type SendWelcomeEmail struct{}
`)
	writeTempFile(t, root, "app/providers/event_service_provider.go", `package providers
import "github.com/goravel/framework/contracts/event"
func listen() map[event.Event][]event.Listener {
	return map[event.Event][]event.Listener{
		&events.UserRegistered{}: {
			&listeners.SendWelcomeEmail{},
		},
	}
}
`)

	cat := indexer.Build(root)

	users := cat.Table("users")
	if users == nil {
		t.Fatalf("expected users table")
	}
	if users.Column("email") == nil {
		t.Errorf("expected old_email to be renamed to email")
	}
	if users.Column("temp_col") != nil {
		t.Errorf("expected temp_col to be dropped by DropColumn")
	}
	if users.Column("phone") == nil {
		t.Errorf("expected phone column added by Schema().Table")
	}

	// Translations & Gates
	if _, ok := cat.Translations["messages.welcome"]; !ok {
		t.Errorf("expected nested translation key messages.welcome")
	}
	if _, ok := cat.Translations["auth.failed"]; !ok {
		t.Errorf("expected flat translation key auth.failed")
	}
	if _, ok := cat.Gates["update-post"]; !ok {
		t.Errorf("expected Gate ability update-post")
	}

	// Events & Listeners linkage
	if len(cat.Events) != 1 || len(cat.Events[0].Listeners) != 1 || cat.Events[0].Listeners[0] != "SendWelcomeEmail" {
		t.Errorf("expected UserRegistered linked to SendWelcomeEmail, got %+v", cat.Events)
	}

	// Unregistered migration auto-registration action
	if len(cat.Unregistered) == 0 {
		t.Fatalf("expected M20260101000002AlterUsers to be detected as unregistered")
	}
	act, ok := framework.ResolveBootstrapRegistrationAction(cat, cat.Unregistered[0])
	if !ok || len(act.Edits) == 0 || !strings.Contains(act.Edits[0].NewText, "M20260101000002AlterUsers") {
		t.Errorf("expected auto-registration edit for M20260101000002AlterUsers, got %+v", act)
	}

	// Reverse Model -> Migration Quick Fix (Bio is on User model, not in migration)
	userModelSrc, _ := os.ReadFile(filepath.Join(root, "app/models/user.go"))
	actions := framework.DriftCodeActions(filepath.Join(root, "app/models/user.go"), string(userModelSrc), cat, 25)
	foundReverse := false
	for _, a := range actions {
		if strings.Contains(a.Title, "Add 1 missing column(s) to migration") && len(a.Edits) > 0 && a.Edits[0].File != "" && strings.Contains(a.Edits[0].NewText, `"bio"`) {
			foundReverse = true
		}
	}
	if !foundReverse {
		t.Errorf("expected reverse Model -> Migration Quick Fix for `bio` column, got %+v", actions)
	}
}

func TestORMJoinMapWhereWithCallbackAndLangGate(t *testing.T) {
	root := t.TempDir()
	writeTempFile(t, root, "app/models/user.go", `package models
type User struct {
	ID    uint   `+"`gorm:\"column:id\"`"+`
	Email string `+"`gorm:\"column:email\"`"+`
	Posts []Post `+"`gorm:\"foreignKey:user_id;references:id\"`"+`
}
func (*User) TableName() string { return "users" }
`)
	writeTempFile(t, root, "app/models/post.go", `package models
type Post struct {
	ID     uint   `+"`gorm:\"column:id\"`"+`
	UserID uint   `+"`gorm:\"column:user_id\"`"+`
	Title  string `+"`gorm:\"column:title\"`"+`
}
func (*Post) TableName() string { return "posts" }
`)
	writeTempFile(t, root, "lang/en.json", `{"messages": {"hello": "Hello World"}}`)
	writeTempFile(t, root, "app/providers/auth.go", `package providers
import "github.com/goravel/framework/facades"
func boot() { facades.Gate().Define("edit-post", nil) }
`)

	cat := indexer.Build(root)

	src := `package controllers
import "github.com/goravel/framework/facades"
func Index(ctx http.Context) {
	var user models.User
	facades.Orm().Query().Model(&user).
		LeftJoin("LEFT JOIN posts ON posts.user_id = users.id").
		Where(map[string]any{"bad_col": 1}).
		With("Posts", func(q orm.Query) orm.Query {
			return q.Where("bad_post_col", "x")
		}).
		Get(&user)
	_ = facades.Lang(ctx).Get("messages.hello")
	_ = facades.Lang(ctx).Get("messages.missing_key")
	_ = facades.Gate().Allows("unknown-ability")
}
`
	f := orm.AnalyzeFile("controllers.go", []byte(src), cat)
	diags := f.Diagnostics(cat)

	hasBadCol := false
	hasBadPostCol := false
	hasMissingLang := false
	hasUnknownGate := false
	for _, d := range diags {
		if strings.Contains(d.Message, "bad_col") && strings.Contains(d.Message, "users") {
			hasBadCol = true
		}
		if strings.Contains(d.Message, "bad_post_col") && strings.Contains(d.Message, "posts") {
			hasBadPostCol = true
		}
		if strings.Contains(d.Message, "messages.missing_key") {
			hasMissingLang = true
		}
		if strings.Contains(d.Message, "unknown-ability") {
			hasUnknownGate = true
		}
	}
	if !hasBadCol {
		t.Errorf("expected diagnostic on map-based Where key `bad_col` against `users`, got %+v", diags)
	}
	if !hasBadPostCol {
		t.Errorf("expected diagnostic on nested .With(\"Posts\", func(q)) key `bad_post_col` against `posts`, got %+v", diags)
	}
	if !hasMissingLang {
		t.Errorf("expected diagnostic on missing translation key `messages.missing_key`, got %+v", diags)
	}
	if !hasUnknownGate {
		t.Errorf("expected diagnostic on unknown Gate ability `unknown-ability`, got %+v", diags)
	}

	hints := f.InlayHints(cat)
	foundLangHint := false
	for _, h := range hints {
		if strings.Contains(h.Label, "Hello World") {
			foundLangHint = true
		}
	}
	if !foundLangHint {
		t.Errorf("expected inlay hint showing translation preview 'Hello World', got %+v", hints)
	}
}

func TestSanitizeSQLPlaceholdersAndERDiagramAndRename(t *testing.T) {
	q := "SELECT * FROM users WHERE id = $1 AND status = ? AND code = @p1"
	sanitized := indexer.SanitizeSQLPlaceholders(q)
	if strings.Contains(sanitized, "$1") || strings.Contains(sanitized, "?") || strings.Contains(sanitized, "@p1") {
		t.Errorf("expected placeholders sanitized to NULL, got %s", sanitized)
	}

	root := t.TempDir()
	writeTempFile(t, root, "app/models/user.go", `package models
type User struct {
	ID uint `+"`gorm:\"column:id\"`"+`
}
func (*User) TableName() string { return "users" }
`)
	writeTempFile(t, root, "config/app.go", `package config
import "github.com/goravel/framework/facades"
func init() {
	facades.Config().Add("app", map[string]any{
		"name": "Goravel",
	})
}
`)
	ctrlPath := writeTempFile(t, root, "app/http/controllers/home.go", `package controllers
import "github.com/goravel/framework/facades"
func Home() string {
	return facades.Config().GetString("app.name")
}
`)

	var out bytes.Buffer
	srv := New(strings.NewReader(""), &out)
	srv.root = root
	srv.cat = indexer.Build(root)

	// Test goravel/erDiagram
	srv.dispatch(&message{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`1`),
		Method:  "goravel/erDiagram",
	})
	if !strings.Contains(out.String(), "erDiagram") || !strings.Contains(out.String(), "users") {
		t.Errorf("expected Mermaid erDiagram in response, got %s", out.String())
	}

	// Test textDocument/rename on config key "app.name"
	ctrlSrc, _ := os.ReadFile(ctrlPath)
	uri := pathToURI(ctrlPath)
	srv.docs[uri] = string(ctrlSrc)
	off := strings.Index(string(ctrlSrc), "app.name") + 4
	pos := offsetToPos(string(ctrlSrc), off)
	rawParams, _ := json.Marshal(map[string]any{
		"textDocument": map[string]any{"uri": uri},
		"position":     pos,
		"newName":      "title",
	})
	edit := srv.rename(rawParams)
	editMap, ok := edit.(map[string]any)
	if !ok {
		t.Fatalf("expected workspace edit map, got %+v", edit)
	}
	changes, _ := editMap["changes"].(map[string][]map[string]any)
	if len(changes) < 2 {
		t.Errorf("expected workspace rename across config definition and controller usage, got %+v", changes)
	}
}
