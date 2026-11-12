package lsp

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rusmanplatd/goravel-lsp/internal/indexer"
	"github.com/rusmanplatd/goravel-lsp/internal/orm"
)

func setupEnhancementProject(t *testing.T) string {
	t.Helper()
	root := t.TempDir()

	mustWrite := func(rel, content string) {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}

	mustWrite("app/models/city.go", `package models
type City struct {
	ID         string `+"`gorm:\"column:id;primaryKey\"`"+`
	ProvinceID string `+"`gorm:\"column:province_id\"`"+`
	Name       string `+"`gorm:\"column:name\"`"+`
}
func (City) TableName() string { return "cities" }
`)

	mustWrite("config/jwt.go", `package config
import "github.com/goravel/framework/facades"
func init() {
	config := facades.Config()
	config.Add("jwt", map[string]any{
		"secret": config.Env("JWT_SECRET", ""),
	})
}
`)

	mustWrite("app/rules/uppercase.go", `package rules
type Uppercase struct{}
func (r *Uppercase) Signature() string { return "uppercase" }
func (r *Uppercase) Message() string { return "must be uppercase" }
`)

	mustWrite("app/console/commands/sync_geo.go", `package commands
type SyncGeo struct{}
func (r *SyncGeo) Signature() string { return "geo:sync" }
func (r *SyncGeo) Description() string { return "Sync geo data" }
`)

	mustWrite("routes/api.go", `package routes
import (
	"github.com/goravel/framework/facades"
	"app/http/controllers"
)
func Api() {
	cityCtrl := controllers.NewCityController()
	facades.Route().Prefix("api").Group(func(router any) {
		router.Get("/cities/{city_id}", cityCtrl.Show)
	})
}
`)

	mustWrite("app/http/controllers/city_controller.go", `package controllers
import (
	"github.com/goravel/framework/contracts/http"
	"github.com/goravel/framework/facades"
)
type CityController struct{}
func NewCityController() *CityController { return &CityController{} }

// @Router /api/cities/{city_id} [post]
func (c *CityController) Show(ctx http.Context) http.Response {
	_ = facades.Config().GetString("jwt.secret")
	_ = ctx.Request().Route("city_id")
	_ = ctx.Request().Route("wrong_param")
	_, _ = ctx.Request().Validate(map[string]string{
		"province_id": "required|exists:cities,province_id|exists:missing_tbl,id|unique:cities,no_col|uppercase",
	})
	return nil
}
`)

	return root
}

func TestEnhancements(t *testing.T) {
	root := setupEnhancementProject(t)
	cat := indexer.Build(root)

	// 1. Custom Artisan command discovery
	if len(cat.Commands) != 1 || cat.Commands[0].Signature != "geo:sync" {
		t.Fatalf("expected command geo:sync, got %+v", cat.Commands)
	}

	// 2. Route params extraction
	routes := cat.RoutesForHandler("CityController", "Show")
	if len(routes) != 1 || len(routes[0].Params) != 1 || routes[0].Params[0] != "city_id" {
		t.Fatalf("expected route param city_id, got %+v", routes)
	}

	// 3. Diagnostics: Swagger @Router mismatch, wrong route param, exists/unique table & column validation
	ctrlPath := filepath.Join(root, "app/http/controllers/city_controller.go")
	ctrlBytes, err := os.ReadFile(ctrlPath)
	if err != nil {
		t.Fatal(err)
	}
	fa := orm.AnalyzeFile(ctrlPath, ctrlBytes, cat)
	diags := fa.Diagnostics(cat)
	var msgs []string
	for _, d := range diags {
		msgs = append(msgs, d.Message)
	}
	joined := strings.Join(msgs, "\n")
	for _, want := range []string{
		"@Router /api/cities/{city_id} [post] does not match registered route GET /api/cities/{city_id}",
		"Route parameter 'wrong_param' is not defined",
		"Table 'missing_tbl' not found in schema for 'exists' rule",
		"Column 'no_col' does not exist on table 'cities'",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing expected diagnostic %q in:\n%s", want, joined)
		}
	}

	// 4. Hover & Definition on exists:cities,province_id
	ctrlText := string(ctrlBytes)
	colOff := strings.Index(ctrlText, "province_id|exists:missing_tbl") + 2
	md, _, _, ok := fa.Hover(colOff, cat)
	if !ok || !strings.Contains(md, "cities.province_id") {
		t.Errorf("expected hover for cities.province_id, got ok=%v md=%q", ok, md)
	}
	defs := fa.Definition(colOff, cat)
	if len(defs) == 0 || !strings.HasSuffix(defs[0].File, "city.go") {
		t.Errorf("expected definition to city.go, got %+v", defs)
	}

	// 5. Server CodeLens (Route Test Request + Artisan Command) & Find References
	srv := New(nil, nil)
	srv.root = root
	srv.cat = cat
	ctrlURI := pathToURI(ctrlPath)
	srv.docs[ctrlURI] = ctrlText

	// CodeLens on controller
	rawLens, _ := json.Marshal(map[string]any{"textDocument": map[string]any{"uri": ctrlURI}})
	lenses, _ := srv.codeLens(rawLens).([]map[string]any)
	if len(lenses) < 2 {
		t.Fatalf("expected >=2 lenses on controller (jumpToRoute + testRequest), got %d", len(lenses))
	}

	// CodeLens on command
	cmdPath := filepath.Join(root, "app/console/commands/sync_geo.go")
	cmdBytes, _ := os.ReadFile(cmdPath)
	cmdURI := pathToURI(cmdPath)
	srv.docs[cmdURI] = string(cmdBytes)
	rawCmdLens, _ := json.Marshal(map[string]any{"textDocument": map[string]any{"uri": cmdURI}})
	cmdLenses, _ := srv.codeLens(rawCmdLens).([]map[string]any)
	if len(cmdLenses) != 1 {
		t.Fatalf("expected 1 artisan CodeLens on sync_geo.go, got %d", len(cmdLenses))
	}

	// Find References on jwt.secret
	cfgOff := strings.Index(ctrlText, "jwt.secret") + 2
	pos := offsetToPos(ctrlText, cfgOff)
	rawRef, _ := json.Marshal(map[string]any{
		"textDocument": map[string]any{"uri": ctrlURI},
		"position":     pos,
	})
	refs, _ := srv.references(rawRef).([]LocationT)
	if len(refs) != 1 {
		t.Errorf("expected 1 reference for jwt.secret, got %d", len(refs))
	}
}

