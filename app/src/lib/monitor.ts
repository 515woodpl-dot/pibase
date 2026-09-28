import "server-only";
import { statfsSync, readFileSync } from "node:fs";
import os from "node:os";
import { and, desc, eq, inArray, isNull, sql } from "drizzle-orm";
import { db, pool } from "@/db";
import { alertState, backup, project } from "@/db/schema";
import { CONTROL_PREFIX } from "./backups";
import { pbInternalUrl } from "./config";
import { mailConfigured, renderMail, sendMail } from "./mail";
import { backupsConfigured, listObjects } from "./storage";

/*
 * Health monitoring for the DATBase server.
 * Every MONITOR_INTERVAL_MIN (default 5) the checks below run. A problem must show up in
 * MONITOR_CONFIRM_RUNS runs in a row (default 2) before it alerts, so a restart or a blip doesn't page
 * you. Alerts (new problems and "resolved") go to ADMIN_EMAIL and ALERT_WEBHOOK_URL (Home Assistant,
 * Discord, Slack or anything that accepts JSON). The same data is served at /api/monitor.
 */

const num = (v: string | undefined, d: number) => (v !== undefined && v !== "" && Number.isFinite(Number(v)) ? Number(v) : d);
const limits = () => ({
  diskPct: num(process.env.MONITOR_DISK_PCT, 85),
  memPct: num(process.env.MONITOR_MEM_PCT, 90),
  backupMaxAgeH: num(process.env.MONITOR_BACKUP_MAX_AGE_H, 36),
  confirmRuns: Math.max(1, num(process.env.MONITOR_CONFIRM_RUNS, 2)),
});

export type Problem = { key: string; message: string; since?: string };
export type ProjectHealth = { id: string; name: string; slug: string; status: string; healthy: boolean | null; lastBackupAt: string | null };
export type MonitorStatus = {
  status: "ok" | "problem";
  checkedAt: string;
  problems: Problem[];
  system: { diskUsedPct: number; diskFreeGb: number; memUsedPct: number; memTotalMb: number; load1: number; cpus: number; uptimeHours: number };
  projects: { total: number; active: number; paused: number; error: number; down: number; list: ProjectHealth[] };
  backups: { enabled: boolean; lastControlBackupAt: string | null; failed24h: number };
  email: { configured: boolean; alertsTo: string | null; webhook: boolean };
};

/* ---------------- measurements ---------------- */

function system() {
  let diskUsedPct = 0;
  let diskFreeGb = 0;
  try {
    const s = statfsSync(process.env.MONITOR_DISK_PATH || "/");
    // Same formula as `df`: used / (used + available to non-root). Reserved blocks don't count.
    const used = (s.blocks - s.bfree) * s.bsize;
    const avail = s.bavail * s.bsize;
    diskUsedPct = used + avail ? Math.round((used / (used + avail)) * 1000) / 10 : 0;
    diskFreeGb = Math.round((avail / 1024 ** 3) * 10) / 10;
  } catch {}
  let total = os.totalmem();
  let avail = os.freemem();
  try {
    // MemAvailable counts reclaimable cache as free (os.freemem() doesn't, which looks alarming).
    const info = readFileSync("/proc/meminfo", "utf8");
    const kb = (k: string) => Number(info.match(new RegExp(`^${k}:\\s+(\\d+)`, "m"))?.[1] ?? NaN) * 1024;
    if (Number.isFinite(kb("MemTotal")) && Number.isFinite(kb("MemAvailable"))) [total, avail] = [kb("MemTotal"), kb("MemAvailable")];
  } catch {}
  return {
    diskUsedPct,
    diskFreeGb,
    memUsedPct: Math.round(((total - avail) / total) * 1000) / 10,
    memTotalMb: Math.round(total / 1024 ** 2),
    load1: Math.round(os.loadavg()[0] * 100) / 100,
    cpus: os.cpus().length,
    uptimeHours: Math.round((os.uptime() / 3600) * 10) / 10,
  };
}

async function healthy(slug: string) {
  try {
    const r = await fetch(`${pbInternalUrl(slug)}/api/health`, { signal: AbortSignal.timeout(4000), cache: "no-store" });
    return r.ok;
  } catch {
    return false;
  }
}

let controlCache: { at: number; value: Date | null } | null = null;
async function lastControlBackup() {
  if (controlCache && Date.now() - controlCache.at < 10 * 60_000) return controlCache.value;
  const objs = await listObjects(CONTROL_PREFIX);
  const newest = objs.map((o) => o.LastModified).filter(Boolean).sort((a, b) => b!.getTime() - a!.getTime())[0] ?? null;
  controlCache = { at: Date.now(), value: newest ?? null };
  return controlCache.value;
}

const started = Date.now();

/** Measure everything and list what's wrong right now (not yet confirmed). */
async function measure() {
  const l = limits();
  const sys = system();
  const rows = await db.query.project.findMany({ where: sql`${project.status} <> 'deleting'`, orderBy: project.name });
  const ids = rows.map((r) => r.id);
  const latest = ids.length
    ? await db
        .select({ projectId: backup.projectId, kind: backup.kind, status: backup.status, createdAt: backup.createdAt, finishedAt: backup.finishedAt, error: backup.error })
        .from(backup)
        .where(inArray(backup.projectId, ids))
        .orderBy(desc(backup.createdAt))
    : [];

  const raw: Problem[] = [];
  const list: ProjectHealth[] = await Promise.all(
    rows.map(async (p) => {
      const ok = p.status === "active" ? await healthy(p.slug) : null;
      const mine = latest.filter((b) => b.projectId === p.id);
      const lastDone = mine.find((b) => b.status === "done");
      return { id: p.id, name: p.name, slug: p.slug, status: p.status, healthy: ok, lastBackupAt: lastDone?.finishedAt?.toISOString() ?? null };
    }),
  );

  const backupsOn = backupsConfigured();
  const maxAge = l.backupMaxAgeH * 3600_000;
  for (const p of list) {
    const row = rows.find((r) => r.id === p.id)!;
    const label = `Project "${p.name}" (${p.slug})`;
    if (p.healthy === false) raw.push({ key: `project-down:${p.id}`, message: `${label} is not responding.` });
    if (p.status === "error") raw.push({ key: `project-error:${p.id}`, message: `${label} is in error: ${row.statusMessage ?? "unknown"}` });
    if (!backupsOn || p.status !== "active") continue;
    const mine = latest.filter((b) => b.projectId === p.id);
    const lastAuto = mine.find((b) => b.kind === "auto" && b.status === "done");
    if (Date.now() - row.createdAt.getTime() > maxAge && (!lastAuto || Date.now() - (lastAuto.finishedAt ?? lastAuto.createdAt).getTime() > maxAge))
      raw.push({ key: `backup-stale:${p.id}`, message: `${label} has no backup from the last ${l.backupMaxAgeH} hours.` });
    const newest = mine.find((b) => b.status !== "running");
    if (newest?.status === "failed") raw.push({ key: `backup-failed:${p.id}`, message: `${label}: last backup failed: ${newest.error ?? "unknown error"}` });
  }

  let lastControl: Date | null = null;
  if (backupsOn) {
    try {
      lastControl = await lastControlBackup();
      const age = lastControl ? Date.now() - lastControl.getTime() : Date.now() - started;
      if (age > maxAge) raw.push({ key: "control-backup-stale", message: `DATBase's own database has no backup from the last ${l.backupMaxAgeH} hours.` });
    } catch (e) {
      raw.push({ key: "backup-storage", message: `Backup storage is not reachable: ${e instanceof Error ? e.message : e}` });
    }
  }
  if (sys.diskUsedPct >= l.diskPct) raw.push({ key: "disk", message: `Disk is ${sys.diskUsedPct}% full (${sys.diskFreeGb} GB free).` });
  if (sys.memUsedPct >= l.memPct) raw.push({ key: "memory", message: `Memory is ${sys.memUsedPct}% used.` });

  const day = Date.now() - 24 * 3600_000;
  const failed24h = latest.filter((b) => b.status === "failed" && b.createdAt.getTime() > day).length;
  return {
    raw,
    sys,
    list,
    backups: { enabled: backupsOn, lastControlBackupAt: lastControl?.toISOString() ?? null, failed24h },
  };
}

/* ---------------- alerting ---------------- */

const strikes = new Map<string, number>();

async function openProblems(): Promise<Problem[]> {
  const rows = await db.select().from(alertState).where(isNull(alertState.resolvedAt)).orderBy(alertState.since);
  return rows.map((r) => ({ key: r.key, message: r.message, since: r.since.toISOString() }));
}

/** Run all checks once; send alerts for new and resolved problems. Only one instance runs at a time. */
export async function runChecks(opts: { force?: boolean } = {}) {
  const client = await pool.connect();
  try {
    const { rows } = await client.query("select pg_try_advisory_lock(hashtext('datbase-monitor')) as ok");
    if (!rows[0].ok) return null;
    try {
      const m = await measure();
      const l = limits();
      const seen = new Set(m.raw.map((p) => p.key));
      for (const k of [...strikes.keys()]) if (!seen.has(k)) strikes.delete(k);
      for (const p of m.raw) strikes.set(p.key, (strikes.get(p.key) ?? 0) + 1);

      const open = await openProblems();
      const openKeys = new Set(open.map((p) => p.key));
      const confirmed = m.raw.filter((p) => opts.force || (strikes.get(p.key) ?? 0) >= l.confirmRuns);
      const added = confirmed.filter((p) => !openKeys.has(p.key));
      const resolved = open.filter((p) => !seen.has(p.key));

      for (const p of added)
        await db
          .insert(alertState)
          .values({ key: p.key, message: p.message, since: new Date(), resolvedAt: null })
          .onConflictDoUpdate({ target: alertState.key, set: { message: p.message, since: new Date(), resolvedAt: null } });
      for (const p of confirmed.filter((p) => openKeys.has(p.key)))
        await db.update(alertState).set({ message: p.message }).where(and(eq(alertState.key, p.key), isNull(alertState.resolvedAt)));
      if (resolved.length)
        await db.update(alertState).set({ resolvedAt: new Date() }).where(inArray(alertState.key, resolved.map((p) => p.key)));

      const status = await snapshot(m);
      if (added.length || resolved.length) await notify(status, added, resolved);
      return status;
    } finally {
      await client.query("select pg_advisory_unlock(hashtext('datbase-monitor'))");
    }
  } finally {
    client.release();
  }
}

async function snapshot(m: Awaited<ReturnType<typeof measure>>): Promise<MonitorStatus> {
  const problems = await openProblems();
  const count = (s: string) => m.list.filter((p) => p.status === s).length;
  shared.datbaseMonitorStatus = {
    status: problems.length ? "problem" : "ok",
    checkedAt: new Date().toISOString(),
    problems,
    system: m.sys,
    projects: { total: m.list.length, active: count("active"), paused: count("paused"), error: count("error"), down: m.list.filter((p) => p.healthy === false).length, list: m.list },
    backups: m.backups,
    email: { configured: mailConfigured(), alertsTo: process.env.ADMIN_EMAIL || null, webhook: !!process.env.ALERT_WEBHOOK_URL },
  };
  return shared.datbaseMonitorStatus;
}

// Shared across module copies: Next.js loads this file separately for the background loop
// (instrumentation) and for routes/pages, so a plain module variable wouldn't be seen by both.
const shared = globalThis as unknown as { datbaseMonitorStatus?: MonitorStatus };

/** Latest status (fresh measurement at most every 15 s; problems are the confirmed open ones). */
export async function getStatus(): Promise<MonitorStatus> {
  const cached = shared.datbaseMonitorStatus;
  if (cached && Date.now() - Date.parse(cached.checkedAt) < 15_000) return cached;
  return snapshot(await measure());
}

async function notify(s: MonitorStatus, added: Problem[], resolved: Problem[]) {
  const base = (process.env.BETTER_AUTH_URL ?? "").replace(/\/+$/, "");
  const title =
    s.status === "ok"
      ? "DATBase: all clear"
      : `DATBase: ${s.problems.length} problem${s.problems.length > 1 ? "s" : ""}${added.length ? ` (${added.length} new)` : ""}`;
  const stillOpen = s.problems.filter((p) => !added.some((a) => a.key === p.key));
  const lines = [
    ...added.map((p) => `NEW: ${p.message}`),
    ...resolved.map((p) => `RESOLVED: ${p.message}`),
    ...stillOpen.map((p) => `Still open: ${p.message}`),
  ];
  await sendAlert(title, lines, s.status === "ok" ? "ok" : "problem", { new: added, resolved, problems: s.problems, adminUrl: `${base}/dashboard/admin` });
}

/** Send one alert to every configured channel. Returns which channels accepted it. */
export async function sendAlert(title: string, lines: string[], level: "ok" | "problem" | "test", extra: Record<string, unknown> = {}) {
  const base = (process.env.BETTER_AUTH_URL ?? "").replace(/\/+$/, "");
  const message = lines.join("\n");
  const sent = { email: false, webhook: false };
  if (process.env.ADMIN_EMAIL) {
    const m = renderMail({ heading: title, lines, button: { label: "Open the status page", url: `${base}/dashboard/admin` } });
    sent.email = await sendMail({ to: process.env.ADMIN_EMAIL, subject: title, ...m });
  }
  if (process.env.ALERT_WEBHOOK_URL) {
    try {
      const r = await fetch(process.env.ALERT_WEBHOOK_URL, {
        method: "POST",
        headers: { "content-type": "application/json" },
        // title/message/level for Home Assistant and generic receivers, text for Slack, content for Discord.
        body: JSON.stringify({ title, message, level, text: `*${title}*\n${message}`, content: `**${title}**\n${message}`, ...extra }),
        signal: AbortSignal.timeout(10_000),
      });
      sent.webhook = r.ok;
      if (!r.ok) console.error(`[monitor] webhook answered ${r.status}`);
    } catch (e) {
      console.error("[monitor] webhook failed:", e instanceof Error ? e.message : e);
    }
  }
  if (!sent.email && !sent.webhook) console.warn(`[monitor] alert not delivered anywhere: ${title}\n${message}`);
  return sent;
}

export function startMonitorLoop() {
  const g = globalThis as unknown as { monitorLoopStarted?: boolean };
  if (g.monitorLoopStarted || process.env.MONITOR_DISABLED === "1") return;
  g.monitorLoopStarted = true;
  const everyMs = Math.max(3_000, num(process.env.MONITOR_INTERVAL_MIN, 5) * 60_000);
  const tick = async () => {
    try {
      await runChecks();
    } catch (e) {
      console.error("[monitor] check failed:", e instanceof Error ? e.message : e);
    } finally {
      setTimeout(() => void tick(), everyMs).unref();
    }
  };
  setTimeout(() => void tick(), Math.max(1, num(process.env.MONITOR_FIRST_DELAY_SEC, 60)) * 1000).unref();
  console.log(`[monitor] checking every ${Math.round(everyMs / 1000)} s; alerts → ${[process.env.ADMIN_EMAIL, process.env.ALERT_WEBHOOK_URL && "webhook"].filter(Boolean).join(", ") || "nowhere (set ADMIN_EMAIL / ALERT_WEBHOOK_URL)"}`);
}
