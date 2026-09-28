import "server-only";
import { eq } from "drizzle-orm";
import { db } from "@/db";
import { project, type ProjectStatus } from "@/db/schema";
import { decrypt, encrypt, randomPassword } from "./crypto";
import { projectHost } from "./config";
import { startInstance, upsertSuperuser, waitHealthy } from "./docker";
import { configureProjectMail } from "./project-mail";

async function setStatus(id: string, status: ProjectStatus, statusMessage: string | null = null) {
  await db
    .update(project)
    .set({ status, statusMessage, updatedAt: new Date() })
    .where(eq(project.id, id));
}

/**
 * Create Project → start container → health check → create admin → active.
 * Runs in the background; the dashboard polls the status. Any failure → "error" (retryable).
 */
export async function provision(p: { id: string; slug: string }) {
  try {
    await startInstance(p.slug, p.id);
    await waitHealthy(p.slug);

    const email = `admin@${projectHost(p.slug)}`;
    const password = randomPassword();
    await upsertSuperuser(p.slug, email, password);

    await db
      .update(project)
      .set({
        status: "active",
        statusMessage: null,
        adminEmail: email,
        adminPasswordEnc: encrypt(password),
        updatedAt: new Date(),
      })
      .where(eq(project.id, p.id));

    // Email for the project's app users (non-fatal: re-applied at every start and from the admin page).
    const row = await db.query.project.findFirst({ where: eq(project.id, p.id) });
    if (row) await configureProjectMail(row).catch((e) => console.error(`[provision] ${p.slug} email setup:`, e));
  } catch (e) {
    console.error(`[provision] ${p.slug} failed`, e);
    await setStatus(p.id, "error", e instanceof Error ? e.message : "Provisioning failed");
  }
}

/** Recreate a project's missing container around its existing data volume, with our admin login. */
export async function recreateInstance(p: { id: string; slug: string; adminEmail: string | null; adminPasswordEnc: string | null }) {
  await startInstance(p.slug, p.id);
  await waitHealthy(p.slug);
  if (p.adminEmail && p.adminPasswordEnc) await upsertSuperuser(p.slug, p.adminEmail, decrypt(p.adminPasswordEnc));
}

export { setStatus };
