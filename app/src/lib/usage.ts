import "server-only";
import { and, eq, sql } from "drizzle-orm";
import { db } from "@/db";
import { project, projectUsage, usageMonth, usageNotice, user, type Project, type ProjectUsage } from "@/db/schema";
import { config } from "./config";
import { enc, json, listCollections, pb, type Page } from "./pb";
import { planFor, type Plan } from "./plans";

export const monthOf = (d: Date) => d.toISOString().slice(0, 7); // "2026-09" (UTC)

export type Limits = { blockAll: boolean; blockWrites: boolean; blockSignups: boolean };

export function limitsFor(u: { dbBytes: number; storageBytes: number; users: number }, requests: number, plan: Plan): Limits {
  return {
    blockAll: requests >= plan.requestsPerMonth,
    blockWrites: u.dbBytes >= plan.dbBytes || u.storageBytes >= plan.storageBytes,
    blockSignups: u.users >= plan.users,
  };
}

/** Measure one project, record it, and switch its limits on/off inside PocketBase if needed. */
export async function collectUsage(p: Project) {
  let warn: Warning[] = [];
  // One collector per project at a time (background loop vs. Refresh button), so each
  // request-log window is counted exactly once (F-06). The lock is released at commit.
  await db.transaction(async (tx) => {
    await tx.execute(sql`select pg_advisory_xact_lock(hashtext(${"usage:" + p.id}))`);
    const now = new Date();
    const prev = await tx.query.projectUsage.findFirst({ where: eq(projectUsage.projectId, p.id) });

    // 1. API requests since the last measurement, counted by the DATBase hook inside the project.
    //    DATBase's own calls aren't counted; admin/server-key traffic is (it's the customer's usage).
    //    "take" returns the count and resets it, so each request is added exactly once.
    const { requests: newRequests } = await pb<{ requests: number }>(p, "/api/datbase/requests/take", { method: "POST" });

    // 2. Sizes (from the DATBase hook inside PocketBase) and 3. app users across auth tables
    const sizes = await pb<{ dbBytes: number; storageBytes: number }>(p, "/api/datbase/usage");
    const authTables = (await listCollections(p)).filter((c) => c.type === "auth");
    const counts = await Promise.all(
      authTables.map((c) => pb<Page<unknown>>(p, `/api/collections/${enc(c.name)}/records?perPage=1&fields=id`)),
    );
    const users = counts.reduce((n, r) => n + r.totalItems, 0);

    // Add this window's requests to the month they happened in
    const month = monthOf(now);
    const [m] = await tx
      .insert(usageMonth)
      .values({ projectId: p.id, month, requests: newRequests })
      .onConflictDoUpdate({
        target: [usageMonth.projectId, usageMonth.month],
        set: { requests: sql`${usageMonth.requests} + ${newRequests}` },
      })
      .returning();

    const snapshot = { dbBytes: sizes.dbBytes, storageBytes: sizes.storageBytes, users };
    const limits = limitsFor(snapshot, m.requests, planFor(p.ownerId));

    const changed =
      !prev ||
      prev.blockAll !== limits.blockAll ||
      prev.blockWrites !== limits.blockWrites ||
      prev.blockSignups !== limits.blockSignups;
    if (changed) await pb(p, "/api/datbase/limits", json("POST", limits));

    const row = { ...snapshot, ...limits, logsUntil: now, checkedAt: now };
    await tx
      .insert(projectUsage)
      .values({ projectId: p.id, ...row })
      .onConflictDoUpdate({ target: projectUsage.projectId, set: row });

    warn = await newWarnings(tx, p, month, snapshot, m.requests, planFor(p.ownerId));
  });
  if (warn.length) await sendWarnings(p, warn); // after commit: never hold the lock while emailing
}

/* ---------- 80% / 100% plan-limit emails to the project owner (once per month, metric and level) ---------- */

type Warning = { metric: string; level: 80 | 100; used: number; limit: number };
type Tx = Parameters<Parameters<typeof db.transaction>[0]>[0];

async function newWarnings(tx: Tx, p: Project, month: string, u: { dbBytes: number; storageBytes: number; users: number }, requests: number, plan: Plan) {
  const metrics = [
    { metric: "requests", used: requests, limit: plan.requestsPerMonth },
    { metric: "database", used: u.dbBytes, limit: plan.dbBytes },
    { metric: "storage", used: u.storageBytes, limit: plan.storageBytes },
    { metric: "users", used: u.users, limit: plan.users },
  ];
  const out: Warning[] = [];
  for (const x of metrics) {
    if (!x.limit || x.used < x.limit * 0.8) continue;
    const level = x.used >= x.limit ? 100 : 80;
    // Record every level reached (a jump straight to 100% also marks 80% as done) and email only the highest new one.
    const inserted = await tx
      .insert(usageNotice)
      .values((level === 100 ? [80, 100] : [80]).map((l) => ({ projectId: p.id, month, metric: x.metric, level: l })))
      .onConflictDoNothing()
      .returning({ level: usageNotice.level });
    if (inserted.some((r) => r.level === level)) out.push({ metric: x.metric, level, used: x.used, limit: x.limit });
  }
  return out;
}

const fmt = (metric: string, n: number) =>
  metric === "database" || metric === "storage" ? `${Math.round((n / 1024 / 1024) * 10) / 10} MB` : n.toLocaleString("en-US");
const NAMES: Record<string, string> = { requests: "API requests this month", database: "database space", storage: "file storage", users: "app users" };
const EFFECT: Record<string, string> = {
  requests: "its API answers every request with an error until the 1st of next month (UTC)",
  database: "new records and edits are refused (reading and deleting still work); delete data to free space",
  storage: "new uploads and edits are refused (reading and deleting still work); delete files to free space",
  users: "new sign-ups are refused; existing users can still sign in",
};

async function sendWarnings(p: Project, list: Warning[]) {
  const owner = await db.query.user.findFirst({ where: eq(user.id, p.ownerId) });
  if (!owner) return;
  const { renderMail, sendMail } = await import("./mail");
  const base = (process.env.BETTER_AUTH_URL ?? "").replace(/\/+$/, "");
  const top = list.some((w) => w.level === 100) ? 100 : 80;
  const subject = top === 100 ? `"${p.name}" reached a plan limit` : `"${p.name}" is close to a plan limit`;
  const lines = list.map((w) =>
    w.level === 100
      ? `Limit reached: ${NAMES[w.metric]}: ${fmt(w.metric, w.used)} of ${fmt(w.metric, w.limit)}. Now ${EFFECT[w.metric]}.`
      : `${Math.floor((w.used / w.limit) * 100)}% used: ${NAMES[w.metric]}: ${fmt(w.metric, w.used)} of ${fmt(w.metric, w.limit)}. At 100%, ${EFFECT[w.metric]}.`,
  );
  const m = renderMail({ heading: subject, lines, button: { label: "See usage", url: `${base}/dashboard/${p.id}/usage` } });
  await sendMail({ to: owner.email, subject, ...m });
}

/** Measure every active project. Errors on one project never stop the others. */
export async function collectAll() {
  const active = await db.query.project.findMany({ where: eq(project.status, "active") });
  const { readyProject } = await import("./busy");
  for (const listed of active) {
    const p = await readyProject(listed.id); // not while it's being restored or updated
    if (!p) continue;
    try {
      await collectUsage(p);
    } catch (e) {
      console.error(`[usage] ${p.slug}:`, e instanceof Error ? e.message : e);
    }
  }
}

/** Start the background loop once per server process. */
export function startUsageLoop() {
  const g = globalThis as unknown as { usageLoop?: NodeJS.Timeout };
  if (g.usageLoop) return;
  const ms = Math.max(1, config.usageIntervalMin) * 60_000;
  setTimeout(() => void collectAll(), 30_000); // first pass shortly after boot
  g.usageLoop = setInterval(() => void collectAll(), ms);
  console.log(`[usage] measuring every ${config.usageIntervalMin} min`);
}

/** What the dashboard shows: snapshot + this month's requests. */
export async function usageView(p: Project): Promise<{ u: ProjectUsage | undefined; requests: number; plan: Plan }> {
  const [u, m] = await Promise.all([
    db.query.projectUsage.findFirst({ where: eq(projectUsage.projectId, p.id) }),
    db.query.usageMonth.findFirst({
      where: and(eq(usageMonth.projectId, p.id), eq(usageMonth.month, monthOf(new Date()))),
    }),
  ]);
  return { u, requests: m?.requests ?? 0, plan: planFor(p.ownerId) };
}
