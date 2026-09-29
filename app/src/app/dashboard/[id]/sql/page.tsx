import { pageProject } from "@/lib/projects";
import { ensureStarterSnippets, getSqlSchema } from "./actions";
import { SqlEditorUI } from "./ui";

export default async function SqlPage({ params }: PageProps<"/dashboard/[id]/sql">) {
  const { id } = await params;
  const p = await pageProject(id);

  // Idempotent: creates "Getting started" folder + 5 starter snippets only on first visit.
  await ensureStarterSnippets(p.id);

  const schema = await getSqlSchema(p.id);

  return <SqlEditorUI projectId={p.id} initialSchema={schema} />;
}
