import * as vscode from "vscode";
import type { CommandInfo } from "../views/explorers";

const ARTISAN_PRESETS: { label: string; description: string; cmd: string; prompt?: string }[] = [
  { label: "make:model", description: "Create a new Eloquent/GORM model class", cmd: "make:model", prompt: "Model name (e.g. User or GeoCity)" },
  { label: "make:controller", description: "Create a new controller class", cmd: "make:controller", prompt: "Controller name (e.g. CityController)" },
  { label: "make:migration", description: "Create a new migration file", cmd: "make:migration", prompt: "Migration name (e.g. create_cities_table)" },
  { label: "make:request", description: "Create a new form request class", cmd: "make:request", prompt: "Request name (e.g. CreateCityRequest)" },
  { label: "make:rule", description: "Create a new validation rule", cmd: "make:rule", prompt: "Rule name (e.g. Uppercase)" },
  { label: "make:seeder", description: "Create a new seeder class", cmd: "make:seeder", prompt: "Seeder name (e.g. CitySeeder)" },
  { label: "make:middleware", description: "Create a new middleware class", cmd: "make:middleware", prompt: "Middleware name" },
  { label: "make:command", description: "Create a new Artisan command", cmd: "make:command", prompt: "Command name" },
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

export function runArtisanInTerminal(args: string) {
  const term = getTerminal();
  term.show(true);
  term.sendText(`go run . artisan ${args}`);
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
  getCustomCommands?: () => CommandInfo[]
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
    vscode.commands.registerCommand("goravel.artisan.migrate", () => runArtisanInTerminal("migrate")),
    vscode.commands.registerCommand("goravel.artisan.migrateRollback", () => runArtisanInTerminal("migrate:rollback"))
  );
}

