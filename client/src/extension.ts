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
  RoutesTreeProvider,
  SeederInfo,
} from "./views/explorers";

let client: LanguageClient | undefined;
let statusBar: vscode.StatusBarItem | undefined;
let customCommands: CommandInfo[] = [];
let customSeeders: SeederInfo[] = [];
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
    modelsProvider.setData(data.tables || []);
    routesProvider.setData(data.routes || []);
    migrationsProvider.setData(data.migrations || []);
    customCommands = data.commands || [];
    customSeeders = data.seeders || [];
    commandsProvider.setData(
      customCommands,
      customSeeders,
      data.schedules || [],
      data.unregistered || []
    );
    if (statusBar) {
      const nt = data.tables?.length || 0;
      const nr = data.routes?.length || 0;
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
        "**/{app/models,app/rules,app/console,app/http/controllers,app/http/requests,app/http/middleware,bootstrap,config,routes,database/migrations*,database/seeders,resources/views}/**/*"
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
      async (route: { method: string; path: string; controller?: string; action?: string }) => {
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
    ),
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
