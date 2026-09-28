import { desc, ilike } from "drizzle-orm";
import { notFound } from "next/navigation";
import Link from "next/link";
import { db } from "@/db";
import { user } from "@/db/schema";
import { isAdmin, requireUser } from "@/lib/auth";
import { monthOf } from "@/lib/usage";
import { suspendAccountAction, suspendProjectAction, unsuspendAccountAction, unsuspendProjectAction } from "../actions";
import { AdminButton, ReasonButton } from "../ui";

export const dynamic = "force-dynamic";

// Operator view of every account and project, with suspend controls (abuse handling).
export default async function AccountsPage({ searchParams }: { searchParams: Promise<Record<string, string | string[] | undefined>> }) {
  const me = await requireUser();
  if (!isAdmin(me)) notFound();
  const q = typeof (await searchParams).q === "string" ? String((await searchParams).q).trim() : "";
  const accounts = await db.query.user.findMany({
    where: q ? ilike(user.email, `%${q.replace(/[%_\\]/g, "\\$&")}%`) : undefined,
    orderBy: desc(user.createdAt),
    limit: 200,
  });
  const projects = await db.query.project.findMany();
  const usage = await db.query.usageMonth.findMany({ where: (m, { eq }) => eq(m.month, monthOf(new Date())) });

  return (
    <div className="space-y-6">
      <div className="flex items-center justify-between">
        <h1 className="text-2xl font-semibold">Accounts &amp; projects</h1>
        <Link href="/dashboard/admin" className="text-sm text-accent-text hover:underline">← Server status</Link>
      </div>
      <form className="flex gap-2">
        <input name="q" defaultValue={q} placeholder="Search by email" className="input max-w-xs" />
        <button className="btn-ghost">Search</button>
      </form>
      <p className="text-sm text-muted">{accounts.length} account{accounts.length === 1 ? "" : "s"}{accounts.length === 200 ? " (first 200)" : ""}.</p>
      <ul className="space-y-3">
        {accounts.map((a) => {
          const mine = projects.filter((p) => p.ownerId === a.id);
          return (
            <li key={a.id} className="card space-y-2" data-account={a.email}>
              <div className="flex flex-wrap items-center justify-between gap-2">
                <div>
                  <b>{a.email}</b> <span className="text-sm text-muted">{a.name} · joined {a.createdAt.toISOString().slice(0, 10)}</span>
                  {!a.emailVerified && <span className="ml-2 text-xs text-amber-700">email not confirmed</span>}
                  {a.suspendedAt && <span className="ml-2 text-xs font-medium text-red-600">SUSPENDED</span>}
                  {a.deletedAt && <span className="ml-2 text-xs text-muted">deleting</span>}
                </div>
                {a.id !== me.id && !a.deletedAt && (
                  a.suspendedAt ? (
                    <AdminButton label="Lift account suspension" run={unsuspendAccountAction.bind(null, a.id)} />
                  ) : (
                    <AdminButton label="Suspend account" run={suspendAccountAction.bind(null, a.id)} />
                  )
                )}
              </div>
              {mine.length > 0 && (
                <table className="w-full text-sm">
                  <tbody>
                    {mine.map((p) => (
                      <tr key={p.id} className="border-t border-border" data-project={p.slug}>
                        <td className="py-1">{p.name} <span className="font-mono text-xs text-muted">{p.slug}</span></td>
                        <td>{p.status}{p.status === "suspended" && p.statusMessage ? ` (${p.statusMessage})` : ""}</td>
                        <td className="text-muted">{(usage.find((u) => u.projectId === p.id)?.requests ?? 0).toLocaleString()} requests this month</td>
                        <td className="text-right">
                          {p.status === "suspended" ? (
                            <AdminButton label="Lift suspension" run={unsuspendProjectAction.bind(null, p.id)} />
                          ) : ["active", "paused", "error"].includes(p.status) ? (
                            <ReasonButton label="Suspend" testId={`suspend-${p.slug}`} run={suspendProjectAction.bind(null, p.id)} />
                          ) : null}
                        </td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              )}
            </li>
          );
        })}
      </ul>
    </div>
  );
}
