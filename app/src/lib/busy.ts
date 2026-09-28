import "server-only";
import { eq } from "drizzle-orm";
import { db } from "@/db";
import { project, type Project } from "@/db/schema";

/*
 * Projects in the middle of a restore or update. Background jobs (usage measuring, email settings,
 * updates) must not talk to such a project: e.g. changing settings while the engine swaps in
 * restored data breaks the restore. Jobs re-read the project right before working on it.
 */
const g = globalThis as unknown as { datbaseBusy?: Set<string> };
const busy = () => (g.datbaseBusy ??= new Set());

export async function whileBusy<T>(projectId: string, fn: () => Promise<T>): Promise<T> {
  busy().add(projectId);
  try {
    return await fn();
  } finally {
    busy().delete(projectId);
  }
}

/** The project as it is now, if it's active and nothing else is working on it; otherwise null. */
export async function readyProject(id: string): Promise<Project | null> {
  if (busy().has(id)) return null;
  const p = await db.query.project.findFirst({ where: eq(project.id, id) });
  return p && p.status === "active" && !busy().has(id) ? p : null;
}
