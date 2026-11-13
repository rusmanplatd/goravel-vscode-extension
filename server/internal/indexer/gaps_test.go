package indexer

import (
	"os"
	"path/filepath"
	"testing"
)

func TestModelSchemaExampleAndTableConst(t *testing.T) {
	root := t.TempDir()
	modelsDir := filepath.Join(root, "app", "models")
	if err := os.MkdirAll(modelsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	src := `package models

const RefGeoCityOriginalTableName = "ref_geo_cities"
const RefGeoCityOriginalSchemaName = "geo_app"

type RefGeoCity struct {
	ID uint ` + "`gorm:\"column:id\"`" + `
	// @Description City display name
	// @Example Jakarta Selatan
	Name string ` + "`gorm:\"column:name\"`" + `
}

func (RefGeoCity) TableName() string {
	return RefGeoCityOriginalTableName
}

func (RefGeoCity) OriginalSchemaName() string {
	return RefGeoCityOriginalSchemaName
}
`
	if err := os.WriteFile(filepath.Join(modelsDir, "ref_geo_city.go"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}

	cat := NewCatalog()
	cat.Root = root
	indexModels(cat, root)

	tbl := cat.Table("ref_geo_cities")
	if tbl == nil {
		t.Fatalf("expected ref_geo_cities table")
	}
	if tbl.Schema != "geo_app" {
		t.Fatalf("expected Schema=geo_app, got %q", tbl.Schema)
	}
	col := tbl.Column("name")
	if col == nil {
		t.Fatalf("expected name column")
	}
	if col.Doc != "City display name" {
		t.Fatalf("expected Doc='City display name', got %q", col.Doc)
	}
	if col.Example != "Jakarta Selatan" {
		t.Fatalf("expected Example='Jakarta Selatan', got %q", col.Example)
	}
	tc := cat.TableConsts["RefGeoCityOriginalTableName"]
	if tc == nil {
		t.Fatalf("expected RefGeoCityOriginalTableName in TableConsts")
	}
	if tc.TableName != "ref_geo_cities" || tc.Model != "RefGeoCity" || !tc.Loc.Valid() {
		t.Fatalf("unexpected TableConstInfo: %+v", tc)
	}
}

func TestResolveDBConfigFallbackToConfigDatabase(t *testing.T) {
	root := t.TempDir()
	cfgDir := filepath.Join(root, "config")
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	cfgSrc := `package config

func init() {
	config.Add("database", map[string]any{
		"default": config.Env("DB_CONNECTION", "mysql"),
		"connections": map[string]any{
			"mysql": map[string]any{
				"host":     config.Env("DB_HOST", "127.0.0.1"),
				"port":     config.Env("DB_PORT", 3306),
				"database": config.Env("DB_DATABASE", "app_db"),
				"username": config.Env("DB_USERNAME", "root"),
				"password": config.Env("DB_PASSWORD", "secret"),
			},
		},
	})
}
`
	if err := os.WriteFile(filepath.Join(cfgDir, "database.go"), []byte(cfgSrc), 0o644); err != nil {
		t.Fatal(err)
	}

	cat := NewCatalog()
	cat.Root = root
	indexConfigs(cat, root)

	dbCfg := ResolveDBConfig(cat)
	if dbCfg.Driver != "mysql" || dbCfg.Host != "127.0.0.1" || dbCfg.Port != "3306" || dbCfg.Database != "app_db" || dbCfg.Username != "root" || dbCfg.Password != "secret" {
		t.Fatalf("unexpected ResolveDBConfig result: %+v", dbCfg)
	}
}

func TestLiveDBParsers(t *testing.T) {
	cat := NewCatalog()
	parseDelimitedColumns(cat, "users|id|uuid|NO\nusers|bio|text|YES\n", "|")
	parseDelimitedRoutines(cat, "public|get_user_count|FUNCTION|integer\n", "|")

	tbl := cat.Table("users")
	if tbl == nil || tbl.Column("id") == nil || tbl.Column("bio") == nil {
		t.Fatalf("expected users columns parsed")
	}
	if !tbl.Column("bio").Nullable || tbl.Column("id").Nullable {
		t.Fatalf("unexpected nullability on parsed columns")
	}
	if len(cat.DBFunctions) != 1 || cat.DBFunctions[0].Name != "get_user_count" {
		t.Fatalf("unexpected DBFunctions: %+v", cat.DBFunctions)
	}

	jsonOut := tsvToJSON("id\tname\n1\tAlice\n2\tNULL\n", "\t")
	if jsonOut == "[]" || jsonOut == "" {
		t.Fatalf("expected non-empty JSON array, got %s", jsonOut)
	}
}

