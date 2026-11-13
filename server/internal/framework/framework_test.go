package framework

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rusmanplatd/goravel-lsp/internal/indexer"
)

func TestRequestSyncDiagnosticsCompletionsAndCodeAction(t *testing.T) {
	cat := indexer.NewCatalog()
	cat.Structs["CreateCityRequest"] = &indexer.StructInfo{
		Name: "CreateCityRequest",
		Fields: []indexer.StructField{
			{Col: "name", Alt: []string{"name"}, GoName: "Name", GoType: "string"},
			{Col: "province_id", Alt: []string{"province_id"}, GoName: "ProvinceID", GoType: "*string"},
		},
	}

	src := `package requests

import "github.com/goravel/framework/contracts/http"

type CreateCityRequest struct {
	Name       string  ` + "`json:\"name\"`" + `
	ProvinceID *string ` + "`json:\"province_id\"`" + `
}

func (r *CreateCityRequest) Rules(ctx http.Context) map[string]string {
	return map[string]string{
		"name": "required|string",
		"typo_field": "required",
	}
}
`
	path := "/proj/app/http/requests/create_city_request.go"
	diags := RequestDiagnostics(path, src, cat)
	if len(diags) != 1 || !strings.Contains(diags[0].Message, "typo_field") {
		t.Fatalf("expected 1 diagnostic for typo_field, got %+v", diags)
	}

	// Completion inside key string
	off := strings.LastIndex(src, `"name"`) + 2
	comps := RequestCompletions(path, src, cat, off)
	if len(comps) != 2 {
		t.Fatalf("expected 2 request field completions, got %d", len(comps))
	}

	// CodeAction for missing province_id
	acts := RequestCodeActions(path, src, cat, off)
	if len(acts) != 1 || !strings.Contains(acts[0].Edits[0].NewText, `"province_id": "nullable|string"`) {
		t.Fatalf("expected Rules code action for province_id, got %+v", acts)
	}
}

func TestControllerRouteParamAndSwaggerDiagnostics(t *testing.T) {
	cat := indexer.NewCatalog()
	cat.Structs["CreateCityRequest"] = &indexer.StructInfo{Name: "CreateCityRequest"}
	cat.Routes = append(cat.Routes, &indexer.RouteItem{
		Method:      "GET",
		Path:        "/api/cities/{id}",
		Params:      []string{"id"},
		Middlewares: []string{"Auth"},
		Controller:  "CityController",
		Action:      "Show",
	})

	src := `package controllers

import "github.com/goravel/framework/contracts/http"

type CityController struct{}

// Show godoc
// @Param request body requests.MissingRequest true "bad"
// @Router /api/wrong/{id} [post]
func (c *CityController) Show(ctx http.Context) http.Response {
	_ = ctx.Request().Route("city_id")
	return nil
}
`
	path := "/proj/app/http/controllers/city_controller.go"
	diags := ControllerDiagnostics(path, src, cat)
	if len(diags) != 3 {
		t.Fatalf("expected 3 diagnostics (param, @Param type, @Router), got %d: %+v", len(diags), diags)
	}

	// Hover on method name
	off := strings.Index(src, "Show(ctx") + 1
	h := RouteAndControllerHover(path, src, cat, off)
	if h == nil || !strings.Contains(h.Markdown, "GET /api/cities/{id}") || !strings.Contains(h.Markdown, "Auth") {
		t.Fatalf("unexpected controller hover: %+v", h)
	}
}

func TestViewAndScheduleAndDrift(t *testing.T) {
	tmp := t.TempDir()
	viewFile := filepath.Join(tmp, "resources", "views", "auth", "signin.tmpl")
	_ = os.MkdirAll(filepath.Dir(viewFile), 0o755)
	_ = os.WriteFile(viewFile, []byte("<h1>{{ .Title }}</h1>"), 0o644)

	cat := indexer.NewCatalog()
	cat.Views["auth/signin.tmpl"] = indexer.Location{File: viewFile}

	src := `package controllers

func Login(ctx any) {
	_ = ctx.Response().View().Make("auth/signin.tmpl", map[string]any{"Title": "Hi", "Unused": 1})
	_ = ctx.Response().View().Make("auth/missing.tmpl", nil)
}
`
	diags := ViewDiagnostics("/proj/app/http/controllers/auth.go", src, cat)
	if len(diags) != 2 {
		t.Fatalf("expected 2 view diagnostics (unused key + missing template), got %d: %+v", len(diags), diags)
	}

	// Schedule inlay hint & hover
	schedSrc := `package console
func Schedule() {
	facades.Schedule().Command("app:sync").Cron("*/5 * * * *")
}
`
	hints := ScheduleInlayHints("/proj/app/console/kernel.go", schedSrc)
	if len(hints) != 1 || !strings.Contains(hints[0].Label, "Every 5 minutes") {
		t.Fatalf("expected cron inlay hint, got %+v", hints)
	}

	// Model <-> Migration drift
	tbl := &indexer.Table{
		Name:   "cities",
		Model:  "City",
		MigLoc: indexer.Location{File: "/proj/database/migrations/001.go", Line: 10},
		Columns: []*indexer.Column{
			{Name: "id", SQLType: "BIGINT", Loc: indexer.Location{File: "/proj/app/models/city.go", Line: 5}, MigLoc: indexer.Location{File: "/proj/database/migrations/001.go", Line: 11}},
			{Name: "population", SQLType: "INTEGER", Nullable: true, MigLoc: indexer.Location{File: "/proj/database/migrations/001.go", Line: 12}},
		},
	}
	cat.Tables["cities"] = tbl
	cat.Models["City"] = tbl

	modelSrc := `package models

type City struct {
	ID int64
}
`
	mPath := "/proj/app/models/city.go"
	driftDiags := DriftDiagnostics(mPath, modelSrc, cat)
	if len(driftDiags) != 1 || !strings.Contains(driftDiags[0].Message, "population") {
		t.Fatalf("expected 1 model drift diagnostic for population, got %+v", driftDiags)
	}
	driftActs := DriftCodeActions(mPath, modelSrc, cat, strings.Index(modelSrc, "ID int64"))
	if len(driftActs) != 1 || !strings.Contains(driftActs[0].Edits[0].NewText, "Population *int") {
		t.Fatalf("expected drift code action adding Population *int, got %+v", driftActs)
	}
}
