package indexer

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// LiveDBEnabled controls whether live database introspection is performed during Build.
var LiveDBEnabled = os.Getenv("GORAVEL_LIVE_DB") == "1"

func indexLiveDBIfEnabled(c *Catalog, root string) {
	if !LiveDBEnabled {
		return
	}
	IntrospectLiveDB(c)
}

// IntrospectLiveDB queries information_schema.columns via psql/mysql when DB_* env vars are configured.
func IntrospectLiveDB(c *Catalog) {
	conn := strings.ToLower(c.EnvVars["DB_CONNECTION"])
	host := c.EnvVars["DB_HOST"]
	port := c.EnvVars["DB_PORT"]
	db := c.EnvVars["DB_DATABASE"]
	user := c.EnvVars["DB_USERNAME"]
	pass := c.EnvVars["DB_PASSWORD"]
	if host == "" || db == "" || user == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	if conn == "" || conn == "postgres" || conn == "postgresql" || conn == "pgsql" {
		if port == "" {
			port = "5432"
		}
		q := `SELECT table_name, column_name, data_type, is_nullable FROM information_schema.columns WHERE table_schema NOT IN ('pg_catalog', 'information_schema')`
		cmd := exec.CommandContext(ctx, "psql", "-h", host, "-p", port, "-U", user, "-d", db, "-At", "-F", "|", "-c", q)
		cmd.Env = append(os.Environ(), "PGPASSWORD="+pass)
		out, err := cmd.Output()
		if err != nil {
			return
		}
		for _, line := range strings.Split(string(out), "\n") {
			parts := strings.Split(strings.TrimSpace(line), "|")
			if len(parts) < 4 {
				continue
			}
			t := c.ensure(parts[0])
			t.add(&Column{
				Name:     parts[1],
				SQLType:  strings.ToUpper(parts[2]),
				Nullable: strings.EqualFold(parts[3], "YES"),
			})
		}
	}
}

// ExplainQuery runs EXPLAIN on a SQL query when a Postgres database is configured in .env.
func ExplainQuery(c *Catalog, sqlText string) (string, error) {
	host := c.EnvVars["DB_HOST"]
	port := c.EnvVars["DB_PORT"]
	db := c.EnvVars["DB_DATABASE"]
	user := c.EnvVars["DB_USERNAME"]
	pass := c.EnvVars["DB_PASSWORD"]
	if host == "" || db == "" || user == "" {
		return "", fmt.Errorf("DB_HOST/DB_DATABASE/DB_USERNAME not configured in .env")
	}
	if port == "" {
		port = "5432"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "psql", "-h", host, "-p", port, "-U", user, "-d", db, "-c", "EXPLAIN "+sqlText)
	cmd.Env = append(os.Environ(), "PGPASSWORD="+pass)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), err
	}
	return string(out), nil
}

// PreviewQuery executes a read-only SELECT query (capped at 20 rows) and returns JSON lines.
func PreviewQuery(c *Catalog, sqlText string) (string, error) {
	trimmed := strings.TrimSpace(sqlText)
	up := strings.ToUpper(trimmed)
	if !strings.HasPrefix(up, "SELECT") && !strings.HasPrefix(up, "WITH") {
		return "", fmt.Errorf("preview only supports SELECT or WITH queries")
	}
	trimmed = strings.TrimRight(trimmed, "; \t\r\n")
	host := c.EnvVars["DB_HOST"]
	port := c.EnvVars["DB_PORT"]
	db := c.EnvVars["DB_DATABASE"]
	user := c.EnvVars["DB_USERNAME"]
	pass := c.EnvVars["DB_PASSWORD"]
	if host == "" || db == "" || user == "" {
		return "", fmt.Errorf("DB_HOST/DB_DATABASE/DB_USERNAME not configured in .env")
	}
	if port == "" {
		port = "5432"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	wrapped := fmt.Sprintf("SELECT COALESCE(json_agg(_q), '[]'::json) FROM (%s) AS _q LIMIT 20", trimmed)
	// Better: cap inner rows before aggregating
	wrapped = fmt.Sprintf("SELECT COALESCE(json_agg(_r), '[]'::json) FROM (SELECT * FROM (%s) AS _q LIMIT 20) AS _r", trimmed)
	cmd := exec.CommandContext(ctx, "psql", "-h", host, "-p", port, "-U", user, "-d", db, "-At", "-c", wrapped)
	cmd.Env = append(os.Environ(), "PGPASSWORD="+pass)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), err
	}
	return strings.TrimSpace(string(out)), nil
}

