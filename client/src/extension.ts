import * as fs from "fs";
import * as path from "path";
import * as vscode from "vscode";
import { LanguageClient, LanguageClientOptions, ServerOptions } from "vscode-languageclient/node";
import { registerArtisanCommands } from "./commands/artisan";
import {
  CommandInfo,
  CommandsTreeProvider,
  MigrationsTreeProvider,
  ModelsTreeProvider,
  OverviewData,
  RouteInfo,
  RoutesTreeProvider,
  SeederInfo,
  TableInfo,
  UnregisteredInfo,
} from "./views/explorers";

let client: LanguageClient | undefined;
let statusBar: vscode.StatusBarItem | undefined;
let customCommands: CommandInfo[] = [];
let customSeeders: SeederInfo[] = [];
let currentTables: TableInfo[] = [];
let currentRoutes: RouteInfo[] = [];
const modelsProvider = new ModelsTreeProvider();
const routesProvider = new RoutesTreeProvider();
const migrationsProvider = new MigrationsTreeProvider();
const commandsProvider = new CommandsTreeProvider();

function serverPath(ctx: vscode.ExtensionContext): string | undefined {
  const cfg = vscode.workspace.getConfiguration("goravel").get<string>("server.path");
  if (cfg && fs.existsSync(cfg)) return cfg;
  const exe = process.platform === "win32" ? "goravel-lsp.exe" : "goravel-lsp";
  const bundled = path.join(ctx.extensionPath, "bin", exe);
  return fs.existsSync(bundled) ? bundled : undefined;
}

async function refreshOverview() {
  if (!client) return;
  try {
    const data = await client.sendRequest<OverviewData>("goravel/overview", {});
    if (!data) return;
    currentTables = data.tables || [];
    currentRoutes = data.routes || [];
    modelsProvider.setData(currentTables);
    routesProvider.setData(currentRoutes);
    migrationsProvider.setData(data.migrations || []);
    customCommands = data.commands || [];
    customSeeders = data.seeders || [];
    commandsProvider.setData(
      customCommands,
      customSeeders,
      data.schedules || [],
      data.unregistered || [],
      data.events || [],
      data.jobs || []
    );
    if (statusBar) {
      const nt = currentTables.length;
      const nr = currentRoutes.length;
      statusBar.text = `$(database) Goravel: ${nt} tables · ${nr} routes`;
      statusBar.tooltip = `Goravel LSP active (${nt} tables, ${nr} routes, ${data.migrations?.length || 0} migrations). Click for Artisan & Server actions.`;
      statusBar.show();
    }
  } catch {
    // ignore if server is still starting
  }
}

async function start(ctx: vscode.ExtensionContext) {
  const cfg = vscode.workspace.getConfiguration("goravel");
  if (!cfg.get<boolean>("sql.enable", true)) return;
  const command = serverPath(ctx);
  if (!command) {
    vscode.window.showErrorMessage("goravel-lsp binary not found. Run `npm run build:server` or set goravel.server.path.");
    return;
  }
  const serverOptions: ServerOptions = { command, args: [] };
  const clientOptions: LanguageClientOptions = {
    documentSelector: [{ scheme: "file", language: "go" }],
    initializationOptions: {
      liveIntrospection: cfg.get<boolean>("sql.liveIntrospection", false),
    },
    synchronize: {
      fileEvents: vscode.workspace.createFileSystemWatcher(
        "**/{app/models,app/rules,app/console,app/events,app/listeners,app/jobs,app/providers,app/http/controllers,app/http/requests,app/http/middleware,bootstrap,config,routes,database/migrations*,database/seeders,resources/views,lang}/**/*"
      ),
    },
  };
  client = new LanguageClient("goravel", "Goravel", serverOptions, clientOptions);
  await client.start();
  await refreshOverview();
}

async function openFileAt(fileOrUri: string, line = 0, col = 0) {
  const uri = fileOrUri.startsWith("file://") ? vscode.Uri.parse(fileOrUri) : vscode.Uri.file(fileOrUri);
  const doc = await vscode.workspace.openTextDocument(uri);
  const pos = new vscode.Position(line, col);
  await vscode.window.showTextDocument(doc, { selection: new vscode.Range(pos, pos) });
}

async function openRouteTestRequest(route: { method: string; path: string; controller?: string; action?: string }) {
  const method = (route.method === "ANY" ? "GET" : route.method) || "GET";
  const urlPath = (route.path || "/").replace(/\{([^}]+)\}/g, "{{$1}}");
  const lines = [
    `### ${route.controller ? `${route.controller}.${route.action}` : `${method} ${route.path}`}`,
    `@host = http://localhost:3000`,
    `@token = `,
    ``,
    `${method} {{host}}${urlPath}`,
    `Authorization: Bearer {{token}}`,
  ];
  if (method === "POST" || method === "PUT" || method === "PATCH") {
    let body = "{\n  \n}";
    if (client && route.controller && route.action) {
      try {
        const sample = await client.sendRequest<{ body?: string }>("goravel/routeSample", {
          controller: route.controller,
          action: route.action,
        });
        if (sample?.body) body = sample.body;
      } catch {
        // fallback to empty JSON object
      }
    }
    lines.push(`Content-Type: application/json`, ``, body);
  }
  const doc = await vscode.workspace.openTextDocument({
    language: "http",
    content: lines.join("\n") + "\n",
  });
  await vscode.window.showTextDocument(doc, { preview: true });
}

export async function activate(ctx: vscode.ExtensionContext) {
  statusBar = vscode.window.createStatusBarItem(vscode.StatusBarAlignment.Right, 100);
  statusBar.command = "goravel.artisan.run";
  ctx.subscriptions.push(statusBar);

  ctx.subscriptions.push(
    vscode.window.registerTreeDataProvider("goravel.views.models", modelsProvider),
    vscode.window.registerTreeDataProvider("goravel.views.routes", routesProvider),
    vscode.window.registerTreeDataProvider("goravel.views.migrations", migrationsProvider),
    vscode.window.registerTreeDataProvider("goravel.views.commands", commandsProvider)
  );

  registerArtisanCommands(
    ctx,
    () => customCommands,
    () => customSeeders
  );

  ctx.subscriptions.push(
    vscode.workspace.onDidChangeConfiguration((e) => {
      if (e.affectsConfiguration("goravel.sql.liveIntrospection") && client) {
        const updated = vscode.workspace.getConfiguration("goravel");
        client.sendNotification("workspace/didChangeConfiguration", {
          settings: {
            goravel: {
              sql: {
                liveIntrospection: updated.get<boolean>("sql.liveIntrospection", false),
              },
            },
          },
        });
      }
    }),
    vscode.commands.registerCommand("goravel.restartServer", async () => {
      await client?.stop();
      client = undefined;
      await start(ctx);
    }),
    vscode.commands.registerCommand("goravel.refreshViews", async () => {
      await refreshOverview();
    }),
    vscode.commands.registerCommand("goravel.openLocation", async (file: string, line: number, col: number) => {
      await openFileAt(file, line, col);
    }),
    vscode.commands.registerCommand("goravel.jumpToRoute", async (uri: string, line: number, col: number) => {
      await openFileAt(uri, line, col);
    }),
    vscode.commands.registerCommand(
      "goravel.route.testRequest",
      async (arg: { method?: string; path?: string; controller?: string; action?: string; route?: RouteInfo }) => {
        const r = arg?.route ? arg.route : arg;
        if (!r || !r.path) return;
        await openRouteTestRequest({
          method: r.method || "GET",
          path: r.path,
          controller: r.controller,
          action: r.action,
        });
      }
    ),
    vscode.commands.registerCommand("goravel.route.openDefinition", async (node?: { route?: RouteInfo }) => {
      const loc = node?.route?.loc;
      if (loc?.file) {
        await openFileAt(loc.file, loc.line || 0, loc.col || 0);
      }
    }),
    vscode.commands.registerCommand("goravel.model.openMigration", async (node?: { table?: TableInfo }) => {
      const loc = node?.table?.migLoc?.file ? node.table.migLoc : node?.table?.loc;
      if (loc?.file) {
        await openFileAt(loc.file, loc.line || 0, loc.col || 0);
      } else {
        vscode.window.showInformationMessage("No migration file found for this table.");
      }
    }),
    vscode.commands.registerCommand("goravel.table.preview", async (node?: { table?: TableInfo }) => {
      const tname = node?.table?.name;
      if (!tname) return;
      await vscode.commands.executeCommand("goravel.previewQuery", `SELECT * FROM ${tname} LIMIT 20`);
    }),
    vscode.commands.registerCommand(
      "goravel.registerBootstrap",
      async (arg?: string | { item?: UnregisteredInfo; file?: string }) => {
        if (!client) return;
        const file = typeof arg === "string" ? arg : arg?.item?.file || arg?.file;
        if (!file) return;
        const res = await client.sendRequest<{
          ok: boolean;
          title?: string;
          error?: string;
          edit?: { changes?: Record<string, { range: { start: { line: number; character: number }; end: { line: number; character: number } }; newText: string }[]> };
        }>("goravel/registerBootstrap", { file });
        if (!res?.ok || !res.edit?.changes) {
          vscode.window.showWarningMessage(res?.error || "Could not auto-register component in bootstrap/.");
          return;
        }
        const wsEdit = new vscode.WorkspaceEdit();
        for (const [uriStr, edits] of Object.entries(res.edit.changes)) {
          const uri = vscode.Uri.parse(uriStr);
          for (const e of edits) {
            wsEdit.replace(
              uri,
              new vscode.Range(
                new vscode.Position(e.range.start.line, e.range.start.character),
                new vscode.Position(e.range.end.line, e.range.end.character)
              ),
              e.newText
            );
          }
        }
        await vscode.workspace.applyEdit(wsEdit);
        // Save modified bootstrap files so re-index picks it up immediately
        for (const uriStr of Object.keys(res.edit.changes)) {
          const doc = await vscode.workspace.openTextDocument(vscode.Uri.parse(uriStr));
          await doc.save();
        }
        vscode.window.showInformationMessage(res.title || "Registered component in bootstrap/.");
        await refreshOverview();
      }
    ),
    vscode.commands.registerCommand("goravel.showErDiagram", async () => {
      if (!client) return;
      const res = await client.sendRequest<{ mermaid?: string }>("goravel/erDiagram", {});
      const mermaid = res?.mermaid || "erDiagram\n";
      const panel = vscode.window.createWebviewPanel(
        "goravelErDiagram",
        "Goravel Schema ER Diagram",
        vscode.ViewColumn.One,
        { enableScripts: true }
      );
      panel.webview.html = renderErDiagramHtml(mermaid, currentTables);
      panel.webview.onDidReceiveMessage(async (msg) => {
        if (msg.type === "open" && msg.file) {
          await openFileAt(msg.file, msg.line || 0, 0);
        }
      });
    }),
    vscode.commands.registerCommand("goravel.showRouteMatrix", async () => {
      await refreshOverview();
      const panel = vscode.window.createWebviewPanel(
        "goravelRouteMatrix",
        "Goravel Route Matrix",
        vscode.ViewColumn.One,
        { enableScripts: true }
      );
      panel.webview.html = renderRouteMatrixHtml(currentRoutes);
      panel.webview.onDidReceiveMessage(async (msg) => {
        if (msg.type === "open" && msg.file) {
          await openFileAt(msg.file, msg.line || 0, 0);
        } else if (msg.type === "test" && msg.route) {
          await openRouteTestRequest(msg.route);
        }
      });
    }),
    vscode.commands.registerCommand("goravel.explainQuery", async (sql: string) => {
      if (!client) return;
      const res = await client.sendRequest<{ ok: boolean; output: string; error?: string }>("goravel/explainQuery", { sql });
      const doc = await vscode.workspace.openTextDocument({
        language: "sql",
        content: `-- EXPLAIN QUERY\n-- ${sql.replace(/\n/g, "\n-- ")}\n\n${res.output || res.error || "No output"}`,
      });
      await vscode.window.showTextDocument(doc, { preview: true });
    }),
    vscode.commands.registerCommand("goravel.previewQuery", async (sql: string) => {
      if (!client) return;
      const res = await client.sendRequest<{ ok: boolean; output: string; error?: string }>("goravel/previewQuery", { sql });
      if (!res.ok) {
        vscode.window.showErrorMessage(`Query preview failed: ${res.error || res.output}`);
        return;
      }
      let rows: Record<string, unknown>[] = [];
      try {
        rows = JSON.parse(res.output || "[]");
      } catch {
        rows = [];
      }
      const panel = vscode.window.createWebviewPanel(
        "goravelQueryPreview",
        "Goravel Query Preview",
        vscode.ViewColumn.Beside,
        { enableScripts: false }
      );
      panel.webview.html = renderQueryPreviewHtml(sql, rows);
    }),
    vscode.workspace.onDidSaveTextDocument(() => {
      setTimeout(() => refreshOverview(), 300);
    })
  );

  await start(ctx);
}

export async function deactivate() {
  await client?.stop();
}

function escapeHtml(s: string): string {
  return s
    .replace(/&/g, "&amp;")
    .replace(/</g, "&lt;")
    .replace(/>/g, "&gt;")
    .replace(/"/g, "&quot;");
}

function renderQueryPreviewHtml(sql: string, rows: Record<string, unknown>[]): string {
  const cols = rows.length > 0 ? Object.keys(rows[0]) : [];
  const header = cols.map((c) => `<th>${escapeHtml(c)}</th>`).join("");
  const body = rows
    .map(
      (r) =>
        `<tr>${cols
          .map((c) => `<td>${escapeHtml(r[c] === null || r[c] === undefined ? "NULL" : String(r[c]))}</td>`)
          .join("")}</tr>`
    )
    .join("");
  const table =
    cols.length > 0
      ? `<table><thead><tr>${header}</tr></thead><tbody>${body}</tbody></table>`
      : `<p><em>0 rows returned</em></p>`;
  return `<!DOCTYPE html>
<html>
<head>
<style>
  body { font-family: var(--vscode-font-family); color: var(--vscode-foreground); padding: 12px; }
  pre { background: var(--vscode-textBlockQuote-background); padding: 8px; border-radius: 4px; overflow-x: auto; }
  table { border-collapse: collapse; width: 100%; margin-top: 12px; font-size: 12px; }
  th, td { border: 1px solid var(--vscode-panel-border); padding: 6px 8px; text-align: left; }
  th { background: var(--vscode-editor-inactiveSelectionBackground); }
</style>
</head>
<body>
  <h3>Query Preview (${rows.length} row${rows.length === 1 ? "" : "s"}, max 20)</h3>
  <pre>${escapeHtml(sql)}</pre>
  ${table}
</body>
</html>`;
}

function renderErDiagramHtml(mermaidSrc: string, tables: TableInfo[]): string {
  const cards = tables
    .map((t) => {
      const cols = (t.columns || [])
        .map(
          (c) =>
            `<tr>
              <td><code>${escapeHtml(c.name)}</code></td>
              <td>${escapeHtml(c.sqlType || c.goType || "")}</td>
              <td>${c.foreignKey ? `FK → <code>${escapeHtml(c.foreignKey)}</code>` : ""}</td>
            </tr>`
        )
        .join("");
      const modelBtn = t.loc?.file
        ? `<button onclick="openFile('${escapeHtml(t.loc.file.replace(/\\/g, "\\\\"))}', ${t.loc.line || 0})">Model (${escapeHtml(t.model || t.name)})</button>`
        : "";
      const migBtn = t.migLoc?.file
        ? `<button onclick="openFile('${escapeHtml(t.migLoc.file.replace(/\\/g, "\\\\"))}', ${t.migLoc.line || 0})">Migration</button>`
        : "";
      return `<div class="card">
        <div class="card-header">
          <strong>${escapeHtml(t.name)}</strong>
          <span>${modelBtn} ${migBtn}</span>
        </div>
        <table>
          <thead><tr><th>Column</th><th>Type</th><th>Relation / FK</th></tr></thead>
          <tbody>${cols}</tbody>
        </table>
      </div>`;
    })
    .join("");

  return `<!DOCTYPE html>
<html>
<head>
<style>
  body { font-family: var(--vscode-font-family); color: var(--vscode-foreground); padding: 16px; }
  .grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(300px, 1fr)); gap: 12px; margin-bottom: 20px; }
  .card { border: 1px solid var(--vscode-panel-border); border-radius: 6px; padding: 10px; background: var(--vscode-editor-background); }
  .card-header { display: flex; justify-content: space-between; align-items: center; margin-bottom: 8px; }
  table { border-collapse: collapse; width: 100%; font-size: 12px; }
  th, td { border-bottom: 1px solid var(--vscode-panel-border); padding: 4px 6px; text-align: left; }
  button { background: var(--vscode-button-secondaryBackground); color: var(--vscode-button-secondaryForeground); border: none; border-radius: 3px; padding: 3px 8px; cursor: pointer; font-size: 11px; }
  button:hover { background: var(--vscode-button-secondaryHoverBackground); }
  pre { background: var(--vscode-textBlockQuote-background); padding: 12px; border-radius: 6px; overflow-x: auto; font-size: 12px; }
</style>
</head>
<body>
  <h2>Goravel Schema Overview (${tables.length} tables)</h2>
  <div class="grid">${cards}</div>
  <h3>Mermaid ER Diagram Source</h3>
  <pre>${escapeHtml(mermaidSrc)}</pre>
  <script>
    const vscode = acquireVsCodeApi();
    function openFile(file, line) {
      vscode.postMessage({ type: "open", file, line });
    }
  </script>
</body>
</html>`;
}

function renderRouteMatrixHtml(routes: RouteInfo[]): string {
  const rowsJson = JSON.stringify(routes).replace(/</g, "\\u003c");
  return `<!DOCTYPE html>
<html>
<head>
<style>
  body { font-family: var(--vscode-font-family); color: var(--vscode-foreground); padding: 16px; }
  .toolbar { display: flex; gap: 8px; margin-bottom: 12px; }
  input, select { background: var(--vscode-input-background); color: var(--vscode-input-foreground); border: 1px solid var(--vscode-input-border, var(--vscode-panel-border)); padding: 6px 10px; border-radius: 4px; }
  input { flex: 1; }
  table { border-collapse: collapse; width: 100%; font-size: 12px; }
  th, td { border: 1px solid var(--vscode-panel-border); padding: 6px 8px; text-align: left; }
  th { background: var(--vscode-editor-inactiveSelectionBackground); }
  .badge { display: inline-block; padding: 2px 6px; border-radius: 3px; font-weight: bold; font-size: 11px; background: var(--vscode-badge-background); color: var(--vscode-badge-foreground); }
  button { background: var(--vscode-button-secondaryBackground); color: var(--vscode-button-secondaryForeground); border: none; border-radius: 3px; padding: 3px 8px; cursor: pointer; font-size: 11px; margin-right: 4px; }
  button:hover { background: var(--vscode-button-secondaryHoverBackground); }
</style>
</head>
<body>
  <h2>Goravel Route Matrix (${routes.length} routes)</h2>
  <div class="toolbar">
    <input id="search" type="text" placeholder="Filter by path, handler, middleware, or module..." oninput="render()" />
    <select id="method" onchange="render()">
      <option value="">All Methods</option>
      <option value="GET">GET</option>
      <option value="POST">POST</option>
      <option value="PUT">PUT</option>
      <option value="PATCH">PATCH</option>
      <option value="DELETE">DELETE</option>
    </select>
  </div>
  <table>
    <thead>
      <tr>
        <th>Method</th>
        <th>Path</th>
        <th>Handler</th>
        <th>Middleware</th>
        <th>Module</th>
        <th>Actions</th>
      </tr>
    </thead>
    <tbody id="tbody"></tbody>
  </table>
  <script>
    const vscode = acquireVsCodeApi();
    const routes = ${rowsJson};
    function esc(s) {
      return String(s || "").replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;").replace(/"/g, "&quot;");
    }
    function openLoc(idx, kind) {
      const r = routes[idx];
      const loc = kind === "ctrl" && r.ctrlLoc && r.ctrlLoc.file ? r.ctrlLoc : r.loc;
      if (loc && loc.file) vscode.postMessage({ type: "open", file: loc.file, line: loc.line || 0 });
    }
    function testRoute(idx) {
      vscode.postMessage({ type: "test", route: routes[idx] });
    }
    function render() {
      const q = document.getElementById("search").value.toLowerCase();
      const m = document.getElementById("method").value;
      const tbody = document.getElementById("tbody");
      tbody.innerHTML = routes
        .map((r, idx) => ({ r, idx }))
        .filter(({ r }) => {
          if (m && r.method !== m) return false;
          if (!q) return true;
          const hay = [r.method, r.path, r.handler, (r.middlewares || []).join(" "), r.module].join(" ").toLowerCase();
          return hay.includes(q);
        })
        .map(({ r, idx }) => \`<tr>
          <td><span class="badge">\${esc(r.method)}</span></td>
          <td><code>\${esc(r.path)}</code></td>
          <td>\${esc(r.handler)}</td>
          <td>\${esc((r.middlewares || []).join(", "))}</td>
          <td>\${esc(r.module)}</td>
          <td>
            <button onclick="testRoute(\${idx})">Test</button>
            <button onclick="openLoc(\${idx}, 'ctrl')">Controller</button>
            <button onclick="openLoc(\${idx}, 'route')">Route</button>
          </td>
        </tr>\`)
        .join("");
    }
    render();
  </script>
</body>
</html>`;
}
