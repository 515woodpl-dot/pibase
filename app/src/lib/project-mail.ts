import "server-only";
import { eq } from "drizzle-orm";
import { db } from "@/db";
import { project, type Project } from "@/db/schema";
import { mailConfigured, mailFrom, smtpSettings } from "./mail";
import { json, pb } from "./pb";

/** Where a project's app users land from their emails (reset password, confirm email). */
export const projectPublicUrl = (slug: string) => `${(process.env.BETTER_AUTH_URL ?? "").replace(/\/+$/, "")}/u/${slug}`;

/**
 * Give a project DATBase's email setup: SMTP, sender and hosted-page links. The project's own hook
 * enforces sender, wording and links, so a project admin can't turn emails into phishing.
 * Safe to call any time (after create, restore, rename, or when SMTP settings change).
 */
export async function configureProjectMail(p: Project) {
  const from = mailFrom().address;
  const publicUrl = projectPublicUrl(p.slug);
  const perHour = Number(process.env.PROJECT_MAIL_PER_HOUR ?? 50);
  await pb(p, "/api/datbase/mail", json("POST", { from, publicUrl, perHour }));
  const s = smtpSettings();
  await pb(
    p,
    "/api/settings",
    json("PATCH", {
      smtp: mailConfigured()
        ? { enabled: true, host: s.host, port: s.port, username: s.user, password: s.pass, tls: s.secure }
        : { enabled: false },
      meta: { appName: p.name, appURL: publicUrl, senderName: p.name, senderAddress: from },
    }),
  );
}

/** Apply the email setup to every active project (startup, and the admin page button). */
export async function configureAllProjectsMail() {
  const list = await db.query.project.findMany({ where: eq(project.status, "active") });
  const { readyProject } = await import("./busy");
  let ok = 0;
  for (const listed of list) {
    const p = await readyProject(listed.id); // skip projects being restored/updated (they re-apply it themselves)
    if (!p) continue;
    try {
      await configureProjectMail(p);
      ok++;
    } catch (e) {
      console.error(`[mail] ${listed.slug}: could not apply email settings:`, e instanceof Error ? e.message : e);
    }
  }
  return { ok, total: list.length };
}
