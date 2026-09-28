import { getCollection, listCollections, type Field } from "@/lib/pb";
import { pageProject } from "@/lib/projects";
import { load, Notice } from "../../notice";
import { FIELD_LABELS, findOwnerField, presetOf, UNIQUE_TYPES, uniqueRules } from "../../shared";
import { AddFieldForm, DeleteTableForm, RemoveFieldButton, RenameTableForm, RulesForm, UniqueRules } from "../../ui";

export default async function TableSettings({ params }: PageProps<"/dashboard/[id]/tables/[table]/settings">) {
  const { id, table: raw } = await params;
  const p = await pageProject(id);
  if (p.status !== "active") return <Notice project={p} />;
  const table = decodeURIComponent(raw);

  const [data, error] = await load(async () => ({
    c: await getCollection(p, table),
    all: await listCollections(p),
  }));
  if (!data) return <Notice project={p} error={error} />;
  const { c, all } = data;
  const isAuth = c.type === "auth";

  const ownerField = findOwnerField(
    c.fields,
    all.filter((x) => x.type === "auth").map((x) => x.id),
  );
  const unique = uniqueRules(c.indexes);
  const uniqueCandidates = c.fields
    .filter((f) => !f.system && !f.hidden && (UNIQUE_TYPES as readonly string[]).includes(f.type) && (f.maxSelect ?? 1) <= 1)
    .map((f) => f.name);
  const describeNumber = (f: Field) =>
    [f.onlyInt ? "whole" : "", f.min != null ? `min ${f.min}` : "", f.max != null ? `max ${f.max}` : ""].filter(Boolean).join(", ");
  const rules = {
    list: presetOf(c.listRule, "list", ownerField),
    view: presetOf(c.viewRule, "view", ownerField),
    create: presetOf(c.createRule, "create", ownerField),
    update: presetOf(c.updateRule, "update", ownerField),
    delete: presetOf(c.deleteRule, "delete", ownerField),
  };

  return (
    <div className="space-y-6">
      <section className="card space-y-4">
        <h2 className="font-medium">Fields</h2>
        <ul className="divide-y divide-border rounded-md border border-border">
          {c.fields
            .filter((f) => !f.hidden)
            .map((f) => (
              <li key={f.id} className="flex items-center justify-between gap-3 px-3 py-2 text-sm">
                <div className="flex min-w-0 items-center gap-2">
                  <span className="font-mono">{f.name}</span>
                  <span className="text-xs text-muted">
                    {FIELD_LABELS[f.type] ?? f.type}
                    {(f.maxSelect ?? 1) > 1 && " · multiple"}
                    {f.required && " · required"}
                    {f.type === "relation" && ` → ${all.find((x) => x.id === f.collectionId)?.name ?? "?"}`}
                    {f.type === "number" && describeNumber(f) && ` · ${describeNumber(f)}`}
                    {unique.some((r) => r.fields.length === 1 && r.fields[0] === f.name) && " · unique"}
                  </span>
                </div>
                {f.system || f.type === "autodate" ? (
                  <span className="text-xs text-muted">built-in</span>
                ) : (
                  <RemoveFieldButton projectId={p.id} table={table} field={f} />
                )}
              </li>
            ))}
        </ul>
        <AddFieldForm projectId={p.id} table={table} tables={all.map(({ id, name }) => ({ id, name }))} />
      </section>

      <section className="card space-y-3">
        <div>
          <h2 className="font-medium">Unique rules</h2>
          <p className="text-sm text-muted">
            Prevent duplicates, enforced by the database even for simultaneous requests. Example: one review per user
            per product = <code>owner</code> + <code>product</code>.
          </p>
        </div>
        <UniqueRules projectId={p.id} table={table} rules={unique} candidates={uniqueCandidates} />
      </section>

      <section className="card space-y-3">
        <div>
          <h2 className="font-medium">Access</h2>
          <p className="text-sm text-muted">
            Who can do what through your API. “Any signed-in user” means any user of your app, including
            other people’s records. Your server admin credentials can always do everything.
            {isAuth && " App users can always sign in; these rules control reading and editing user records."}
          </p>
        </div>
        <RulesForm projectId={p.id} table={table} current={rules} ownerField={isAuth ? null : ownerField} />
        {!isAuth && !ownerField && (
          <p className="text-xs text-muted">
            Want each user to see and change only their own records? Add a <b>Link to table</b> field pointing to{" "}
            <b>users</b> (for example <code>owner</code>) and the “Only its owner” option appears.
          </p>
        )}
      </section>

      {!isAuth && (
        <section className="card space-y-3">
          <h2 className="font-medium">Rename table</h2>
          <RenameTableForm projectId={p.id} table={table} />
        </section>
      )}

      <section className="card space-y-3 border-red-500/40">
        <h2 className="font-medium text-red-600">Danger zone</h2>
        <DeleteTableForm projectId={p.id} table={table} />
      </section>
    </div>
  );
}
