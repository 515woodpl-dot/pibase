import "server-only";
import { and, eq, inArray, isNotNull, sql } from "drizzle-orm";
import { db } from "@/db";
import { backup, project, projectUsage, session, user, type Project } from "@/db/schema";
import { json, pb, forgetToken } from "./pb";
import { setStatus } from "./provision";

/*
 * Project and account lifecycle actions shared by the owner's dashboard and the operator's admin
 * pages: delete, suspend, update to the current project image.
 */

const ACCOUNT_SUSPENDED = "The account is suspended.";
const ACCOUNT_SUSPENDED_PAUSED = "The account is suspended (project was paused).";

/** Put DATBase's limits and email settings back into a running project (after a new container). */
async function reapply(p: Project) {
  forgetToken(p.id);
  const u = await db.query.projectUsage.findFirst({ where: eq(projectUsage.projectId, p.id) });
  if (u) await pb(p, "/api/datbase/limits", json("POST", { blockAll: u.blockAll, blockWrites: u.blockWrites, blockSignups: u.blockSignups }));
  const { configureProjectMail } = await import("./project-mail");
  await configureProjectMail(p);
}

/* ---------------- delete ---------------- */

/** Container, data and backups. Returns "pending" when backups couldn't be removed yet (retried later). */
export async function deleteProjectFully(p: Project): Promise<"deleted" | "pending"> {
  await setStatus(p.id, "deleting");
  const { destroyInstance } = await import("./docker");
  try {
    await destroyInstance(p.slug);
  } catch (e) {
    await setStatus(p.id, "error", "Delete failed. Try again.");
    throw e;
  }
  const { deleteProjectBackups } = await import("./backups");
  try {
    await deleteProjectBackups(p.id);
  } catch (e) {
    // Don't finish until the backups are really gone (R-02); the backup scheduler retries.
    console.error(`[delete] backups of ${p.slug} not removed yet; will retry`, e);
    await setStatus(p.id, "deleting", "Removing backups… this finishes automatically.");
    return "pending";
  }
  await db.delete(project).where(eq(project.id, p.id));
  return "deleted";
}

/* ---------------- suspend (operator) ---------------- */

export async function suspendProject(p: Project, reason: string) {
  if (!["active", "paused", "error"].includes(p.status)) throw new Error(`Can't suspend a project that is ${p.status}.`);
  const { stopInstance } = await import("./docker");
  await stopInstance(p.slug).catch(() => {});
  await setStatus(p.id, "suspended", reason);
}

/** Lift a suspension: the project comes back online. */
export async function unsuspendProject(p: Project) {
  if (p.status !== "suspended") return;
  const { instanceExists, resumeInstance, waitHealthy } = await import("./docker");
  if (await instanceExists(p.slug)) {
    await resumeInstance(p.slug);
    await waitHealthy(p.slug);
  } else {
    const { recreateInstance } = await import("./provision");
    await recreateInstance(p);
  }
  await setStatus(p.id, "active");
}

/** Suspend an account: no sign-in, sessions ended, every project stopped. */
export async function suspendAccount(userId: string) {
  await db.update(user).set({ suspendedAt: new Date() }).where(eq(user.id, userId));
  await db.delete(session).where(eq(session.userId, userId));
  const list = await db.query.project.findMany({ where: eq(project.ownerId, userId) });
  for (const p of list)
    if (["active", "paused", "error"].includes(p.status)) await suspendProject(p, p.status === "paused" ? ACCOUNT_SUSPENDED_PAUSED : ACCOUNT_SUSPENDED);
}

export async function unsuspendAccount(userId: string) {
  await db.update(user).set({ suspendedAt: null }).where(eq(user.id, userId));
  const list = await db.query.project.findMany({ where: and(eq(project.ownerId, userId), eq(project.status, "suspended")) });
  // Only projects suspended because of the account come back; individually suspended ones stay.
  for (const p of list) {
    if (p.statusMessage === ACCOUNT_SUSPENDED) await unsuspendProject(p);
    else if (p.statusMessage === ACCOUNT_SUSPENDED_PAUSED) await setStatus(p.id, "paused"); // the owner had paused it
  }
}

/* ---------------- account deletion (owner) ---------------- */

/** Delete an account and everything in it. The user row goes once all projects (and backups) are gone. */
export async function deleteAccount(userId: string) {
  await db.update(user).set({ deletedAt: new Date() }).where(eq(user.id, userId));
  await db.delete(session).where(eq(session.userId, userId));
  const list = await db.query.project.findMany({ where: eq(project.ownerId, userId) });
  for (const p of list) {
    try {
      await deleteProjectFully(p);
    } catch (e) {
      console.error(`[account] deleting ${p.slug}:`, e instanceof Error ? e.message : e);
      await setStatus(p.id, "deleting", "Removing… this finishes automatically.");
    }
  }
  await removeDeletedAccounts();
}

/** Remove deleted accounts that no longer own any project (called after deletions and by the scheduler). */
export async function removeDeletedAccounts() {
  const gone = await db
    .select({ id: user.id })
    .from(user)
    .where(and(isNotNull(user.deletedAt), sql`not exists (select 1 from ${project} where ${project.ownerId} = ${user.id})`));
  if (gone.length) await db.delete(user).where(inArray(user.id, gone.map((g) => g.id)));
}

/* ---------------- update every project to the current image ---------------- */

type UpdateState = {
  running: boolean;
  total: number;
  done: number;
  skipped: string[];
  failed: { slug: string; error: string }[];
  startedAt: string | null;
  finishedAt: string | null;
};
const g = globalThis as unknown as { datbaseUpdate?: UpdateState };
export const updateState = (): UpdateState =>
  g.datbaseUpdate ?? { running: false, total: 0, done: 0, skipped: [], failed: [], startedAt: null, finishedAt: null };

/**
 * Give one project a fresh container from the current project image, keeping its data volume.
 * A few seconds of downtime. Paused and suspended projects stay stopped.
 */
export async function updateProject(p: Project) {
  const { whileBusy } = await import("./busy");
  await whileBusy(p.id, async () => {
    const { recreateInstance } = await import("./provision");
    await recreateInstance(p);
    if (p.status === "active") await reapply(p);
    else {
      const { stopInstance } = await import("./docker");
      await stopInstance(p.slug);
    }
  });
}

/** Which projects run an older image than the current one (Docker needed; null when unknown). */
export async function outdatedProjects(): Promise<{ total: number; outdated: string[] } | null> {
  try {
    const { currentImageId, instanceImageId } = await import("./docker");
    const current = await currentImageId();
    const list = await db.query.project.findMany({ where: inArray(project.status, ["active", "paused", "suspended"]) });
    const outdated: string[] = [];
    for (const p of list) if ((await instanceImageId(p.slug)) !== current) outdated.push(p.slug);
    return { total: list.length, outdated };
  } catch {
    return null;
  }
}

/** Update every project, one at a time, in the background. Returns false if an update is already running. */
export function startUpdateAll() {
  if (g.datbaseUpdate?.running) return false;
  const st: UpdateState = { running: true, total: 0, done: 0, skipped: [], failed: [], startedAt: new Date().toISOString(), finishedAt: null };
  g.datbaseUpdate = st;
  void (async () => {
    try {
      const list = await db.query.project.findMany({ where: inArray(project.status, ["active", "paused", "suspended"]), orderBy: project.createdAt });
      st.total = list.length;
      for (const listed of list) {
        // Re-read: the project may have changed while earlier ones were updating.
        const p = await db.query.project.findFirst({ where: eq(project.id, listed.id) });
        const busy = p && (await db.query.backup.findFirst({ where: and(eq(backup.projectId, p.id), eq(backup.status, "running")) }));
        if (!p || !["active", "paused", "suspended"].includes(p.status) || busy) {
          st.skipped.push(listed.slug);
        } else {
          try {
            await updateProject(p);
            console.log(`[update] ${p.slug} now runs the current image`);
          } catch (e) {
            const error = e instanceof Error ? e.message : String(e);
            st.failed.push({ slug: p.slug, error });
            console.error(`[update] ${p.slug}:`, error);
          }
        }
        st.done++;
      }
    } finally {
      st.running = false;
      st.finishedAt = new Date().toISOString();
    }
  })();
  return true;
}
