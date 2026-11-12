// Package indexer builds an in-memory schema catalog from Goravel models and migrations.
package indexer

import (
	"strings"
	"sync"
)

// Location is a 0-based position in a file.
type Location struct {
	File string
	Line int
	Col  int
}

func (l Location) Valid() bool { return l.File != "" }

// Column describes a table column.
type Column struct {
	Name     string
	GoName   string
	GoType   string
	SQLType  string
	Doc      string
	Nullable bool
	Loc      Location // Go model field
	MigLoc   Location // migration column definition
}

// Table describes a table and the Go model backing it.
type Table struct {
	Name    string
	Model   string
	Columns []*Column
	Loc     Location // Go struct
	MigLoc  Location // CREATE TABLE
	byName  map[string]*Column
}

func (t *Table) Column(name string) *Column {
	if t == nil {
		return nil
	}
	return t.byName[strings.ToLower(name)]
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
		return
	}
	t.byName[key] = c
	t.Columns = append(t.Columns, c)
}

// Catalog holds all known tables.
type Catalog struct {
	mu     sync.RWMutex
	Tables map[string]*Table // by table name (no schema)
	Models map[string]*Table // by Go model name
}

func NewCatalog() *Catalog {
	return &Catalog{Tables: map[string]*Table{}, Models: map[string]*Table{}}
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

func (c *Catalog) TableNames() []string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]string, 0, len(c.Tables))
	for n := range c.Tables {
		out = append(out, n)
	}
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
	indexModels(c, root)
	indexMigrations(c, root)
	return c
}

