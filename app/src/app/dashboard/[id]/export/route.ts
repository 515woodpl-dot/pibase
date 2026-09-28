import { Readable } from "node:stream";
import { exportProject } from "@/lib/export";
import { findOwnedProject } from "@/lib/projects";

// Owner downloads all of a project's data (records + files) as a zip.
export async function GET(_: Request, ctx: { params: Promise<{ id: string }> }) {
  const { id } = await ctx.params;
  const p = await findOwnedProject(id);
  if (!p) return new Response("Not found", { status: 404 });
  if (p.status !== "active") return new Response("The project must be active to export its data.", { status: 409 });
  const day = new Date().toISOString().slice(0, 10);
  return new Response(Readable.toWeb(exportProject(p)) as ReadableStream, {
    headers: {
      "content-type": "application/zip",
      "content-disposition": `attachment; filename="${p.slug}-data-${day}.zip"`,
      "cache-control": "private, no-store",
    },
  });
}
