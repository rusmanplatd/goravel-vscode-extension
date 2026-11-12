import * as fs from "fs";
import * as path from "path";
import * as vscode from "vscode";
import { LanguageClient, LanguageClientOptions, ServerOptions } from "vscode-languageclient/node";

let client: LanguageClient | undefined;

function serverPath(ctx: vscode.ExtensionContext): string | undefined {
  const cfg = vscode.workspace.getConfiguration("goravel").get<string>("server.path");
  if (cfg && fs.existsSync(cfg)) return cfg;
  const exe = process.platform === "win32" ? "goravel-lsp.exe" : "goravel-lsp";
  const bundled = path.join(ctx.extensionPath, "bin", exe);
  return fs.existsSync(bundled) ? bundled : undefined;
}

async function start(ctx: vscode.ExtensionContext) {
  if (!vscode.workspace.getConfiguration("goravel").get<boolean>("sql.enable", true)) return;
  const command = serverPath(ctx);
  if (!command) {
    vscode.window.showErrorMessage("goravel-lsp binary not found. Run `npm run build:server` or set goravel.server.path.");
    return;
  }
  const serverOptions: ServerOptions = { command, args: [] };
  const clientOptions: LanguageClientOptions = {
    documentSelector: [{ scheme: "file", language: "go" }],
    synchronize: {
      fileEvents: vscode.workspace.createFileSystemWatcher("**/{app/models,database/migrations*}/**/*.go"),
    },
  };
  client = new LanguageClient("goravel", "Goravel", serverOptions, clientOptions);
  await client.start();
}

export async function activate(ctx: vscode.ExtensionContext) {
  ctx.subscriptions.push(
    vscode.commands.registerCommand("goravel.restartServer", async () => {
      await client?.stop();
      client = undefined;
      await start(ctx);
    })
  );
  await start(ctx);
}

export async function deactivate() {
  await client?.stop();
}
