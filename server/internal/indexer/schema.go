// Package indexer builds an in-memory catalog from Goravel models, migrations,
// config files, routes, and validation rules.
package indexer

import (
	"sort"
	"strings"
	"sync"
)

// Location is a 0-based position in a file.
type Location struct {
	File string `json:"file"`
	Line int    `json:"line"`
	Col  int    `json:"col"`
}

func (l Location) Valid() bool { return l.File != "" }

// Column describes a table column.
type Column struct {
	Name       string   `json:"name"`
	GoName     string   `json:"goName"`
	GoType     string   `json:"goType"`
	SQLType    string   `json:"sqlType"`
	Doc        string   `json:"doc"`
	Nullable   bool     `json:"nullable"`
	ForeignKey string   `json:"foreignKey,omitempty"` // target_table.target_col
	Loc        Location `json:"loc"`                  // Go model field
	MigLoc     Location `json:"migLoc"`               // migration column definition
}

// Relation describes a model relationship (BelongsTo, HasMany, HasOne, ManyToMany) or FK.
type Relation struct {
	Name        string   `json:"name"`        // Go field name on struct (e.g. "Province")
	TargetModel string   `json:"targetModel"` // e.g. "RefGeoProvince"
	TargetTable string   `json:"targetTable"` // e.g. "ref_geo_provinces"
	ForeignKey  string   `json:"foreignKey"`  // column on this table or target table (e.g. "province_id")
	References  string   `json:"references"`  // column on referenced table (e.g. "id")
	Loc         Location `json:"loc"`
}

// Table describes a table and the Go model backing it.
type Table struct {
	Name      string      `json:"name"`
	Model     string      `json:"model"`
	Columns   []*Column   `json:"columns"`
	Relations []*Relation `json:"relations,omitempty"`
	Loc       Location    `json:"loc"`    // Go struct
	MigLoc    Location    `json:"migLoc"` // CREATE TABLE
	byName    map[string]*Column
}

func (t *Table) Column(name string) *Column {
	if t == nil {
		return nil
	}
	return t.byName[strings.ToLower(name)]
}

func (t *Table) Relation(name string) *Relation {
	if t == nil {
		return nil
	}
	for _, r := range t.Relations {
		if strings.EqualFold(r.Name, name) {
			return r
		}
	}
	return nil
}

func (t *Table) add(c *Column) {
	if t.byName == nil {
		t.byName = map[string]*Column{}
	}
	key := strings.ToLower(c.Name)
	if old, ok := t.byName[key]; ok {
		if c.SQLType != "" {
			old.SQLType = c.SQLType
			old.Nullable = c.Nullable
			old.MigLoc = c.MigLoc
		}
		if c.ForeignKey != "" {
			old.ForeignKey = c.ForeignKey
		}
		if old.GoName == "" && c.GoName != "" {
			old.GoName = c.GoName
			old.GoType = c.GoType
			old.Loc = c.Loc
		}
		return
	}
	t.byName[key] = c
	t.Columns = append(t.Columns, c)
}

func (t *Table) addRelation(r *Relation) {
	for _, existing := range t.Relations {
		if existing.Name != "" && strings.EqualFold(existing.Name, r.Name) {
			return
		}
		if r.Name == "" && existing.TargetTable == r.TargetTable && existing.ForeignKey == r.ForeignKey {
			return
		}
	}
	t.Relations = append(t.Relations, r)
}

// StructField is a Go struct field with the DB column it scans from.
type StructField struct {
	Col      string
	Alt      []string // other accepted column names (json tag, snake_case)
	GoName   string
	GoType   string
	Relation string // target model name if this is a relation field
	FK       string // gorm foreignKey tag
	Refs     string // gorm references tag
	Loc      Location
}

// StructInfo describes a Go struct in app/models or app/http/requests.
type StructInfo struct {
	Name   string
	Fields []StructField
	Embeds []string
	Loc    Location
}

func (s *StructInfo) Has(col string) *StructField {
	for i := range s.Fields {
		if strings.EqualFold(s.Fields[i].Col, col) {
			return &s.Fields[i]
		}
		for _, a := range s.Fields[i].Alt {
			if strings.EqualFold(a, col) {
				return &s.Fields[i]
			}
		}
	}
	return nil
}

// ConfigItem describes a configuration key in config/*.go.
type ConfigItem struct {
	Key     string   `json:"key"`     // e.g. "jwt.secret"
	EnvKey  string   `json:"envKey"`  // e.g. "JWT_SECRET"
	Default string   `json:"default"` // e.g. "\"\"" or "60"
	EnvVal  string   `json:"envVal,omitempty"`
	Doc     string   `json:"doc,omitempty"`
	Loc     Location `json:"loc"`
}

// RouteItem describes a registered HTTP route in routes/**/*.go.
type RouteItem struct {
	Method     string   `json:"method"`     // GET, POST, PUT, PATCH, DELETE, ANY
	Path       string   `json:"path"`       // e.g. "/api/geo/cities"
	Handler    string   `json:"handler"`    // e.g. "CityController.Index"
	Controller string   `json:"controller"` // e.g. "CityController"
	Action     string   `json:"action"`     // e.g. "Index"
	Module     string   `json:"module"`     // e.g. "api_geo_app"
	Loc        Location `json:"loc"`        // route registration line
	CtrlLoc    Location `json:"ctrlLoc"`    // controller method line
}

// RuleItem describes a built-in or custom validation rule.
type RuleItem struct {
	Name    string   `json:"name"`
	Custom  bool     `json:"custom"`
	Message string   `json:"message,omitempty"`
	Doc     string   `json:"doc,omitempty"`
	Loc     Location `json:"loc,omitempty"`
}

// MigrationItem describes a migration file in database/migrations*.
type MigrationItem struct {
	Name   string   `json:"name"`
	Folder string   `json:"folder"`
	Table  string   `json:"table"`
	Loc    Location `json:"loc"`
}

// Catalog holds all indexed project metadata.
type Catalog struct {
	mu          sync.RWMutex
	Root        string
	Tables      map[string]*Table               // by table name (no schema)
	Models      map[string]*Table               // by Go model name
	Structs     map[string]*StructInfo          // all structs in app/models and app/http/requests
	Configs     map[string]*ConfigItem          // by dot key (e.g. "jwt.secret")
	EnvVars     map[string]string               // by env var name from .env*
	Routes      []*RouteItem                    // all registered routes
	Rules       map[string]*RuleItem            // by rule signature
	Migrations  []*MigrationItem                // all migration files
	Controllers map[string]map[string]Location  // Controller -> Method -> Location
}

func NewCatalog() *Catalog {
	c := &Catalog{
		Tables:      map[string]*Table{},
		Models:      map[string]*Table{},
		Structs:     map[string]*StructInfo{},
		Configs:     map[string]*ConfigItem{},
		EnvVars:     map[string]string{},
		Rules:       map[string]*RuleItem{},
		Controllers: map[string]map[string]Location{},
	}
	seedBuiltinRules(c)
	return c
}

// StripSchema turns `geo_app.ref_geo_cities` into `ref_geo_cities`.
func StripSchema(name string) string {
	name = strings.Trim(name, "\"`")
	if i := strings.LastIndex(name, "."); i >= 0 {
		name = name[i+1:]
	}
	return strings.Trim(strings.ToLower(name), "\"`")
}

func (c *Catalog) Table(name string) *Table {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.Tables[StripSchema(name)]
}

func (c *Catalog) Model(name string) *Table {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.Models[name]
}

func (c *Catalog) Struct(name string) *StructInfo {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.Structs[name]
}

func (c *Catalog) Config(key string) *ConfigItem {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.Configs[key]
}

func (c *Catalog) ConfigKeys() []string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]string, 0, len(c.Configs))
	for k := range c.Configs {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func (c *Catalog) Rule(name string) *RuleItem {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.Rules[name]
}

func (c *Catalog) RuleNames() []string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]string, 0, len(c.Rules))
	for k := range c.Rules {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func (c *Catalog) TableNames() []string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]string, 0, len(c.Tables))
	for n := range c.Tables {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

func (c *Catalog) ensure(name string) *Table {
	name = StripSchema(name)
	t := c.Tables[name]
	if t == nil {
		t = &Table{Name: name}
		c.Tables[name] = t
	}
	return t
}

// Build indexes the project at root.
func Build(root string) *Catalog {
	c := NewCatalog()
	c.mu.Lock()
	defer c.mu.Unlock()
	c.Root = root
	indexModels(c, root)
	indexMigrations(c, root)
	resolveModelRelations(c)
	indexConfigs(c, root)
	indexRoutes(c, root)
	indexRules(c, root)
	indexRequests(c, root)
	indexLiveDBIfEnabled(c, root)
	return c
}
