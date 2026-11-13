# Goravel VS Code Extension (`goravel-vscode`) — Implementation Gap Analysis

Comparison of the current codebase in `/var/www/html/rpd/goravel-vscode-extension` against [implementation_plan.md](./implementation_plan.md).

**Overall Status:** Most of Phases 1–4 (Raw SQL, ORM, Config, Routes, Validation Rules, Form Requests, Views, Drift/Bootstrap checks, Schedules, Artisan commands, Snippets, and Sidebar TreeViews) are implemented, and `go test ./...` passes in `server/`. The items below are **not implemented yet** or **partially implemented**.

---

## 1. Not Implemented Yet (Missing)

| Feature | Plan Section | Current Gap |
| :--- | :--- | :--- |
| **SQL Syntax Error Diagnostics** | §3.3 | `server/go.mod` has no SQL parser dependency (stdlib only). `rawsql.Doc.Check` (`server/internal/rawsql/analyze.go`) validates qualified `alias.column` references via regex; malformed SQL syntax, dangling commas, or unclosed clauses are not diagnosed. |
| **`OriginalSchemaName()` Evaluation** | §3.1 | `server/internal/indexer/models.go` evaluates `TableName()` and `*OriginalTableName` constants, but does not parse or store `OriginalSchemaName()` methods. |
| **`@Example` Doc Comment Parsing** | §3.1 | `server/internal/indexer/models.go` extracts `@Description` into `Column.Doc`, but `@Example` annotations are not parsed or surfaced in hover cards. |
| **`config/database.go` Fallback for Live DB** | §3.1 | `IntrospectLiveDB`, `ExplainQuery`, and `PreviewQuery` in `server/internal/indexer/livedb.go` read `DB_*` credentials solely from `.env` (`c.EnvVars`) and do not fall back to `config/database.go`. |

---

## 2. Partially Implemented / Limited vs. Plan

- **Live DB Dialect Support (PostgreSQL / `psql` CLI only)** (§2, §3.1, §3.3, §5.5)
  - `server/internal/indexer/livedb.go` shells out exclusively to the `psql` binary (`exec.CommandContext(ctx, "psql", ...)`).
  - MySQL, SQLite, and SQL Server are not supported for Live DB introspection, `EXPLAIN`, or `Preview Query Results` (`goravel/previewQuery`).
  - Database functions and stored procedures are not introspected.

- **Unknown Table & Unqualified Column Diagnostics** (§3.3)
  - `rawsql.Doc.Check` (`server/internal/rawsql/analyze.go`) flags unknown qualified `alias.col` references, but does not flag unknown table names in `FROM`/`JOIN` or unknown unqualified column names.

- **`*Query*` / `*SQL*` Variable Name Heuristic** (§3.2)
  - `handle` in `server/internal/rawsql/extract.go` detects SQL strings via leading SQL keyword regex (`sqlish` / `anySQL`) and DB/ORM call sinks (`server/internal/rawsql/query.go`), rather than variable names matching `*Query*` or `*SQL*`.

- **`fmt.Sprintf` Placeholder Normalization** (§3.2)
  - `neutralize` in `server/internal/rawsql/extract.go` replaces `%d`/`%s`/`%v` with `__` instead of sequential `$1..$N` placeholders, and queries containing `%` are marked `Dynamic = true`, which skips placeholder count verification (`server/internal/rawsql/query.go`).

- **SQL Function & `INSERT INTO (...)` Completions** (§3.3)
  - `completion` in `server/internal/lsp/server.go` hardcodes only `COUNT`, `COALESCE`, and `NOW` for SQL functions.
  - `INSERT INTO table (...)` does not narrow column completions exclusively to the target table.

- **`OriginalTableName` Constant Symbol Indexing** (§3.1)
  - `server/internal/indexer/models.go` uses `*OriginalTableName` constants to resolve `TableName()`, but the constant identifier itself is not indexed for hover or go-to-definition.

- **Dynamic `goravel.sql.liveIntrospection` Setting Reload** (§3.1)
  - `server/internal/lsp/server.go` reads `liveIntrospection` on `initialize` only; `workspace/didChangeConfiguration` is not handled without restarting the server.

- **Explorer Context Menu for Modular Migrations** (§5.5)
  - `package.json` (`menus.explorer/context`) uses `"when": "resourcePath =~ /\\/database\\/migrations/"` for `goravel.artisan.makeMigration`, which misses right-clicking the `database/migrations` folder itself (no trailing `/`) and modular folders like `database/migrations_geo_app`.

- **Test Runner Portability & Command Palette Exposure** (§5.5)
  - `goravel.test.run` in `client/src/commands/artisan.ts` works via `Test*` CodeLens, but is not declared in `package.json` `contributes.commands` and uses POSIX `APP_ENV=testing go test ...` syntax.

