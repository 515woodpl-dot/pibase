"use client";

import {
  useCallback,
  useEffect,
  useRef,
  useState,
  useTransition,
} from "react";
import type { SqlFolder, SqlRun, SqlSnippet } from "@/db/schema";
import {
  createFolder,
  createSnippet,
  deleteFolder,
  deleteSnippet,
  duplicateSnippet,
  getSqlHistory,
  getSqlSchema,
  getSidebarData,
  moveSnippet,
  renameFolder,
  renameSnippet,
  runSql,
  searchSnippets,
  toggleFavorite,
  updateSnippet,
  type SqlResult,
} from "./actions";

// ---- CodeMirror 6 (bundled, no CDN) ----
import { EditorState } from "@codemirror/state";
import { EditorView, keymap, highlightActiveLine, lineNumbers } from "@codemirror/view";
import { defaultKeymap, history, historyKeymap } from "@codemirror/commands";
import { sql as sqlLang, SQLite } from "@codemirror/lang-sql";
import { autocompletion, completionKeymap } from "@codemirror/autocomplete";
import { oneDark } from "@codemirror/theme-one-dark";
import { syntaxHighlighting, defaultHighlightStyle } from "@codemirror/language";

/* ========== Types ========== */
type SchemaTable = { name: string; columns: { name: string; type: string }[] };
type SidebarData = { folders: SqlFolder[]; snippets: SqlSnippet[] };

/* ========== CodeMirror editor component ========== */
function SqlEditor({
  value,
  onChange,
  schema,
  onRun,
}: {
  value: string;
  onChange: (v: string) => void;
  schema: SchemaTable[];
  onRun: () => void;
}) {
  const containerRef = useRef<HTMLDivElement>(null);
  const viewRef = useRef<EditorView | null>(null);

  useEffect(() => {
    if (!containerRef.current) return;

    // Build autocomplete schema for SQLite language support.
    const schemaMap: Record<string, string[]> = {};
    for (const t of schema) {
      schemaMap[t.name] = t.columns.map((c) => c.name);
    }

    const runKeymap = keymap.of([
      {
        key: "Ctrl-Enter",
        mac: "Cmd-Enter",
        run() {
          onRun();
          return true;
        },
      },
    ]);

    const view = new EditorView({
      state: EditorState.create({
        doc: value,
        extensions: [
          lineNumbers(),
          highlightActiveLine(),
          history(),
          keymap.of([...defaultKeymap, ...historyKeymap, ...completionKeymap]),
          runKeymap,
          sqlLang({ dialect: SQLite, schema: schemaMap }),
          autocompletion(),
          oneDark,
          EditorView.updateListener.of((update) => {
            if (update.docChanged) {
              onChange(update.state.doc.toString());
            }
          }),
        ],
      }),
      parent: containerRef.current,
    });
    viewRef.current = view;

    return () => view.destroy();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []); // mount once

  // Sync schema changes to autocomplete without recreating editor.
  useEffect(() => {
    // Schema updates are low-frequency; just update via extension dispatch if needed.
    // For simplicity we recreate on schema change only when the view exists.
    // (A full reconfigure would need Compartment — keep it simple here.)
  }, [schema]);

  // Sync external value (e.g. loading a snippet) into the editor.
  const lastExternal = useRef(value);
  useEffect(() => {
    if (!viewRef.current) return;
    const current = viewRef.current.state.doc.toString();
    if (value !== current && value !== lastExternal.current) {
      // Replace entire doc.
      viewRef.current.dispatch({
        changes: { from: 0, to: current.length, insert: value },
      });
    }
    lastExternal.current = value;
  }, [value]);

  return (
    <div
      ref={containerRef}
      className="h-full min-h-[200px] overflow-auto rounded border border-border font-mono text-sm"
    />
  );
}

/* ========== Results table ========== */
function ResultsTable({ result }: { result: SqlResult }) {
  if ("error" in result) {
    return (
      <div className="rounded border border-red-500/30 bg-red-50 p-3 text-sm text-red-700 dark:bg-red-950/30 dark:text-red-400">
        <span className="font-medium">Error: </span>{result.error}
        {result.elapsedMs !== undefined && (
          <span className="ml-2 text-xs text-muted">({result.elapsedMs} ms)</span>
        )}
      </div>
    );
  }

  const { columns, rows, rowCount, truncated, elapsedMs } = result;

  return (
    <div className="space-y-2">
      <p className="text-xs text-muted">
        {rowCount} row{rowCount !== 1 ? "s" : ""}
        {truncated && " (showing first 1 000)"}
        {" · "}{elapsedMs} ms
      </p>
      {columns.length > 0 ? (
        <div className="overflow-auto rounded border border-border">
          <table className="w-full text-sm">
            <thead className="bg-muted/50">
              <tr>
                {columns.map((c) => (
                  <th key={c} className="border-b border-border px-3 py-1.5 text-left font-medium">
                    {c}
                  </th>
                ))}
              </tr>
            </thead>
            <tbody>
              {rows.map((row, i) => (
                <tr key={i} className="border-b border-border last:border-0 hover:bg-muted/20">
                  {row.map((cell, j) => (
                    <td key={j} className="px-3 py-1.5 font-mono text-xs">
                      {cell === null ? (
                        <span className="italic text-muted">null</span>
                      ) : String(cell)}
                    </td>
                  ))}
                </tr>
              ))}
              {rows.length === 0 && (
                <tr>
                  <td colSpan={columns.length} className="px-3 py-4 text-center text-sm text-muted">
                    No rows returned.
                  </td>
                </tr>
              )}
            </tbody>
          </table>
        </div>
      ) : (
        <p className="text-sm text-muted">No rows returned.</p>
      )}
    </div>
  );
}

/* ========== History panel ========== */
function HistoryPanel({
  projectId,
  onLoad,
}: {
  projectId: string;
  onLoad: (sql: string) => void;
}) {
  const [history, setHistory] = useState<SqlRun[] | null>(null);

  useEffect(() => {
    getSqlHistory(projectId).then(setHistory);
  }, [projectId]);

  if (!history) return <p className="text-sm text-muted">Loading…</p>;
  if (history.length === 0) return <p className="text-sm text-muted">No history yet.</p>;

  return (
    <ul className="space-y-1">
      {history.map((run) => (
        <li key={run.id} className="rounded border border-border p-2 text-xs hover:bg-muted/20">
          <button
            className="w-full text-left"
            onClick={() => onLoad(run.sql)}
          >
            <span className={run.error ? "text-red-600 dark:text-red-400" : "text-green-700 dark:text-green-400"}>
              {run.error ? "✗" : "✓"}
            </span>
            <span className="ml-1 font-mono text-muted">{run.sql.slice(0, 80)}{run.sql.length > 80 ? "…" : ""}</span>
          </button>
          <div className="mt-0.5 text-muted">
            {run.elapsedMs != null && `${run.elapsedMs} ms · `}
            {run.rowCount != null && `${run.rowCount} rows · `}
            {new Date(run.createdAt).toLocaleString()}
          </div>
          {run.error && <div className="mt-0.5 text-red-600 dark:text-red-400">{run.error.slice(0, 100)}</div>}
        </li>
      ))}
    </ul>
  );
}

/* ========== Sidebar (folders + snippets) ========== */
function Sidebar({
  projectId,
  onLoad,
}: {
  projectId: string;
  onLoad: (sql: string) => void;
}) {
  const [data, setData] = useState<SidebarData | null>(null);
  const [search, setSearch] = useState("");
  const [searchResults, setSearchResults] = useState<SqlSnippet[] | null>(null);
  const [, startTransition] = useTransition();

  const reload = useCallback(() => {
    getSidebarData(projectId).then(setData);
  }, [projectId]);

  useEffect(() => { reload(); }, [reload]);

  useEffect(() => {
    if (!search) { setSearchResults(null); return; }
    const t = setTimeout(() => {
      startTransition(() => {
        searchSnippets(projectId, search).then(setSearchResults);
      });
    }, 300);
    return () => clearTimeout(t);
  }, [search, projectId]);

  if (!data) return <p className="text-sm text-muted p-2">Loading…</p>;

  // Build folder tree.
  const topFolders = data.folders.filter((f) => !f.parentId);
  const getChildren = (parentId: string) => data.folders.filter((f) => f.parentId === parentId);
  const getFolderSnippets = (folderId: string | null) =>
    data.snippets.filter((s) => s.folderId === folderId);
  const favorites = data.snippets.filter((s) => s.favorite);
  const unfiled = getFolderSnippets(null);

  const SnippetItem = ({ s }: { s: SqlSnippet }) => (
    <li className="flex items-center gap-1 rounded px-1 py-0.5 hover:bg-muted/30">
      <button
        className="flex-1 truncate text-left text-sm"
        onClick={() => onLoad(s.sqlText)}
        title={s.name}
      >
        {s.name}
      </button>
      <button
        className="text-xs text-muted hover:text-foreground"
        title={s.favorite ? "Unfavorite" : "Favorite"}
        onClick={async () => {
          const form = new FormData();
          form.set("projectId", projectId);
          form.set("snippetId", s.id);
          await toggleFavorite(undefined, form);
          reload();
        }}
      >
        {s.favorite ? "★" : "☆"}
      </button>
    </li>
  );

  const FolderTree = ({ folders, depth = 0 }: { folders: SqlFolder[]; depth?: number }) => (
    <>
      {folders.map((f) => {
        const children = getChildren(f.id);
        const snippets = getFolderSnippets(f.id);
        return (
          <li key={f.id}>
            <div className="flex items-center gap-1 rounded px-1 py-0.5 hover:bg-muted/20" style={{ paddingLeft: `${(depth + 1) * 12}px` }}>
              <span className="text-muted">▸</span>
              <span className="flex-1 truncate text-sm font-medium">{f.name}</span>
            </div>
            {snippets.length > 0 && (
              <ul style={{ paddingLeft: `${(depth + 2) * 12}px` }}>
                {snippets.map((s) => <SnippetItem key={s.id} s={s} />)}
              </ul>
            )}
            {children.length > 0 && (
              <ul>
                <FolderTree folders={children} depth={depth + 1} />
              </ul>
            )}
          </li>
        );
      })}
    </>
  );

  if (searchResults !== null) {
    return (
      <div className="space-y-2 p-2">
        <input
          className="w-full rounded border border-border px-2 py-1 text-sm"
          placeholder="Search snippets…"
          value={search}
          onChange={(e) => setSearch(e.target.value)}
          autoFocus
        />
        {searchResults.length === 0 ? (
          <p className="text-sm text-muted">No results.</p>
        ) : (
          <ul>
            {searchResults.map((s) => <SnippetItem key={s.id} s={s} />)}
          </ul>
        )}
      </div>
    );
  }

  return (
    <div className="space-y-2 p-2">
      <input
        className="w-full rounded border border-border px-2 py-1 text-sm"
        placeholder="Search snippets…"
        value={search}
        onChange={(e) => setSearch(e.target.value)}
      />
      {favorites.length > 0 && (
        <section>
          <p className="text-xs font-medium uppercase tracking-wide text-muted">Favorites</p>
          <ul>
            {favorites.map((s) => <SnippetItem key={s.id} s={s} />)}
          </ul>
        </section>
      )}
      <section>
        <p className="text-xs font-medium uppercase tracking-wide text-muted">Folders</p>
        <ul>
          <FolderTree folders={topFolders} />
        </ul>
      </section>
      {unfiled.length > 0 && (
        <section>
          <p className="text-xs font-medium uppercase tracking-wide text-muted">Unsorted</p>
          <ul>
            {unfiled.map((s) => <SnippetItem key={s.id} s={s} />)}
          </ul>
        </section>
      )}
    </div>
  );
}

/* ========== Schema browser ========== */
function SchemaBrowser({ projectId }: { projectId: string }) {
  const [schema, setSchema] = useState<SchemaTable[] | null>(null);
  const [open, setOpen] = useState<string | null>(null);

  useEffect(() => {
    getSqlSchema(projectId).then(setSchema);
  }, [projectId]);

  if (!schema) return <p className="text-sm text-muted p-2">Loading schema…</p>;
  if (schema.length === 0) return <p className="text-sm text-muted p-2">No tables yet.</p>;

  return (
    <ul className="divide-y divide-border text-sm">
      {schema.map((t) => (
        <li key={t.name}>
          <button
            className="flex w-full items-center justify-between px-3 py-2 text-left hover:bg-muted/20"
            onClick={() => setOpen(open === t.name ? null : t.name)}
          >
            <span className="font-medium">{t.name}</span>
            <span className="text-xs text-muted">{t.columns.length}</span>
          </button>
          {open === t.name && (
            <ul className="bg-muted/10 px-3 pb-2">
              {t.columns.map((c) => (
                <li key={c.name} className="flex items-center justify-between py-0.5 font-mono text-xs">
                  <span>{c.name}</span>
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

/* ========== Main SQL editor page ========== */
type Tab = "schema" | "snippets" | "history";

export function SqlEditorUI({
  projectId,
  initialSchema,
}: {
  projectId: string;
  initialSchema: SchemaTable[];
}) {
  const [query, setQuery] = useState("SELECT * FROM users LIMIT 20");
  const [result, setResult] = useState<SqlResult | null>(null);
  const [running, startRun] = useTransition();
  const abortRef = useRef<AbortController | null>(null);
  const [sideTab, setSideTab] = useState<Tab>("schema");

  const handleRun = useCallback(() => {
    if (running) return;
    abortRef.current?.abort();
    const ctrl = new AbortController();
    abortRef.current = ctrl;

    startRun(async () => {
      const res = await runSql(projectId, query);
      if (!ctrl.signal.aborted) setResult(res);
    });
  }, [projectId, query, running]);

  const handleCancel = () => {
    abortRef.current?.abort();
    setResult({ error: "Query cancelled." });
  };

  return (
    <div className="flex h-[calc(100vh-10rem)] gap-4">
      {/* Left sidebar */}
      <div className="flex w-56 flex-shrink-0 flex-col rounded-lg border border-border bg-card">
        <div className="flex border-b border-border">
          {(["schema", "snippets", "history"] as Tab[]).map((t) => (
            <button
              key={t}
              className={`flex-1 py-1.5 text-xs capitalize ${sideTab === t ? "border-b-2 border-accent font-medium" : "text-muted"}`}
              onClick={() => setSideTab(t)}
            >
              {t}
            </button>
          ))}
        </div>
        <div className="flex-1 overflow-y-auto">
          {sideTab === "schema" && <SchemaBrowser projectId={projectId} />}
          {sideTab === "snippets" && (
            <Sidebar projectId={projectId} onLoad={(sql) => setQuery(sql)} />
          )}
          {sideTab === "history" && (
            <div className="p-2">
              <HistoryPanel projectId={projectId} onLoad={(sql) => setQuery(sql)} />
            </div>
          )}
        </div>
      </div>

      {/* Main editor area */}
      <div className="flex flex-1 flex-col gap-3 overflow-hidden">
        {/* Toolbar */}
        <div className="flex items-center gap-2">
          <button
            className="rounded bg-accent px-4 py-1.5 text-sm font-medium text-white disabled:opacity-50"
            onClick={handleRun}
            disabled={running}
          >
            {running ? "Running…" : "Run"}
          </button>
          {running && (
            <button
              className="rounded border border-border px-3 py-1.5 text-sm text-muted hover:text-foreground"
              onClick={handleCancel}
            >
              Cancel
            </button>
          )}
          <button
            className="ml-auto rounded border border-border px-3 py-1.5 text-sm text-muted hover:text-foreground"
            onClick={async () => {
              const name = prompt("Snippet name:");
              if (!name?.trim()) return;
              const form = new FormData();
              form.set("projectId", projectId);
              form.set("name", name.trim());
              form.set("sql", query);
              await createSnippet(undefined, form);
            }}
          >
            Save as snippet
          </button>
          <span className="text-xs text-muted">Ctrl/⌘+Enter to run</span>
        </div>

        {/* Editor */}
        <div className="flex-shrink-0" style={{ height: "40%" }}>
          <SqlEditor
            value={query}
            onChange={setQuery}
            schema={initialSchema}
            onRun={handleRun}
          />
        </div>

        {/* Results */}
        <div className="flex-1 overflow-auto rounded-lg border border-border bg-card p-3">
          {!result && !running && (
            <p className="text-sm text-muted">Results will appear here.</p>
          )}
          {running && <p className="text-sm text-muted">Running…</p>}
          {result && !running && <ResultsTable result={result} />}
        </div>
      </div>
    </div>
  );
}
