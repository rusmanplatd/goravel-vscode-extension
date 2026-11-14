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

func TestP4LiveMigrationsObserversTemplatesGrpcAndSwagger(t *testing.T) {
	root := t.TempDir()

	// 1. Model + Scope (P4.2)
	modelPath := writeTempFile(t, root, "app/models/user.go", `package models
import "github.com/goravel/framework/contracts/database/orm"
type User struct {
	ID   uint   `+"`gorm:\"column:id\"`"+`
	Name string `+"`gorm:\"column:name\"`"+`
}
func (*User) TableName() string { return "users" }
func ScopeActive(q orm.Query) orm.Query {
	return q.Where("active", true)
}
`)

	// 2. Observer + Registration (P4.2)
	writeTempFile(t, root, "app/observers/user_observer.go", `package observers
import "github.com/goravel/framework/contracts/database/orm"
type UserObserver struct{}
func (u *UserObserver) Creating(event orm.Event) error { return nil }
func (u *UserObserver) Created(event orm.Event) error  { return nil }
func (u *UserObserver) Deleted(event orm.Event) error  { return nil }
`)
	writeTempFile(t, root, "app/providers/event_service_provider.go", `package providers
import "github.com/goravel/framework/facades"
func boot() {
	facades.Orm().Observe(models.User{}, &observers.UserObserver{})
}
`)

	// 3. Migrations (P4.1)
	writeTempFile(t, root, "database/migrations/20260101000001_create_users.go", `package migrations
type M20260101000001CreateUsers struct{}
func (r *M20260101000001CreateUsers) Signature() string { return "20260101000001_create_users" }
`)
	writeTempFile(t, root, "database/migrations/20260101000002_add_avatar_to_users.go", `package migrations
type M20260101000002AddAvatar struct{}
func (r *M20260101000002AddAvatar) Signature() string { return "20260101000002_add_avatar_to_users" }
`)

	// 4. Form Request + Controller + View + HTTP Route (P4.3 & P4.5)
	writeTempFile(t, root, "app/http/requests/store_user_request.go", `package requests
type StoreUserRequest struct {
	Name string `+"`form:\"name\" json:\"name\"`"+`
}
`)
	tplPath := writeTempFile(t, root, "resources/views/welcome.tmpl", `<html><body><h1>{{ .title }}</h1><p>{{ . }}</p></body></html>`)
	ctrlPath := writeTempFile(t, root, "app/http/controllers/user_controller.go", `package controllers
import "github.com/goravel/framework/contracts/http"
type UserController struct{}
func NewUserController() *UserController { return &UserController{} }

func (r *UserController) Store(ctx http.Context) http.Response {
	var req requests.StoreUserRequest
	_ = ctx.Request().Bind(&req)
	_ = ctx.Request().Query("source")
	var user models.User
	return ctx.Response().Success().Json(user)
}

func (r *UserController) Welcome(ctx http.Context) http.Response {
	return ctx.Response().View().Make("welcome.tmpl", map[string]any{
		"title":   "Goravel Framework",
		"version": "1.17",
	})
}
`)
	writeTempFile(t, root, "routes/api.go", `package routes
import "github.com/goravel/framework/contracts/route"
import "github.com/goravel/framework/facades"
func Api() {
	userCtrl := controllers.NewUserController()
	facades.Route().Middleware(middleware.Jwt()).Prefix("api").Group(func(r route.Router) {
		r.Post("/users/:id", userCtrl.Store)
		r.Get("/welcome", userCtrl.Welcome)
	})
}
`)

	// 5. gRPC Controller + Proto + routes/grpc.go (P4.4)
	writeTempFile(t, root, "proto/user.proto", `syntax = "proto3";
package proto;
service UserService {
  rpc GetUser (UserRequest) returns (UserResponse);
}
`)
	writeTempFile(t, root, "app/grpc/controllers/user_grpc_controller.go", `package controllers
import "context"
type UserGrpcController struct{}
func NewUserGrpcController() *UserGrpcController { return &UserGrpcController{} }
func (c *UserGrpcController) GetUser(ctx context.Context, req any) (any, error) {
	return nil, nil
}
`)
	writeTempFile(t, root, "routes/grpc.go", `package routes
import "github.com/goravel/framework/facades"
func Grpc() {
	proto.RegisterUserServiceServer(facades.Grpc().Server(), controllers.NewUserGrpcController())
}
`)

	cat := indexer.Build(root)

	// Verify P4.1: Live migration applied vs pending status
	indexer.ApplyLiveMigrationStatus(cat, map[string]int{
		"20260101000001_create_users": 3,
	})
	if len(cat.Migrations) != 2 {
		t.Fatalf("expected 2 migrations, got %d", len(cat.Migrations))
	}
	if cat.Migrations[0].Status != "applied" || cat.Migrations[0].Batch != 3 {
		t.Errorf("expected first migration applied (batch 3), got %+v", cat.Migrations[0])
	}
	if cat.Migrations[1].Status != "pending" || cat.Migrations[1].Batch != 0 {
		t.Errorf("expected second migration pending, got %+v", cat.Migrations[1])
	}

	// Verify P4.2: Observers & Scopes indexing + Model CodeLens
	if len(cat.Observers) != 1 || cat.Observers[0].Model != "User" || len(cat.Observers[0].Hooks) != 3 {
		t.Errorf("expected UserObserver with 3 hooks linked to User, got %+v", cat.Observers)
	}
	if len(cat.Scopes) != 1 || cat.Scopes[0].Name != "ScopeActive" || cat.Scopes[0].Model != "User" {
		t.Errorf("expected ScopeActive linked to User, got %+v", cat.Scopes)
	}
	var out bytes.Buffer
	srv := New(strings.NewReader(""), &out)
	srv.root = root
	srv.cat = cat
	modelURI := pathToURI(modelPath)
	rawLens, _ := json.Marshal(map[string]any{
		"textDocument": map[string]any{"uri": modelURI},
	})
	lenses, _ := srv.codeLens(rawLens).([]map[string]any)
	hasObsLens, hasScopeLens := false, false
	for _, l := range lenses {
		cmd, _ := l["command"].(map[string]any)
		title, _ := cmd["title"].(string)
		if strings.Contains(title, "Observer: UserObserver (Creating, Created, Deleted)") {
			hasObsLens = true
		}
		if strings.Contains(title, "Scope(s): ScopeActive") {
			hasScopeLens = true
		}
	}
	if !hasObsLens || !hasScopeLens {
		t.Errorf("expected Observer and Scope CodeLens on User model, got %+v", lenses)
	}

	// Verify P4.3: Template variable completion, hover, and definition in resources/views/welcome.tmpl
	tplSrc, _ := os.ReadFile(tplPath)
	tplURI := pathToURI(tplPath)
	srv.docs[tplURI] = string(tplSrc)
	dotOff := strings.Index(string(tplSrc), "{{ . }}") + 4
	compItems := framework.TemplateCompletions(tplPath, string(tplSrc), cat, dotOff)
	if len(compItems) < 2 {
		t.Errorf("expected template variable completions (title, version), got %+v", compItems)
	}
	titleOff := strings.Index(string(tplSrc), ".title") + 2
	hov := framework.TemplateHover(tplPath, string(tplSrc), cat, titleOff)
	if hov == nil || !strings.Contains(hov.Markdown, "UserController.Welcome") {
		t.Errorf("expected template hover showing controller origin UserController.Welcome, got %+v", hov)
	}
	defs := framework.TemplateDefinition(tplPath, string(tplSrc), cat, titleOff)
	if len(defs) == 0 || filepath.Clean(defs[0].File) != filepath.Clean(ctrlPath) {
		t.Errorf("expected template definition jumping to controller file, got %+v", defs)
	}

	// Verify P4.4: gRPC routes indexed alongside HTTP routes
	foundGrpc := false
	for _, r := range cat.Routes {
		if r.Method == "gRPC" && r.Path == "/UserService/GetUser" && r.Controller == "UserGrpcController" {
			foundGrpc = true
		}
	}
	if !foundGrpc {
		t.Errorf("expected gRPC route /UserService/GetUser for UserGrpcController, got %+v", cat.Routes)
	}

	// Verify P4.5: Complete Swagger / OpenAPI Code Action generation on controller method
	ctrlSrc, _ := os.ReadFile(ctrlPath)
	storeOff := strings.Index(string(ctrlSrc), "Store(ctx")
	cActions := framework.ControllerCodeActions(ctrlPath, string(ctrlSrc), cat, storeOff)
	if len(cActions) == 0 || len(cActions[0].Edits) == 0 {
		t.Fatalf("expected Swagger generation CodeAction on Store, got %+v", cActions)
	}
	swagText := cActions[0].Edits[0].NewText
	for _, expected := range []string{
		"@Summary Store User",
		"@Tags User",
		"@Security BearerAuth",
		`@Param id path string true "id"`,
		`@Param source query string false "source"`,
		`@Param request body requests.StoreUserRequest true "Request payload"`,
		"@Success 201 {object} models.User",
		"@Router /api/users/{id} [post]",
	} {
		if !strings.Contains(swagText, expected) {
			t.Errorf("expected Swagger annotation to contain %q, got:\n%s", expected, swagText)
		}
	}
}
