import { notFound } from "next/navigation";
import { isAdmin, requireUser } from "@/lib/auth";
import { getStatus } from "@/lib/monitor";
import { outdatedProjects, updateState } from "@/lib/lifecycle";
import Link from "next/link";
import { applyEmailSettings, runChecksNow, sendTestAlert, updateAllProjects } from "./actions";
import { AdminButton } from "./ui";
import { UpdateProgress } from "./UpdateProgress";

export const dynamic = "force-dynamic";

const ago = (iso: string | null) => {
  if (!iso) return "never";
  const h = (Date.now() - Date.parse(iso)) / 3600_000;
  return h < 1 ? `${Math.max(1, Math.round(h * 60))} min ago` : h < 48 ? `${Math.round(h)} h ago` : `${Math.round(h / 24)} days ago`;
};

export default async function AdminPage() {
  const user = await requireUser();
  if (!isAdmin(user)) notFound();
  const s = await getStatus();
  const [img, up] = [await outdatedProjects(), updateState()];
  const bar = (pct: number, warn: number) => (
    <div className="h-2 w-full rounded bg-border">
      <div className={`h-2 rounded ${pct >= warn ? "bg-red-500" : "bg-brand-green"}`} style={{ width: `${Math.min(100, pct)}%` }} />
    </div>
  );

  return (
    <div className="space-y-6">
      <div className="flex items-center justify-between">
        <h1 className="text-2xl font-semibold">Server status</h1>
        <div className="flex items-center gap-4">
        <Link href="/dashboard/admin/accounts" className="text-sm text-accent-text hover:underline">Accounts &amp; projects →</Link>
        <span data-testid="server-status" className={`rounded px-2 py-1 text-sm font-medium ${s.status === "ok" ? "bg-brand-green/20 text-success" : "bg-red-100 text-red-800"}`}>
          {s.status === "ok" ? "All OK" : `${s.problems.length} problem${s.problems.length > 1 ? "s" : ""}`}
        </span>
        </div>
      </div>

      {s.problems.length > 0 && (
        <section className="card space-y-2 border-red-300">
          <h2 className="font-medium">Open problems</h2>
          <ul className="list-disc space-y-1 pl-5 text-sm" data-testid="problems">
            {s.problems.map((p) => (
              <li key={p.key}>
                {p.message} <span className="text-muted">(since {ago(p.since ?? null)})</span>
              </li>
            ))}
          </ul>
        </section>
      )}

      <section className="card grid gap-4 sm:grid-cols-3">
        <div className="space-y-1 text-sm">
          <div className="flex justify-between"><span>Disk</span><span>{s.system.diskUsedPct}% · {s.system.diskFreeGb} GB free</span></div>
          {bar(s.system.diskUsedPct, Number(process.env.MONITOR_DISK_PCT ?? 85))}
        </div>
        <div className="space-y-1 text-sm">
          <div className="flex justify-between"><span>Memory</span><span>{s.system.memUsedPct}% of {Math.round(s.system.memTotalMb / 1024 * 10) / 10} GB</span></div>
          {bar(s.system.memUsedPct, Number(process.env.MONITOR_MEM_PCT ?? 90))}
        </div>
        <div className="text-sm">
          Load {s.system.load1} on {s.system.cpus} CPUs
          <br />
          <span className="text-muted">Up {s.system.uptimeHours} h</span>
        </div>
      </section>

      <section className="card space-y-3">
        <h2 className="font-medium">
          Projects <span className="text-sm font-normal text-muted">{s.projects.active} active · {s.projects.paused} paused · {s.projects.error} error · {s.projects.down} down</span>
        </h2>
        <table className="w-full text-sm">
          <thead className="text-left text-muted">
            <tr><th className="py-1">Project</th><th>Status</th><th>Responding</th><th>Last backup</th></tr>
          </thead>
          <tbody>
            {s.projects.list.map((p) => (
              <tr key={p.id} className="border-t border-border" data-project={p.slug}>
                <td className="py-1">{p.name} <span className="font-mono text-xs text-muted">{p.slug}</span></td>
                <td>{p.status}</td>
                <td>{p.healthy === null ? "–" : p.healthy ? "yes" : <span className="text-red-600">no</span>}</td>
                <td>{s.backups.enabled ? ago(p.lastBackupAt) : "backups off"}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </section>

      <section className="card space-y-1 text-sm">
        <h2 className="font-medium">Backups & alerts</h2>
        <p>Backups: {s.backups.enabled ? `on · DATBase database backed up ${ago(s.backups.lastControlBackupAt)} · ${s.backups.failed24h} failed in 24 h` : "off (BACKUP_S3_* not set)"}</p>
        <p>Email: {s.email.configured ? "on" : "off (SMTP_* not set)"} · Alerts to: {s.email.alertsTo ?? "–"}{s.email.webhook ? " + webhook" : ""}</p>
        <p className="text-muted">Checked {ago(s.checkedAt)}.</p>
      </section>

      <section className="card space-y-2 text-sm" data-testid="image-status">
        <h2 className="font-medium">Project software</h2>
        <p>
          {img === null
            ? "Unknown (Docker not reachable)."
            : img.outdated.length
              ? `${img.outdated.length} of ${img.total} projects run an older version. Update them after every rebuild (a few seconds offline each).`
              : `All ${img.total} projects run the current version.`}
        </p>
        <UpdateProgress initial={up} />
        <AdminButton label="Update all projects" run={updateAllProjects} />
      </section>

      <section className="flex flex-wrap gap-4">
        <AdminButton label="Run checks now" run={runChecksNow} />
        <AdminButton label="Send test alert" run={sendTestAlert} />
        <AdminButton label="Apply email settings to all projects" run={applyEmailSettings} />
      </section>
    </div>
  );
}
