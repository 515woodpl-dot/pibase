import { findOwnedProject } from "@/lib/projects";
import { runProjectSql } from "@/lib/sql";

// POST {sql} → run one read-only query for the signed-in owner (the SQL editor's Run button).
// A route handler, not a server action: Next.js runs server actions one at a time per tab, so a
// Cancel action would wait behind the query it should stop. Here the browser aborts its fetch, the
// request's signal aborts the engine call, and the engine interrupts the query.
export async function POST(req: Request, ctx: { params: Promise<{ id: string }> }) {
  // Cookies make this callable from other sites' pages; a JSON body forces a CORS preflight (which
  // fails) and browsers mark cross-site requests. Either one stops cross-site calls.
  if (req.headers.get("sec-fetch-site") === "cross-site" || !(req.headers.get("content-type") ?? "").startsWith("application/json")) {
    return Response.json({ error: "Forbidden." }, { status: 403 });
  }
  const { id } = await ctx.params;
  const p = await findOwnedProject(id);
  if (!p) return Response.json({ error: "Not found." }, { status: 404 });
  const body = (await req.json().catch(() => null)) as { sql?: unknown } | null;
  const r = await runProjectSql(p, typeof body?.sql === "string" ? body.sql : "", req.signal);
  return Response.json(r.body, { status: r.status, headers: { "cache-control": "private, no-store" } });
}
