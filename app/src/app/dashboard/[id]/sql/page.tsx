import { headers } from "next/headers";
import { pageProject } from "@/lib/projects";
import { load, Notice } from "../tables/notice";
import { ensureStarterSnippets, getSqlSchema } from "./actions";
import { SqlEditorUI } from "./ui";

export default async function SqlPage({ params }: PageProps<"/dashboard/[id]/sql">) {
  const { id } = await params;
  const p = await pageProject(id);
  // A paused/suspended/restoring project has no engine to ask: say so instead of failing.
  if (p.status !== "active") return <Notice project={p} />;

  await ensureStarterSnippets(p.id); // once per project; later visits do nothing
  const [schema, error] = await load(() => getSqlSchema(p.id));
  if (!schema) return <Notice project={p} error={error} />;

  // CodeMirror adds its styles at runtime; the nonce lets them pass the page's CSP (src/proxy.ts).
  const nonce = (await headers()).get("x-nonce") ?? "";
  return <SqlEditorUI projectId={p.id} schema={schema} nonce={nonce} />;
}
