import * as vscode from "vscode";

export interface Loc {
  file: string;
  line: number;
  col: number;
}

export interface ColumnInfo {
  name: string;
  goName: string;
  goType: string;
  sqlType: string;
  doc: string;
  nullable: boolean;
  foreignKey?: string;
  loc: Loc;
  migLoc: Loc;
}

export interface RelationInfo {
  name: string;
  targetModel: string;
  targetTable: string;
  foreignKey: string;
  references: string;
  loc: Loc;
}

export interface TableInfo {
  name: string;
  model: string;
  columns: ColumnInfo[];
  relations?: RelationInfo[];
  loc: Loc;
  migLoc: Loc;
}

export interface RouteInfo {
  method: string;
  path: string;
  handler: string;
  controller: string;
  action: string;
  module: string;
  loc: Loc;
  ctrlLoc: Loc;
}

export interface MigrationInfo {
  name: string;
  folder: string;
  table: string;
  loc: Loc;
}

export interface OverviewData {
  tables: TableInfo[];
  routes: RouteInfo[];
  migrations: MigrationInfo[];
}

function openCmd(loc: Loc | undefined, title = "Open Definition"): vscode.Command | undefined {
  if (!loc || !loc.file) return undefined;
  return {
    title,
    command: "goravel.openLocation",
    arguments: [loc.file, loc.line || 0, loc.col || 0],
  };
}

// ---- Models & Schema Explorer ----

type ModelNode =
  | { kind: "table"; table: TableInfo }
  | { kind: "column"; table: TableInfo; col: ColumnInfo }
  | { kind: "relation"; table: TableInfo; rel: RelationInfo };

export class ModelsTreeProvider implements vscode.TreeDataProvider<ModelNode> {
  private _onDidChangeTreeData = new vscode.EventEmitter<ModelNode | undefined | void>();
  readonly onDidChangeTreeData = this._onDidChangeTreeData.event;
  private tables: TableInfo[] = [];

  setData(tables: TableInfo[]) {
    this.tables = tables || [];
    this._onDidChangeTreeData.fire();
  }

  getTreeItem(el: ModelNode): vscode.TreeItem {
    if (el.kind === "table") {
      const t = el.table;
      const item = new vscode.TreeItem(
        t.name,
        (t.columns?.length || 0) > 0 ? vscode.TreeItemCollapsibleState.Collapsed : vscode.TreeItemCollapsibleState.None
      );
      item.description = t.model ? `models.${t.model} (${t.columns?.length || 0} cols)` : `${t.columns?.length || 0} cols`;
      item.iconPath = new vscode.ThemeIcon("database");
      item.command = openCmd(t.loc?.file ? t.loc : t.migLoc);
      return item;
    }
    if (el.kind === "column") {
      const c = el.col;
      const item = new vscode.TreeItem(c.name, vscode.TreeItemCollapsibleState.None);
      const typ = c.sqlType || c.goType || "";
      const nullStr = c.nullable ? "NULL" : "NOT NULL";
      const fkStr = c.foreignKey ? ` → ${c.foreignKey}` : "";
      item.description = `${typ} ${nullStr}${fkStr}`;
      item.tooltip = c.doc ? `${c.name}: ${c.doc}` : `${c.name} (${typ})`;
      item.iconPath = new vscode.ThemeIcon(c.foreignKey ? "key" : "symbol-field");
      item.command = openCmd(c.loc?.file ? c.loc : c.migLoc);
      return item;
    }
    const r = el.rel;
    const item = new vscode.TreeItem(r.name || r.targetTable, vscode.TreeItemCollapsibleState.None);
    item.description = `→ ${r.targetModel || r.targetTable} (${r.foreignKey})`;
    item.iconPath = new vscode.ThemeIcon("references");
    item.command = openCmd(r.loc);
    return item;
  }

  getChildren(el?: ModelNode): ModelNode[] {
    if (!el) {
      return this.tables.map((table) => ({ kind: "table", table }));
    }
    if (el.kind === "table") {
      const cols: ModelNode[] = (el.table.columns || []).map((col) => ({ kind: "column", table: el.table, col }));
      const rels: ModelNode[] = (el.table.relations || [])
        .filter((r) => r.name)
        .map((rel) => ({ kind: "relation", table: el.table, rel }));
      return [...cols, ...rels];
    }
    return [];
  }
}

// ---- Routes Explorer ----

type RouteNode =
  | { kind: "module"; module: string; routes: RouteInfo[] }
  | { kind: "route"; route: RouteInfo };

export class RoutesTreeProvider implements vscode.TreeDataProvider<RouteNode> {
  private _onDidChangeTreeData = new vscode.EventEmitter<RouteNode | undefined | void>();
  readonly onDidChangeTreeData = this._onDidChangeTreeData.event;
  private routes: RouteInfo[] = [];

  setData(routes: RouteInfo[]) {
    this.routes = routes || [];
    this._onDidChangeTreeData.fire();
  }

  getTreeItem(el: RouteNode): vscode.TreeItem {
    if (el.kind === "module") {
      const item = new vscode.TreeItem(el.module, vscode.TreeItemCollapsibleState.Collapsed);
      item.description = `${el.routes.length} routes`;
      item.iconPath = new vscode.ThemeIcon("folder-library");
      return item;
    }
    const r = el.route;
    const item = new vscode.TreeItem(`${r.method} ${r.path}`, vscode.TreeItemCollapsibleState.None);
    item.description = r.handler;
    item.iconPath = new vscode.ThemeIcon("globe");
    item.command = openCmd(r.ctrlLoc?.file ? r.ctrlLoc : r.loc);
    return item;
  }

  getChildren(el?: RouteNode): RouteNode[] {
    if (!el) {
      const groups = new Map<string, RouteInfo[]>();
      for (const r of this.routes) {
        const mod = r.module || "routes";
        if (!groups.has(mod)) groups.set(mod, []);
        groups.get(mod)!.push(r);
      }
      return Array.from(groups.entries())
        .sort((a, b) => a[0].localeCompare(b[0]))
        .map(([module, routes]) => ({ kind: "module", module, routes }));
    }
    if (el.kind === "module") {
      return el.routes.map((route) => ({ kind: "route", route }));
    }
    return [];
  }
}

// ---- Migrations Explorer ----

type MigrationNode =
  | { kind: "folder"; folder: string; items: MigrationInfo[] }
  | { kind: "migration"; item: MigrationInfo };

export class MigrationsTreeProvider implements vscode.TreeDataProvider<MigrationNode> {
  private _onDidChangeTreeData = new vscode.EventEmitter<MigrationNode | undefined | void>();
  readonly onDidChangeTreeData = this._onDidChangeTreeData.event;
  private migrations: MigrationInfo[] = [];

  setData(migrations: MigrationInfo[]) {
    this.migrations = migrations || [];
    this._onDidChangeTreeData.fire();
  }

  getTreeItem(el: MigrationNode): vscode.TreeItem {
    if (el.kind === "folder") {
      const item = new vscode.TreeItem(el.folder, vscode.TreeItemCollapsibleState.Collapsed);
      item.description = `${el.items.length} files`;
      item.iconPath = new vscode.ThemeIcon("folder");
      return item;
    }
    const m = el.item;
    const item = new vscode.TreeItem(m.name, vscode.TreeItemCollapsibleState.None);
    item.description = m.table ? `→ ${m.table}` : "";
    item.iconPath = new vscode.ThemeIcon("file-code");
    item.command = openCmd(m.loc);
    return item;
  }

  getChildren(el?: MigrationNode): MigrationNode[] {
    if (!el) {
      const groups = new Map<string, MigrationInfo[]>();
      for (const m of this.migrations) {
        const f = m.folder || "migrations";
        if (!groups.has(f)) groups.set(f, []);
        groups.get(f)!.push(m);
      }
      return Array.from(groups.entries())
        .sort((a, b) => a[0].localeCompare(b[0]))
        .map(([folder, items]) => ({ kind: "folder", folder, items }));
    }
    if (el.kind === "folder") {
      return el.items.map((item) => ({ kind: "migration", item }));
    }
    return [];
  }
}

