# Goravel VS Code Extension (`goravel-vscode`) — Implementation Gap Analysis

Comparison of the current codebase in `/var/www/html/rpd/goravel-vscode-extension` against [implementation_plan.md](./implementation_plan.md).

**Overall Status:** All Phases 1–4 features and previously identified gaps below are now implemented, with `go test ./...`, `npm run typecheck`, and `npm run build` passing.

---

## 1. Previously Missing Features — Resolved

| Feature | Plan Section | Status & Implementation |
| :--- | :--- | :--- |
| **SQL Syntax Error Diagnostics** | §3.3 | Implemented in `server/internal/rawsql/syntax.go` (`checkSQLSyntax`) and wired into `Doc.CheckWithCatalog` (`server/internal/rawsql/analyze.go`), diagnosing unclosed quotes/parentheses, dangling/consecutive/leading commas, empty `SELECT ... FROM`, trailing incomplete clauses, and consecutive logical operators. |
| **`OriginalSchemaName()` Evaluation** | §3.1 | Implemented in `server/internal/indexer/models.go` (`Table.Schema` in `schema.go`), parsing `OriginalSchemaName()` methods and constants and surfacing `schema.table` in table hover cards (`tableMD` in `server/internal/lsp/server.go`). |
| **`@Example` Doc Comment Parsing** | §3.1 | Implemented in `server/internal/indexer/models.go` (`parseFieldDoc` -> `Column.Example`) and rendered in column hover cards (`colMD` in `server/internal/lsp/server.go`). |
| **`config/database.go` Fallback for Live DB** | §3.1 | Implemented in `server/internal/indexer/livedb.go` (`ResolveDBConfig` / `resolveDBConfigUnlocked`), reading `.env` `DB_*` first and falling back to `database.default` and `database.connections.<driver>.*` in `config/database.go`. |

---

## 2. Previously Partial Features — Resolved

- **Live DB Dialect Support & Routine Introspection** (§2, §3.1, §3.3, §5.5)
  - `server/internal/indexer/livedb.go` now supports **PostgreSQL (`psql`)**, **MySQL/MariaDB (`mysql`)**, **SQLite (`sqlite3`)**, and **SQL Server (`sqlcmd`)** for `IntrospectLiveDB`, `ExplainQuery`, and `PreviewQuery`.
  - Database functions and stored procedures (`information_schema.routines`) are introspected into `Catalog.DBFunctions` and offered in SQL completions.

- **Unknown Table & Unqualified Column Diagnostics** (§3.3)
  - `Doc.CheckWithCatalog` (`server/internal/rawsql/analyze.go`) and `checkUnqualifiedColumns` (`server/internal/rawsql/syntax.go`) now flag unknown table names in `FROM`/`JOIN`/`UPDATE`/`INTO` and unknown unqualified column names against tables in scope.

- **`*Query*` / `*SQL*` Variable Name Heuristic** (§3.2)
  - `Extract` (`server/internal/rawsql/extract.go`) now extracts string literals, concatenations, and `fmt.Sprintf` calls assigned to variables whose names contain `query` or `sql` (case-insensitive).

- **`fmt.Sprintf` Placeholder Normalization** (§3.2)
  - `appendSprintfLit` (`server/internal/rawsql/extract.go`) normalizes `%d`/`%s`/`%v` verbs inside `fmt.Sprintf` to sequential `$1..$N` SQL placeholders while preserving `Seg` byte offsets, and `LIKE '%...'` literals outside `fmt.Sprintf` no longer set `Dynamic = true`.

- **SQL Function & `INSERT INTO (...)` Completions** (§3.3)
  - `completion` (`server/internal/lsp/server.go`) now provides an expanded list of standard SQL/PostgreSQL/MySQL functions plus introspected `Catalog.DBFunctions`.
  - `INSERT INTO <table> (...)` (`insertTargetTable` in `server/internal/lsp/server.go`) narrows completions exclusively to the target table's columns.

- **`OriginalTableName` Constant Symbol Indexing** (§3.1)
  - `indexModels` (`server/internal/indexer/models.go`) indexes `*OriginalTableName` constants into `Catalog.TableConsts`, supporting hover cards and go-to-definition in `server/internal/lsp/server.go`.

- **Dynamic `goravel.sql.liveIntrospection` Setting Reload** (§3.1)
  - `client/src/extension.ts` listens to `workspace.onDidChangeConfiguration` and sends `workspace/didChangeConfiguration`; `server/internal/lsp/server.go` handles it to toggle `indexer.LiveDBEnabled`, reindex, and re-diagnose open documents without restarting.

- **Explorer Context Menu for Modular Migrations** (§5.5)
  - `package.json` (`menus.explorer/context`) now uses `"when": "resourcePath =~ /\\/database\\/migrations([_a-zA-Z0-9-]*)(\\/|$)/"` so right-clicking `database/migrations` itself or modular folders like `database/migrations_geo_app` exposes `goravel.artisan.makeMigration`.

- **Test Runner Portability & Command Palette Exposure** (§5.5)
  - `goravel.test.run` is declared in `package.json` `contributes.commands` and uses `vscode.ShellExecution` with `env: { APP_ENV: "testing" }` in `client/src/commands/artisan.ts` for cross-platform compatibility across POSIX, PowerShell, and `cmd.exe`.
