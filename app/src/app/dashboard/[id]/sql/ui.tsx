"use client";

import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState, type RefObject } from "react";
import type { SqlFolder, SqlRun, SqlSnippet } from "@/db/schema";
import type { SqlResult } from "@/lib/sql";
import {
  createFolder,
  createSnippet,
  deleteFolder,
  deleteSnippet,
  duplicateSnippet,
  folderContents,
  getSidebarData,
  getSqlHistory,
  moveFolder,
  moveSnippet,
  renameFolder,
  renameSnippet,
  searchSnippets,
  toggleFavorite,
  updateSnippet,
  type ActionResult,
} from "./actions";
import { descendants, folderPaths } from "./folders";

// CodeMirror 6 is bundled (no CDN). Its runtime styles get the page's CSP nonce.
import { autocompletion, completionKeymap } from "@codemirror/autocomplete";
import { defaultKeymap, history, historyKeymap } from "@codemirror/commands";
import { sql as sqlLang, SQLite } from "@codemirror/lang-sql";
import { defaultHighlightStyle, syntaxHighlighting } from "@codemirror/language";
import { EditorState } from "@codemirror/state";
import { oneDark } from "@codemirror/theme-one-dark";
import { EditorView, highlightActiveLine, keymap, lineNumbers, placeholder } from "@codemirror/view";

type SchemaTable = { name: string; kind: string; columns: { name: string; type: string }[] };
type Sidebar = { folders: SqlFolder[]; snippets: SqlSnippet[] };
type EditorApi = { textToRun: () => string; focus: () => void };
type Open = { id: string; name: string; folderId: string | null; saved: string } | null;

/* ========== Editor ========== */

function SqlEditor({
  value,
  onChange,
  schema,
  onRun,
  api,
  nonce,
}: {
  value: string;
  onChange: (v: string) => void;
  schema: SchemaTable[];
  onRun: RefObject<() => void>;
  api: RefObject<EditorApi | null>;
  nonce: string;
}) {
  const box = useRef<HTMLDivElement>(null);
  const view = useRef<EditorView | null>(null);
  const change = useRef(onChange);
  change.current = onChange;

  useEffect(() => {
    if (!box.current) return;
    const dark = window.matchMedia?.("(prefers-color-scheme: dark)").matches;
    const tables: Record<string, string[]> = {};
    for (const t of schema) tables[t.name] = t.columns.map((c) => c.name);
    const v = new EditorView({
      parent: box.current,
      state: EditorState.create({
        doc: value,
        extensions: [
          EditorView.cspNonce.of(nonce),
          lineNumbers(),
          highlightActiveLine(),
          history(),
          // Run first so Ctrl/Cmd+Enter isn't taken by "insert new line".
          keymap.of([{ key: "Mod-Enter", preventDefault: true, run: () => (onRun.current(), true) }]),
          keymap.of([...completionKeymap, ...defaultKeymap, ...historyKeymap]),
          sqlLang({ dialect: SQLite, schema: tables, upperCaseKeywords: true }),
          autocompletion(),
          placeholder("SELECT * FROM users LIMIT 20"),
          dark ? oneDark : syntaxHighlighting(defaultHighlightStyle),
          EditorView.theme({ "&": { height: "100%" }, ".cm-scroller": { fontFamily: "ui-monospace, monospace" } }),
          EditorView.updateListener.of((u) => {
            if (u.docChanged) change.current(u.state.doc.toString());
          }),
          EditorView.contentAttributes.of({ "aria-label": "SQL query", "data-testid": "sql-input" }),
        ],
      }),
    });
    view.current = v;
    api.current = {
      textToRun: () => {
        const sel = v.state.selection.main;
        return sel.empty ? v.state.doc.toString() : v.state.sliceDoc(sel.from, sel.to);
      },
      focus: () => v.focus(),
    };
    return () => v.destroy();
    // The editor is created once; schema and nonce don't change while the page is open.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  // Text set from outside (opening a snippet or a history entry) replaces the document.
  useEffect(() => {
    const v = view.current;
    if (v && v.state.doc.toString() !== value) v.dispatch({ changes: { from: 0, to: v.state.doc.length, insert: value } });
  }, [value]);

  return <div ref={box} className="h-full overflow-hidden rounded-lg border border-border bg-card text-sm" />;
}

/* ========== Results ========== */

const cellText = (v: unknown) => (v === null ? "" : typeof v === "object" ? JSON.stringify(v) : String(v));

function toCsv(columns: string[], rows: unknown[][]) {
  const q = (s: string) => (/[",\n\r]/.test(s) ? `"${s.replace(/"/g, '""')}"` : s);
  return [columns.map(q).join(","), ...rows.map((r) => r.map((v) => q(cellText(v))).join(","))].join("\r\n") + "\r\n";
}

function Results({ result, running }: { result: SqlResult | null; running: boolean }) {
  if (running) return <p className="text-sm text-muted">Running…</p>;
  if (!result) return <p className="text-sm text-muted">Run a query to see results here.</p>;
  if ("error" in result) {
    return (
      <p data-testid="sql-error" className="text-sm text-red-600">
        {result.error}
        {result.elapsedMs !== undefined && <span className="ml-2 text-xs text-muted">({result.elapsedMs} ms)</span>}
      </p>
    );
  }
  const { columns, rows, rowCount, truncated, elapsedMs } = result;
  const download = () => {
    const url = URL.createObjectURL(new Blob([toCsv(columns, rows)], { type: "text/csv" }));
    const a = document.createElement("a");
    a.href = url;
    a.download = "query-results.csv";
    a.click();
    setTimeout(() => URL.revokeObjectURL(url), 1000);
  };
  return (
    <div className="flex h-full flex-col gap-2" data-testid="sql-results">
      <div className="flex items-center gap-3 text-xs text-muted">
        <span data-testid="sql-rowcount">
          {rowCount} row{rowCount === 1 ? "" : "s"}
          {truncated && " (only the first 1,000 are shown)"} · {elapsedMs} ms
        </span>
        {rows.length > 0 && (
          <button className="underline hover:text-foreground" onClick={download}>
            Download CSV
          </button>
        )}
      </div>
      <div className="min-h-0 flex-1 overflow-auto rounded border border-border">
        <table className="w-full text-xs">
          <thead className="sticky top-0 bg-card">
            <tr>
              {columns.map((c, i) => (
                <th key={i} className="border-b border-border px-2 py-1.5 text-left font-medium whitespace-nowrap">
                  {c}
                </th>
              ))}
            </tr>
          </thead>
          <tbody>
            {rows.map((row, i) => (
              <tr key={i} className="border-b border-border last:border-0">
                {row.map((v, j) => (
                  <td key={j} className="max-w-[24rem] truncate px-2 py-1 font-mono" title={cellText(v)}>
                    {v === null ? <span className="text-muted italic">null</span> : cellText(v)}
                  </td>
                ))}
              </tr>
            ))}
            {rows.length === 0 && (
              <tr>
                <td colSpan={Math.max(columns.length, 1)} className="px-2 py-4 text-center text-muted">
                  No rows.
                </td>
              </tr>
            )}
          </tbody>
        </table>
      </div>
    </div>
  );
}

/* ========== Side panels ========== */

type MenuAt =
  | { x: number; y: number; kind: "root" }
  | { x: number; y: number; kind: "folder"; f: SqlFolder }
  | { x: number; y: number; kind: "snippet"; s: SqlSnippet };
type MenuFor = MenuAt extends infer M ? (M extends MenuAt ? Omit<M, "x" | "y"> : never) : never;

/** A small menu at a screen position (right-click, or the ⋯ / + buttons). Closes on Escape or a click outside. */
function ContextMenu({ at, onClose, children }: { at: { x: number; y: number }; onClose: () => void; children: React.ReactNode }) {
  const box = useRef<HTMLDivElement>(null);
  const [pos, setPos] = useState(at);
  useLayoutEffect(() => {
    const el = box.current;
    if (!el) return;
    const r = el.getBoundingClientRect();
    setPos({ x: Math.max(4, Math.min(at.x, window.innerWidth - r.width - 4)), y: Math.max(4, Math.min(at.y, window.innerHeight - r.height - 4)) });
    el.querySelector<HTMLElement>("[role=menuitem]")?.focus();
  }, [at]);
  useEffect(() => {
    const down = (e: MouseEvent) => !box.current?.contains(e.target as Node) && onClose();
    const key = (e: KeyboardEvent) => {
      if (e.key === "Escape") onClose();
      if (e.key !== "ArrowDown" && e.key !== "ArrowUp") return;
      const items = [...(box.current?.querySelectorAll<HTMLElement>("[role=menuitem]") ?? [])];
      const i = items.indexOf(document.activeElement as HTMLElement);
      items[(i + (e.key === "ArrowDown" ? 1 : items.length - 1)) % items.length]?.focus();
      e.preventDefault();
    };
    const away = () => onClose();
    document.addEventListener("mousedown", down);
    document.addEventListener("keydown", key);
    window.addEventListener("resize", away);
    window.addEventListener("blur", away);
    return () => {
      document.removeEventListener("mousedown", down);
      document.removeEventListener("keydown", key);
      window.removeEventListener("resize", away);
      window.removeEventListener("blur", away);
    };
  }, [onClose]);
  return (
    <div
      ref={box}
      role="menu"
      data-testid="context-menu"
      className="fixed z-50 flex w-52 flex-col rounded-md border border-border bg-card p-1 text-sm shadow-lg"
      style={{ left: pos.x, top: pos.y }}
      onContextMenu={(e) => e.preventDefault()}
    >
      {children}
    </div>
  );
}

function MenuItem({ onClick, children, danger }: { onClick: () => void; children: React.ReactNode; danger?: boolean }) {
  return (
    <button role="menuitem" className={`rounded px-2 py-1 text-left outline-none hover:bg-background focus:bg-background ${danger ? "text-red-600" : ""}`} onClick={onClick}>
      {children}
    </button>
  );
}

const MenuLine = () => <div className="my-1 border-t border-border" />;

function MoveSelect({ options, current, onPick }: { options: { id: string | null; label: string }[]; current: string | null; onPick: (id: string | null) => void }) {
  return (
    <label className="px-2 py-1 text-xs text-muted">
      Move to
      <select
        data-testid="move-to"
        className="mt-0.5 w-full rounded border border-border bg-background px-1 py-0.5 text-sm text-foreground"
        value={current ?? ""}
        onChange={(e) => onPick(e.target.value || null)}
      >
        {options.map((o) => (
          <option key={o.id ?? ""} value={o.id ?? ""}>
            {o.label}
          </option>
        ))}
      </select>
    </label>
  );
}

function SnippetsPanel({
  projectId,
  data,
  open,
  onOpen,
  onNewQuery,
  act,
}: {
  projectId: string;
  data: Sidebar | null;
  open: Open;
  onOpen: (s: SqlSnippet) => void;
  onNewQuery: (folderId: string | null) => void;
  act: (p: Promise<ActionResult>, done?: string) => Promise<ActionResult>;
}) {
  const [search, setSearch] = useState("");
  const [found, setFound] = useState<SqlSnippet[] | null>(null);
  const [closed, setClosed] = useState<Set<string>>(new Set());
  const [menu, setMenu] = useState<MenuAt | null>(null);
  const closeMenu = useCallback(() => setMenu(null), []);

  useEffect(() => {
    if (!search.trim()) {
      setFound(null);
      return;
    }
    const t = setTimeout(() => searchSnippets(projectId, search).then(setFound), 250);
    return () => clearTimeout(t);
  }, [search, projectId, data]);

  const paths = useMemo(() => folderPaths(data?.folders ?? []), [data]);
  if (!data) return <p className="p-2 text-sm text-muted">Loading…</p>;

  const folderOptions = (exclude: string[] = []) => [
    { id: null, label: "Top level" },
    ...data.folders
      .filter((f) => !exclude.includes(f.id) && paths.has(f.id))
      .map((f) => ({ id: f.id, label: paths.get(f.id)!.join(" / ") }))
      .sort((a, b) => a.label.localeCompare(b.label)),
  ];
  // Right-click, or the ⋯ button (touch screens have no right-click): the same menu.
  const menuAt = (e: React.MouseEvent, m: MenuFor) => {
    e.preventDefault();
    e.stopPropagation();
    const r = e.type === "contextmenu" ? { x: e.clientX, y: e.clientY } : (() => { const b = (e.currentTarget as HTMLElement).getBoundingClientRect(); return { x: b.left, y: b.bottom + 2 }; })();
    setMenu({ ...r, ...m } as MenuAt);
  };
  const then = (fn: () => unknown) => () => {
    setMenu(null);
    void fn();
  };

  const newFolder = async (parentId: string | null) => {
    const name = prompt(parentId ? "Name of the new subfolder:" : "Name of the new folder:");
    if (!name?.trim()) return;
    const r = await act(createFolder(projectId, name, parentId));
    if (!("error" in r) && parentId) setClosed((c) => (c.delete(parentId), new Set(c)));
  };
  const renameF = async (f: SqlFolder) => {
    const name = prompt("New folder name:", f.name);
    if (name?.trim() && name !== f.name) await act(renameFolder(projectId, f.id, name));
  };
  const deleteF = async (f: SqlFolder) => {
    const c = await folderContents(projectId, f.id);
    if ("error" in c) return void act(Promise.resolve(c));
    const what = [c.folders && `${c.folders} subfolder${c.folders === 1 ? "" : "s"}`, c.snippets && `${c.snippets} quer${c.snippets === 1 ? "y" : "ies"}`].filter(Boolean).join(" and ");
    if (confirm(what ? `Delete the folder "${f.name}" and the ${what} inside it?` : `Delete the empty folder "${f.name}"?`)) await act(deleteFolder(projectId, f.id), "Deleted.");
  };
  const renameS = async (s: SqlSnippet) => {
    const name = prompt("New name:", s.name);
    if (name?.trim() && name !== s.name) await act(renameSnippet(projectId, s.id, name));
  };
  const deleteS = async (s: SqlSnippet) => {
    if (confirm(`Delete the query "${s.name}"?`)) await act(deleteSnippet(projectId, s.id), "Deleted.");
  };

  const moreButton = (label: string, onClick: (e: React.MouseEvent) => void) => (
    <button className="rounded px-1 text-muted opacity-60 hover:text-foreground hover:opacity-100 group-hover:opacity-100" aria-label={label} title={label} data-testid="more" onClick={onClick}>
      ⋯
    </button>
  );

  const snippetRow = (s: SqlSnippet, depth: number) => (
    <li
      key={s.id}
      data-testid="snippet"
      data-name={s.name}
      className={`group flex items-center gap-1 rounded py-0.5 pr-1 hover:bg-background ${open?.id === s.id ? "bg-background font-medium" : ""}`}
      style={{ paddingLeft: 8 + depth * 12 }}
      onContextMenu={(e) => menuAt(e, { kind: "snippet", s })}
    >
      <button className="min-w-0 flex-1 truncate text-left text-sm hover:text-accent" title={s.name} onClick={() => onOpen(s)} onDoubleClick={() => renameS(s)}>
        {s.name}
      </button>
      <button className="text-xs text-muted hover:text-foreground" title={s.favorite ? "Remove from favorites" : "Add to favorites"} aria-label="Favorite" onClick={() => act(toggleFavorite(projectId, s.id))}>
        {s.favorite ? "★" : "☆"}
      </button>
      {moreButton("Query actions", (e) => menuAt(e, { kind: "snippet", s }))}
    </li>
  );

  const folderRows = (parentId: string | null, depth: number): React.ReactNode[] =>
    data.folders
      .filter((f) => f.parentId === parentId)
      .flatMap((f) => {
        const isClosed = closed.has(f.id);
        const inside = data.snippets.filter((s) => s.folderId === f.id);
        return [
          <li key={f.id} data-testid="folder" data-name={f.name} className="group flex items-center gap-1 rounded py-0.5 pr-1 hover:bg-background" style={{ paddingLeft: 4 + depth * 12 }} onContextMenu={(e) => menuAt(e, { kind: "folder", f })}>
            <button
              className="flex min-w-0 flex-1 items-center gap-1 text-left text-sm font-medium"
              onClick={() => setClosed((c) => (c.has(f.id) ? (c.delete(f.id), new Set(c)) : new Set(c.add(f.id))))}
              onDoubleClick={() => renameF(f)}
            >
              <span className="w-3 text-xs text-muted">{isClosed ? "▸" : "▾"}</span>
              <span className="truncate">{f.name}</span>
            </button>
            {moreButton("Folder actions", (e) => menuAt(e, { kind: "folder", f }))}
          </li>,
          ...(isClosed ? [] : [...folderRows(f.id, depth + 1), ...inside.map((s) => snippetRow(s, depth + 1))]),
        ];
      });

  const favorites = data.snippets.filter((s) => s.favorite);
  const loose = data.snippets.filter((s) => !s.folderId || !paths.has(s.folderId));
  return (
    <div className="flex min-h-full flex-col gap-3 p-2" data-testid="snippets-panel" onContextMenu={(e) => menuAt(e, { kind: "root" })}>
      <div className="flex gap-1">
        <input className="input min-w-0 flex-1 py-1 text-sm" placeholder="Search queries" value={search} onChange={(e) => setSearch(e.target.value)} onContextMenu={(e) => e.stopPropagation()} />
        <button className="rounded border border-border px-2 text-base leading-none" title="New query or folder" aria-label="New query or folder" data-testid="sql-new" onClick={(e) => menuAt(e, { kind: "root" })}>
          +
        </button>
      </div>
      {found ? (
        found.length ? (
          <ul>{found.map((s) => snippetRow(s, 0))}</ul>
        ) : (
          <p className="text-sm text-muted">No queries match.</p>
        )
      ) : (
        <>
          {favorites.length > 0 && (
            <section>
              <p className="px-1 text-xs font-medium tracking-wide text-muted uppercase">Favorites</p>
              <ul>{favorites.map((s) => snippetRow(s, 0))}</ul>
            </section>
          )}
          <section className="flex-1">
            <p className="px-1 text-xs font-medium tracking-wide text-muted uppercase">Saved queries</p>
            <ul>
              {folderRows(null, 0)}
              {loose.map((s) => snippetRow(s, 0))}
            </ul>
            {!data.folders.length && !data.snippets.length && <p className="px-1 text-sm text-muted">Nothing saved yet. Press + to add a query or a folder.</p>}
          </section>
        </>
      )}

      {menu && (
        <ContextMenu at={menu} onClose={closeMenu}>
          {menu.kind === "root" && (
            <>
              <MenuItem onClick={then(() => onNewQuery(null))}>New query</MenuItem>
              <MenuItem onClick={then(() => newFolder(null))}>New folder</MenuItem>
            </>
          )}
          {menu.kind === "folder" && (
            <>
              <MenuItem onClick={then(() => onNewQuery(menu.f.id))}>New query here</MenuItem>
              <MenuItem onClick={then(() => newFolder(menu.f.id))}>New subfolder</MenuItem>
              <MenuLine />
              <MenuItem onClick={then(() => renameF(menu.f))}>Rename</MenuItem>
              <MoveSelect options={folderOptions([menu.f.id, ...descendants(data.folders, menu.f.id)])} current={menu.f.parentId} onPick={(id) => then(() => act(moveFolder(projectId, menu.f.id, id)))()} />
              <MenuLine />
              <MenuItem danger onClick={then(() => deleteF(menu.f))}>
                Delete
              </MenuItem>
            </>
          )}
          {menu.kind === "snippet" && (
            <>
              <MenuItem onClick={then(() => onOpen(menu.s))}>Open</MenuItem>
              <MenuItem onClick={then(() => renameS(menu.s))}>Rename</MenuItem>
              <MenuItem onClick={then(() => act(duplicateSnippet(projectId, menu.s.id), "Duplicated."))}>Duplicate</MenuItem>
              <MenuItem onClick={then(() => act(toggleFavorite(projectId, menu.s.id)))}>{menu.s.favorite ? "Remove from favorites" : "Add to favorites"}</MenuItem>
              <MoveSelect options={folderOptions()} current={menu.s.folderId} onPick={(id) => then(() => act(moveSnippet(projectId, menu.s.id, id)))()} />
              <MenuLine />
              <MenuItem danger onClick={then(() => deleteS(menu.s))}>
                Delete
              </MenuItem>
            </>
          )}
        </ContextMenu>
      )}
    </div>
  );
}

function HistoryPanel({ projectId, version, onPick }: { projectId: string; version: number; onPick: (sql: string) => void }) {
  const [runs, setRuns] = useState<SqlRun[] | null>(null);
  useEffect(() => {
    getSqlHistory(projectId).then(setRuns);
  }, [projectId, version]);
  if (!runs) return <p className="p-2 text-sm text-muted">Loading…</p>;
  if (!runs.length) return <p className="p-2 text-sm text-muted">Queries you run show up here (the last 20).</p>;
  return (
    <ul className="space-y-1 p-2" data-testid="history-panel">
      {runs.map((r) => (
        <li key={r.id}>
          <button data-testid="history-item" className="w-full rounded border border-border p-2 text-left text-xs hover:bg-background" onClick={() => onPick(r.sql)} title={r.sql}>
            <span className="block truncate font-mono">{r.sql}</span>
            <span className={`mt-0.5 block truncate ${r.error ? "text-red-600" : "text-muted"}`}>
              {r.error ? r.error : `${r.rowCount ?? 0} rows`}
              {r.elapsedMs != null && ` · ${r.elapsedMs} ms`} · {new Date(r.createdAt).toLocaleTimeString()}
            </span>
          </button>
        </li>
      ))}
    </ul>
  );
}

function TablesPanel({ schema, onQuery }: { schema: SchemaTable[]; onQuery: (sql: string) => void }) {
  const [openName, setOpenName] = useState<string | null>(null);
  if (!schema.length) return <p className="p-2 text-sm text-muted">No tables yet.</p>;
  return (
    <ul className="divide-y divide-border text-sm" data-testid="tables-panel">
      {schema.map((t) => (
        <li key={t.name}>
          <div className="flex items-center gap-1 px-2 py-1.5">
            <button className="min-w-0 flex-1 truncate text-left font-medium" onClick={() => setOpenName(openName === t.name ? null : t.name)}>
              {t.name}
            </button>
            <button className="text-xs text-muted hover:text-foreground" title={`Query ${t.name}`} onClick={() => onQuery(`SELECT *\nFROM ${/^[A-Za-z][A-Za-z0-9_]*$/.test(t.name) ? t.name : `"${t.name.replace(/"/g, '""')}"`}\nLIMIT 100`)}>
              Query
            </button>
          </div>
          {openName === t.name && (
            <ul className="px-3 pb-2">
              {t.columns.map((c) => (
                <li key={c.name} className="flex justify-between gap-2 font-mono text-xs">
                  <span className="truncate">{c.name}</span>
                  <span className="text-muted">{c.type}</span>
                </li>
              ))}
            </ul>
          )}
        </li>
      ))}
    </ul>
  );
}

/* ========== Page ========== */

type Tab = "tables" | "snippets" | "history";

export function SqlEditorUI({ projectId, schema, nonce }: { projectId: string; schema: SchemaTable[]; nonce: string }) {
  const [query, setQuery] = useState("");
  const [open, setOpen] = useState<Open>(null);
  const [result, setResult] = useState<SqlResult | null>(null);
  const [running, setRunning] = useState(false);
  const [tab, setTab] = useState<Tab>("snippets");
  const [sidebar, setSidebar] = useState<Sidebar | null>(null);
  const [historyVersion, setHistoryVersion] = useState(0);
  const [note, setNote] = useState<{ text: string; error: boolean } | null>(null);
  const abort = useRef<AbortController | null>(null);
  const editor = useRef<EditorApi | null>(null);

  const reload = useCallback(async () => setSidebar(await getSidebarData(projectId)), [projectId]);
  useEffect(() => void reload(), [reload]);

  const act = useCallback(
    async (p: Promise<ActionResult>, done?: string) => {
      const r = await p;
      setNote("error" in r ? { text: r.error, error: true } : done ? { text: done, error: false } : null);
      await reload(); // callers that open what they just made must see it in the list first
      return r;
    },
    [reload],
  );

  const run = useCallback(async () => {
    if (abort.current) return; // already running
    const sql = editor.current?.textToRun() ?? query;
    if (!sql.trim()) return;
    const ctrl = new AbortController();
    abort.current = ctrl;
    setRunning(true);
    try {
      const res = await fetch(`/dashboard/${encodeURIComponent(projectId)}/sql/run`, {
        method: "POST",
        headers: { "content-type": "application/json" },
        body: JSON.stringify({ sql }),
        signal: ctrl.signal,
      });
      const body = (await res.json().catch(() => null)) as SqlResult | null;
      setResult(body ?? { error: res.redirected ? "You were signed out. Reload the page and sign in again." : `The request failed (${res.status}).` });
    } catch {
      setResult({ error: "Cancelled." });
    } finally {
      abort.current = null;
      setRunning(false);
      setHistoryVersion((v) => v + 1);
    }
  }, [projectId, query]);
  const runRef = useRef(run);
  runRef.current = run;

  const dirty = open ? query !== open.saved : query.trim() !== "";
  const confirmDiscard = () => !dirty || confirm("Discard the changes in the editor?");

  const openSnippet = (s: SqlSnippet) => {
    if (open?.id === s.id || !confirmDiscard()) return;
    setOpen({ id: s.id, name: s.name, folderId: s.folderId, saved: s.sqlText });
    setQuery(s.sqlText);
    setNote(null);
    editor.current?.focus();
  };
  const loadText = (sql: string) => {
    if (!confirmDiscard()) return;
    setOpen(null);
    setQuery(sql);
    editor.current?.focus();
  };

  // "+ → New query": a named, empty query saved right away (in a folder when made from its menu).
  const newQuery = async (folderId: string | null) => {
    if (!confirmDiscard()) return;
    const name = prompt("Name of the new query:");
    if (!name?.trim()) return;
    const r = await act(createSnippet(projectId, name, "", folderId));
    if ("error" in r || !r.id) return;
    setOpen({ id: r.id, name: name.trim(), folderId, saved: "" });
    setQuery("");
    setResult(null);
    editor.current?.focus();
  };

  const save = async () => {
    if (!open) return;
    const r = await act(updateSnippet(projectId, open.id, query), "Saved.");
    if (!("error" in r)) setOpen({ ...open, saved: query });
  };
  const saveAs = async () => {
    const name = prompt("Save this query as a snippet named:", open ? `${open.name} (copy)` : "");
    if (!name?.trim()) return;
    const r = await act(createSnippet(projectId, name, query, open?.folderId ?? null), "Saved.");
    if (!("error" in r) && r.id) setOpen({ id: r.id, name: name.trim(), folderId: open?.folderId ?? null, saved: query });
  };

  // Keep the open snippet's name in sync after a rename, and forget it if it was deleted.
  useEffect(() => {
    if (!open || !sidebar) return;
    const s = sidebar.snippets.find((x) => x.id === open.id);
    if (!s) setOpen(null);
    else if (s.name !== open.name || s.folderId !== open.folderId) setOpen({ ...open, name: s.name, folderId: s.folderId });
  }, [sidebar, open]);

  return (
    <div className="flex h-[calc(100vh-11rem)] min-h-[32rem] flex-col gap-3 md:flex-row">
      <aside className="flex max-h-72 flex-shrink-0 flex-col rounded-lg border border-border bg-card md:max-h-none md:w-64">
        <div className="flex border-b border-border text-xs" role="tablist">
          {(["snippets", "tables", "history"] as Tab[]).map((t) => (
            <button key={t} role="tab" aria-selected={tab === t} data-testid={`tab-${t}`} className={`flex-1 py-2 capitalize ${tab === t ? "border-b-2 border-accent font-medium" : "text-muted"}`} onClick={() => setTab(t)}>
              {t}
            </button>
          ))}
        </div>
        <div className="min-h-0 flex-1 overflow-y-auto">
          {tab === "snippets" && <SnippetsPanel projectId={projectId} data={sidebar} open={open} onOpen={openSnippet} onNewQuery={newQuery} act={act} />}
          {tab === "tables" && <TablesPanel schema={schema} onQuery={loadText} />}
          {tab === "history" && <HistoryPanel projectId={projectId} version={historyVersion} onPick={loadText} />}
        </div>
      </aside>

      <section className="flex min-w-0 flex-1 flex-col gap-2">
        <div className="flex flex-wrap items-center gap-2">
          <button className="btn" data-testid="sql-run" onClick={run} disabled={running} title="Run (Ctrl/⌘ + Enter). With text selected, runs only the selection.">
            {running ? "Running…" : "Run"}
          </button>
          {running && (
            <button className="rounded border border-border px-3 py-1.5 text-sm" data-testid="sql-cancel" onClick={() => abort.current?.abort()}>
              Cancel
            </button>
          )}
          <span className="min-w-0 truncate text-sm text-muted" data-testid="sql-open-name">
            {open ? (
              <>
                {open.name}
                {dirty && " •"}
              </>
            ) : (
              "Untitled query"
            )}
          </span>
          <div className="ml-auto flex items-center gap-2">
            {note && (
              <span data-testid="sql-note" className={`text-xs ${note.error ? "text-red-600" : "text-muted"}`}>
                {note.text}
              </span>
            )}
            {open && (
              <button className="rounded border border-border px-3 py-1.5 text-sm disabled:opacity-50" data-testid="sql-save" onClick={save} disabled={!dirty}>
                Save
              </button>
            )}
            <button className="rounded border border-border px-3 py-1.5 text-sm disabled:opacity-50" data-testid="sql-save-as" onClick={saveAs} disabled={!query.trim()}>
              Save as…
            </button>
            <details className="relative">
              <summary className="cursor-pointer list-none rounded border border-border px-2 py-1.5 text-sm" title="Help">
                ?
              </summary>
              <div className="absolute right-0 z-20 mt-1 w-80 space-y-2 rounded-md border border-border bg-card p-3 text-xs shadow-lg">
                <p>
                  <b>DATBase SQL</b> reads your project&apos;s data. It is SQLite-style SQL: <code>SELECT</code>, <code>WITH</code> and <code>EXPLAIN</code>, with functions such as{" "}
                  <code>date(&apos;now&apos;)</code>, <code>json_extract()</code> and <code>count(*)</code>.
                </p>
                <p>It never changes anything: edit rows in Tables and add tables or fields with Import schema.</p>
                <p>One statement per run, up to 1,000 rows and 10 seconds. Internal tables and hidden fields such as passwords aren&apos;t visible. Dates are stored as UTC text.</p>
                <p>Ctrl/⌘ + Enter runs the query; select part of it to run only that part.</p>
              </div>
            </details>
          </div>
        </div>
        <div className="h-2/5 min-h-40 flex-shrink-0">
          <SqlEditor value={query} onChange={setQuery} schema={schema} onRun={runRef} api={editor} nonce={nonce} />
        </div>
        <div className="min-h-0 flex-1 overflow-hidden rounded-lg border border-border bg-card p-3">
          <Results result={result} running={running} />
        </div>
      </section>
    </div>
  );
}
