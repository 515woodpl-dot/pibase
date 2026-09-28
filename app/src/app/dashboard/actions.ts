"use server";

import { count, eq, sql } from "drizzle-orm";
import { revalidatePath } from "next/cache";
import { redirect } from "next/navigation";
import { db } from "@/db";
import { project } from "@/db/schema";
import { requireUser } from "@/lib/auth";
import { planFor } from "@/lib/plans";
import { randomId } from "@/lib/crypto";
import { resumeInstance, stopInstance } from "@/lib/docker";
import { actionProject as ownedProject } from "@/lib/projects";
import { provision, setStatus } from "@/lib/provision";

export type FormState = { error?: string } | undefined;

function slugify(name: string) {
  const base = name
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, "-")
    .replace(/^-+|-+$/g, "")
    .slice(0, 20);
  return `${base || "project"}-${randomId(6)}`;
}

export async function createProject(_: FormState, form: FormData): Promise<FormState> {
  const user = await requireUser();
  const name = String(form.get("name") ?? "").trim();
  if (name.length < 2 || name.length > 50) return { error: "Name must be 2–50 characters." };

  const plan = planFor(user.id);
  const row = { id: randomId(16), ownerId: user.id, name, slug: slugify(name) };

  // Count + insert under a per-account lock so parallel requests can't exceed the plan (F-01).
  const allowed = await db.transaction(async (tx) => {
    await tx.execute(sql`select pg_advisory_xact_lock(hashtext(${"project-create:" + user.id}))`);
    const [{ n }] = await tx.select({ n: count() }).from(project).where(eq(project.ownerId, user.id));
    if (n >= plan.projects) return false;
    await tx.insert(project).values(row);
    return true;
  });
  if (!allowed) return { error: `Your ${plan.name} plan allows ${plan.projects} projects.` };

  void provision(row); // background — dashboard polls status

  redirect(`/dashboard/${row.id}`);
}

export async function renameProject(_: FormState, form: FormData): Promise<FormState> {
  const p = await ownedProject(String(form.get("id")));
  const name = String(form.get("name") ?? "").trim();
  if (name.length < 2 || name.length > 50) return { error: "Name must be 2–50 characters." };
  await db.update(project).set({ name, updatedAt: new Date() }).where(eq(project.id, p.id));
  if (p.status === "active") {
    const { configureProjectMail } = await import("@/lib/project-mail");
    await configureProjectMail({ ...p, name }).catch((e) => console.error(`[rename] ${p.slug} email name:`, e));
  }
  revalidatePath(`/dashboard/${p.id}`, "layout");
  return {};
}

export async function pauseProject(id: string) {
  const p = await ownedProject(id);
  if (p.status !== "active") return;
  await stopInstance(p.slug);
  await setStatus(p.id, "paused");
  revalidatePath(`/dashboard/${p.id}`, "layout");
}

export async function resumeProject(id: string) {
  const p = await ownedProject(id);
  if (p.status !== "paused") return;
  const { instanceExists } = await import("@/lib/docker");
  if (await instanceExists(p.slug)) {
    await resumeInstance(p.slug);
  } else {
    // The container is gone (e.g. after moving servers): rebuild it around the project's data volume (R-01).
    const { recreateInstance } = await import("@/lib/provision");
    await recreateInstance(p);
  }
  await setStatus(p.id, "active");
  revalidatePath(`/dashboard/${p.id}`, "layout");
}

export async function retryProject(id: string) {
  const p = await ownedProject(id);
  if (p.status !== "error") return;
  await setStatus(p.id, "creating");
  void provision(p);
  revalidatePath(`/dashboard/${p.id}`, "layout");
}

export async function deleteProject(_: FormState, form: FormData): Promise<FormState> {
  const p = await ownedProject(String(form.get("id")));
  if (String(form.get("confirm") ?? "") !== p.name)
    return { error: "Type the project name exactly to confirm." };
  if (p.status === "restoring") return { error: "Wait for the restore to finish." };

  const { deleteProjectFully } = await import("@/lib/lifecycle");
  await deleteProjectFully(p);
  redirect("/dashboard");
}

export async function refreshUsage(id: string) {
  const p = await ownedProject(id);
  if (p.status !== "active") return;
  const { collectUsage } = await import("@/lib/usage");
  await collectUsage(p);
  revalidatePath(`/dashboard/${p.id}`, "layout");
}

export async function backupNowAction(id: string): Promise<string | null> {
  const p = await ownedProject(id);
  const { requestManualBackup } = await import("@/lib/backups");
  const err = await requestManualBackup(p);
  revalidatePath(`/dashboard/${p.id}/backups`);
  return err;
}

export async function restoreAction(_: FormState, form: FormData): Promise<FormState> {
  const p = await ownedProject(String(form.get("id")));
  if (String(form.get("confirm") ?? "") !== p.name) return { error: "Type the project name exactly to confirm." };
  const { requestRestore } = await import("@/lib/backups");
  const err = await requestRestore(p, String(form.get("backupId") ?? ""));
  if (err) return { error: err };
  revalidatePath(`/dashboard/${p.id}`, "layout");
  return {};
}

export type KeyState = { error?: string; token?: string; name?: string } | undefined;

export async function createServerKeyAction(_: KeyState, form: FormData): Promise<KeyState> {
  const p = await ownedProject(String(form.get("id")));
  if (p.status !== "active") return { error: "The project must be active." };
  const name = String(form.get("name") ?? "").trim();
  const days = Number(form.get("days"));
  const { createServerKey, KEY_DAYS, KeyError } = await import("@/lib/server-keys");
  if (name.length < 2 || name.length > 50) return { error: "Name must be 2–50 characters." };
  if (!(KEY_DAYS as readonly number[]).includes(days)) return { error: "Choose how long the key is valid." };
  try {
    const token = await createServerKey(p, name, days);
    revalidatePath(`/dashboard/${p.id}`);
    return { token, name };
  } catch (e) {
    if (e instanceof KeyError) return { error: e.message };
    const { PbError } = await import("@/lib/pb");
    if (e instanceof PbError) return { error: e.message };
    throw e;
  }
}

export async function revokeServerKeyAction(projectId: string, keyId: string) {
  const p = await ownedProject(projectId);
  const { revokeServerKey } = await import("@/lib/server-keys");
  await revokeServerKey(p, keyId);
  revalidatePath(`/dashboard/${p.id}`);
}
