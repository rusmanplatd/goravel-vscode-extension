import * as fs from "fs";
import * as path from "path";
import * as vscode from "vscode";
import { LanguageClient, LanguageClientOptions, ServerOptions } from "vscode-languageclient/node";
import { registerArtisanCommands } from "./commands/artisan";
import { MigrationsTreeProvider, ModelsTreeProvider, OverviewData, RoutesTreeProvider } from "./views/explorers";

let client: LanguageClient | undefined;
let statusBar: vscode.StatusBarItem | undefined;
const modelsProvider = new ModelsTreeProvider();
const routesProvider = new RoutesTreeProvider();
const migrationsProvider = new MigrationsTreeProvider();

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
        "**/{app/models,app/rules,app/http/controllers,app/http/requests,config,routes,database/migrations*}/**/*.go"
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
    vscode.window.registerTreeDataProvider("goravel.views.migrations", migrationsProvider)
  );

  registerArtisanCommands(ctx);

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
    vscode.commands.registerCommand("goravel.explainQuery", async (sql: string) => {
      if (!client) return;
      const res = await client.sendRequest<{ ok: boolean; output: string; error?: string }>("goravel/explainQuery", { sql });
      const doc = await vscode.workspace.openTextDocument({
        language: "sql",
        content: `-- EXPLAIN QUERY\n-- ${sql.replace(/\n/g, "\n-- ")}\n\n${res.output || res.error || "No output"}`,
      });
      await vscode.window.showTextDocument(doc, { preview: true });
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
