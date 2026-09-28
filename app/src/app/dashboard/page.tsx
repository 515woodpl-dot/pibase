import Link from "next/link";
import { desc, eq } from "drizzle-orm";
import { db } from "@/db";
import { project, projectUsage } from "@/db/schema";
import { requireUser } from "@/lib/auth";
import { projectHost } from "@/lib/config";
import { planFor } from "@/lib/plans";
import { CreateProjectForm, StatusBadge, StatusPoller } from "./ui";

export default async function Dashboard() {
  const user = await requireUser();
  const projects = await db
    .select({ p: project, u: projectUsage })
    .from(project)
    .leftJoin(projectUsage, eq(projectUsage.projectId, project.id))
    .where(eq(project.ownerId, user.id))
    .orderBy(desc(project.createdAt));
  const plan = planFor(user.id);
  const atLimit = projects.length >= plan.projects;

  return (
    <div className="space-y-8">
      <StatusPoller status={projects.some(({ p }) => p.status === "creating") ? "creating" : "active"} />
      <div>
        <h1 className="text-2xl font-semibold">Projects</h1>
        <p className="text-sm text-muted">
          {projects.length} of {plan.projects} projects used · {plan.name} plan
        </p>
      </div>

      {projects.length > 0 && (
        <ul className="divide-y divide-border rounded-lg border border-border bg-card">
          {projects.map(({ p, u }) => (
            <li key={p.id}>
              <Link
                href={`/dashboard/${p.id}`}
                className="flex items-center justify-between gap-4 px-5 py-4 hover:bg-border/30"
              >
                <div className="min-w-0">
                  <div className="font-medium">{p.name}</div>
                  <div className="truncate font-mono text-xs text-muted">{projectHost(p.slug)}</div>
                </div>
                <div className="flex items-center gap-2">
                  {u && (u.blockAll || u.blockWrites || u.blockSignups) && (
                    <span className="rounded-full bg-red-500/15 px-2.5 py-0.5 text-xs font-medium text-red-700 dark:text-red-400">
                      Limit reached
                    </span>
                  )}
                  <StatusBadge status={p.status} />
                </div>
              </Link>
            </li>
          ))}
        </ul>
      )}

      {atLimit ? (
        <p className="text-sm text-muted">You&apos;ve reached your project limit.</p>
      ) : (
        <CreateProjectForm />
      )}
    </div>
  );
}
