package indexer

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// LiveDBEnabled controls whether live database introspection is performed during Build.
var LiveDBEnabled = os.Getenv("GORAVEL_LIVE_DB") == "1"

func indexLiveDBIfEnabled(c *Catalog, root string) {
	if !LiveDBEnabled {
		return
	}
	introspectLiveDBWithConfig(c, resolveDBConfigUnlocked(c))
}

type dbConnConfig struct {
	Driver   string
	Host     string
	Port     string
	Database string
	Username string
	Password string
}

func configStringUnlocked(c *Catalog, key string) string {
	if c == nil {
		return ""
	}
	ci := c.Configs[key]
	if ci == nil {
		return ""
	}
	if ci.EnvVal != "" {
		return ci.EnvVal
	}
	v := strings.TrimSpace(ci.Default)
	if len(v) >= 2 && ((v[0] == '"' && v[len(v)-1] == '"') || (v[0] == '`' && v[len(v)-1] == '`')) {
		if u, err := strconv.Unquote(v); err == nil {
			return u
		}
	}
	return v
}

// ResolveDBConfig reads DB_* settings from .env and falls back to config/database.go.
func ResolveDBConfig(c *Catalog) dbConnConfig {
	if c == nil {
		return dbConnConfig{}
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	return resolveDBConfigUnlocked(c)
}

func resolveDBConfigUnlocked(c *Catalog) dbConnConfig {
	var cfg dbConnConfig
	if c == nil {
		return cfg
	}
	env := func(k string) string { return c.EnvVars[k] }
	root := c.Root

	cfg.Driver = strings.ToLower(env("DB_CONNECTION"))
	if cfg.Driver == "" {
		cfg.Driver = strings.ToLower(configStringUnlocked(c, "database.default"))
	}
	if cfg.Driver == "" {
		cfg.Driver = "postgres"
	}

	prefix := "database.connections." + cfg.Driver + "."
	pick := func(envKey, cfgField string) string {
		if v := env(envKey); v != "" {
			return v
		}
		return configStringUnlocked(c, prefix+cfgField)
	}

	cfg.Host = pick("DB_HOST", "host")
	cfg.Port = pick("DB_PORT", "port")
	cfg.Database = pick("DB_DATABASE", "database")
	cfg.Username = pick("DB_USERNAME", "username")
	cfg.Password = pick("DB_PASSWORD", "password")

	if isSQLite(cfg.Driver) && cfg.Database != "" && !filepath.IsAbs(cfg.Database) && root != "" {
		cfg.Database = filepath.Join(root, cfg.Database)
	}
	return cfg
}

func isPostgres(d string) bool {
	return d == "" || d == "postgres" || d == "postgresql" || d == "pgsql"
}

func isMySQL(d string) bool {
	return d == "mysql" || d == "mariadb"
}

func isSQLite(d string) bool {
	return d == "sqlite" || d == "sqlite3"
}

func isSQLServer(d string) bool {
	return d == "sqlserver" || d == "mssql"
}

func (cfg dbConnConfig) validate() error {
	if isSQLite(cfg.Driver) {
		if cfg.Database == "" {
			return fmt.Errorf("DB_DATABASE not configured in .env or config/database.go")
		}
		return nil
	}
	if cfg.Host == "" || cfg.Database == "" || cfg.Username == "" {
		return fmt.Errorf("DB_HOST/DB_DATABASE/DB_USERNAME not configured in .env or config/database.go")
	}
	return nil
}

// IntrospectLiveDB queries schema columns and routines for Postgres, MySQL, SQLite, or SQL Server.
func IntrospectLiveDB(c *Catalog) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	introspectLiveDBWithConfig(c, resolveDBConfigUnlocked(c))
}

func introspectLiveDBWithConfig(c *Catalog, cfg dbConnConfig) {
	if err := cfg.validate(); err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	switch {
	case isPostgres(cfg.Driver):
		port := cfg.Port
		if port == "" {
			port = "5432"
		}
		q := `SELECT table_name, column_name, data_type, is_nullable FROM information_schema.columns WHERE table_schema NOT IN ('pg_catalog', 'information_schema')`
		cmd := exec.CommandContext(ctx, "psql", "-h", cfg.Host, "-p", port, "-U", cfg.Username, "-d", cfg.Database, "-At", "-F", "|", "-c", q)
		cmd.Env = append(os.Environ(), "PGPASSWORD="+cfg.Password)
		if out, err := cmd.Output(); err == nil {
			parseDelimitedColumns(c, string(out), "|")
		}
		rq := `SELECT routine_schema, routine_name, routine_type, COALESCE(data_type, '') FROM information_schema.routines WHERE routine_schema NOT IN ('pg_catalog', 'information_schema')`
		rcmd := exec.CommandContext(ctx, "psql", "-h", cfg.Host, "-p", port, "-U", cfg.Username, "-d", cfg.Database, "-At", "-F", "|", "-c", rq)
		rcmd.Env = append(os.Environ(), "PGPASSWORD="+cfg.Password)
		if out, err := rcmd.Output(); err == nil {
			parseDelimitedRoutines(c, string(out), "|")
		}

	case isMySQL(cfg.Driver):
		port := cfg.Port
		if port == "" {
			port = "3306"
		}
		q := `SELECT table_name, column_name, data_type, is_nullable FROM information_schema.columns WHERE table_schema = DATABASE()`
		args := mysqlArgs(cfg, port, "-N", "-B", "-e", q)
		if out, err := exec.CommandContext(ctx, "mysql", args...).Output(); err == nil {
			parseDelimitedColumns(c, string(out), "\t")
		}
		rq := `SELECT routine_schema, routine_name, routine_type, COALESCE(data_type, '') FROM information_schema.routines WHERE routine_schema = DATABASE()`
		rargs := mysqlArgs(cfg, port, "-N", "-B", "-e", rq)
		if out, err := exec.CommandContext(ctx, "mysql", rargs...).Output(); err == nil {
			parseDelimitedRoutines(c, string(out), "\t")
		}

	case isSQLite(cfg.Driver):
		q := `SELECT m.name || '|' || p.name || '|' || COALESCE(p.type, '') || '|' || CASE WHEN p."notnull" = 0 THEN 'YES' ELSE 'NO' END FROM sqlite_master m JOIN pragma_table_info(m.name) p WHERE m.type IN ('table','view') AND m.name NOT LIKE 'sqlite_%'`
		if out, err := exec.CommandContext(ctx, "sqlite3", cfg.Database, q).Output(); err == nil {
			parseDelimitedColumns(c, string(out), "|")
		}

	case isSQLServer(cfg.Driver):
		server := cfg.Host
		if cfg.Port != "" {
			server += "," + cfg.Port
		}
		q := `SET NOCOUNT ON; SELECT TABLE_NAME, COLUMN_NAME, DATA_TYPE, IS_NULLABLE FROM INFORMATION_SCHEMA.COLUMNS`
		cmd := exec.CommandContext(ctx, "sqlcmd", "-S", server, "-U", cfg.Username, "-P", cfg.Password, "-d", cfg.Database, "-h", "-1", "-W", "-s", "|", "-Q", q)
		if out, err := cmd.Output(); err == nil {
			parseDelimitedColumns(c, string(out), "|")
		}
		rq := `SET NOCOUNT ON; SELECT ROUTINE_SCHEMA, ROUTINE_NAME, ROUTINE_TYPE, COALESCE(DATA_TYPE, '') FROM INFORMATION_SCHEMA.ROUTINES`
		rcmd := exec.CommandContext(ctx, "sqlcmd", "-S", server, "-U", cfg.Username, "-P", cfg.Password, "-d", cfg.Database, "-h", "-1", "-W", "-s", "|", "-Q", rq)
		if out, err := rcmd.Output(); err == nil {
			parseDelimitedRoutines(c, string(out), "|")
		}
	}
}

func mysqlArgs(cfg dbConnConfig, port string, extra ...string) []string {
	args := []string{"-h", cfg.Host, "-P", port, "-u", cfg.Username}
	if cfg.Password != "" {
		args = append(args, "-p"+cfg.Password)
	}
	args = append(args, "-D", cfg.Database)
	return append(args, extra...)
}

func parseDelimitedColumns(c *Catalog, out, sep string) {
	for _, line := range strings.Split(out, "\n") {
		parts := strings.Split(strings.TrimSpace(line), sep)
		if len(parts) < 4 {
			continue
		}
		tbl := strings.TrimSpace(parts[0])
		col := strings.TrimSpace(parts[1])
		typ := strings.TrimSpace(parts[2])
		nul := strings.TrimSpace(parts[3])
		if tbl == "" || col == "" {
			continue
		}
		t := c.ensure(tbl)
		t.add(&Column{
			Name:     col,
			SQLType:  strings.ToUpper(typ),
			Nullable: strings.EqualFold(nul, "YES"),
		})
	}
}

func parseDelimitedRoutines(c *Catalog, out, sep string) {
	for _, line := range strings.Split(out, "\n") {
		parts := strings.Split(strings.TrimSpace(line), sep)
		if len(parts) < 3 {
			continue
		}
		ret := ""
		if len(parts) >= 4 {
			ret = strings.TrimSpace(parts[3])
		}
		name := strings.TrimSpace(parts[1])
		if name == "" {
			continue
		}
		c.DBFunctions = append(c.DBFunctions, &DBFunction{
			Schema:     strings.TrimSpace(parts[0]),
			Name:       name,
			Kind:       strings.ToUpper(strings.TrimSpace(parts[2])),
			ReturnType: strings.ToUpper(ret),
		})
	}
}

var sqlPlaceholderRe = regexp.MustCompile(`(\$\d+|\?|\B@[A-Za-z_]\w*)`)

// SanitizeSQLPlaceholders replaces bind parameters ($1, ?, @p1) outside single-quoted literals with safe dummy literals for EXPLAIN / Preview.
func SanitizeSQLPlaceholders(sqlText string) string {
	var out strings.Builder
	inQuote := false
	i := 0
	for i < len(sqlText) {
		ch := sqlText[i]
		if ch == '\'' {
			inQuote = !inQuote
			out.WriteByte(ch)
			i++
			continue
		}
		if !inQuote {
			if m := sqlPlaceholderRe.FindStringIndex(sqlText[i:]); m != nil && m[0] == 0 {
				out.WriteString("'1'")
				i += m[1]
				continue
			}
		}
		out.WriteByte(ch)
		i++
	}
	return out.String()
}

// ExplainQuery runs EXPLAIN on a SQL query when a supported database is configured.
func ExplainQuery(c *Catalog, sqlText string) (string, error) {
	sqlText = SanitizeSQLPlaceholders(sqlText)
	cfg := ResolveDBConfig(c)
	if err := cfg.validate(); err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var cmd *exec.Cmd
	switch {
	case isPostgres(cfg.Driver):
		port := cfg.Port
		if port == "" {
			port = "5432"
		}
		cmd = exec.CommandContext(ctx, "psql", "-h", cfg.Host, "-p", port, "-U", cfg.Username, "-d", cfg.Database, "-c", "EXPLAIN "+sqlText)
		cmd.Env = append(os.Environ(), "PGPASSWORD="+cfg.Password)
	case isMySQL(cfg.Driver):
		port := cfg.Port
		if port == "" {
			port = "3306"
		}
		cmd = exec.CommandContext(ctx, "mysql", mysqlArgs(cfg, port, "-e", "EXPLAIN "+sqlText)...)
	case isSQLite(cfg.Driver):
		cmd = exec.CommandContext(ctx, "sqlite3", cfg.Database, "EXPLAIN QUERY PLAN "+sqlText)
	case isSQLServer(cfg.Driver):
		server := cfg.Host
		if cfg.Port != "" {
			server += "," + cfg.Port
		}
		cmd = exec.CommandContext(ctx, "sqlcmd", "-S", server, "-U", cfg.Username, "-P", cfg.Password, "-d", cfg.Database, "-Q", "SET SHOWPLAN_TEXT ON; "+sqlText)
	default:
		return "", fmt.Errorf("unsupported DB_CONNECTION %q", cfg.Driver)
	}

	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), err
	}
	return string(out), nil
}

// PreviewQuery executes a read-only SELECT query (capped at 20 rows) and returns JSON array string.
func PreviewQuery(c *Catalog, sqlText string) (string, error) {
	trimmed := strings.TrimSpace(SanitizeSQLPlaceholders(sqlText))
	up := strings.ToUpper(trimmed)
	if !strings.HasPrefix(up, "SELECT") && !strings.HasPrefix(up, "WITH") {
		return "", fmt.Errorf("preview only supports SELECT or WITH queries")
	}
	trimmed = strings.TrimRight(trimmed, "; \t\r\n")

	cfg := ResolveDBConfig(c)
	if err := cfg.validate(); err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	switch {
	case isPostgres(cfg.Driver):
		port := cfg.Port
		if port == "" {
			port = "5432"
		}
		wrapped := fmt.Sprintf("SELECT COALESCE(json_agg(_r), '[]'::json) FROM (SELECT * FROM (%s) AS _q LIMIT 20) AS _r", trimmed)
		cmd := exec.CommandContext(ctx, "psql", "-h", cfg.Host, "-p", port, "-U", cfg.Username, "-d", cfg.Database, "-At", "-c", wrapped)
		cmd.Env = append(os.Environ(), "PGPASSWORD="+cfg.Password)
		out, err := cmd.CombinedOutput()
		if err != nil {
			return string(out), err
		}
		return strings.TrimSpace(string(out)), nil

	case isMySQL(cfg.Driver):
		port := cfg.Port
		if port == "" {
			port = "3306"
		}
		wrapped := fmt.Sprintf("SELECT * FROM (%s) AS _q LIMIT 20", trimmed)
		out, err := exec.CommandContext(ctx, "mysql", mysqlArgs(cfg, port, "-B", "-e", wrapped)...).CombinedOutput()
		if err != nil {
			return string(out), err
		}
		return tsvToJSON(string(out), "\t"), nil

	case isSQLite(cfg.Driver):
		wrapped := fmt.Sprintf("SELECT * FROM (%s) AS _q LIMIT 20", trimmed)
		out, err := exec.CommandContext(ctx, "sqlite3", "-json", cfg.Database, wrapped).CombinedOutput()
		if err != nil {
			return string(out), err
		}
		s := strings.TrimSpace(string(out))
		if s == "" {
			return "[]", nil
		}
		return s, nil

	case isSQLServer(cfg.Driver):
		server := cfg.Host
		if cfg.Port != "" {
			server += "," + cfg.Port
		}
		wrapped := fmt.Sprintf("SET NOCOUNT ON; SELECT TOP 20 * FROM (%s) AS _q", trimmed)
		out, err := exec.CommandContext(ctx, "sqlcmd", "-S", server, "-U", cfg.Username, "-P", cfg.Password, "-d", cfg.Database, "-W", "-s", "\t", "-Q", wrapped).CombinedOutput()
		if err != nil {
			return string(out), err
		}
		return tsvToJSON(string(out), "\t"), nil

	default:
		return "", fmt.Errorf("unsupported DB_CONNECTION %q", cfg.Driver)
	}
}

func tsvToJSON(raw, sep string) string {
	lines := strings.Split(strings.TrimSpace(raw), "\n")
	if len(lines) < 2 {
		return "[]"
	}
	headers := strings.Split(lines[0], sep)
	for i := range headers {
		headers[i] = strings.TrimSpace(headers[i])
	}
	var rows []map[string]any
	for _, line := range lines[1:] {
		if strings.TrimSpace(line) == "" {
			continue
		}
		// skip sqlcmd separator line (e.g. "---\t---")
		if strings.HasPrefix(strings.TrimSpace(line), "---") {
			continue
		}
		cols := strings.Split(line, sep)
		row := map[string]any{}
		for i, h := range headers {
			if h == "" {
				continue
			}
			if i < len(cols) {
				v := cols[i]
				if v == "NULL" {
					row[h] = nil
				} else {
					row[h] = v
				}
			} else {
				row[h] = nil
			}
		}
		rows = append(rows, row)
	}
	if len(rows) == 0 {
		return "[]"
	}
	b, err := json.Marshal(rows)
	if err != nil {
		return "[]"
	}
	return string(b)
}
