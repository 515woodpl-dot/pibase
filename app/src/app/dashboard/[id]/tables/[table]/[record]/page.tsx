import {
  editableFields,
  enc,
  filterLiteral,
  getCollection,
  listCollections,
  pb,
  recordLabel,
  type Page,
  type RecordItem,
} from "@/lib/pb";
import { pageProject } from "@/lib/projects";
import { load, Notice } from "../../notice";
import { RecordForm, type FieldInput } from "../../ui";

export default async function RecordPage({ params }: PageProps<"/dashboard/[id]/tables/[table]/[record]">) {
  const { id, table: raw, record: recordId } = await params;
  const p = await pageProject(id);
  if (p.status !== "active") return <Notice project={p} />;
  const table = decodeURIComponent(raw);
  const isNew = recordId === "new";

  const [data, error] = await load(async () => {
    const c = await getCollection(p, table);
    const record = isNew
      ? null
      : await pb<RecordItem>(p, `/api/collections/${enc(c.id)}/records/${enc(recordId)}`);

    const all = c.fields.some((f) => f.type === "relation") ? await listCollections(p) : [];
    const fields: FieldInput[] = await Promise.all(
      editableFields(c).map(async (f) => {
        if (f.type === "select")
          return { ...f, options: ((f.values as string[]) ?? []).map((v) => ({ value: v, label: v })) };
        if (f.type === "relation") {
          // Only the currently linked records are loaded here; the picker searches the rest on demand.
          const target = all.find((x) => x.id === f.collectionId);
          const ids = ([] as unknown[]).concat(record?.[f.name] ?? []).map(String).filter(Boolean);
          if (!target || !ids.length) return { ...f, options: [] };
          const filter = ids.map((x) => `id=${filterLiteral(x)}`).join(" || ");
          const rs = await pb<Page<RecordItem>>(
            p,
            `/api/collections/${enc(target.id)}/records?perPage=${ids.length}&filter=${enc(filter)}`,
          );
          return { ...f, options: rs.items.map((r) => ({ value: r.id, label: recordLabel(target, r) })) };
        }
        return f;
      }),
    );
    return { c, record, fields };
  });
  if (!data) return <Notice project={p} error={error} />;

  return (
    <div className="max-w-2xl space-y-3">
      <h2 className="font-medium">
        {isNew ? `New ${table === "users" ? "user" : "record"}` : `Edit ${data.record?.id}`}
      </h2>
      {data.fields.length === 0 && data.c.type !== "auth" ? (
        <div className="card text-sm text-muted">Add fields in Settings before creating records.</div>
      ) : (
        <RecordForm
          projectId={p.id}
          table={table}
          collectionId={data.c.id}
          isAuth={data.c.type === "auth"}
          fields={data.fields}
          record={data.record}
        />
      )}
    </div>
  );
}
