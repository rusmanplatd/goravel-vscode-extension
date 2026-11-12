# Goravel VS Code Extension

Raw-SQL intelligence for [Goravel](https://www.goravel.dev) projects. Full roadmap: [docs/implementation_plan.md](docs/implementation_plan.md).

## Features (Phase 1 — implemented)

Inside Go strings passed to `facades.DB()` / built with `+` concatenation and `fmt.Sprintf`:

- **Completion**: tables after `FROM`/`JOIN`, columns after `alias.`, columns in scope elsewhere.
- **Hover**: column → Go field, SQL type, nullability, `@Description`.
- **Go to definition**: column/table → model field/struct **and** migration `CREATE TABLE` line.
- **Diagnostics**: unknown `alias.column`.
- **SQL highlighting** in raw strings.

Table variables like `x := (models.RefGeoCity{}).TableName(ctx)` are resolved through the model's
`OriginalTableName` constant, so `FROM ` + x + ` ct` knows `ct` is `ref_geo_cities`.
The schema is indexed statically from `app/models/*.go` and `database/migrations*/**/*.go`.

## Develop

```bash
npm install
npm run build          # builds bin/goravel-lsp and dist/extension.js
npm run test:server    # go tests
./bin/goravel-lsp --check /path/to/goravel-project   # lint all SQL in a project (CLI)
./bin/goravel-lsp --dump  /path/to/goravel-project   # list indexed tables
```

Press F5 in VS Code (Extension Development Host) and open a Goravel project.

## Roadmap status

| Milestone | Status |
|---|---|
| 1. Scaffolding, SQL grammar, LSP skeleton | done |
| 2. Schema indexer (models + migrations) | done (live DB introspection deferred) |
| 3. Raw SQL: completion, hover, definition, unknown-column diagnostics | done |
| 3b. Scan-struct mismatch, placeholder count, "Generate Row struct", `SELECT *` expand | todo |
| 4. ORM string args, Config keys, Routes, Artisan | todo |

