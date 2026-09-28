"use server";

import { notFound } from "next/navigation";
import { isAdmin, requireUser } from "@/lib/auth";
import { runChecks, sendAlert } from "@/lib/monitor";
import { configureAllProjectsMail } from "@/lib/project-mail";

export type AdminState = { message?: string; error?: string };

async function admin() {
  const u = await requireUser();
  if (!isAdmin(u)) notFound();
}

export async function runChecksNow(): Promise<AdminState> {
  await admin();
  const s = await runChecks({ force: true });
  return s ? { message: `Checked: ${s.status === "ok" ? "all OK" : `${s.problems.length} problem(s)`}.` } : { error: "A check is already running. Try again in a moment." };
}

export async function sendTestAlert(): Promise<AdminState> {
  await admin();
  const r = await sendAlert("DATBase: test alert", ["This is a test. Alerts reach you here."], "test");
  if (!r.email && !r.webhook) return { error: "Not delivered: set ADMIN_EMAIL with SMTP, or ALERT_WEBHOOK_URL (see the server log)." };
  return { message: `Test alert sent (${[r.email && "email", r.webhook && "webhook"].filter(Boolean).join(" + ")}).` };
}

export async function applyEmailSettings(): Promise<AdminState> {
  await admin();
  const r = await configureAllProjectsMail();
  return { message: `Email settings applied to ${r.ok} of ${r.total} active projects.` };
}

/* ---------------- projects image update ---------------- */

export async function updateAllProjects(): Promise<AdminState> {
  await admin();
  const { startUpdateAll } = await import("@/lib/lifecycle");
  return startUpdateAll()
    ? { message: "Updating all projects, one at a time. This page shows the progress." }
    : { error: "An update is already running." };
}

/* ---------------- suspend / unsuspend ---------------- */

async function projectById(id: string) {
  const { db } = await import("@/db");
  const { project } = await import("@/db/schema");
  const { eq } = await import("drizzle-orm");
  const p = await db.query.project.findFirst({ where: eq(project.id, id) });
  if (!p) throw new Error("Project not found");
  return p;
}

export async function suspendProjectAction(id: string, reason: string): Promise<AdminState> {
  await admin();
  const { suspendProject } = await import("@/lib/lifecycle");
  try {
    await suspendProject(await projectById(id), reason.trim().slice(0, 200) || "Suspended by the operator.");
  } catch (e) {
    return { error: e instanceof Error ? e.message : "Failed" };
  }
  return { message: "Project suspended." };
}

export async function unsuspendProjectAction(id: string): Promise<AdminState> {
  await admin();
  const { unsuspendProject } = await import("@/lib/lifecycle");
  await unsuspendProject(await projectById(id));
  return { message: "Project is back online." };
}

export async function suspendAccountAction(userId: string): Promise<AdminState> {
  const me = await requireUser();
  if (!isAdmin(me)) notFound();
  if (me.id === userId) return { error: "You can't suspend your own account." };
  const { suspendAccount } = await import("@/lib/lifecycle");
  await suspendAccount(userId);
  return { message: "Account suspended: signed out, projects stopped." };
}

export async function unsuspendAccountAction(userId: string): Promise<AdminState> {
  await admin();
  const { unsuspendAccount } = await import("@/lib/lifecycle");
  await unsuspendAccount(userId);
  return { message: "Account restored." };
}
