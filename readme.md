# Goravel VS Code Extension

Full-featured language server (`goravel-lsp`) and VS Code extension for [Goravel](https://www.goravel.dev) projects. Full architecture: [docs/implementation_plan.md](docs/implementation_plan.md).

## Features

### 1. Raw SQL Intelligence & Syntax Highlighting (`facades.DB()` & `facades.Orm().Query().Raw()`)
Works across Go raw backtick strings (`` `...` ``), double-quoted strings (`"..."`), `+` string concatenations (`FROM ` + `refGeoCityTableName` + ` ct`), and `fmt.Sprintf`:
- **Rich SQL Syntax Highlighting**:
  - **TextMate SQL Injection Grammar** (`syntaxes/goravel-sql.injection.tmLanguage.json`) for both raw and double-quoted SQL strings and continuation fragments (`LEFT JOIN`, `WHERE`, `AND`, `ON`, etc.).
  - **LSP Semantic Tokens (`textDocument/semanticTokens/full`)** coloring SQL keywords, functions (`COUNT`, `NOW`, `COALESCE`), table aliases (`ct`, `p`, `c`), columns (`id`, `province_id`, `name`), parameters (`$1`, `?`), strings, numbers, and operators.
- **Completion**:
  - After `FROM`/`JOIN`: suggests both database table names and in-scope Go table variables (e.g. `refGeoCityTableName`).
  - After `JOIN <table> <alias> ON`: suggests foreign-key join conditions (e.g. `ct.province_id = p.id`).
  - After `alias.`: suggests columns for that table alias with SQL types, nullability, and `@Description`.
- **Hover & Go to Definition**:
  - Hovering over a column or table alias shows Go struct field, SQL type, nullability, foreign keys, and doc comments.
  - Go to Definition jumps to both the Go model struct/field (`app/models/*.go`) **and** the migration `CREATE TABLE` definition (`database/migrations*/**/*.go`).
- **Diagnostics**:
  - Unknown `alias.column` references (with typo suggestions).
  - Index-aware hints when `WHERE` / `ON` filters on unindexed columns of indexed tables.
  - Placeholder count vs. passed arguments (`$N` / `?`).
  - Scan destination struct mismatch (`Select(&dest, ...)` or `Raw(...).Scan(&dest)`): warns on missing struct fields or nullable/outer-joined columns scanned into non-nullable Go fields.
- **Code Actions**:
  - **Expand `*` / `alias.*`** to explicit columns.
  - **Generate `<Func>Row` scan struct** from a `SELECT` query (using `sql.Null*` / `uuid.NullUUID` for nullable and `LEFT JOIN` columns).
  - **Explain SQL Query against DB** and **Preview Query Results (Live DB)** Webview table (when `DB_HOST` is configured in `.env`).

### 2. ORM & Query Builder Intelligence (`facades.Orm().Query()`, `facades.DB().Table()`, `BuildPatchQuery*`)
- Column completion, hover, go-to-definition, and unknown-column diagnostics inside `.Where("...")`, `.OrWhere("...")`, `.Select("...")`, `.Order("...")`, `.Group("...")`, `.Update("...", val)`, and `.Pluck("...", &dest)`.
- Relation completion, hover, go-to-definition, and diagnostics inside `.With("Province")` and nested `.With("Province.Country")`.
- Patch map column key validation and completion for `BuildPatchQuery` / `BuildPatchQueryWithWhere`.

### 3. Goravel Framework Intelligence (Config, Routes, Validation, Views, Drift & Artisan)
- **Config & Env (`facades.Config()`)**:
  - Indexes `config/*.go` (`config.Add(...)`) and `.env*` files.
  - Completion, hover, go-to-definition, unknown-key diagnostics, and **Inlay Hints** (`→ ENV_VAR (default)`) on `facades.Config().Get*("...")` and `config.Env("...")`.
- **Routes, Middleware, Swagger & `.http` Generator (`facades.Route()`)**:
  - Indexes `routes/**/*.go` (including modular route packages like `routes/api_geo_app/*.go`), resolving `.Prefix()`, `.Group()`, `.Middleware()`, and `.Resource()`.
  - Displays clickable **CodeLenses** (`GET /api/geo/cities` and **Test Request**) above Controller methods in `app/http/controllers/**/*.go`, pre-filling `.http` JSON bodies from bound `requests.*Request` structs.
  - Validates `ctx.Request().Route(...)` params against route `{param}` definitions, checks Swagger `@Router` / `@Param` / `@Success` sync (with a Code Action to generate Swagger comments), and supports middleware hover & Go-to-Definition.
- **Form Requests & Validation Rules (`Rules(ctx)` & `Validate()`)**:
  - Indexes built-in Goravel validation rules and custom rules in `app/rules/*.go` (`Signature()` & `Message()`).
  - Completion, hover, go-to-definition (jumping to `app/rules/*.go`), and unknown-rule diagnostics inside `Rules(ctx)` and `Validate(...)`.
  - Syncs `Rules(ctx)`, `Messages(ctx)`, and `Attributes(ctx)` keys with the Form Request struct's `json`/`form` fields, with a Code Action to generate missing rules.
- **Views & Templates (`resources/views/**/*.tmpl`)**:
  - Path completion, hover, Go-to-Definition, missing-template diagnostics, and unused data-key hints on `ctx.Response().View().Make(...)`.
- **Model ↔ Migration Drift & Bootstrap Checks**:
  - Warns when migration columns are missing from `app/models/*.go` (with a Code Action to add them), when model fields are absent from migrations, and when migrations, seeders, rules, or commands are not registered in `bootstrap/`.
- **Schedules, Tests, Logs, Snippets & Sidebar Explorers**:
  - **Schedules (`facades.Schedule()`)**: Human-readable `.Cron("...")` Inlay Hints and Hover cards.
  - **Goravel Activity Bar Views**: **Models & Schema**, **Routes**, **Migrations**, and **Commands, Seeders & Schedules** explorers.
  - **Status Bar & Artisan CLI**: Displays indexed table and route counts; supports `make:*`, `migrate`, `migrate:rollback`, `db:seed --seeder=...`, custom Artisan commands, and Explorer right-click `make:*` context menus.
  - **Testing, Logs & Snippets**: `Run Goravel Test` CodeLenses on `tests/**/*_test.go` (auto-using `.env.testing`), `Goravel: Open storage/logs/goravel.log`, and Go snippets (`gctrl`, `greq`, `gmodel`, `groute`, `grule`, `gcmd`, `gmig`, `gtest`).

## Develop & Build

```bash
npm install
npm run build          # builds bin/goravel-lsp and dist/extension.js
npm run install:server # installs goravel-lsp to $(go env GOPATH)/bin (or $GOBIN)
npm run test:server    # runs Go unit & integration tests
npm run typecheck      # runs TypeScript type checking
npm run package        # packages goravel-vscode-<version>.vsix
./bin/goravel-lsp --check /path/to/goravel-project   # lint all SQL, ORM, Config, and Rules in a project (CLI)
./bin/goravel-lsp --dump  /path/to/goravel-project   # list indexed tables, routes, configs, rules, and migrations
```

## Roadmap Status

| Milestone | Status |
|---|---|
| 1. Scaffolding, SQL grammar (raw + double quotes + continuations), Semantic Tokens | done |
| 2. Schema indexer (models, embedded structs, relations, raw DDL + `Schema().Create` blueprints, optional live DB) | done |
| 3. Raw SQL: completion (`FROM`/`JOIN` vars, `JOIN ON` FKs, `alias.col`), hover, definition, diagnostics, code actions | done |
| 4. ORM query builder, Config keys & inlay hints, Route CodeLenses, Validation rules, Artisan CLI & Sidebar TreeViews | done |
| 5. Form Request sync, Route/Swagger/Middleware checks, Views, Model-Migration drift, Bootstrap checks, Schedules, Query Preview, Tests, Logs & Snippets | done |
