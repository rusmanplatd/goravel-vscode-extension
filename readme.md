# Goravel VS Code Extension

Raw-SQL intelligence for [Goravel](https://www.goravel.dev) projects. Full roadmap: [docs/implementation_plan.md](docs/implementation_plan.md).

## Features (Phase 1 — implemented)

Inside Go strings passed to `facades.DB()` / built with `+` concatenation and `fmt.Sprintf`:

- **Completion**: tables after `FROM`/`JOIN`, columns after `alias.`, columns in scope elsewhere.
- **Hover**: column → Go field, SQL type, nullability, `@Description`.
- **Go to definition**: column/table → model field/struct **and** migration `CREATE TABLE` line.
- **Diagnostics**: unknown `alias.column`; placeholder count vs. arguments (`$N`/`?`);
  `Select(&dest, ...)` columns with no matching field in the destination struct; columns that may be NULL
  scanned into non-nullable fields (info).
- **Code actions**: expand `*` / `alias.*` to explicit columns; generate a `<Func>Row` struct from a `Select` call
  (uses `sql.Null*` for nullable / LEFT JOIN columns).
- **SQL highlighting** in raw strings.

Table variables like `x := (models.RefGeoCity{}).TableName(ctx)` are resolved through the model's
`OriginalTableName` constant, so `FROM ` + x + ` ct` knows `ct` is `ref_geo_cities`.
The schema is indexed statically from `app/models/*.go` and `database/migrations*/**/*.go`.

## Develop

```bash
npm install
npm run build          # builds bin/goravel-lsp and dist/extension.js
npm run install:server # installs goravel-lsp to $(go env GOPATH)/bin (or $GOBIN)
npm run test:server    # go tests
./bin/goravel-lsp --check /path/to/goravel-project   # lint all SQL in a project (CLI)
./bin/goravel-lsp --dump  /path/to/goravel-project   # list indexed tables
```

Press F5 in VS Code (Extension Development Host) and open a Goravel project.

## Package & Publish

```bash
npm run package        # builds and creates goravel-vscode-<version>.vsix
npx vsce login rusmanplatd   # authenticate once with an Azure DevOps PAT (Marketplace: Manage)
npm run publish        # builds and publishes current version to VS Code Marketplace
npm run publish:patch  # bumps patch version, builds, and publishes
npm run publish:minor  # bumps minor version, builds, and publishes
npm run publish:major  # bumps major version, builds, and publishes
```

### Auto-publish via GitHub Actions

[`.github/workflows/publish.yml`](.github/workflows/publish.yml) runs typecheck, Go tests, packages the `.vsix`, and publishes to the VS Code Marketplace using **Trusted Publishing (OIDC)** on `v*` tag pushes or manual `workflow_dispatch` (no `VSCE_PAT` secret required).

Push a version tag to release:
```bash
git tag v0.1.0 && git push origin v0.1.0
```

## Roadmap status

| Milestone | Status |
|---|---|
| 1. Scaffolding, SQL grammar, LSP skeleton | done |
| 2. Schema indexer (models + migrations) | done (live DB introspection deferred) |
| 3. Raw SQL: completion, hover, definition, unknown-column diagnostics | done |
| 3b. Scan-struct mismatch, placeholder count, "Generate Row struct", `SELECT *` expand | done |
| 4. ORM string args, Config keys, Routes, Artisan | todo |

