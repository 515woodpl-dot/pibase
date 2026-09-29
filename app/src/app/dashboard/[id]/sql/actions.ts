"use server";

import { and, asc, desc, eq, ilike, isNull, or, sql as drizzleSql } from "drizzle-orm";
import { notFound } from "next/navigation";
import { db } from "@/db";
import { sqlFolder, sqlRun, sqlSnippet, type SqlFolder, type SqlSnippet } from "@/db/schema";
import { listCollections } from "@/lib/pb";
import { actionProject } from "@/lib/projects";
import { randomId } from "@/lib/crypto";

export type SqlResult =
  | { columns: string[]; rows: unknown[][]; rowCount: number; truncated: boolean; elapsedMs: number }
  | { error: string; elapsedMs?: number };

export type SnippetState = { error?: string; ok?: boolean; id?: string } | undefined;

/** Rate-limit store: runs per (projectId, window). */
const runCounts = new Map<string, { count: number; windowStart: number }>();
const RATE_WINDOW_MS = 60_000;
const RATE_LIMIT = 30;

function checkRateLimit(projectId: string): boolean {
  const now = Date.now();
  const key = projectId;
  const entry = runCounts.get(key);
  if (!entry || now - entry.windowStart > RATE_WINDOW_MS) {
    runCounts.set(key, { count: 1, windowStart: now });
    return true;
  }
  if (entry.count >= RATE_LIMIT) return false;
  entry.count++;
  return true;
}

/* ---------- SQL execution ---------- */

export async function runSql(projectId: string, query: string): Promise<SqlResult> {
  const p = await actionProject(projectId);

  if (p.status !== "active") {
    return { error: `Project is ${p.status}. No queries can run while the project is not active.` };
  }

  if (!checkRateLimit(p.id)) {
    return { error: "Rate limit reached: max 30 queries per minute. Wait a moment and try again." };
  }

  const trimmed = query.trim();
  if (!trimmed) return { error: "No SQL provided." };

  const { pbInternalUrl } = await import("@/lib/config");
  const { controlHeaders: makeControlHeaders } = await import("@/lib/pb");
  const url = `${pbInternalUrl(p.slug)}/api/datbase/sql`;
  const start = Date.now();
  let result: SqlResult;

  try {
    const res = await fetch(url, {
      method: "POST",
      headers: {
        "content-type": "application/json",
        ...makeControlHeaders(p.slug),
      },
      body: JSON.stringify({ sql: trimmed }),
      signal: AbortSignal.timeout(12_000), // hook enforces 10 s; outer is 12 s safety
    });

    const body = await res.json();
    if (!res.ok || body.error) {
      result = { error: body.error ?? `Query failed (${res.status}).`, elapsedMs: body.elapsedMs ?? (Date.now() - start) };
    } else {
      result = {
        columns: body.columns ?? [],
        rows: body.rows ?? [],
        rowCount: body.rowCount ?? 0,
        truncated: body.truncated ?? false,
        elapsedMs: body.elapsedMs ?? (Date.now() - start),
      };
    }
  } catch (e) {
    const msg = e instanceof Error ? e.message : String(e);
    result = { error: msg.includes("timed out") || msg.includes("abort") ? "Query timed out (10 s limit)." : msg, elapsedMs: Date.now() - start };
  }

  // Persist run history (last 20 per project).
  const isError = "error" in result;
  await db.insert(sqlRun).values({
    id: randomId(),
    projectId: p.id,
    sql: trimmed.slice(0, 10_000),
    elapsedMs: "elapsedMs" in result ? result.elapsedMs : undefined,
    rowCount: isError ? undefined : (result as { rowCount: number }).rowCount,
    error: isError ? (result as { error: string }).error : undefined,
  });

  // Trim to last 20.
  const all = await db
    .select({ id: sqlRun.id })
    .from(sqlRun)
    .where(eq(sqlRun.projectId, p.id))
    .orderBy(desc(sqlRun.createdAt));
  if (all.length > 20) {
    const toDelete = all.slice(20).map((r) => r.id);
    await db.delete(sqlRun).where(and(eq(sqlRun.projectId, p.id), drizzleSql`id = ANY(ARRAY[${drizzleSql.join(toDelete.map((id) => drizzleSql`${id}`), drizzleSql`, `)}]::text[])`));
  }

  return result;
}

/* ---------- Schema browser ---------- */

export async function getSqlSchema(projectId: string) {
  const p = await actionProject(projectId);
  const tables = await listCollections(p);
  return tables.map((t) => ({
    name: t.name,
    columns: t.fields
      .filter((f) => !f.hidden && f.type !== "password")
      .map((f) => ({ name: f.name, type: f.type })),
  }));
}

/* ---------- Run history ---------- */

export async function getSqlHistory(projectId: string) {
  const p = await actionProject(projectId);
  return db.query.sqlRun.findMany({
    where: eq(sqlRun.projectId, p.id),
    orderBy: desc(sqlRun.createdAt),
    limit: 20,
  });
}

/* ---------- Folders ---------- */

const MAX_DEPTH = 5;

async function folderDepth(id: string | null): Promise<number> {
  if (!id) return 0;
  const f = await db.query.sqlFolder.findFirst({ where: eq(sqlFolder.id, id) });
  if (!f) return 0;
  return 1 + (await folderDepth(f.parentId ?? null));
}

export async function createFolder(_: SnippetState, form: FormData): Promise<SnippetState> {
  const p = await actionProject(form.get("projectId"));
  const name = String(form.get("name") ?? "").trim().slice(0, 80);
  const parentId = String(form.get("parentId") ?? "").trim() || null;
  if (!name) return { error: "Folder name is required." };
  if (/\//.test(name)) return { error: "Folder names cannot contain slashes." };
  if (parentId) {
    const parent = await db.query.sqlFolder.findFirst({ where: and(eq(sqlFolder.id, parentId), eq(sqlFolder.projectId, p.id)) });
    if (!parent) return { error: "Parent folder not found." };
    const depth = await folderDepth(parentId);
    if (depth >= MAX_DEPTH) return { error: `Folders can be nested up to ${MAX_DEPTH} levels.` };
  }
  try {
    const id = randomId();
    await db.insert(sqlFolder).values({ id, projectId: p.id, parentId, name });
    return { ok: true, id };
  } catch (e) {
    const msg = e instanceof Error ? e.message : String(e);
    if (/unique/i.test(msg)) return { error: "A folder with that name already exists here." };
    throw e;
  }
}

export async function renameFolder(_: SnippetState, form: FormData): Promise<SnippetState> {
  const p = await actionProject(form.get("projectId"));
  const folderId = String(form.get("folderId") ?? "");
  const name = String(form.get("name") ?? "").trim().slice(0, 80);
  if (!name) return { error: "Folder name is required." };
  if (/\//.test(name)) return { error: "Folder names cannot contain slashes." };
  const folder = await db.query.sqlFolder.findFirst({ where: and(eq(sqlFolder.id, folderId), eq(sqlFolder.projectId, p.id)) });
  if (!folder) notFound();
  try {
    await db.update(sqlFolder).set({ name }).where(and(eq(sqlFolder.id, folderId), eq(sqlFolder.projectId, p.id)));
    return { ok: true };
  } catch (e) {
    const msg = e instanceof Error ? e.message : String(e);
    if (/unique/i.test(msg)) return { error: "A folder with that name already exists here." };
    throw e;
  }
}

export async function moveFolder(_: SnippetState, form: FormData): Promise<SnippetState> {
  const p = await actionProject(form.get("projectId"));
  const folderId = String(form.get("folderId") ?? "");
  const newParentId = String(form.get("parentId") ?? "").trim() || null;
  const folder = await db.query.sqlFolder.findFirst({ where: and(eq(sqlFolder.id, folderId), eq(sqlFolder.projectId, p.id)) });
  if (!folder) notFound();
  if (newParentId === folderId) return { error: "A folder cannot be its own parent." };
  if (newParentId) {
    const newParent = await db.query.sqlFolder.findFirst({ where: and(eq(sqlFolder.id, newParentId), eq(sqlFolder.projectId, p.id)) });
    if (!newParent) return { error: "Target folder not found." };
    const depth = await folderDepth(newParentId);
    if (depth >= MAX_DEPTH) return { error: `Folders can be nested up to ${MAX_DEPTH} levels.` };
  }
  try {
    await db.update(sqlFolder).set({ parentId: newParentId }).where(and(eq(sqlFolder.id, folderId), eq(sqlFolder.projectId, p.id)));
    return { ok: true };
  } catch (e) {
    const msg = e instanceof Error ? e.message : String(e);
    if (/unique/i.test(msg)) return { error: "A folder with that name already exists there." };
    throw e;
  }
}

export async function deleteFolder(_: SnippetState, form: FormData): Promise<SnippetState> {
  const p = await actionProject(form.get("projectId"));
  const folderId = String(form.get("folderId") ?? "");
  const confirmed = String(form.get("confirmed") ?? "") === "yes";
  const folder = await db.query.sqlFolder.findFirst({ where: and(eq(sqlFolder.id, folderId), eq(sqlFolder.projectId, p.id)) });
  if (!folder) notFound();
  // Count contents.
  const [{ snippetCount }] = await db
    .select({ snippetCount: drizzleSql<number>`count(*)` })
    .from(sqlSnippet)
    .where(eq(sqlSnippet.folderId, folderId));
  const [{ subFolderCount }] = await db
    .select({ subFolderCount: drizzleSql<number>`count(*)` })
    .from(sqlFolder)
    .where(eq(sqlFolder.parentId, folderId));
  const total = Number(snippetCount) + Number(subFolderCount);
  if (total > 0 && !confirmed) {
    return { error: `This folder contains ${total} item(s). Confirm by sending confirmed=yes.` };
  }
  // Cascade deletes snippets and subfolders via FK.
  await db.delete(sqlFolder).where(and(eq(sqlFolder.id, folderId), eq(sqlFolder.projectId, p.id)));
  return { ok: true };
}

/* ---------- Snippets ---------- */

const MAX_SQL_BYTES = 100 * 1024; // 100 KB

export async function createSnippet(_: SnippetState, form: FormData): Promise<SnippetState> {
  const p = await actionProject(form.get("projectId"));
  const name = String(form.get("name") ?? "").trim().slice(0, 200);
  const sqlText = String(form.get("sql") ?? "");
  const folderId = String(form.get("folderId") ?? "").trim() || null;
  if (!name) return { error: "Snippet name is required." };
  if (Buffer.byteLength(sqlText, "utf8") > MAX_SQL_BYTES) return { error: "SQL exceeds 100 KB limit." };
  if (folderId) {
    const folder = await db.query.sqlFolder.findFirst({ where: and(eq(sqlFolder.id, folderId), eq(sqlFolder.projectId, p.id)) });
    if (!folder) return { error: "Folder not found." };
  }
  const id = randomId();
  await db.insert(sqlSnippet).values({ id, projectId: p.id, folderId, name, sqlText });
  return { ok: true, id };
}

export async function updateSnippet(_: SnippetState, form: FormData): Promise<SnippetState> {
  const p = await actionProject(form.get("projectId"));
  const snippetId = String(form.get("snippetId") ?? "");
  const snippet = await db.query.sqlSnippet.findFirst({ where: and(eq(sqlSnippet.id, snippetId), eq(sqlSnippet.projectId, p.id)) });
  if (!snippet) notFound();
  const name = String(form.get("name") ?? snippet.name).trim().slice(0, 200);
  const sqlText = String(form.get("sql") ?? snippet.sqlText);
  if (!name) return { error: "Snippet name is required." };
  if (Buffer.byteLength(sqlText, "utf8") > MAX_SQL_BYTES) return { error: "SQL exceeds 100 KB limit." };
  await db.update(sqlSnippet).set({ name, sqlText, updatedAt: new Date() }).where(and(eq(sqlSnippet.id, snippetId), eq(sqlSnippet.projectId, p.id)));
  return { ok: true };
}

export async function renameSnippet(_: SnippetState, form: FormData): Promise<SnippetState> {
  const p = await actionProject(form.get("projectId"));
  const snippetId = String(form.get("snippetId") ?? "");
  const name = String(form.get("name") ?? "").trim().slice(0, 200);
  if (!name) return { error: "Snippet name is required." };
  const snippet = await db.query.sqlSnippet.findFirst({ where: and(eq(sqlSnippet.id, snippetId), eq(sqlSnippet.projectId, p.id)) });
  if (!snippet) notFound();
  await db.update(sqlSnippet).set({ name, updatedAt: new Date() }).where(and(eq(sqlSnippet.id, snippetId), eq(sqlSnippet.projectId, p.id)));
  return { ok: true };
}

export async function duplicateSnippet(_: SnippetState, form: FormData): Promise<SnippetState> {
  const p = await actionProject(form.get("projectId"));
  const snippetId = String(form.get("snippetId") ?? "");
  const snippet = await db.query.sqlSnippet.findFirst({ where: and(eq(sqlSnippet.id, snippetId), eq(sqlSnippet.projectId, p.id)) });
  if (!snippet) notFound();
  const id = randomId();
  await db.insert(sqlSnippet).values({ id, projectId: p.id, folderId: snippet.folderId, name: `${snippet.name} (copy)`, sqlText: snippet.sqlText });
  return { ok: true, id };
}

export async function moveSnippet(_: SnippetState, form: FormData): Promise<SnippetState> {
  const p = await actionProject(form.get("projectId"));
  const snippetId = String(form.get("snippetId") ?? "");
  const folderId = String(form.get("folderId") ?? "").trim() || null;
  const snippet = await db.query.sqlSnippet.findFirst({ where: and(eq(sqlSnippet.id, snippetId), eq(sqlSnippet.projectId, p.id)) });
  if (!snippet) notFound();
  if (folderId) {
    const folder = await db.query.sqlFolder.findFirst({ where: and(eq(sqlFolder.id, folderId), eq(sqlFolder.projectId, p.id)) });
    if (!folder) return { error: "Folder not found." };
  }
  await db.update(sqlSnippet).set({ folderId, updatedAt: new Date() }).where(and(eq(sqlSnippet.id, snippetId), eq(sqlSnippet.projectId, p.id)));
  return { ok: true };
}

export async function toggleFavorite(_: SnippetState, form: FormData): Promise<SnippetState> {
  const p = await actionProject(form.get("projectId"));
  const snippetId = String(form.get("snippetId") ?? "");
  const snippet = await db.query.sqlSnippet.findFirst({ where: and(eq(sqlSnippet.id, snippetId), eq(sqlSnippet.projectId, p.id)) });
  if (!snippet) notFound();
  await db.update(sqlSnippet).set({ favorite: !snippet.favorite }).where(and(eq(sqlSnippet.id, snippetId), eq(sqlSnippet.projectId, p.id)));
  return { ok: true };
}

export async function deleteSnippet(_: SnippetState, form: FormData): Promise<SnippetState> {
  const p = await actionProject(form.get("projectId"));
  const snippetId = String(form.get("snippetId") ?? "");
  const snippet = await db.query.sqlSnippet.findFirst({ where: and(eq(sqlSnippet.id, snippetId), eq(sqlSnippet.projectId, p.id)) });
  if (!snippet) notFound();
  await db.delete(sqlSnippet).where(and(eq(sqlSnippet.id, snippetId), eq(sqlSnippet.projectId, p.id)));
  return { ok: true };
}

export async function searchSnippets(projectId: string, query: string) {
  const p = await actionProject(projectId);
  const q = query.trim();
  if (!q) return [];
  return db.query.sqlSnippet.findMany({
    where: and(
      eq(sqlSnippet.projectId, p.id),
      or(ilike(sqlSnippet.name, `%${q}%`), ilike(sqlSnippet.sqlText, `%${q}%`)),
    ),
    orderBy: asc(sqlSnippet.name),
    limit: 50,
  });
}

/* ---------- Sidebar data ---------- */

export async function getSidebarData(projectId: string) {
  const p = await actionProject(projectId);
  const [folders, snippets] = await Promise.all([
    db.query.sqlFolder.findMany({ where: eq(sqlFolder.projectId, p.id), orderBy: asc(sqlFolder.name) }),
    db.query.sqlSnippet.findMany({ where: eq(sqlSnippet.projectId, p.id), orderBy: asc(sqlSnippet.name) }),
  ]);
  return { folders, snippets };
}

/* ---------- Starter snippets (created lazily on first visit) ---------- */

const STARTERS: { name: string; sql: string }[] = [
  {
    name: "Row counts per table",
    sql: `-- Row count of every customer table.
-- Replace 'your_table' with any table name below.
SELECT 'your_table' AS table_name, COUNT(*) AS rows FROM your_table`,
  },
  {
    name: "Newest 20 rows",
    sql: `-- Most recently created rows in a table.
-- Replace 'your_table' with any table name.
SELECT * FROM your_table ORDER BY created DESC LIMIT 20`,
  },
  {
    name: "Find duplicates in a column",
    sql: `-- Find values that appear more than once.
-- Replace 'your_table' and 'your_column'.
SELECT your_column, COUNT(*) AS occurrences
FROM your_table
GROUP BY your_column
HAVING COUNT(*) > 1
ORDER BY occurrences DESC`,
  },
  {
    name: "Rows created today",
    sql: `-- Records created since midnight UTC.
-- Replace 'your_table' with any table name.
SELECT * FROM your_table
WHERE created >= date('now')
ORDER BY created DESC`,
  },
  {
    name: "Storage usage by table",
    sql: `-- Approximate number of rows per table (SQLite page estimate).
-- Use row counts above for exact numbers.
SELECT name AS table_name
FROM sqlite_master
WHERE type = 'table' AND name NOT LIKE '_%'
ORDER BY name`,
  },
];

export async function ensureStarterSnippets(projectId: string): Promise<void> {
  const p = await actionProject(projectId);
  // Only create once: check if any snippet exists for this project.
  const existing = await db.query.sqlSnippet.findFirst({ where: eq(sqlSnippet.projectId, p.id) });
  if (existing) return;
  // Create the Getting started folder.
  const folderId = randomId();
  await db.insert(sqlFolder).values({ id: folderId, projectId: p.id, parentId: null, name: "Getting started" });
  for (const s of STARTERS) {
    await db.insert(sqlSnippet).values({ id: randomId(), projectId: p.id, folderId, name: s.name, sqlText: s.sql });
  }
}
