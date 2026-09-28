import { pageProject } from "@/lib/projects";
import { usageView } from "@/lib/usage";
import { RefreshUsageButton } from "../../ui";

const fmtBytes = (b: number) => {
  if (b < 1024 * 1024) return `${(b / 1024).toFixed(0)} KB`;
  if (b < 1024 ** 3) return `${(b / 1024 ** 2).toFixed(1)} MB`;
  return `${(b / 1024 ** 3).toFixed(2)} GB`;
};
const fmtNum = (n: number) => n.toLocaleString("en-US");

function Meter({
  label,
  used,
  limit,
  fmt,
  blockedNote,
}: {
  label: string;
  used: number;
  limit: number;
  fmt: (n: number) => string;
  blockedNote: string;
}) {
  const pct = Math.min(100, (used / limit) * 100);
  const over = used >= limit;
  const warn = !over && pct >= 80;
  const bar = over ? "bg-red-600" : warn ? "bg-amber-500" : "bg-accent";
  return (
    <div className="space-y-2">
      <div className="flex items-baseline justify-between gap-3 text-sm">
        <span className="font-medium">{label}</span>
        <span className="tabular-nums text-muted">
          {fmt(used)} / {fmt(limit)}
        </span>
      </div>
      <div className="h-2 overflow-hidden rounded-full bg-border/60">
        <div className={`h-full rounded-full ${bar}`} style={{ width: `${Math.max(pct, used > 0 ? 1 : 0)}%` }} />
      </div>
      {over && <p className="text-xs text-red-600">Limit reached: {blockedNote}</p>}
      {warn && <p className="text-xs text-amber-600">{pct.toFixed(0)}% used</p>}
    </div>
  );
}

export default async function UsagePage({ params }: PageProps<"/dashboard/[id]/usage">) {
  const { id } = await params;
  const p = await pageProject(id);
  const { u, requests, plan } = await usageView(p);
  const month = new Date().toLocaleString("en-US", { month: "long", timeZone: "UTC" });

  return (
    <div className="space-y-6">
      <section className="card space-y-5">
        <div className="flex flex-wrap items-center justify-between gap-2">
          <div>
            <h2 className="font-medium">{plan.name} plan usage</h2>
            <p className="text-xs text-muted">
              {u ? `Updated ${u.checkedAt.toISOString().slice(0, 16).replace("T", " ")} UTC` : "Not measured yet"}
            </p>
          </div>
          {p.status === "active" && <RefreshUsageButton id={p.id} />}
        </div>

        <Meter
          label={`API requests in ${month}`}
          used={requests}
          limit={plan.requestsPerMonth}
          fmt={fmtNum}
          blockedNote="your apps get an error until next month."
        />
        <Meter
          label="Database"
          used={u?.dbBytes ?? 0}
          limit={plan.dbBytes}
          fmt={fmtBytes}
          blockedNote="new and edited records are blocked. Delete data to free space."
        />
        <Meter
          label="File storage"
          used={u?.storageBytes ?? 0}
          limit={plan.storageBytes}
          fmt={fmtBytes}
          blockedNote="new and edited records are blocked. Delete files to free space."
        />
        <Meter
          label="App users"
          used={u?.users ?? 0}
          limit={plan.users}
          fmt={fmtNum}
          blockedNote="new sign-ups are blocked."
        />
      </section>
      <p className="text-xs text-muted">
        Usage is measured every few minutes, so it can run slightly behind. Requests made from this dashboard
        don&apos;t count.
      </p>
    </div>
  );
}
