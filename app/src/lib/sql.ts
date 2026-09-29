import "server-only";
import { and, desc, eq, inArray } from "drizzle-orm";
import { db } from "@/db";
import { project, sqlRun, type Project } from "@/db/schema";
import { readyProject } from "./busy";
import { pbInternalUrl } from "./config";
import { randomId } from "./crypto";
import { controlHeaders } from "./pb";

/*
 * DATBase SQL: runs one read-only query against a project for its owner's dashboard SQL editor.
 * The checks that make it read-only and hide internal data live in the project's engine hook
 * (pocketbase/pb_hooks/datbase_sql.js + the /api/datbase/sql handler); this side adds the owner's
 * limits: project must be ready, 30 runs per minute per project, a 10 s deadline, history.
 * Callers must already have checked ownership (findOwnedProject).
 */

export const SQL_TIMEOUT_MS = 10_000;
/** Per project; SQL_RUNS_PER_MINUTE overrides it (tests use it; the default is the product limit). */
export const SQL_RUNS_PER_MINUTE = Math.max(1, Number(process.env.SQL_RUNS_PER_MINUTE) || 30);
export const SQL_HISTORY = 20;

export type SqlResult =
  | { columns: string[]; rows: unknown[][]; rowCount: number; truncated: boolean; elapsedMs: number }
  | { error: string; elapsedMs?: number };

// Survives dev hot reloads like busy.ts; one Next.js process serves the dashboard.
const g = globalThis as unknown as { datbaseSqlRuns?: Map<string, number[]> };
const runs = () => (g.datbaseSqlRuns ??= new Map());

/** Sliding one-minute window: true (and counted) if the project may run another query now. */
function takeRun(projectId: string, now = Date.now()): boolean {
  const recent = (runs().get(projectId) ?? []).filter((t: number) => now - t < 60_000);
  if (recent.length >= SQL_RUNS_PER_MINUTE) {
    runs().set(projectId, recent);
    return false;
  }
  recent.push(now);
  runs().set(projectId, recent);
  return true;
}

function notReadyMessage(p: Project): string {
  switch (p.status) {
    case "paused":
      return "This project is paused. Resume it on the Overview tab to run queries.";
    case "suspended":
      return "This project is suspended by DATBase, so queries can't run.";
    case "creating":
      return "Your backend is still being set up.";
    case "restoring":
      return "A backup is being restored. Try again when it's done.";
    case "error":
      return "Setup failed. Retry it from the Overview tab.";
    default:
      return "The project is being updated. Try again in a moment.";
  }
}

async function record(projectId: string, sql: string, r: SqlResult) {
  await db.insert(sqlRun).values({
    id: randomId(),
    projectId,
    sql: sql.slice(0, 10_000),
    elapsedMs: r.elapsedMs ?? null,
    rowCount: "error" in r ? null : r.rowCount,
    error: "error" in r ? r.error.slice(0, 1000) : null,
  });
  const keep = await db
    .select({ id: sqlRun.id })
    .from(sqlRun)
    .where(eq(sqlRun.projectId, projectId))
    .orderBy(desc(sqlRun.createdAt), desc(sqlRun.id))
    .offset(SQL_HISTORY);
  if (keep.length) await db.delete(sqlRun).where(and(eq(sqlRun.projectId, projectId), inArray(sqlRun.id, keep.map((k) => k.id))));
}

/**
 * Run a query for an owned project. `signal` is the browser's request: when the owner presses Cancel
 * (or closes the tab) the engine request is aborted and the engine interrupts the query.
 */
export async function runProjectSql(owned: Project, query: string, signal: AbortSignal): Promise<{ status: number; body: SqlResult }> {
  const p = await readyProject(owned.id);
  if (!p) {
    const now = (await db.query.project.findFirst({ where: eq(project.id, owned.id) })) ?? owned;
    return { status: 409, body: { error: notReadyMessage(now) } };
  }
  const sql = String(query ?? "");
  if (!sql.trim()) return { status: 400, body: { error: "No SQL provided." } };
  if (Buffer.byteLength(sql, "utf8") > 100 * 1024) return { status: 400, body: { error: "Query too large (max 100 KB)." } };
  if (!takeRun(p.id)) {
    return { status: 429, body: { error: `Rate limit reached: at most ${SQL_RUNS_PER_MINUTE} queries per minute per project. Wait a moment and try again.` } };
  }

  const deadline = AbortSignal.timeout(SQL_TIMEOUT_MS);
  const started = Date.now();
  let body: SqlResult;
  let status = 200;
  try {
    const res = await fetch(`${pbInternalUrl(p.slug)}/api/datbase/sql`, {
      method: "POST",
      headers: { "content-type": "application/json", ...controlHeaders(p.slug) },
      body: JSON.stringify({ sql }),
      signal: AbortSignal.any([signal, deadline]),
    });
    const json = (await res.json().catch(() => null)) as SqlResult | null;
    if (res.status === 500 || res.status === 404) {
      // 404: an engine from before the SQL editor; 500: the first (broken) SQL version of the hook.
      status = 502;
      body = { error: "This project needs an update before the SQL editor works (the operator runs Update all projects on Server status).", elapsedMs: Date.now() - started };
    } else if (!json) {
      status = 502;
      body = { error: `The project didn't answer (${res.status}). If the project was just updated, try again.`, elapsedMs: Date.now() - started };
    } else if (!res.ok || "error" in json) {
      status = 400;
      body = {
        error: "error" in json ? json.error : `Query failed (${res.status}).`,
        elapsedMs: json.elapsedMs ?? Date.now() - started,
      };
    } else {
      body = json;
    }
  } catch {
    status = 400;
    body = deadline.aborted
      ? { error: `The query took longer than ${SQL_TIMEOUT_MS / 1000} s and was stopped.`, elapsedMs: Date.now() - started }
      : signal.aborted
        ? { error: "Cancelled.", elapsedMs: Date.now() - started }
        : { error: "Couldn't reach the project. Check that it is running.", elapsedMs: Date.now() - started };
  }
  await record(p.id, sql, body).catch((e) => console.error(`[sql] history for ${p.slug}:`, e instanceof Error ? e.message : e));
  return { status, body };
}
