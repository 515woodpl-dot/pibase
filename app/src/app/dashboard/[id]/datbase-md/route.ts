import { buildGuide } from "@/lib/ai-guide";
import { projectUrl } from "@/lib/config";
import { mailConfigured } from "@/lib/mail";
import { listCollections, PbError } from "@/lib/pb";
import { findOwnedProject } from "@/lib/projects";

// Downloads DATBASE.md for a project: the AI/developer guide generated from its live schema.
export async function GET(req: Request, ctx: RouteContext<"/dashboard/[id]/datbase-md">) {
  const { id } = await ctx.params;
  const p = await findOwnedProject(id);
  if (!p) return new Response("Not found", { status: 404 });
  if (p.status !== "active") return new Response("The project must be active to generate DATBASE.md.", { status: 409 });

  try {
    const md = buildGuide({
      projectName: p.name,
      url: projectUrl(p.slug),
      collections: await listCollections(p),
      generatedAt: new Date(),
      email: mailConfigured(),
    });
    const inline = new URL(req.url).searchParams.has("view");
    return new Response(md, {
      headers: {
        "content-type": "text/markdown; charset=utf-8",
        "content-disposition": `${inline ? "inline" : "attachment"}; filename="DATBASE.md"`,
        "cache-control": "private, no-store",
      },
    });
  } catch (e) {
    if (e instanceof PbError) return new Response("Project backend is not reachable right now.", { status: 503 });
    throw e;
  }
}
