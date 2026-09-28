import { backupsConfigured, KEEP_DAILY, KEEP_WEEKLY, listBackups } from "@/lib/backups";
import { pageProject } from "@/lib/projects";
import { BackupNowButton, RestoreForm, StatusPoller } from "../../ui";

const fmtBytes = (b: number | null) =>
  b == null ? "—" : b < 1024 ** 2 ? `${Math.max(1, Math.round(b / 1024))} KB` : `${(b / 1024 ** 2).toFixed(1)} MB`;
const fmtDate = (d: Date) => `${d.toISOString().slice(0, 16).replace("T", " ")} UTC`;
const KIND: Record<string, string> = { auto: "Daily", manual: "Manual", "pre-restore": "Before restore" };

export default async function BackupsPage({ params }: PageProps<"/dashboard/[id]/backups">) {
  const { id } = await params;
  const p = await pageProject(id);

  if (!backupsConfigured())
    return (
      <div className="card text-sm text-muted">
        Backups aren&apos;t set up on this server yet. The operator needs to add the <code>BACKUP_S3_*</code> settings.
      </div>
    );

  const rows = await listBackups(p.id);
  const running = rows.some((r) => r.status === "running");

  return (
    <div className="space-y-6">
      <StatusPoller status={running ? "creating" : p.status} />

      {p.status === "restoring" && (
        <div className="card border-amber-500/40 text-sm">
          <b>Restoring…</b> Taking a safety backup, then loading the selected backup. The API is briefly unavailable.
        </div>
      )}
      {p.status === "active" && p.statusMessage?.startsWith("Restore failed") && (
        <div className="card border-red-500/40 text-sm">
          <b className="text-red-600">{p.statusMessage}</b> Your data was not changed.
        </div>
      )}

      <section className="card space-y-3">
        <div className="flex flex-wrap items-center justify-between gap-3">
          <div>
            <h2 className="font-medium">Backups</h2>
            <p className="text-sm text-muted">
              Daily, encrypted and stored off this server. We keep one backup for each of the last {KEEP_DAILY} days,
              plus one per week for {KEEP_WEEKLY} more weeks. Manual and “before restore” backups are kept on top for
              7 days. Each backup includes all tables, app users and files.
            </p>
          </div>
          <BackupNowButton id={p.id} disabled={p.status !== "active" || running} />
        </div>
      </section>

      {rows.length === 0 ? (
        <p className="text-sm text-muted">No backups yet. The first daily backup runs within about an hour.</p>
      ) : (
        <ul className="divide-y divide-border rounded-lg border border-border bg-card">
          {rows.map((b) => (
            <li key={b.id} data-backup-id={b.id} className="px-5 py-3">
              <div className="flex flex-wrap items-center justify-between gap-3">
                <div className="text-sm">
                  <div className="font-medium tabular-nums">{fmtDate(b.createdAt)}</div>
                  <div className="text-xs text-muted">
                    {KIND[b.kind]} · {fmtBytes(b.sizeBytes)}
                  </div>
                </div>
                <div className="flex items-center gap-3">
                  {b.status === "running" && <span className="text-sm text-amber-600">In progress…</span>}
                  {b.status === "failed" && (
                    <span className="text-sm text-red-600" title={b.error ?? ""}>
                      Failed
                    </span>
                  )}
                  {b.status === "done" && p.status === "active" && (
                    <RestoreForm id={p.id} name={p.name} backupId={b.id} label={fmtDate(b.createdAt)} />
                  )}
                </div>
              </div>
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}
