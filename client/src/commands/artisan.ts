import * as fs from "fs";
import * as path from "path";
import * as vscode from "vscode";
import type { CommandInfo, SeederInfo } from "../views/explorers";

const ARTISAN_PRESETS: { label: string; description: string; cmd: string; prompt?: string }[] = [
  { label: "make:model", description: "Create a new Eloquent/GORM model class", cmd: "make:model", prompt: "Model name (e.g. User or GeoCity)" },
  { label: "make:controller", description: "Create a new controller class", cmd: "make:controller", prompt: "Controller name (e.g. CityController)" },
  { label: "make:migration", description: "Create a new migration file", cmd: "make:migration", prompt: "Migration name (e.g. create_cities_table)" },
  { label: "make:request", description: "Create a new form request class", cmd: "make:request", prompt: "Request name (e.g. CreateCityRequest)" },
  { label: "make:rule", description: "Create a new validation rule", cmd: "make:rule", prompt: "Rule name (e.g. Uppercase)" },
  { label: "make:seeder", description: "Create a new seeder class", cmd: "make:seeder", prompt: "Seeder name (e.g. CitySeeder)" },
  { label: "make:middleware", description: "Create a new middleware class", cmd: "make:middleware", prompt: "Middleware name" },
  { label: "make:command", description: "Create a new Artisan command", cmd: "make:command", prompt: "Command name" },
  { label: "make:event", description: "Create a new event class", cmd: "make:event", prompt: "Event name (e.g. UserRegistered)" },
  { label: "make:listener", description: "Create a new event listener class", cmd: "make:listener", prompt: "Listener name (e.g. SendWelcomeEmail)" },
  { label: "make:job", description: "Create a new queue job class", cmd: "make:job", prompt: "Job name (e.g. ProcessPodcast)" },
  { label: "make:observer", description: "Create a new model observer class", cmd: "make:observer", prompt: "Observer name (e.g. UserObserver)" },
  { label: "make:policy", description: "Create a new authorization policy class", cmd: "make:policy", prompt: "Policy name (e.g. PostPolicy)" },
  { label: "make:provider", description: "Create a new service provider class", cmd: "make:provider", prompt: "Provider name (e.g. PaymentServiceProvider)" },
  { label: "make:factory", description: "Create a new model factory", cmd: "make:factory", prompt: "Factory name (e.g. UserFactory)" },
  { label: "make:test", description: "Create a new test class", cmd: "make:test", prompt: "Test name (e.g. UserTest)" },
  { label: "make:view", description: "Create a new view template file", cmd: "make:view", prompt: "View name (e.g. admin/dashboard.tmpl)" },
  { label: "make:filter", description: "Create a new filter class", cmd: "make:filter", prompt: "Filter name" },
  { label: "package:install", description: "Install a Goravel package or driver", cmd: "package:install", prompt: "Package name or facade" },
  { label: "migrate", description: "Run the database migrations", cmd: "migrate" },
  { label: "migrate:rollback", description: "Rollback the last database migration", cmd: "migrate:rollback" },
  { label: "migrate:status", description: "Show the status of each migration", cmd: "migrate:status" },
  { label: "db:seed", description: "Seed the database with records", cmd: "db:seed" },
  { label: "route:list", description: "List all registered routes", cmd: "route:list" },
  { label: "jwt:secret", description: "Set the JWTAuth secret key used to sign tokens", cmd: "jwt:secret" },
  { label: "key:generate", description: "Set the application key", cmd: "key:generate" },
];

function getTerminal(): vscode.Terminal {
  const existing = vscode.window.terminals.find((t) => t.name === "Goravel Artisan");
  if (existing) return existing;
  return vscode.window.createTerminal("Goravel Artisan");
}

function getArtisanPrefix(): string {
  const cfg = vscode.workspace.getConfiguration("goravel").get<string>("artisan.command");
  return cfg && cfg.trim() ? cfg.trim() : "go run . artisan";
}

export function runArtisanInTerminal(args: string) {
  const term = getTerminal();
  term.show(true);
  term.sendText(`${getArtisanPrefix()} ${args}`);
}

async function promptAndRun(cmd: string, prompt: string) {
  const name = await vscode.window.showInputBox({
    title: `Goravel Artisan: ${cmd}`,
    prompt,
    ignoreFocusOut: true,
  });
  if (!name || !name.trim()) return;
  runArtisanInTerminal(`${cmd} ${name.trim()}`);
}

export function registerArtisanCommands(
  ctx: vscode.ExtensionContext,
  getCustomCommands?: () => CommandInfo[],
  getSeeders?: () => SeederInfo[]
) {
  ctx.subscriptions.push(
    vscode.commands.registerCommand("goravel.artisan.runCommand", (sig: string) => {
      if (sig && sig.trim()) {
        runArtisanInTerminal(sig.trim());
      }
    }),
    vscode.commands.registerCommand("goravel.artisan.run", async () => {
      const customCmds: { label: string; description: string; cmd: string; prompt?: string }[] = (
        getCustomCommands?.() || []
      ).map((c) => ({
        label: `$(terminal) ${c.signature}`,
        description: c.description || `${c.structName} (app/console/commands)`,
        cmd: c.signature,
      }));
      const picked = await vscode.window.showQuickPick(
        [
          ...customCmds,
          ...ARTISAN_PRESETS,
          { label: "$(terminal) Custom command...", description: "Enter custom artisan arguments", cmd: "__custom__" },
        ],
        { placeHolder: "Select a Goravel Artisan command to run" }
      );
      if (!picked) return;
      if (picked.cmd === "__custom__") {
        const custom = await vscode.window.showInputBox({
          title: "Goravel Artisan",
          prompt: "Enter arguments after `go run . artisan`",
          placeHolder: "make:model City",
        });
        if (custom && custom.trim()) {
          runArtisanInTerminal(custom.trim());
        }
        return;
      }
      if (picked.prompt) {
        await promptAndRun(picked.cmd, picked.prompt);
      } else {
        runArtisanInTerminal(picked.cmd);
      }
    }),
    vscode.commands.registerCommand("goravel.artisan.makeModel", () =>
      promptAndRun("make:model", "Model name (e.g. RefGeoCity)")
    ),
    vscode.commands.registerCommand("goravel.artisan.makeController", () =>
      promptAndRun("make:controller", "Controller name (e.g. CityController)")
    ),
    vscode.commands.registerCommand("goravel.artisan.makeMigration", () =>
      promptAndRun("make:migration", "Migration name (e.g. create_cities_table)")
    ),
    vscode.commands.registerCommand("goravel.artisan.makeRequest", () =>
      promptAndRun("make:request", "Form Request name (e.g. CreateCityRequest)")
    ),
    vscode.commands.registerCommand("goravel.artisan.makeRule", () =>
      promptAndRun("make:rule", "Validation Rule name (e.g. AlphaDash)")
    ),
    vscode.commands.registerCommand("goravel.artisan.makeSeeder", () =>
      promptAndRun("make:seeder", "Seeder name (e.g. CitySeeder)")
    ),
    vscode.commands.registerCommand("goravel.artisan.makeEvent", () =>
      promptAndRun("make:event", "Event name (e.g. UserRegistered)")
    ),
    vscode.commands.registerCommand("goravel.artisan.makeListener", () =>
      promptAndRun("make:listener", "Listener name (e.g. SendWelcomeEmail)")
    ),
    vscode.commands.registerCommand("goravel.artisan.makeJob", () =>
      promptAndRun("make:job", "Job name (e.g. ProcessPodcast)")
    ),
    vscode.commands.registerCommand("goravel.artisan.makeObserver", () =>
      promptAndRun("make:observer", "Observer name (e.g. UserObserver)")
    ),
    vscode.commands.registerCommand("goravel.artisan.makePolicy", () =>
      promptAndRun("make:policy", "Policy name (e.g. PostPolicy)")
    ),
    vscode.commands.registerCommand("goravel.artisan.makeProvider", () =>
      promptAndRun("make:provider", "Provider name (e.g. PaymentServiceProvider)")
    ),
    vscode.commands.registerCommand("goravel.artisan.makeFactory", () =>
      promptAndRun("make:factory", "Factory name (e.g. UserFactory)")
    ),
    vscode.commands.registerCommand("goravel.artisan.makeTest", () =>
      promptAndRun("make:test", "Test name (e.g. UserTest)")
    ),
    vscode.commands.registerCommand("goravel.artisan.makeView", () =>
      promptAndRun("make:view", "View name (e.g. admin/dashboard.tmpl)")
    ),
    vscode.commands.registerCommand("goravel.artisan.migrate", () => runArtisanInTerminal("migrate")),
    vscode.commands.registerCommand("goravel.artisan.migrateRollback", () => runArtisanInTerminal("migrate:rollback")),
    vscode.commands.registerCommand("goravel.artisan.seedSpecific", async () => {
      const seeders = getSeeders?.() || [];
      if (seeders.length === 0) {
        const name = await vscode.window.showInputBox({
          title: "Goravel Artisan: db:seed",
          prompt: "Seeder signature (leave blank to run DatabaseSeeder)",
        });
        if (name === undefined) return;
        runArtisanInTerminal(name.trim() ? `db:seed --seeder=${name.trim()}` : "db:seed");
        return;
      }
      const picked = await vscode.window.showQuickPick(
        [
          { label: "$(database) All (db:seed)", description: "Run default DatabaseSeeder", sig: "" },
          ...seeders.map((s) => ({
            label: `$(play) ${s.signature}`,
            description: s.name,
            sig: s.signature,
          })),
        ],
        { placeHolder: "Select a seeder to run" }
      );
      if (!picked) return;
      runArtisanInTerminal(picked.sig ? `db:seed --seeder=${picked.sig}` : "db:seed");
    }),
    vscode.commands.registerCommand("goravel.logs.open", async () => {
      const ws = vscode.workspace.workspaceFolders?.[0]?.uri.fsPath;
      if (!ws) return;
      const logPath = path.join(ws, "storage", "logs", "goravel.log");
      if (!fs.existsSync(logPath)) {
        vscode.window.showInformationMessage(`Log file not found at ${logPath}`);
        return;
      }
      const doc = await vscode.workspace.openTextDocument(vscode.Uri.file(logPath));
      const editor = await vscode.window.showTextDocument(doc, { preview: false });
      const lastLine = Math.max(0, doc.lineCount - 1);
      const pos = new vscode.Position(lastLine, 0);
      editor.revealRange(new vscode.Range(pos, pos), vscode.TextEditorRevealType.Default);
    }),
    vscode.commands.registerCommand("goravel.test.run", async (dir?: string, testName?: string) => {
      const activeFile = vscode.window.activeTextEditor?.document.uri.fsPath;
      const ws = vscode.workspace.workspaceFolders?.[0]?.uri.fsPath || dir || (activeFile ? path.dirname(activeFile) : "");
      if (!ws) {
        vscode.window.showErrorMessage("No workspace folder open.");
        return;
      }
      if (!dir) {
        dir = activeFile && activeFile.endsWith(".go") ? path.dirname(activeFile) : ws;
      }
      if (testName === undefined) {
        const input = await vscode.window.showInputBox({
          prompt: "Go test filter (-run regex, leave empty to run all tests in package)",
          placeHolder: "TestExample",
        });
        if (input === undefined) return;
        testName = input.trim();
      }
      const hasEnvTesting = fs.existsSync(path.join(ws, ".env.testing"));
      const rel = path.relative(ws, dir) || ".";
      const pkg = rel.startsWith(".") ? rel : `./${rel}`;
      const args = ["test", "-v"];
      if (testName) {
        args.push("-run", testName);
      }
      args.push(pkg === "." && !activeFile ? "./..." : pkg);
      const exec = new vscode.ShellExecution("go", args, {
        cwd: ws,
        env: hasEnvTesting ? { APP_ENV: "testing" } : undefined,
      });
      const task = new vscode.Task(
        { type: "goravel-test" },
        vscode.TaskScope.Workspace,
        testName ? `Go Test: ${testName}` : "Go Test",
        "goravel",
        exec
      );
      await vscode.tasks.executeTask(task);
    })
  );
}

