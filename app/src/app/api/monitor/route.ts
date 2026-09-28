import { timingSafeEqual } from "node:crypto";
import { getStatus } from "@/lib/monitor";

// Server status as JSON for Home Assistant (or any monitor): GET with "Authorization: Bearer <MONITOR_TOKEN>".
// Returns 404 when MONITOR_TOKEN isn't set, 401 for a wrong token.
export const dynamic = "force-dynamic";

function authorized(req: Request) {
  const want = process.env.MONITOR_TOKEN ?? "";
  const got = (req.headers.get("authorization") ?? "").replace(/^Bearer\s+/i, "");
  const a = Buffer.from(want);
  const b = Buffer.from(got);
  return a.length > 0 && a.length === b.length && timingSafeEqual(a, b);
}

export async function GET(req: Request) {
  if (!process.env.MONITOR_TOKEN) return new Response("Not found", { status: 404 });
  if (!authorized(req)) return Response.json({ error: "unauthorized" }, { status: 401 });
  const s = await getStatus();
  // Flat fields first: easy to use in Home Assistant templates (value_json.disk_used_pct …).
  return Response.json(
    {
      status: s.status,
      problems_count: s.problems.length,
      problems: s.problems.map((p) => p.message),
      checked_at: s.checkedAt,
      disk_used_pct: s.system.diskUsedPct,
      disk_free_gb: s.system.diskFreeGb,
      memory_used_pct: s.system.memUsedPct,
      load_1m: s.system.load1,
      uptime_hours: s.system.uptimeHours,
      projects_total: s.projects.total,
      projects_active: s.projects.active,
      projects_paused: s.projects.paused,
      projects_error: s.projects.error,
      projects_down: s.projects.down,
      backups_enabled: s.backups.enabled,
      backups_failed_24h: s.backups.failed24h,
      last_control_backup: s.backups.lastControlBackupAt,
      email_configured: s.email.configured,
      detail: s,
    },
    { headers: { "cache-control": "no-store" } },
  );
}
