# Goravel VS Code Extension — Feature Analysis & Implementation Roadmap

This document tracks the analysis of existing capabilities and implemented enhancements (**P0–P4**) for `goravel-vscode-extension`.

---

## Status Legend
- `[ ]` Planned (Backlog)
- `[~]` In Progress
- `[x]` Implemented & Verified

---

## 1. Capability Matrix (Baseline vs. Enhanced)

| Domain | Baseline (`v0.5.0`) | Enhanced (`Current`) |
|---|---|---|
| **Schema & Migrations** | `Schema().Create` & raw `CREATE TABLE` indexing; Model ← Migration drift Quick Fix | + Chronological `Schema().Table` (`DropColumn`, `RenameColumn`, add column) & raw `ALTER TABLE` parsing; bidirectional **Model ↔ Migration** cross-file Quick Fixes; live DB **Applied (batch N) / Pending** migration badges & inline Migrate/Rollback actions |
| **Bootstrap Wiring** | Read-only warnings for unregistered items | + One-click **Cross-File Quick Fix** & Sidebar button (`goravel.registerBootstrap`) to auto-register migrations (including `bootstrap/bootstrap_<module>/migrations.go`), seeders, rules, and commands |
| **ORM Query Builder & Models** | `.Where`, `.Select`, `.Order`, `.Group`, `.Update("col", v)`, `.Pluck`, `.With("Rel")` | + `.Join` / `.LeftJoin` / `.InnerJoin` / `.RightJoin` (table, column & FK `ON` completion + alias resolution), map-based `.Where(map[string]any{...})` & `.Updates(map[string]any{...})`, `.Distinct`, `.WhereColumn`, `.Increment`, `.Decrement`, nested `.With("Rel", func(q orm.Query) ...)` target-table inference, and **Model Observer & Scope CodeLens** |
| **Routes, gRPC & Controllers** | Route indexing, `ctx.Request().Route("param")`, controller CodeLens, `@Router` drift check | + `routes/*.go` **Inlay Hints** & **Hover** (`→ METHOD /full/path [Middlewares]`), **gRPC controllers & `.proto` service indexing** (`routes/grpc.go`, `app/grpc/controllers`), `ctx.Request().Input*("...")` & `Query*("...")` completion/hover/definition, and full **Swagger / OpenAPI annotation scaffolding** & `@Router` sync Quick Fixes |
| **Views & Templates** | `View().Make("...")` template path completion & unused key hint | + Context variable indexing from `View().Make("...", map[string]any{...})` with **completion, hover, and go-to-definition inside `.tmpl` / `.html` / `.gohtml` files** in `resources/views/` |
| **Config & `.env`** | `facades.Config().Get*` completion, hover, inlay hints, go-to-definition | + Missing `.env` key diagnostics on `config.Env("KEY")` and cross-file Quick Fix to append missing keys to `.env.example` / `.env` |
| **Localization (`Lang`)** | *Not supported* | + Full `lang/**/*.json` indexing (flat & nested keys), `facades.Lang(ctx).Get("...")` completion, multi-locale Markdown hover, inline preview inlay hints, go-to-definition, references, rename, missing-key diagnostics, and Quick Fix to add keys to `lang/en.json` |
| **Authorization (`Gate`)** | *Not supported* | + Indexes `facades.Gate().Define("ability", ...)` across `app/` & `bootstrap/`; provides completion, hover, definition, references, rename, and unknown-ability diagnostics on `Allows`, `Denies`, `Inspect`, `Check`, `Any`, `None` |
| **Events, Listeners & Jobs** | *Not supported* | + Indexes `app/events`, `app/listeners`, `app/jobs` & provider event maps; Event struct **CodeLens** (`N listener(s)`), sidebar sections, and unregistered Job detection |
| **Raw SQL & Live DB** | SQL completion, hover, diagnostics, `SELECT *` expand, N+1 warning, Explain/Preview on static SQL | + Automatic placeholder sanitization (`$1`, `?`, `@p1` → `NULL`) for parameterized **Explain** & **Preview**; Quick Fix to insert missing SQL scan columns into Go destination structs |
| **Refactoring (`Rename`)** | *Not supported* | + Workspace-wide `textDocument/rename` for Config keys, Custom Validation Rules, Gate abilities, Translation keys, SQL aliases, and Table columns |
| **Sidebar & Webviews** | 4 TreeViews + basic Artisan runner | + Inline action buttons on Tables, Routes & Unregistered items; **Database ER Diagram** Webview (`goravel.showErDiagram`); searchable **Route Matrix** Webview (`goravel.showRouteMatrix`); configurable `goravel.artisan.command` & 10 new `make:*` / `package:install` commands |

---

## 2. Implemented Roadmap (P0 → P3)

### P0 — Immediate High-Impact UX & Bootstrap Automation

- [x] **P0.1: Sidebar Inline Action Buttons & Richer Navigation**
  - Added inline buttons (`view/item/context`) on **Routes** tree items (`goravelRoute`): `Test Route Request` (`$(send)`) and `Open Route Registration` (`$(go-to-file)`).
  - Added inline buttons on **Models & Schema** tree items (`goravelTable`): `Preview Table Data (Top 20)` (`$(eye)`) and `Open Table Migration` (`$(file-code)`).
  - Added inline action on **Unregistered** tree items (`goravelUnregistered`): `Register in bootstrap/` (`$(add)`).
  - *Files:* `package.json`, `client/src/views/explorers.ts`, `client/src/extension.ts`.
- [x] **P0.2: Cross-File Code Action to Auto-Register Unregistered Components in `bootstrap/`**
  - When a migration, seeder, custom validation rule, or console command is not registered in `bootstrap/`, provides a Quick Fix (`Register <Item> in bootstrap/...`) that edits the corresponding `bootstrap/*.go` or `bootstrap/bootstrap_<module>/migrations.go` file via cross-file `WorkspaceEdit`.
  - Also supports triggering registration directly from the Sidebar `Unregistered in bootstrap/` node via `goravel/registerBootstrap`.
  - *Files:* `server/internal/framework/drift.go`, `server/internal/framework/types.go`, `server/internal/lsp/server.go`, `client/src/extension.ts`.
- [x] **P0.3: Configurable Artisan Runner & Complete `make:*` / `package:install` Presets**
  - Added `goravel.artisan.command` setting (default `"go run . artisan"`, customizable for env prefixes or Docker).
  - Expanded `ARTISAN_PRESETS`, commands, and `explorer/context` menus with `make:event`, `make:listener`, `make:job`, `make:observer`, `make:policy`, `make:provider`, `make:factory`, `make:test`, `make:filter`, and `package:install`.
  - *Files:* `package.json`, `client/src/commands/artisan.ts`.

---

### P1 — Deepening Core Schema, ORM, Route & Controller Intelligence

- [x] **P1.1: Incremental Migration Parsing (`Schema().Table` & `ALTER TABLE`) + Bidirectional Drift Quick Fixes**
  - Sorted migration files chronologically and parsed `facades.Schema().Table("...", func(table schema.Blueprint) { ... })` (`DropColumn`, `RenameColumn`, and column additions) plus raw SQL `ALTER TABLE ... ADD COLUMN / DROP COLUMN / RENAME COLUMN` in `server/internal/indexer/migrations.go`.
  - Added reverse Quick Fix (`buildModelToMigrationAction`) in `server/internal/framework/drift.go`: when a Go model field is missing from its migration, offers a cross-file Quick Fix to insert the column into the migration file (supports both `Schema().Create` blueprints and raw `CREATE TABLE` SQL).
  - *Files:* `server/internal/indexer/migrations.go`, `server/internal/indexer/schema.go`, `server/internal/framework/drift.go`.
- [x] **P1.2: Advanced ORM Query Builder Intelligence (`Join`, Map `.Where`/`.Updates`, `.With` Callback Scope)**
  - Provided table, column, and FK join condition completion (`LEFT JOIN <target> ON ...`), hover, definition, and diagnostics inside `.Join("...")` / `.InnerJoin("...")` / `.LeftJoin("...")` / `.RightJoin("...")` strings, plus alias registration for subsequent `.Where`/`.Select` calls.
  - Added support for map literal keys in `.Where(map[string]any{...})`, `.OrWhere(map[string]any{...})`, `.Update(map[string]any{...})`, and `.Updates(map[string]any{...})`.
  - Added support for `.Distinct("col")`, `.WhereColumn("col1", "col2")`, `.Increment("col")`, and `.Decrement("col")`.
  - Inferred the target relation table inside nested callbacks `.With("Relation", func(q orm.Query) orm.Query { ... })`.
  - *Files:* `server/internal/orm/analyzer.go`.
- [x] **P1.3: Controller Request Input/Query Completion & Route Inlay Hints**
  - In `routes/*.go`, added **Inlay Hints** (`RouteInlayHints`) and **Hover** (`RouteAndControllerHover`) on route definitions showing the full resolved URL path and inherited middlewares (e.g. `→ GET /api/geo/cities [Jwt]`).
  - In controllers (`app/http/controllers/*.go`), added completion, hover, and definition (`SpanRequestInput`) for `ctx.Request().Input("...")`, `ctx.Request().Query("...")`, `ctx.Request().InputInt("...")`, etc. based on bound `requests.*` struct fields and route parameters.
  - Added Quick Fix in `ControllerCodeActions` to update out-of-sync Swagger `@Router` annotations to match the registered route.
  - *Files:* `server/internal/framework/routes_check.go`, `server/internal/orm/analyzer.go`, `server/internal/lsp/server.go`.

---

### P2 — Framework Ecosystem Expansion (i18n, Gates, `.env`, Live DB Enhancements)

- [x] **P2.1: Localization / Translation (`facades.Lang(ctx).Get(...)`) Intelligence**
  - Indexed JSON translation files in `lang/` (`lang/en.json`, `lang/<locale>/**/*.json`, both flat and nested keys).
  - Added Autocompletion, Hover (Markdown table across all locales), Inlay Hints (inline preview of default translation string), Go to Definition, Find References, missing-key Diagnostics, and Quick Fix (`Add translation key to lang/en.json`) for `facades.Lang(ctx).Get("...")`.
  - *Files:* `server/internal/indexer/extras.go`, `server/internal/orm/analyzer.go`, `server/internal/framework/routes_check.go`.
- [x] **P2.2: Authorization Gates (`facades.Gate()`) Intelligence**
  - Indexed `facades.Gate().Define("<ability>", ...)` declarations across `app/` and `bootstrap/`.
  - Added Autocompletion, Hover, Go to Definition, Find References, Rename, and Diagnostics on `facades.Gate().Allows("...")`, `Denies("...")`, `Inspect("...")`, `Check("...")`, `Any(...)`, `None(...)`.
  - *Files:* `server/internal/indexer/extras.go`, `server/internal/orm/analyzer.go`, `server/internal/lsp/references.go`, `server/internal/lsp/rename.go`.
- [x] **P2.3: `.env` ↔ `config/*.go` Sync Diagnostics & Quick Fixes**
  - Added diagnostics in `config/*.go` when `config.Env("KEY")` references a key not present in `.env*`.
  - Added Quick Fix (`Add KEY to .env.example`) for missing env keys in `EnvAndLangCodeActions`.
  - *Files:* `server/internal/orm/analyzer.go`, `server/internal/framework/routes_check.go`.
- [x] **P2.4: Parameterized SQL Explain / Preview & Missing Scan Struct Field Quick Fix**
  - Added `SanitizeSQLPlaceholders` in `server/internal/indexer/livedb.go` to replace `$1`, `?`, and `@p1` placeholders with `NULL` outside string literals so `goravel.explainQuery` and `goravel.previewQuery` work seamlessly on parameterized queries.
  - Added Quick Fix (`AddMissingScanFields`) in `server/internal/rawsql/actions.go` to insert missing scan columns directly into the destination Go struct.
  - *Files:* `server/internal/indexer/livedb.go`, `server/internal/rawsql/actions.go`, `server/internal/lsp/server.go`.

---

### P3 — Refactoring, Architecture Visualization & Event/Job Tooling

- [x] **P3.1: Rename Symbol (`textDocument/rename`) Support**
  - Implemented `textDocument/rename` across the workspace for:
    - Config keys (`config/*.go` and `facades.Config().Get*("...")`)
    - Custom Validation Rule signatures (`app/rules/*.go` and `Rules(ctx)` strings)
    - Gate ability names (`facades.Gate().Define` and `Allows`/`Denies`/etc.)
    - Translation keys (`facades.Lang(ctx).Get` and `lang/*.json`)
    - SQL table/column aliases within a query and table column references across Go models, migrations, ORM calls, and raw SQL queries.
  - *Files:* `server/internal/lsp/rename.go`, `server/internal/lsp/server.go`.
- [x] **P3.2: Interactive Database ER Diagram & Route Matrix Webviews**
  - Added `Goravel: Show Database ER Diagram` (`goravel.showErDiagram`) command and Models view title button rendering an interactive schema overview with click-to-open Model/Migration buttons and generated Mermaid `erDiagram` source (`goravel/erDiagram`).
  - Added `Goravel: Show Route Matrix` (`goravel.showRouteMatrix`) command and Routes view title button rendering a searchable, method-filterable route table with one-click HTTP test request generation and Controller/Route navigation.
  - *Files:* `server/internal/lsp/server.go`, `client/src/extension.ts`, `package.json`.
- [x] **P3.3: Events, Listeners & Jobs Indexing + CodeLens**
  - Indexed `app/events/`, `app/listeners/`, and `app/jobs/`, plus event-to-listener registrations (`map[event.Event][]event.Listener`).
  - Added CodeLens on Event structs jumping to linked Listeners, sidebar sections for **Events & Listeners** and **Queue Jobs**, and drift detection for unregistered Jobs.
  - *Files:* `server/internal/indexer/extras.go`, `server/internal/lsp/server.go`, `client/src/views/explorers.ts`.

---

### P4 — Live Migration Status, Observers/Scopes, Template Intelligence, gRPC & OpenAPI Scaffolding

- [x] **P4.1: Live Migration Applied/Pending Status in Sidebar**
  - Extracted migration `Signature()` and queried the live database `migrations` table (PostgreSQL, MySQL, SQLite, SQL Server) when `goravel.sql.liveIntrospection` is enabled (`ApplyLiveMigrationStatus`).
  - Badged each migration in the **Migrations** TreeView as `Applied (batch N)` vs. `Pending`, with inline `Migrate` (`$(play)`) and `Rollback` (`$(discard)`) buttons.
  - *Files:* `server/internal/indexer/schema.go`, `server/internal/indexer/migrations.go`, `server/internal/indexer/livedb.go`, `client/src/views/explorers.ts`, `package.json`.
- [x] **P4.2: Model Observers & Scopes Intelligence**
  - Indexed `app/observers/` structs, lifecycle hook methods (`Creating`, `Created`, `Updating`, `Updated`, `Saving`, `Saved`, `Deleting`, `Deleted`, `Restoring`, `Restored`, `ForceDeleting`, `ForceDeleted`, `Retrieved`), and `facades.Orm().Observe(models.User{}, &observers.UserObserver{})` registrations.
  - Indexed reusable ORM query scopes (`func Scope...(q orm.Query) orm.Query`) in `app/models/`.
  - Added **CodeLens** on Model structs jumping to attached Observers and Query Scopes, plus a **Model Observers** section in the Sidebar and unregistered Observer detection.
  - *Files:* `server/internal/indexer/schema.go`, `server/internal/indexer/extras.go`, `server/internal/lsp/server.go`, `client/src/views/explorers.ts`.
- [x] **P4.3: Template Variable Completion in `resources/views/`**
  - Indexed controller and route `View().Make("view.tmpl", map[string]any{...})` / `View().First(...)` context map keys, value expressions, and locations (`Catalog.ViewVars`).
  - Enabled LSP `completion`, `hover`, and `definition` inside `.tmpl`, `.html`, and `.gohtml` template files under `resources/views/` when editing inside `{{ ... }}` blocks.
  - *Files:* `server/internal/indexer/extras.go`, `server/internal/framework/views.go`, `server/internal/lsp/server.go`, `client/src/extension.ts`.
- [x] **P4.4: gRPC Controllers & Proto Linkage**
  - Indexed `.proto` services/RPCs (`proto/`, `app/grpc/`, `contracts/proto/`), gRPC controllers (`app/grpc/controllers`), and `Register<Service>Server(...)` calls in `routes/grpc.go` and providers.
  - Surfaced `gRPC` routes (`/Service/Method`) alongside HTTP routes in the **Routes Explorer**, **Route Matrix** (with `gRPC` method filter), and Controller CodeLens.
  - *Files:* `server/internal/indexer/routes.go`, `server/internal/indexer/schema.go`, `client/src/views/explorers.ts`, `client/src/extension.ts`.
- [x] **P4.5: Auto-Generate Swagger / OpenAPI Annotations for Controllers**
  - Upgraded `ControllerCodeActions` to scaffold complete `@Summary`, `@Tags`, `@Accept json`, `@Produce json`, `@Security BearerAuth` (when auth middleware is attached), path `@Param`, query `@Param` (from `ctx.Request().Query*("...")` and GET `requests.*` structs), body `@Param` (from bound `requests.*` structs), inferred `@Success` response model (`{object} models.X` / `{array} models.X`), and normalized `@Router` annotations.
  - *Files:* `server/internal/framework/routes_check.go`.

---

## 3. Verification & Test Suite

- **Go LSP Unit Tests:** `server/internal/lsp/features_test.go` (`npm run test:server`)
  - `TestAlterMigrationAndTranslationsAndGates` — Verifies chronological `Schema().Table` (`RenameColumn`, `DropColumn`, column additions), `lang/*.json` flat/nested indexing, `facades.Gate().Define` indexing, Event → Listener mapping, `ResolveBootstrapRegistrationAction`, and reverse Model → Migration Quick Fix.
  - `TestORMJoinMapWhereWithCallbackAndLangGate` — Verifies `.LeftJoin`, map-based `.Where(map[string]any{...})`, nested `.With("Posts", func(q orm.Query) ...)` callback table inference, missing translation key diagnostics & inlay hints, and unknown Gate ability diagnostics.
  - `TestSanitizeSQLPlaceholdersAndERDiagramAndRename` — Verifies `SanitizeSQLPlaceholders` (`$1`, `?`, `@p1`), `goravel/erDiagram` Mermaid output, and workspace-wide `textDocument/rename`.
  - `TestP4LiveMigrationsObserversTemplatesGrpcAndSwagger` — Verifies live migration `applied` (with batch number) vs `pending` status (`P4.1`), Observer & Scope indexing + Model CodeLens (`P4.2`), `.tmpl` template variable completion/hover/definition (`P4.3`), gRPC controller & `.proto` route indexing (`P4.4`), and full Swagger/OpenAPI annotation Code Action generation (`P4.5`).
- **Client Typecheck & Bundle:** `npm run typecheck && npm run build`
