import Link from "next/link";
import { eq } from "drizzle-orm";
import { db } from "@/db";
import { projectUsage } from "@/db/schema";
import { pageProject } from "@/lib/projects";
import { LifecycleButtons, StatusBadge, StatusPoller } from "../ui";
import { Tabs } from "./tabs";

export default async function ProjectLayout({ children, params }: LayoutProps<"/dashboard/[id]">) {
  const { id } = await params;
  const p = await pageProject(id);
  const usage = await db.query.projectUsage.findFirst({ where: eq(projectUsage.projectId, p.id) });

  return (
    <div className="space-y-6">
      <StatusPoller status={p.status} />
      <Link href="/dashboard" className="text-sm text-muted hover:underline">
        ← Projects
      </Link>

      <div className="flex flex-wrap items-center justify-between gap-3">
        <div className="flex items-center gap-3">
          <h1 className="text-2xl font-semibold">{p.name}</h1>
          <StatusBadge status={p.status} />
        </div>
        <LifecycleButtons id={p.id} status={p.status} />
      </div>

      {usage && (usage.blockAll || usage.blockWrites || usage.blockSignups) && (
        <Link
          href={`/dashboard/${p.id}/usage`}
          className="block rounded-lg border border-red-500/40 bg-red-500/10 px-4 py-3 text-sm"
        >
          <b className="text-red-600">Plan limit reached.</b>{" "}
          {[
            usage.blockAll && "Your apps' API requests are blocked until next month.",
            usage.blockWrites && "New and edited records are blocked until you free up space.",
            usage.blockSignups && "New user sign-ups are blocked.",
          ]
            .filter(Boolean)
            .join(" ")}{" "}
          <span className="underline">See usage</span>
        </Link>
      )}

      <Tabs id={p.id} />
      {children}
    </div>
  );
}
