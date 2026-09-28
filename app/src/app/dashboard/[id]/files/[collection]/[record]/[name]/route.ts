import { controlHeaders, fileToken, fileUrl, getCollection, PbError } from "@/lib/pb";
import { findOwnedProject } from "@/lib/projects";

const notFound = () => new Response("Not found", { status: 404 });

// Streams a project file to its owner in the dashboard (works for protected files too).
export async function GET(_: Request, ctx: RouteContext<"/dashboard/[id]/files/[collection]/[record]/[name]">) {
  const { id, collection, record, name: rawName } = await ctx.params;
  const name = decodeURIComponent(rawName);
  if (!/^[a-zA-Z0-9_-]{1,64}$/.test(record) || !name || name.length > 255 || /[/\\\0]/.test(name)) return notFound();

  const p = await findOwnedProject(id);
  if (!p || p.status !== "active") return notFound();

  try {
    const c = await getCollection(p, collection); // customer tables only
    const token = await fileToken(p);
    const upstream = await fetch(`${fileUrl(p, c.id, record, name)}?token=${encodeURIComponent(token)}`, {
      headers: controlHeaders(p.slug),
    });
    if (!upstream.ok) return notFound();

    return new Response(upstream.body, {
      headers: {
        "content-type": upstream.headers.get("content-type") ?? "application/octet-stream",
        "cache-control": "private, no-store",
        "x-content-type-options": "nosniff",
        // Never render uploaded HTML/SVG inline on the dashboard's origin.
        "content-security-policy": "sandbox",
      },
    });
  } catch (e) {
    if (e instanceof PbError) return notFound();
    throw e;
  }
}
