import Link from "next/link";
import { editableFields, enc, getCollection, pb, type Field, type Page, type RecordItem } from "@/lib/pb";
import { pageProject } from "@/lib/projects";
import { load, Notice } from "../notice";
import { DeleteRecordButton } from "../ui";

const PER_PAGE = 25;

function Cell({ field, value, href }: { field: Field; value: unknown; href: (n: string) => string }) {
  if (value == null || value === "" || (Array.isArray(value) && value.length === 0))
    return <span className="text-muted">—</span>;
  if (field.type === "bool") return <span>{value ? "✓" : "—"}</span>;
  if (field.type === "file") {
    const files = Array.isArray(value) ? (value as string[]) : [String(value)];
    return (
      <span className="flex flex-col">
        {files.slice(0, 2).map((n) => (
          <a key={n} href={href(n)} target="_blank" rel="noreferrer" className="truncate text-accent-text hover:underline">
            {n}
          </a>
        ))}
        {files.length > 2 && <span className="text-xs text-muted">+{files.length - 2} more</span>}
      </span>
    );
  }
  const text =
    typeof value === "object" && !Array.isArray(value)
      ? JSON.stringify(value)
      : Array.isArray(value)
        ? value.join(", ")
        : field.type === "date" || field.type === "autodate"
          ? String(value).slice(0, 16)
          : String(value);
  return <span title={text}>{text.length > 60 ? `${text.slice(0, 60)}…` : text}</span>;
}

export default async function TableData({
  params,
  searchParams,
}: PageProps<"/dashboard/[id]/tables/[table]">) {
  const { id, table: raw } = await params;
  const sp = await searchParams;
  const p = await pageProject(id);
  if (p.status !== "active") return <Notice project={p} />;

  const table = decodeURIComponent(raw);
  const page = Math.max(1, Number(sp.page) || 1);
  const filter = typeof sp.filter === "string" ? sp.filter : "";

  const [data, error] = await load(async () => {
    const c = await getCollection(p, table);
    const names = c.fields.map((f) => f.name);
    const sortField = typeof sp.sort === "string" ? sp.sort.replace(/^-/, "") : "";
    const sort = names.includes(sortField) ? String(sp.sort) : names.includes("created") ? "-created" : "";
    const q = new URLSearchParams({ page: String(page), perPage: String(PER_PAGE) });
    if (sort) q.set("sort", sort);
    if (filter) q.set("filter", filter);
    const records = await pb<Page<RecordItem>>(p, `/api/collections/${enc(table)}/records?${q}`);
    return { c, records, sort };
  });

  if (!data) {
    // A bad filter is the most common error here; keep the filter box visible so it can be fixed.
    return (
      <div className="space-y-4">
        <FilterBar filter={filter} />
        <Notice project={p} error={error} />
      </div>
    );
  }

  const { c, records, sort } = data;
  const fields = editableFields(c);
  const created = c.fields.find((f) => f.name === "created");
  const columns = created ? [...fields, created] : fields;
  const base = `/dashboard/${p.id}/tables/${enc(table)}`;
  const link = (over: Record<string, string | number>) => {
    const q = new URLSearchParams({ ...(filter && { filter }), ...(sort && { sort }), page: String(page) });
    for (const [k, v] of Object.entries(over)) q.set(k, String(v));
    return `${base}?${q}`;
  };
  const fileHref = (rid: string) => (n: string) => `/dashboard/${p.id}/files/${c.id}/${rid}/${encodeURIComponent(n)}`;

  return (
    <div className="space-y-4">
      <FilterBar filter={filter} />

      {columns.length === 0 ? (
        <div className="card text-sm text-muted">
          This table has no fields yet.{" "}
          <Link href={`${base}/settings`} className="text-accent-text hover:underline">
            Add fields
          </Link>
        </div>
      ) : (
        <div className="overflow-x-auto rounded-lg border border-border bg-card">
          <table className="w-full text-sm">
            <thead className="border-b border-border text-left">
              <tr>
                {columns.map((f) => {
                  const active = sort.replace(/^-/, "") === f.name;
                  const next = active && !sort.startsWith("-") ? `-${f.name}` : f.name;
                  return (
                    <th key={f.name} className="whitespace-nowrap px-3 py-2 font-medium">
                      <Link href={link({ sort: next, page: 1 })} className="hover:underline">
                        {f.name}
                        {active && (sort.startsWith("-") ? " ↓" : " ↑")}
                      </Link>
                    </th>
                  );
                })}
                <th className="px-3 py-2" />
              </tr>
            </thead>
            <tbody className="divide-y divide-border">
              {records.items.map((r) => (
                <tr key={r.id} className="hover:bg-border/20">
                  {columns.map((f) => (
                    <td key={f.name} className="max-w-64 px-3 py-2 align-top">
                      <Cell field={f} value={r[f.name]} href={fileHref(r.id)} />
                    </td>
                  ))}
                  <td className="whitespace-nowrap px-3 py-2 text-right align-top">
                    <Link href={`${base}/${r.id}`} className="mr-3 text-sm text-accent-text hover:underline">
                      Edit
                    </Link>
                    <DeleteRecordButton projectId={p.id} table={table} recordId={r.id} />
                  </td>
                </tr>
              ))}
              {records.items.length === 0 && (
                <tr>
                  <td colSpan={columns.length + 1} className="px-3 py-8 text-center text-muted">
                    {filter ? "No records match this filter." : "No records yet."}
                  </td>
                </tr>
              )}
            </tbody>
          </table>
        </div>
      )}

      {records.totalPages > 1 && (
        <div className="flex items-center justify-between text-sm">
          <span className="text-muted">
            {records.totalItems} records · page {page} of {records.totalPages}
          </span>
          <div className="flex gap-2">
            {page > 1 && (
              <Link href={link({ page: page - 1 })} className="btn-ghost">
                ← Prev
              </Link>
            )}
            {page < records.totalPages && (
              <Link href={link({ page: page + 1 })} className="btn-ghost">
                Next →
              </Link>
            )}
          </div>
        </div>
      )}
    </div>
  );
}

function FilterBar({ filter }: { filter: string }) {
  return (
    <form className="flex gap-2">
      <input
        name="filter"
        defaultValue={filter}
        placeholder={`Filter, e.g.  price > 10 && name ~ "mug"`}
        className="input font-mono text-xs"
      />
      <button className="btn-ghost shrink-0">Apply</button>
    </form>
  );
}
