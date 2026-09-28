import "server-only";
import { spawn } from "node:child_process";
import { mkdtemp, rm } from "node:fs/promises";
import { openAsBlob } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { Readable } from "node:stream";
import { and, desc, eq, inArray, lt, or } from "drizzle-orm";
import { db, pool } from "@/db";
import { backup, project, projectUsage, type Backup, type Project } from "@/db/schema";
import { randomId } from "./crypto";
import { fileToken, forgetToken, json, pb } from "./pb";
import { KEEP_DAILY, KEEP_WEEKLY, pickExpired } from "./retention";
import {
  backupsConfigured,
  copyObject,
  decryptFile,
  getToFile,
  listObjects,
  putEncrypted,
  removeKeys,
  removePrefix,
} from "./storage";

export { backupsConfigured, KEEP_DAILY, KEEP_WEEKLY };

const DAY = 86_400_000;
export const MANUAL_COOLDOWN_MIN = 10;

/** Talk to the project's PocketBase even while its status is "restoring". */
const live = (p: Project): Project => ({ ...p, status: "active" });
const stamp = (d = new Date()) => d.toISOString().replace(/[-:]/g, "").replace(/\..+/, "Z"); // 20260926T194755Z
const errMsg = (e: unknown, fallback: string) => (e instanceof Error ? e.message : fallback);

/** Backups currently being restored. Retention never deletes these (R-04). */
const inUse = new Set<string>();

/* ------------------------------------------------------------------ */
/* Backup one project                                                  */
/* ------------------------------------------------------------------ */

/**
 * Reserve a backup slot. The unique index allows only one running backup per project,
 * so double clicks, the scheduler and a restore's safety backup can never overlap.
 */
async function reserve(p: Project, kind: Backup["kind"]) {
  try {
    const [row] = await db.insert(backup).values({ id: randomId(16), projectId: p.id, kind }).returning();
    return row;
  } catch {
    return null; // another backup of this project is already running
  }
}

/**
 * PocketBase zip → encrypt → bucket → mark done. Never holds the whole backup in memory or on disk.
 * If the upload succeeded but recording it failed, the uploaded object is removed, so nothing is orphaned (R-06).
 */
async function run(p: Project, row: Backup): Promise<Backup> {
  const name = `dbk_${row.id}.zip`;
  const key = `projects/${p.id}/${stamp(row.createdAt)}-${row.id}.dbk`;
  let uploaded = false;
  try {
    await pb(live(p), "/api/backups", json("POST", { name })); // PocketBase snapshots data + files
    const token = await fileToken(live(p));
    const { pbInternalUrl } = await import("./config");
    const { controlHeaders } = await import("./pb");
    const res = await fetch(`${pbInternalUrl(p.slug)}/api/backups/${name}?token=${encodeURIComponent(token)}`, {
      headers: controlHeaders(p.slug),
    });
    if (!res.ok || !res.body) throw new Error(`Download from project failed (${res.status})`);
    const size = await putEncrypted(key, Readable.fromWeb(res.body as import("stream/web").ReadableStream));
    uploaded = true;

    const [done] = await db
      .update(backup)
      .set({ status: "done", storageKey: key, sizeBytes: size, finishedAt: new Date() })
      .where(eq(backup.id, row.id))
      .returning();
    if (!done) throw new Error("Backup record disappeared");
    return done;
  } catch (e) {
    const error = errMsg(e, "Backup failed");
    console.error(`[backup] ${p.slug}:`, error);
    if (uploaded) await removeKeys([key]).catch(() => {});
    await db
      .update(backup)
      .set({ status: "failed", error, finishedAt: new Date() })
      .where(eq(backup.id, row.id))
      .catch(() => {});
    throw e;
  } finally {
    // The zip inside the project's volume was only a staging copy.
    await pb(live(p), `/api/backups/${name}`, { method: "DELETE" }).catch(() => {});
  }
}

/** Retention runs separately: a cleanup failure never touches the backup that was just made (R-06). */
async function tidy(projectId: string) {
  try {
    await applyRetention(projectId);
  } catch (e) {
    console.error(`[backup] retention for ${projectId} will retry next time:`, errMsg(e, "failed"));
  }
}

/** Back up a project now (awaits completion). Returns null if one is already running. */
export async function backupNow(p: Project, kind: Backup["kind"]) {
  const row = await reserve(p, kind);
  if (!row) return null;
  const done = await run(p, row);
  await tidy(p.id);
  return done;
}

/** For the "Back up now" button: starts in the background, with a cooldown against abuse. */
export async function requestManualBackup(p: Project): Promise<string | null> {
  if (!backupsConfigured()) return "Backups aren't set up on this server.";
  if (p.status !== "active") return `Project is ${p.status}.`;
  const recent = await db.query.backup.findFirst({
    where: and(eq(backup.projectId, p.id), eq(backup.kind, "manual")),
    orderBy: desc(backup.createdAt),
  });
  if (recent && Date.now() - recent.createdAt.getTime() < MANUAL_COOLDOWN_MIN * 60_000)
    return `You can back up manually once every ${MANUAL_COOLDOWN_MIN} minutes.`;
  const row = await reserve(p, "manual");
  if (!row) return "A backup is already running.";
  void run(p, row)
    .then(() => tidy(p.id))
    .catch(() => {}); // status is tracked on the row
  return null;
}

/* ------------------------------------------------------------------ */
/* Retention (rules in ./retention.ts)                                 */
/* ------------------------------------------------------------------ */

async function applyRetention(projectId: string) {
  const done = await db.query.backup.findMany({
    where: and(eq(backup.projectId, projectId), eq(backup.status, "done")),
  });
  const expired = pickExpired(done, new Date(), inUse);
  if (expired.length) {
    // Objects first: if the bucket call fails, the rows stay and the next run retries.
    await removeKeys(expired.map((d) => d.storageKey!).filter(Boolean));
    await db.delete(backup).where(inArray(backup.id, expired.map((x) => x.id)));
  }
  // Failed attempts older than a week are just noise.
  await db
    .delete(backup)
    .where(and(eq(backup.projectId, projectId), eq(backup.status, "failed"), lt(backup.createdAt, new Date(Date.now() - 7 * DAY))));
}

/* ------------------------------------------------------------------ */
/* Restore                                                             */
/* ------------------------------------------------------------------ */

async function bootId(p: Project) {
  return (await pb<{ bootId: string }>(live(p), "/api/datbase/boot")).bootId;
}

/** Download + decrypt + authenticate a backup into a temp folder. Fails on any tampering. */
async function prepare(b: Backup) {
  const dir = await mkdtemp(join(tmpdir(), "dbk-"));
  try {
    await getToFile(b.storageKey!, join(dir, "enc"));
    await decryptFile(join(dir, "enc"), join(dir, "backup.zip"));
    await rm(join(dir, "enc"), { force: true });
    return { zip: join(dir, "backup.zip"), cleanup: () => rm(dir, { recursive: true, force: true }) };
  } catch (e) {
    await rm(dir, { recursive: true, force: true });
    throw e;
  }
}

/** Load a prepared backup into the project's PocketBase and wait for its restart. */
async function apply(p: Project, backupId: string, zip: string, onReplaceStart: () => void = () => {}) {
  const name = `dbk_restore_${backupId}.zip`;
  const form = new FormData();
  form.append("file", await openAsBlob(zip), name);
  await pb(live(p), "/api/backups/upload", { method: "POST", body: form });

  const before = await bootId(p);
  onReplaceStart(); // from here on, PocketBase may already be swapping its data
  await pb(live(p), `/api/backups/${name}/restore`, { method: "POST" });

  // PocketBase swaps its data and restarts itself; wait for the new boot.
  const until = Date.now() + 120_000;
  for (;;) {
    await new Promise((r) => setTimeout(r, 1000));
    forgetToken(p.id);
    const now = await bootId(p).catch(() => null);
    if (now && now !== before) break;
    if (Date.now() > until) throw new Error("Project did not restart after restore");
  }
  await pb(live(p), `/api/backups/${name}`, { method: "DELETE" }).catch(() => {});
}

/** After a restore: make sure DATBase can still sign in, and re-apply current plan limits. */
async function afterRestore(p: Project) {
  forgetToken(p.id);
  try {
    // The restored data may carry an older admin password; put ours back (needs Docker).
    const { upsertSuperuser } = await import("./docker");
    const { decrypt } = await import("./crypto");
    if (p.adminEmail && p.adminPasswordEnc) await upsertSuperuser(p.slug, p.adminEmail, decrypt(p.adminPasswordEnc));
  } catch {
    // No Docker (local development): the admin account in the backup is the same one.
  }
  // The restored data may contain server-key accounts that were revoked later (or lack newer ones).
  const { reconcileServerKeys } = await import("./server-keys");
  await reconcileServerKeys(live(p)).catch((e) => console.error(`[restore] ${p.slug} key reconcile:`, e));
  const { configureProjectMail } = await import("./project-mail");
  await configureProjectMail(live(p)).catch((e) => console.error(`[restore] ${p.slug} email setup:`, e));
  const u = await db.query.projectUsage.findFirst({ where: eq(projectUsage.projectId, p.id) });
  if (u) {
    const limits = { blockAll: u.blockAll, blockWrites: u.blockWrites, blockSignups: u.blockSignups };
    await pb(live(p), "/api/datbase/limits", json("POST", limits));
  }
}

/**
 * Restore a project to one of its backups. Order matters (R-04):
 *   1. protect the chosen backup from retention,
 *   2. download and verify it (a damaged backup fails here, nothing else happens),
 *   3. take a safety backup of the current state (so the restore can be undone),
 *   4. load the chosen backup.
 * Runs in the background; progress is the project status.
 */
export async function requestRestore(p: Project, backupId: string): Promise<string | null> {
  if (!backupsConfigured()) return "Backups aren't set up on this server.";
  const b = await db.query.backup.findFirst({
    where: and(eq(backup.id, backupId), eq(backup.projectId, p.id), eq(backup.status, "done")),
  });
  if (!b) return "Backup not found.";

  // Atomic: only one restore at a time, and only from "active".
  const [locked] = await db
    .update(project)
    .set({ status: "restoring", statusMessage: null, updatedAt: new Date() })
    .where(and(eq(project.id, p.id), eq(project.status, "active")))
    .returning();
  if (!locked) return "The project must be active to restore.";

  inUse.add(b.id);
  const { whileBusy } = await import("./busy");
  void whileBusy(p.id, async () => {
    let reached = false; // true once PocketBase has started replacing its data
    try {
      const prepared = await prepare(b);
      try {
        const safety = await backupNow(p, "pre-restore");
        if (!safety) throw new Error("Another backup is running. Try again in a minute.");
        await apply(p, b.id, prepared.zip, () => (reached = true));
      } finally {
        await prepared.cleanup();
      }
      await afterRestore(p);
      await db.update(project).set({ status: "active", statusMessage: null, updatedAt: new Date() }).where(eq(project.id, p.id));
      const { collectUsage } = await import("./usage");
      await collectUsage({ ...p, status: "active" }).catch(() => {});
    } catch (e) {
      const msg = errMsg(e, "Restore failed");
      console.error(`[restore] ${p.slug}:`, msg);
      // Before PocketBase touched its data the project is unchanged, so it stays usable.
      await db
        .update(project)
        .set(
          reached
            ? { status: "error", statusMessage: `Restore failed: ${msg}. Use Retry, then restore again.` }
            : { status: "active", statusMessage: `Restore failed: ${msg}` },
        )
        .where(eq(project.id, p.id));
    } finally {
      inUse.delete(b.id);
    }
  });
  return null;
}

/* ------------------------------------------------------------------ */
/* DATBase's own database                                              */
/* ------------------------------------------------------------------ */

export const CONTROL_PREFIX = "control/";
const CONTROL_TMP = "control-tmp/";
const KEEP_CONTROL = 14;
const MIN_DUMP_BYTES = 1024; // a real dump is always larger; an empty/failed one is ~32 bytes encrypted

/**
 * Dump → temporary key → verify → promote into control/ (R-05). Only complete, successful dumps
 * ever appear under control/, so the scheduler and the recovery tool never pick a broken one.
 */
export async function backupControlDb() {
  await removePrefix(CONTROL_TMP).catch(() => {}); // leftovers from an interrupted run
  const tmp = `${CONTROL_TMP}${stamp()}-${randomId(6)}.dump.dbk`;
  try {
    const dump = spawn("pg_dump", ["--format=custom", "--no-owner", "--dbname", process.env.DATABASE_URL!]);
    let stderr = "";
    dump.stderr.on("data", (d) => (stderr += d));
    const exited = new Promise<number>((r) => {
      dump.on("close", (code) => r(code ?? 1));
      dump.on("error", () => r(1));
    });
    const size = await putEncrypted(tmp, dump.stdout);
    const code = await exited;
    if (code !== 0) throw new Error(`pg_dump failed (exit ${code}): ${stderr.slice(0, 300)}`);
    if (size < MIN_DUMP_BYTES) throw new Error(`pg_dump produced only ${size} bytes`);

    const final = `${CONTROL_PREFIX}${tmp.slice(CONTROL_TMP.length)}`;
    await copyObject(tmp, final);
    const all = (await listObjects(CONTROL_PREFIX)).sort((a, b) => (b.Key! > a.Key! ? 1 : -1));
    await removeKeys(all.slice(KEEP_CONTROL).map((o) => o.Key!));
    return { key: final, size };
  } finally {
    await removeKeys([tmp]).catch(() => {});
  }
}

/* ------------------------------------------------------------------ */
/* Scheduler, cleanup, deletion                                        */
/* ------------------------------------------------------------------ */

/**
 * Deleted projects whose backups couldn't be removed at the time stay in "deleting"
 * and are finished here, retried every sweep until the bucket confirms (R-02).
 */
export async function finishPendingDeletions() {
  const pending = await db.query.project.findMany({
    where: and(eq(project.status, "deleting"), lt(project.updatedAt, new Date(Date.now() - 60_000))),
  });
  for (const p of pending) {
    try {
      const { destroyInstance } = await import("./docker");
      await destroyInstance(p.slug); // idempotent
      await deleteProjectBackups(p.id);
      await db.delete(project).where(eq(project.id, p.id));
      console.log(`[delete] finished deleting ${p.slug} and its backups`);
    } catch (e) {
      console.error(`[delete] ${p.slug} still pending:`, errMsg(e, "failed"));
    }
  }
  const { removeDeletedAccounts } = await import("./lifecycle");
  await removeDeletedAccounts();
}

/** One sweep: pending deletions, daily project backups, then DATBase's own database. */
export async function backupDue() {
  if (!backupsConfigured()) return;
  await finishPendingDeletions();

  const active = await db.query.project.findMany({ where: eq(project.status, "active") });
  for (const p of active) {
    // Only an automatic backup counts as "today's daily backup is done" (R-03).
    const last = await db.query.backup.findFirst({
      where: and(eq(backup.projectId, p.id), eq(backup.status, "done"), eq(backup.kind, "auto")),
      orderBy: desc(backup.createdAt),
    });
    if (last && Date.now() - last.createdAt.getTime() < DAY) continue;
    await backupNow(p, "auto").catch(() => {}); // one at a time; failures are recorded on the row
  }
  try {
    const newest = (await listObjects(CONTROL_PREFIX)).sort((a, b) => (b.Key! > a.Key! ? 1 : -1))[0];
    if (!newest || Date.now() - (newest.LastModified?.getTime() ?? 0) > DAY) await backupControlDb();
  } catch (e) {
    console.error("[backup] control database:", errMsg(e, "failed"));
  }
}

const SWEEP_LOCK = 0x0da7ba5e; // advisory lock id shared by every DATBase instance

/**
 * Run a sweep unless one is already running, in this process or any other (R-07).
 * Returns false if skipped.
 */
export async function runSweepExclusive(sweep: () => Promise<void> = backupDue) {
  const g = globalThis as unknown as { backupSweeping?: boolean };
  if (g.backupSweeping) return false;
  g.backupSweeping = true;
  const client = await pool.connect();
  try {
    const { rows } = await client.query("select pg_try_advisory_lock($1) as ok", [SWEEP_LOCK]);
    if (!rows[0]?.ok) return false;
    try {
      await sweep();
    } finally {
      await client.query("select pg_advisory_unlock($1)", [SWEEP_LOCK]).catch(() => {});
    }
    return true;
  } finally {
    client.release();
    g.backupSweeping = false;
  }
}

/** On startup: anything that was mid-flight when the server stopped is marked, not left hanging. */
export async function cleanupInterrupted() {
  await db
    .update(backup)
    .set({ status: "failed", error: "Interrupted (server restarted)", finishedAt: new Date() })
    .where(eq(backup.status, "running"));
  await db
    .update(project)
    .set({ status: "error", statusMessage: "Restore was interrupted. Use Retry, then restore again." })
    .where(eq(project.status, "restoring"));
}

/** Next sweep is scheduled only after the previous one finishes, so sweeps never pile up (R-07). */
export function startBackupLoop() {
  const g = globalThis as unknown as { backupLoopStarted?: boolean };
  if (g.backupLoopStarted) return;
  if (!backupsConfigured()) {
    console.warn("[backup] BACKUP_S3_* not set: backups are OFF");
    return;
  }
  g.backupLoopStarted = true;
  const min = Math.max(1, Number(process.env.BACKUP_CHECK_MIN ?? 15));
  const tick = async () => {
    try {
      await runSweepExclusive();
    } catch (e) {
      console.error("[backup] sweep failed:", errMsg(e, "failed"));
    } finally {
      setTimeout(() => void tick(), min * 60_000);
    }
  };
  const firstDelaySec = Math.max(1, Number(process.env.BACKUP_FIRST_DELAY_SEC ?? 60));
  setTimeout(() => void tick(), firstDelaySec * 1000);
  console.log(`[backup] checking every ${min} min (daily per project, keep ${KEEP_DAILY} daily + ${KEEP_WEEKLY} weekly)`);
}

/**
 * Disaster recovery (RECOVER_ON_START=1 after restoring the control database on a new server):
 * recreate every active AND paused project's container and load its newest backup (R-01).
 * Paused projects are stopped again afterwards, so they stay paused but can be resumed.
 */
export async function recoverAll() {
  const { instanceExists, startInstance, stopInstance, waitHealthy, upsertSuperuser } = await import("./docker");
  const { decrypt } = await import("./crypto");
  const targets = await db.query.project.findMany({
    where: or(eq(project.status, "active"), eq(project.status, "paused"), eq(project.status, "suspended")),
  });
  const { whileBusy } = await import("./busy");
  for (const p of targets) {
    // Busy: background jobs (e.g. applying email settings at start) must not touch it mid-restore.
    await whileBusy(p.id, async () => {
    try {
      // Only rebuild what is actually missing. If this flag is left on by mistake, running
      // projects are never overwritten with an older backup.
      if (await instanceExists(p.slug)) return;
      const candidates = await db.query.backup.findMany({
        where: and(eq(backup.projectId, p.id), eq(backup.status, "done")),
        orderBy: desc(backup.createdAt),
        limit: 5,
      });
      await startInstance(p.slug, p.id);
      await waitHealthy(p.slug);
      if (p.adminEmail && p.adminPasswordEnc) await upsertSuperuser(p.slug, p.adminEmail, decrypt(p.adminPasswordEnc));
      // Newest backup first; if one is missing or damaged, fall back to the next (up to 5).
      let latest: Backup | null = null;
      for (const c of candidates) {
        let prepared: Awaited<ReturnType<typeof prepare>>;
        try {
          prepared = await prepare(c);
        } catch (e) {
          console.error(`[recover] ${p.slug}: backup ${c.id} unusable (${errMsg(e, "failed")}), trying an older one`);
          continue;
        }
        try {
          await apply(p, c.id, prepared.zip);
        } finally {
          await prepared.cleanup();
        }
        latest = c;
        break;
      }
      if (candidates.length && !latest) throw new Error("no usable backup");
      if (latest) await afterRestore(p);
      if (p.status !== "active") await stopInstance(p.slug); // paused/suspended stay stopped
      console.log(
        `[recover] ${p.slug} (${p.status}): ${latest ? `restored backup ${latest.id}` : "no backup found, started empty"}`,
      );
    } catch (e) {
      console.error(`[recover] ${p.slug}:`, errMsg(e, "failed"));
      await db.update(project).set({ status: "error", statusMessage: "Recovery failed. Use Retry." }).where(eq(project.id, p.id));
    }
    });
  }
}

/** Remove every backup object of a project. Throws if the bucket can't confirm. */
export async function deleteProjectBackups(projectId: string) {
  if (backupsConfigured()) await removePrefix(`projects/${projectId}/`);
}

export async function listBackups(projectId: string) {
  return db.query.backup.findMany({ where: eq(backup.projectId, projectId), orderBy: desc(backup.createdAt), limit: 50 });
}
