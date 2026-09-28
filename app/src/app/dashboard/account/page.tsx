import { eq } from "drizzle-orm";
import { db } from "@/db";
import { project } from "@/db/schema";
import { requireUser } from "@/lib/auth";
import { DeleteAccountForm } from "./ui";

export default async function AccountPage() {
  const u = await requireUser();
  const projects = await db.query.project.findMany({ where: eq(project.ownerId, u.id), orderBy: project.createdAt });
  return (
    <div className="space-y-6">
      <h1 className="text-2xl font-semibold">Account</h1>
      <section className="card space-y-1 text-sm">
        <p><span className="text-muted">Name:</span> {u.name}</p>
        <p><span className="text-muted">Email:</span> {u.email}</p>
      </section>

      <section className="card space-y-3">
        <h2 className="font-medium">Download your data</h2>
        <p className="text-sm text-muted">
          A zip with every table as JSON and all uploaded files, for each project. App users&apos; passwords are never included.
        </p>
        {projects.length === 0 ? (
          <p className="text-sm">No projects.</p>
        ) : (
          <ul className="space-y-1 text-sm">
            {projects.map((p) => (
              <li key={p.id} className="flex items-center justify-between">
                <span>{p.name}</span>
                {p.status === "active" ? (
                  <a className="text-accent-text hover:underline" href={`/dashboard/${p.id}/export`} data-testid={`export-${p.slug}`}>Download data</a>
                ) : (
                  <span className="text-muted">available when the project is active</span>
                )}
              </li>
            ))}
          </ul>
        )}
      </section>

      <section className="card space-y-3 border-red-500/40">
        <h2 className="font-medium text-red-600">Delete account</h2>
        <p className="text-sm">
          Deletes your account and <b>all {projects.length} project{projects.length === 1 ? "" : "s"}</b>: their data, files, app users and
          backups. Download your data first if you want to keep it. This can&apos;t be undone.
        </p>
        <DeleteAccountForm email={u.email} />
      </section>
    </div>
  );
}
