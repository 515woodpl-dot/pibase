"use server";

import { and, asc, desc, eq, ilike, inArray, or } from "drizzle-orm";
import { db } from "@/db";
import { sqlEditorState, sqlFolder, sqlRun, sqlSnippet } from "@/db/schema";
import { randomId } from "@/lib/crypto";
import { listCollections } from "@/lib/pb";
import { actionProject } from "@/lib/projects";
import { checkName, depthOf, descendants, MAX_FOLDER_DEPTH, MAX_SNIPPET_BYTES, moveProblem } from "./folders";

// Snippets, folders and history of the SQL editor. Every action loads the project through
// actionProject (404 unless the signed-in user owns it) and only touches folders and snippets whose
// project_id is that project, so ids from another project behave exactly like ids that don't exist.
// Running queries is not here: see ./run/route.ts (server actions run one at a time per tab).

export type ActionResult = { ok: true; id?: string } | { error: string };

const NOT_FOUND = { error: "Not found. It may have been deleted in another tab; reload the page." } as const;

function isUniqueViolation(e: unknown): boolean {
  for (let x = e as { code?: string; cause?: unknown } | undefined, i = 0; x && i < 5; x = x.cause as typeof x, i++) {
    if (x.code === "23505") return true;
  }
  return false;
}

async function projectFolders(projectId: string) {
  return db.select({ id: sqlFolder.id, parentId: sqlFolder.parentId, name: sqlFolder.name }).from(sqlFolder).where(eq(sqlFolder.projectId, projectId));
}

async function ownedSnippet(projectId: string, snippetId: unknown) {
  return db.query.sqlSnippet.findFirst({ where: and(eq(sqlSnippet.id, String(snippetId)), eq(sqlSnippet.projectId, projectId)) });
}

/* ---------- Read ---------- */

/** Tables and visible columns for the schema browser and autocomplete (hidden fields left out). */
export async function getSqlSchema(projectId: string) {
  const p = await actionProject(projectId);
  const tables = await listCollections(p);
  return tables.map((t) => ({
    name: t.name,
    kind: t.type === "auth" ? "users" : "data",
    columns: t.fields.filter((f) => !f.hidden && f.type !== "password").map((f) => ({ name: f.name, type: f.type })),
  }));
}

export async function getSqlHistory(projectId: string) {
  const p = await actionProject(projectId);
  return db.query.sqlRun.findMany({ where: eq(sqlRun.projectId, p.id), orderBy: [desc(sqlRun.createdAt), desc(sqlRun.id)], limit: 20 });
}

export async function getSidebarData(projectId: string) {
  const p = await actionProject(projectId);
  const [folders, snippets] = await Promise.all([
    db.query.sqlFolder.findMany({ where: eq(sqlFolder.projectId, p.id), orderBy: asc(sqlFolder.name) }),
    db.query.sqlSnippet.findMany({ where: eq(sqlSnippet.projectId, p.id), orderBy: asc(sqlSnippet.name) }),
  ]);
  return { folders, snippets };
}

export async function searchSnippets(projectId: string, query: string) {
  const p = await actionProject(projectId);
  const q = String(query ?? "").trim().slice(0, 200);
  if (!q) return [];
  const like = `%${q.replace(/[\\%_]/g, (c) => "\\" + c)}%`; // search for the text as typed
  return db.query.sqlSnippet.findMany({
    where: and(eq(sqlSnippet.projectId, p.id), or(ilike(sqlSnippet.name, like), ilike(sqlSnippet.sqlText, like))),
    orderBy: asc(sqlSnippet.name),
    limit: 50,
  });
}

/* ---------- Folders ---------- */

export async function createFolder(projectId: string, rawName: string, parentId: string | null): Promise<ActionResult> {
  const p = await actionProject(projectId);
  const n = checkName(rawName, "Folder");
  if ("error" in n) return n;
  const all = await projectFolders(p.id);
  if (parentId) {
    if (!all.some((f) => f.id === parentId)) return NOT_FOUND;
    if (depthOf(all, parentId) >= MAX_FOLDER_DEPTH) return { error: `Folders can be nested up to ${MAX_FOLDER_DEPTH} levels.` };
  }
  try {
    const id = randomId();
    await db.insert(sqlFolder).values({ id, projectId: p.id, parentId: parentId || null, name: n.name });
    return { ok: true, id };
  } catch (e) {
    if (isUniqueViolation(e)) return { error: `There is already a folder called "${n.name}" here.` };
    throw e;
  }
}

export async function renameFolder(projectId: string, folderId: string, rawName: string): Promise<ActionResult> {
  const p = await actionProject(projectId);
  const n = checkName(rawName, "Folder");
  if ("error" in n) return n;
  try {
    const r = await db.update(sqlFolder).set({ name: n.name }).where(and(eq(sqlFolder.id, String(folderId)), eq(sqlFolder.projectId, p.id))).returning({ id: sqlFolder.id });
    return r.length ? { ok: true } : NOT_FOUND;
  } catch (e) {
    if (isUniqueViolation(e)) return { error: `There is already a folder called "${n.name}" here.` };
    throw e;
  }
}

export async function moveFolder(projectId: string, folderId: string, newParentId: string | null): Promise<ActionResult> {
  const p = await actionProject(projectId);
  const all = await projectFolders(p.id);
  const folder = all.find((f) => f.id === folderId);
  if (!folder || (newParentId && !all.some((f) => f.id === newParentId))) return NOT_FOUND;
  const problem = moveProblem(all, folder.id, newParentId || null);
  if (problem) return { error: problem };
  try {
    await db.update(sqlFolder).set({ parentId: newParentId || null }).where(and(eq(sqlFolder.id, folder.id), eq(sqlFolder.projectId, p.id)));
    return { ok: true };
  } catch (e) {
    if (isUniqueViolation(e)) return { error: `There is already a folder called "${folder.name}" there.` };
    throw e;
  }
}

/** What deleting a folder would remove (for the confirmation). */
export async function folderContents(projectId: string, folderId: string): Promise<{ folders: number; snippets: number } | { error: string }> {
  const p = await actionProject(projectId);
  const all = await projectFolders(p.id);
  if (!all.some((f) => f.id === folderId)) return NOT_FOUND;
  const ids = [folderId, ...descendants(all, folderId)];
  const snippets = await db.select({ id: sqlSnippet.id }).from(sqlSnippet).where(and(eq(sqlSnippet.projectId, p.id), inArray(sqlSnippet.folderId, ids)));
  return { folders: ids.length - 1, snippets: snippets.length };
}

/** Deletes the folder, its subfolders and every snippet in them (foreign-key cascade). */
export async function deleteFolder(projectId: string, folderId: string): Promise<ActionResult> {
  const p = await actionProject(projectId);
  const r = await db.delete(sqlFolder).where(and(eq(sqlFolder.id, String(folderId)), eq(sqlFolder.projectId, p.id))).returning({ id: sqlFolder.id });
  return r.length ? { ok: true } : NOT_FOUND;
}

/* ---------- Snippets ---------- */

function checkSql(sqlText: unknown): { sql: string } | { error: string } {
  const sql = String(sqlText ?? "");
  if (Buffer.byteLength(sql, "utf8") > MAX_SNIPPET_BYTES) return { error: "Snippets can be at most 100 KB." };
  return { sql };
}

async function folderOk(projectId: string, folderId: string | null) {
  if (!folderId) return true;
  return !!(await db.query.sqlFolder.findFirst({ where: and(eq(sqlFolder.id, folderId), eq(sqlFolder.projectId, projectId)) }));
}

export async function createSnippet(projectId: string, rawName: string, sqlText: string, folderId: string | null): Promise<ActionResult> {
  const p = await actionProject(projectId);
  const n = checkName(rawName, "Snippet");
  if ("error" in n) return n;
  const s = checkSql(sqlText);
  if ("error" in s) return s;
  if (!(await folderOk(p.id, folderId || null))) return NOT_FOUND;
  const id = randomId();
  await db.insert(sqlSnippet).values({ id, projectId: p.id, folderId: folderId || null, name: n.name, sqlText: s.sql });
  return { ok: true, id };
}

/** Save the editor's text into an existing snippet. */
export async function updateSnippet(projectId: string, snippetId: string, sqlText: string): Promise<ActionResult> {
  const p = await actionProject(projectId);
  const s = checkSql(sqlText);
  if ("error" in s) return s;
  const r = await db.update(sqlSnippet).set({ sqlText: s.sql, updatedAt: new Date() }).where(and(eq(sqlSnippet.id, String(snippetId)), eq(sqlSnippet.projectId, p.id))).returning({ id: sqlSnippet.id });
  return r.length ? { ok: true } : NOT_FOUND;
}

export async function renameSnippet(projectId: string, snippetId: string, rawName: string): Promise<ActionResult> {
  const p = await actionProject(projectId);
  const n = checkName(rawName, "Snippet");
  if ("error" in n) return n;
  const r = await db.update(sqlSnippet).set({ name: n.name, updatedAt: new Date() }).where(and(eq(sqlSnippet.id, String(snippetId)), eq(sqlSnippet.projectId, p.id))).returning({ id: sqlSnippet.id });
  return r.length ? { ok: true } : NOT_FOUND;
}

export async function duplicateSnippet(projectId: string, snippetId: string): Promise<ActionResult> {
  const p = await actionProject(projectId);
  const s = await ownedSnippet(p.id, snippetId);
  if (!s) return NOT_FOUND;
  const id = randomId();
  await db.insert(sqlSnippet).values({ id, projectId: p.id, folderId: s.folderId, name: `${s.name} (copy)`.slice(0, 200), sqlText: s.sqlText });
  return { ok: true, id };
}

export async function moveSnippet(projectId: string, snippetId: string, folderId: string | null): Promise<ActionResult> {
  const p = await actionProject(projectId);
  if (!(await folderOk(p.id, folderId || null))) return NOT_FOUND;
  const r = await db.update(sqlSnippet).set({ folderId: folderId || null, updatedAt: new Date() }).where(and(eq(sqlSnippet.id, String(snippetId)), eq(sqlSnippet.projectId, p.id))).returning({ id: sqlSnippet.id });
  return r.length ? { ok: true } : NOT_FOUND;
}

export async function toggleFavorite(projectId: string, snippetId: string): Promise<ActionResult> {
  const p = await actionProject(projectId);
  const s = await ownedSnippet(p.id, snippetId);
  if (!s) return NOT_FOUND;
  await db.update(sqlSnippet).set({ favorite: !s.favorite }).where(and(eq(sqlSnippet.id, s.id), eq(sqlSnippet.projectId, p.id)));
  return { ok: true };
}

export async function deleteSnippet(projectId: string, snippetId: string): Promise<ActionResult> {
  const p = await actionProject(projectId);
  const r = await db.delete(sqlSnippet).where(and(eq(sqlSnippet.id, String(snippetId)), eq(sqlSnippet.projectId, p.id))).returning({ id: sqlSnippet.id });
  return r.length ? { ok: true } : NOT_FOUND;
}

/* ---------- Starter snippets (once per project, on the first visit) ---------- */

// Every project has the users table, so these run as they are.
const STARTERS: { name: string; sql: string }[] = [
  {
    name: "Newest sign-ups",
    sql: `-- The 20 most recent app users.
SELECT id, email, verified, created
FROM users
ORDER BY created DESC
LIMIT 20`,
  },
  {
    name: "Sign-ups per day",
    sql: `-- New app users per day (created is stored in UTC).
SELECT date(created) AS day, count(*) AS new_users
FROM users
GROUP BY day
ORDER BY day DESC`,
  },
  {
    name: "Unconfirmed emails",
    sql: `-- App users who haven't confirmed their email address yet.
SELECT email, created
FROM users
WHERE verified = 0
ORDER BY created`,
  },
  {
    name: "Find duplicates",
    sql: `-- Values that appear more than once. Change the table and column to check your own data.
SELECT name, count(*) AS times
FROM users
WHERE name != ''
GROUP BY name
HAVING count(*) > 1
ORDER BY times DESC`,
  },
  {
    name: "How a query runs",
    sql: `-- EXPLAIN QUERY PLAN shows whether a query uses an index (SEARCH) or reads the whole table (SCAN).
EXPLAIN QUERY PLAN
SELECT * FROM users WHERE email = 'someone@example.com'`,
  },
];

/** Creates the "Getting started" folder and its snippets the first time; later calls do nothing. */
export async function ensureStarterSnippets(projectId: string): Promise<void> {
  const p = await actionProject(projectId);
  await db.transaction(async (tx) => {
    const first = await tx.insert(sqlEditorState).values({ projectId: p.id }).onConflictDoNothing().returning({ projectId: sqlEditorState.projectId });
    if (!first.length) return; // already done (possibly by a request running at the same moment)
    let folderId: string | null = randomId();
    const made = await tx.insert(sqlFolder).values({ id: folderId, projectId: p.id, parentId: null, name: "Getting started" }).onConflictDoNothing().returning({ id: sqlFolder.id });
    if (!made.length) folderId = null; // the owner already has a folder with that name: put them at the top
    await tx.insert(sqlSnippet).values(STARTERS.map((s) => ({ id: randomId(), projectId: p.id, folderId, name: s.name, sqlText: s.sql })));
  });
}
