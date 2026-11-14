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

func TestP5ViewFacadeAndTemplateLSP(t *testing.T) {
	root := t.TempDir()

	// 1. User model for nested struct field completion/definition in .tmpl
	modelPath := writeTempFile(t, root, "app/models/user.go", `package models
type User struct {
	ID    uint   `+"`gorm:\"column:id\"`"+`
	Email string `+"`gorm:\"column:email\"`"+`
}
func (*User) TableName() string { return "users" }
`)

	// 2. Package ServiceProvider registering LoadViewsFrom + View().Share
	providerPath := writeTempFile(t, root, "packages/viewtest/service_provider.go", `package viewtest
import (
	"github.com/goravel/framework/contracts/foundation"
	"github.com/goravel/framework/support/path"
	"goravel/app/facades"
)
type ServiceProvider struct{}
func (r *ServiceProvider) Boot(app foundation.Application) {
	facades.View().LoadViewsFrom(path.Base("packages", "viewtest", "views"))
	facades.View().Share("site_name", "Goravel Portal")
}
`)
	pkgOnlyPath := writeTempFile(t, root, "packages/viewtest/views/package_only.tmpl", `<p>package-only: {{ .name }} - {{ .site_name }}</p>`)
	pkgSharedPath := writeTempFile(t, root, "packages/viewtest/views/shared.tmpl", `<p>package-shared: {{ .name }}</p>`)

	// 3. App views (including override of shared.tmpl and nested admin/index.tmpl -> admin/header.tmpl)
	appSharedPath := writeTempFile(t, root, "resources/views/shared.tmpl", `<p>app-shared: {{ .name }}</p>`)
	headerPath := writeTempFile(t, root, "resources/views/admin/header.tmpl", `{{ define "admin/header.tmpl" }}
<header>{{ .header_title }} | {{ .site_name }} | {{ .user.Email }}</header>
{{ end }}`)
	adminIdxPath := writeTempFile(t, root, "resources/views/admin/index.tmpl", `{{ define "admin/index.tmpl" }}
{{ template "admin/header.tmpl" . }}
{{ range $idx, $u := .users }}
  <p>{{ $idx }}: {{ .headline }}</p>
{{ end }}
{{ end }}`)

	// 4. Controller & Routes using Make, First, Exists, Shared
	ctrlPath := writeTempFile(t, root, "app/http/controllers/view_controller.go", `package controllers
import (
	"github.com/goravel/framework/contracts/http"
	"goravel/app/facades"
	"goravel/app/models"
)
type ViewController struct{}
func (c *ViewController) Admin(ctx http.Context) http.Response {
	var user models.User
	_ = facades.View().Exists("package_only.tmpl")
	_ = facades.View().Exists("shared.tmpl")
	_ = facades.View().Shared("site_name")
	return ctx.Response().View().Make("admin/index.tmpl", map[string]any{
		"headline":     "Dashboard",
		"header_title": "UsedInHeaderPartial",
		"user":         user,
		"users":        []models.User{user},
	})
}
func (c *ViewController) Fallback(ctx http.Context) http.Response {
	return ctx.Response().View().First([]string{"nonexistent_custom.tmpl", "package_only.tmpl"}, map[string]any{
		"name": "Goravel",
	})
}
func (c *ViewController) Missing(ctx http.Context) http.Response {
	return ctx.Response().View().Make("admin/new_report.tmpl", map[string]any{
		"report_name": "Q4",
	})
}
`)

	cat := indexer.Build(root)

	// Verify 1: LoadViewsFrom & App Override Precedence
	pkgVi := cat.LookupView("package_only.tmpl")
	if pkgVi == nil || pkgVi.Source != "package" || filepath.Clean(pkgVi.Loc.File) != filepath.Clean(pkgOnlyPath) {
		t.Fatalf("expected package_only.tmpl indexed from package view dir, got %+v", pkgVi)
	}
	sharedVi := cat.LookupView("shared.tmpl")
	if sharedVi == nil || sharedVi.Source != "app" || filepath.Clean(sharedVi.Loc.File) != filepath.Clean(appSharedPath) || len(sharedVi.PackageLocs) != 1 || filepath.Clean(sharedVi.PackageLocs[0].File) != filepath.Clean(pkgSharedPath) {
		t.Fatalf("expected shared.tmpl to resolve to app view and record package fallback, got %+v", sharedVi)
	}

	// Verify 2: Diagnostics on controller (no false-positive on package_only.tmpl, First fallback, or header_title used in partial; warning only on admin/new_report.tmpl)
	ctrlBytes, _ := os.ReadFile(ctrlPath)
	ctrlSrc := string(ctrlBytes)
	vDiags := framework.ViewDiagnostics(ctrlPath, ctrlSrc, cat)
	for _, d := range vDiags {
		if strings.Contains(d.Message, "package_only.tmpl") || strings.Contains(d.Message, "First") || strings.Contains(d.Message, "header_title") {
			t.Errorf("unexpected diagnostic: %+v", d)
		}
	}
	hasMissingReportDiag := false
	for _, d := range vDiags {
		if strings.Contains(d.Message, "admin/new_report.tmpl") {
			hasMissingReportDiag = true
		}
	}
	if !hasMissingReportDiag {
		t.Errorf("expected missing view diagnostic for admin/new_report.tmpl, got %+v", vDiags)
	}

	// Verify 3: Quick Fix to scaffold missing view template from View().Make("admin/new_report.tmpl", ...)
	missingOff := strings.Index(ctrlSrc, "admin/new_report.tmpl") + 5
	vActions := framework.ViewCodeActions(ctrlPath, ctrlSrc, cat, missingOff)
	if len(vActions) == 0 || len(vActions[0].Edits) == 0 || !strings.Contains(vActions[0].Edits[0].NewText, "{{ .report_name }}") {
		t.Errorf("expected Quick Fix to create admin/new_report.tmpl with report_name placeholder, got %+v", vActions)
	}

	// Verify 4: Go ViewLocations on Exists("shared.tmpl") returns both app view and overridden package view
	existsSharedOff := strings.Index(ctrlSrc, `"shared.tmpl"`) + 3
	locs := framework.ViewLocations(ctrlPath, ctrlSrc, cat, existsSharedOff)
	if len(locs) != 2 {
		t.Errorf("expected 2 locations (app + overridden package) for shared.tmpl, got %+v", locs)
	}

	// Verify 5: .tmpl Completions (partial include, nested struct field, inherited var, shared var, local $var)
	headerBytes, _ := os.ReadFile(headerPath)
	headerSrc := string(headerBytes)

	// 5a: Nested struct field completion on `.user.`
	userFieldOff := strings.Index(headerSrc, ".user.Email") + len(".user.")
	fieldItems := framework.TemplateCompletions(headerPath, headerSrc, cat, userFieldOff)
	hasEmailField := false
	for _, it := range fieldItems {
		if it.Label == "Email" {
			hasEmailField = true
		}
	}
	if !hasEmailField {
		t.Errorf("expected nested struct field completion 'Email' on .user., got %+v", fieldItems)
	}

	// 5b: Go-to-Definition on `.user.Email` jumps to User struct in app/models/user.go
	emailDefs := framework.TemplateDefinition(headerPath, headerSrc, cat, userFieldOff+2)
	if len(emailDefs) == 0 || filepath.Clean(emailDefs[0].File) != filepath.Clean(modelPath) {
		t.Errorf("expected definition on .user.Email to jump to %s, got %+v", modelPath, emailDefs)
	}

	// 5c: Go-to-Definition on shared var `.site_name` jumps to facades.View().Share in service_provider.go
	siteNameOff := strings.Index(headerSrc, ".site_name") + 3
	siteDefs := framework.TemplateDefinition(headerPath, headerSrc, cat, siteNameOff)
	if len(siteDefs) == 0 || filepath.Clean(siteDefs[0].File) != filepath.Clean(providerPath) {
		t.Errorf("expected definition on .site_name to jump to %s, got %+v", providerPath, siteDefs)
	}

	// 5d: Go-to-Definition on `{{ template "admin/header.tmpl" . }}` in admin/index.tmpl jumps to header.tmpl
	adminBytes, _ := os.ReadFile(adminIdxPath)
	adminSrc := string(adminBytes)
	incOff := strings.Index(adminSrc, `"admin/header.tmpl"`) + 4
	incDefs := framework.TemplateDefinition(adminIdxPath, adminSrc, cat, incOff)
	if len(incDefs) == 0 || filepath.Clean(incDefs[0].File) != filepath.Clean(headerPath) {
		t.Errorf("expected definition on template include to jump to %s, got %+v", headerPath, incDefs)
	}

	// 5e: Local variable $idx hover & definition in admin/index.tmpl
	idxUseOff := strings.LastIndex(adminSrc, "$idx") + 2
	idxHov := framework.TemplateHover(adminIdxPath, adminSrc, cat, idxUseOff)
	if idxHov == nil || !strings.Contains(idxHov.Markdown, "Local Template Variable") {
		t.Errorf("expected local template variable hover on $idx, got %+v", idxHov)
	}

	// Verify 6: Template Diagnostics (unclosed block & missing included partial)
	brokenTpl := "{{ define \"broken.tmpl\" }}\n{{ template \"missing_partial.tmpl\" . }}\n{{ if .cond }}\n<p>unclosed</p>\n{{ end }}"
	tDiags := framework.TemplateDiagnostics(filepath.Join(root, "resources/views/broken.tmpl"), brokenTpl, cat)
	hasMissingPartial, hasUnclosedBlock := false, false
	for _, d := range tDiags {
		if strings.Contains(d.Message, "missing_partial.tmpl") {
			hasMissingPartial = true
		}
		if strings.Contains(d.Message, "Unclosed") {
			hasUnclosedBlock = true
		}
	}
	if !hasMissingPartial || !hasUnclosedBlock {
		t.Errorf("expected missing partial and unclosed block diagnostics, got %+v", tDiags)
	}

	// Verify 7: Template CodeLens (Overrides package view on shared.tmpl, Included by on admin/header.tmpl)
	sharedLenses := framework.TemplateCodeLenses(appSharedPath, cat)
	hasOverrideLens := false
	for _, l := range sharedLenses {
		if strings.Contains(l.Title, "Overrides package view") {
			hasOverrideLens = true
		}
	}
	if !hasOverrideLens {
		t.Errorf("expected 'Overrides package view' CodeLens on resources/views/shared.tmpl, got %+v", sharedLenses)
	}

	headerLenses := framework.TemplateCodeLenses(headerPath, cat)
	hasIncludedByLens := false
	for _, l := range headerLenses {
		if strings.Contains(l.Title, "Included by admin/index.tmpl") {
			hasIncludedByLens = true
		}
	}
	if !hasIncludedByLens {
		t.Errorf("expected 'Included by admin/index.tmpl' CodeLens on admin/header.tmpl, got %+v", headerLenses)
	}

	// Verify 8: Cross-file Rename of template variable `.headline` across controller and .tmpl
	var out bytes.Buffer
	srv := New(strings.NewReader(""), &out)
	srv.root = root
	srv.cat = cat
	adminURI := pathToURI(adminIdxPath)
	srv.docs[adminURI] = adminSrc
	headlineOff := strings.Index(adminSrc, ".headline") + 3
	pos := offsetToPos(adminSrc, headlineOff)
	rawRename, _ := json.Marshal(map[string]any{
		"textDocument": map[string]any{"uri": adminURI},
		"position":     pos,
		"newName":      "page_title",
	})
	edit := srv.rename(rawRename)
	editMap, ok := edit.(map[string]any)
	if !ok {
		t.Fatalf("expected rename workspace edit map, got %+v", edit)
	}
	changes, _ := editMap["changes"].(map[string][]map[string]any)
	if len(changes[adminURI]) == 0 || len(changes[pathToURI(ctrlPath)]) == 0 {
		t.Errorf("expected cross-file rename edits in both admin/index.tmpl and view_controller.go, got %+v", changes)
	}
}
