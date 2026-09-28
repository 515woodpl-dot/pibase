import Link from "next/link";
import { enc, listCollections, pb, type Page } from "@/lib/pb";
import { pageProject } from "@/lib/projects";
import { load, Notice } from "./notice";
import { CreateTableForm } from "./ui";

export default async function TablesPage({ params }: PageProps<"/dashboard/[id]/tables">) {
  const { id } = await params;
  const p = await pageProject(id);
  if (p.status !== "active") return <Notice project={p} />;

  const [tables, error] = await load(async () => {
    const cols = await listCollections(p);
    return Promise.all(
      cols.map(async (c) => {
        const r = await pb<Page<unknown>>(p, `/api/collections/${enc(c.name)}/records?perPage=1&skipTotal=0&fields=id`);
        return { ...c, count: r.totalItems };
      }),
    );
  });
  if (!tables) return <Notice project={p} error={error} />;

  return (
    <div className="space-y-6">
      {tables.length > 0 ? (
        <ul className="divide-y divide-border rounded-lg border border-border bg-card">
          {tables.map((t) => (
            <li key={t.id}>
              <Link
                href={`/dashboard/${p.id}/tables/${enc(t.name)}`}
                className="flex items-center justify-between gap-4 px-5 py-3 hover:bg-border/30"
              >
                <div>
                  <span className="font-mono text-sm font-medium">{t.name}</span>
                  {t.type === "auth" && (
                    <span className="ml-2 rounded bg-accent/15 px-1.5 py-0.5 text-xs text-accent-text">app users</span>
                  )}
                </div>
                <span className="text-sm text-muted">
                  {t.count} {t.count === 1 ? "record" : "records"}
                </span>
              </Link>
            </li>
          ))}
        </ul>
      ) : (
        <p className="text-sm text-muted">No tables yet.</p>
      )}
      <CreateTableForm projectId={p.id} />
    </div>
  );
}
