"use client";

import { useRouter } from "next/navigation";
import { useActionState, useEffect, useRef, useState, useTransition } from "react";
import type { Field } from "@/lib/pb";
import {
  addField,
  addUniqueRule,
  createTable,
  deleteRecord,
  importSchema,
  deleteTable,
  removeField,
  removeUniqueRule,
  renameTable,
  saveRecord,
  saveRules,
  searchRecords,
  type FormState,
} from "./actions";
import { FIELD_LABELS, FIELD_TYPES, RULE_LABELS, UNIQUE_TYPES, type RulePreset, type UniqueRule } from "./shared";

function Err({ state }: { state: FormState }) {
  return state?.error ? <p className="text-sm text-red-600">{state.error}</p> : null;
}

export function CreateTableForm({ projectId }: { projectId: string }) {
  const [state, action, pending] = useActionState(createTable, undefined);
  return (
    <form action={action} className="card space-y-3">
      <h2 className="font-medium">New table</h2>
      <input type="hidden" name="projectId" value={projectId} />
      <div className="flex flex-col gap-2 sm:flex-row">
        <input name="name" placeholder="products" required className="input" autoComplete="off" />
        <button className="btn shrink-0" disabled={pending}>
          {pending ? "Creating…" : "Create table"}
        </button>
      </div>
      <Err state={state} />
    </form>
  );
}

const IMPORT_EXAMPLE = `{
  "tables": [
    {
      "name": "categories",
      "fields": [
        { "name": "name", "type": "text", "required": true },
        { "name": "slug", "type": "text", "unique": true }
      ],
      "access": { "read": "public", "write": "admin" }
    },
    {
      "name": "products",
      "fields": [
        { "name": "title", "type": "text", "required": true },
        { "name": "price", "type": "number", "min": 0 },
        { "name": "category", "type": "relation", "table": "categories" },
        { "name": "photos", "type": "file", "multiple": true }
      ],
      "access": { "read": "public", "write": "admin" }
    }
  ]
}`;

/** Paste a whole schema (usually written by an AI assistant from DATBASE.md) and create every table at once. */
export function ImportSchemaForm({ projectId }: { projectId: string }) {
  const [state, action, pending] = useActionState(importSchema, undefined);
  const [text, setText] = useState("");
  const [open, setOpen] = useState(false);

  return (
    <details className="card" open={open} onToggle={(e) => setOpen((e.target as HTMLDetailsElement).open)}>
      <summary className="cursor-pointer font-medium">Import schema (many tables at once)</summary>
      <form action={action} className="mt-3 space-y-3">
        <input type="hidden" name="projectId" value={projectId} />
        <p className="text-sm text-muted">
          Paste a schema in JSON. Ask your AI assistant: <em>“Write a DATBase schema import for my app”</em>. The
          format is explained in DATBASE.md, and common SQL / Supabase type names work too. Tables that already exist
          get the new fields added; nothing is deleted. If anything fails, nothing changes.
        </p>
        <textarea
          name="schema"
          value={text}
          onChange={(e) => setText(e.target.value)}
          rows={14}
          spellCheck={false}
          placeholder={IMPORT_EXAMPLE}
          className="input font-mono text-xs"
        />
        <div className="flex flex-wrap gap-2">
          <button className="btn" disabled={pending || !text.trim()}>
            {pending ? "Importing…" : "Import"}
          </button>
          <button type="button" className="btn-ghost" onClick={() => setText(IMPORT_EXAMPLE)}>
            Load example
          </button>
        </div>
        {state?.error && <p className="text-sm text-red-600">{state.error}</p>}
        {state?.ok && state.created && (
          <div className="text-sm">
            <p className="font-medium text-green-700">Imported:</p>
            <ul className="list-disc pl-5">
              {state.created.map((c) => (
                <li key={c}>{c}</li>
              ))}
            </ul>
            <p className="mt-1 text-muted">Download DATBASE.md again so your AI assistant sees the new tables.</p>
          </div>
        )}
        {state?.notes && state.notes.length > 0 && (
          <div className="text-sm text-muted">
            <p className="font-medium">Notes:</p>
            <ul className="list-disc pl-5">
              {state.notes.map((n) => (
                <li key={n}>{n}</li>
              ))}
            </ul>
          </div>
        )}
      </form>
    </details>
  );
}

/* ---------- Settings: fields ---------- */

export function AddFieldForm({
  projectId,
  table,
  tables,
}: {
  projectId: string;
  table: string;
  tables: { id: string; name: string }[];
}) {
  const [state, action, pending] = useActionState(addField, undefined);
  const [type, setType] = useState<string>("text");
  const canMulti = type === "select" || type === "file" || type === "relation";
  const [multi, setMulti] = useState(false);
  const canUnique = (UNIQUE_TYPES as readonly string[]).includes(type) && !(type === "relation" && multi);

  return (
    <form action={action} className="space-y-3 rounded-md border border-dashed border-border p-4">
      <input type="hidden" name="projectId" value={projectId} />
      <input type="hidden" name="table" value={table} />
      <div className="grid gap-2 sm:grid-cols-[1fr_180px]">
        <input name="name" placeholder="Field name, e.g. price" required className="input" autoComplete="off" />
        <select name="type" value={type} onChange={(e) => setType(e.target.value)} className="input">
          {FIELD_TYPES.map((t) => (
            <option key={t} value={t}>
              {FIELD_LABELS[t]}
            </option>
          ))}
        </select>
      </div>

      {type === "select" && (
        <input name="values" placeholder="Choices, comma-separated: small, medium, large" className="input" />
      )}
      {type === "number" && (
        <div className="space-y-1">
          <div className="flex flex-wrap items-center gap-2 text-sm">
            <input name="min" type="number" step="any" placeholder="Min (optional)" className="input w-36" />
            <input name="max" type="number" step="any" placeholder="Max (optional)" className="input w-36" />
            <label className="flex items-center gap-2">
              <input type="checkbox" name="onlyInt" /> Whole numbers only
            </label>
          </div>
          <p className="text-xs text-muted">0 counts as “empty”. Tick Required if 0 must not be allowed (e.g. a 1–5 rating).</p>
        </div>
      )}
      {type === "file" && (
        <label className="flex items-center gap-2 text-sm">
          Max size (MB)
          <input name="maxSizeMb" type="number" min={1} max={100} defaultValue={5} className="input w-24" />
        </label>
      )}
      {type === "relation" && (
        <select name="target" className="input" defaultValue="">
          <option value="" disabled>
            Links to which table?
          </option>
          {tables.map((t) => (
            <option key={t.id} value={t.id}>
              {t.name}
            </option>
          ))}
        </select>
      )}

      <div className="flex flex-wrap items-center gap-4 text-sm">
        <label className="flex items-center gap-2">
          <input type="checkbox" name="required" /> Required
        </label>
        {canMulti && (
          <label className="flex items-center gap-2">
            <input type="checkbox" name="multiple" checked={multi} onChange={(e) => setMulti(e.target.checked)} /> Allow multiple
          </label>
        )}
        {canUnique && (
          <label className="flex items-center gap-2" title="No two records may have the same value">
            <input type="checkbox" name="unique" /> Unique
          </label>
        )}
        <button className="btn ml-auto" disabled={pending}>
          {pending ? "Adding…" : "Add field"}
        </button>
      </div>
      <Err state={state} />
    </form>
  );
}

export function RemoveFieldButton({ projectId, table, field }: { projectId: string; table: string; field: Field }) {
  const [pending, start] = useTransition();
  const [error, setError] = useState<string>();
  return (
    <span className="flex items-center gap-2">
      {error && <span className="text-xs text-red-600">{error}</span>}
      <button
        className="text-sm text-red-600 hover:underline disabled:opacity-50"
        disabled={pending}
        onClick={() => {
          if (!confirm(`Remove "${field.name}"? Its data in every record will be deleted.`)) return;
          start(async () => setError((await removeField(projectId, table, field.id!))?.error));
        }}
      >
        {pending ? "Removing…" : "Remove"}
      </button>
    </span>
  );
}

/* ---------- Settings: access ---------- */

const OPS = [
  ["list", "See the list"],
  ["view", "View one record"],
  ["create", "Add records"],
  ["update", "Edit records"],
  ["delete", "Delete records"],
] as const;

export function RulesForm({
  projectId,
  table,
  current,
  ownerField,
}: {
  projectId: string;
  table: string;
  current: Record<string, RulePreset | "custom">;
  ownerField: string | null;
}) {
  const [state, action, pending] = useActionState(saveRules, undefined);
  return (
    <form action={action} className="space-y-3">
      <input type="hidden" name="projectId" value={projectId} />
      <input type="hidden" name="table" value={table} />
      <div className="divide-y divide-border rounded-md border border-border">
        {OPS.map(([op, label]) => (
          <label key={op} className="flex items-center justify-between gap-3 px-3 py-2 text-sm">
            <span>{label}</span>
            <select name={op} defaultValue={current[op] === "custom" ? "keep" : current[op]} className="input w-52">
              {current[op] === "custom" && <option value="keep">Custom rule (keep)</option>}
              {(Object.keys(RULE_LABELS) as RulePreset[])
                .filter((k) => k !== "owner" || ownerField)
                .map((k) => (
                  <option key={k} value={k}>
                    {k === "owner" ? `${RULE_LABELS[k]} (${ownerField})` : RULE_LABELS[k]}
                  </option>
                ))}
            </select>
          </label>
        ))}
      </div>
      <div className="flex items-center gap-3">
        <button className="btn" disabled={pending}>
          {pending ? "Saving…" : "Save access"}
        </button>
        {state?.ok && <span className="text-sm text-success">Saved</span>}
        <Err state={state} />
      </div>
    </form>
  );
}

/* ---------- Settings: rename / delete ---------- */

export function RenameTableForm({ projectId, table }: { projectId: string; table: string }) {
  const [state, action, pending] = useActionState(renameTable, undefined);
  return (
    <form action={action} className="space-y-2">
      <input type="hidden" name="projectId" value={projectId} />
      <input type="hidden" name="table" value={table} />
      <div className="flex flex-col gap-2 sm:flex-row">
        <input name="name" defaultValue={table} required className="input" autoComplete="off" />
        <button className="btn-ghost shrink-0" disabled={pending}>
          {pending ? "Saving…" : "Rename"}
        </button>
      </div>
      <p className="text-xs text-muted">Renaming changes the API URL. Update your apps.</p>
      <Err state={state} />
    </form>
  );
}

export function DeleteTableForm({ projectId, table }: { projectId: string; table: string }) {
  const [state, action, pending] = useActionState(deleteTable, undefined);
  const [typed, setTyped] = useState("");
  return (
    <form action={action} className="space-y-2">
      <input type="hidden" name="projectId" value={projectId} />
      <input type="hidden" name="table" value={table} />
      <p className="text-sm text-muted">
        Deletes the table and every record in it. Type <b>{table}</b> to confirm.
      </p>
      <div className="flex flex-col gap-2 sm:flex-row">
        <input name="confirm" value={typed} onChange={(e) => setTyped(e.target.value)} className="input" autoComplete="off" />
        <button className="btn-danger shrink-0" disabled={pending || typed !== table}>
          {pending ? "Deleting…" : "Delete table"}
        </button>
      </div>
      <Err state={state} />
    </form>
  );
}

/* ---------- Records ---------- */

export type FieldInput = Field & { options?: { value: string; label: string }[] };

type Option = { value: string; label: string };

/** Search-as-you-type picker for linked records; works for any table size (F-05). */
function RelationPicker({ projectId, field }: { projectId: string; field: FieldInput }) {
  const multi = (field.maxSelect ?? 1) > 1;
  const [selected, setSelected] = useState<Option[]>(field.options ?? []);
  const [q, setQ] = useState("");
  const [results, setResults] = useState<Option[]>([]);
  const [open, setOpen] = useState(false);
  const seq = useRef(0);

  useEffect(() => {
    if (!open) return;
    const n = ++seq.current;
    const t = setTimeout(async () => {
      const r = await searchRecords(projectId, field.collectionId ?? "", q);
      if (n === seq.current) setResults(r); // ignore out-of-order responses
    }, 250);
    return () => clearTimeout(t);
  }, [q, open, projectId, field.collectionId]);

  const pick = (o: Option) => {
    setSelected((cur) => (multi ? (cur.some((c) => c.value === o.value) ? cur : [...cur, o]) : [o]));
    if (!multi) setOpen(false);
    setQ("");
  };
  const name = `f_${field.name}`;

  return (
    <div className="space-y-2">
      {selected.length === 0 && <input type="hidden" name={name} value="" />}
      {selected.map((o) => (
        <input key={o.value} type="hidden" name={name} value={o.value} />
      ))}
      {selected.length > 0 && (
        <div className="flex flex-wrap gap-1.5">
          {selected.map((o) => (
            <span key={o.value} className="inline-flex items-center gap-1 rounded bg-border/60 px-2 py-0.5 text-xs">
              {o.label}
              <button
                type="button"
                aria-label={`Remove ${o.label}`}
                className="text-muted hover:text-foreground"
                onClick={() => setSelected((cur) => cur.filter((c) => c.value !== o.value))}
              >
                ×
              </button>
            </span>
          ))}
        </div>
      )}
      <div className="relative">
        <input
          value={q}
          onChange={(e) => setQ(e.target.value)}
          onFocus={() => setOpen(true)}
          onBlur={() => setTimeout(() => setOpen(false), 150)}
          placeholder={multi || !selected.length ? "Search to link a record…" : "Search to change…"}
          aria-label={`Search ${field.name}`}
          className="input"
          autoComplete="off"
        />
        {open && (
          <ul className="absolute z-10 mt-1 max-h-60 w-full overflow-auto rounded-md border border-border bg-card text-sm shadow">
            {results.length === 0 && <li className="px-3 py-2 text-muted">No matches</li>}
            {results.map((o) => (
              <li key={o.value}>
                <button
                  type="button"
                  onMouseDown={(e) => e.preventDefault()}
                  onClick={() => pick(o)}
                  className="block w-full truncate px-3 py-2 text-left hover:bg-border/40"
                >
                  {o.label}
                </button>
              </li>
            ))}
          </ul>
        )}
      </div>
    </div>
  );
}

function FieldControl({
  field,
  value,
  fileHref,
  projectId,
}: {
  field: FieldInput;
  value: unknown;
  fileHref: (n: string) => string;
  projectId: string;
}) {
  const name = `f_${field.name}`;
  const multi = (field.maxSelect ?? 1) > 1;
  const list = Array.isArray(value) ? (value as string[]) : value ? [String(value)] : [];

  switch (field.type) {
    case "bool":
      return <input type="checkbox" name={name} defaultChecked={Boolean(value)} className="h-4 w-4" />;
    case "number":
      return <input type="number" step="any" name={name} defaultValue={value == null ? "" : String(value)} className="input" />;
    case "email":
      return <input type="email" name={name} defaultValue={String(value ?? "")} className="input" />;
    case "url":
      return <input type="url" name={name} defaultValue={String(value ?? "")} className="input" />;
    case "date": {
      const v = String(value ?? "").replace(" ", "T").slice(0, 16);
      return (
        <div className="flex items-center gap-2">
          <input type="datetime-local" name={name} defaultValue={v} className="input" />
          <span className="text-xs text-muted">UTC</span>
        </div>
      );
    }
    case "json":
      return (
        <textarea
          name={name}
          rows={4}
          defaultValue={value == null ? "" : JSON.stringify(value, null, 2)}
          className="input font-mono text-xs"
        />
      );
    case "relation":
      return <RelationPicker projectId={projectId} field={field} />;
    case "select":
      return (
        <select name={name} multiple={multi} defaultValue={multi ? list : list[0] ?? ""} className="input">
          {!multi && <option value="">—</option>}
          {(field.options ?? []).map((o) => (
            <option key={o.value} value={o.value}>
              {o.label}
            </option>
          ))}
        </select>
      );
    case "file":
      return (
        <div className="space-y-2">
          {list.map((n) => (
            <label key={n} className="flex items-center gap-2 text-sm">
              <a href={fileHref(n)} target="_blank" rel="noreferrer" className="truncate text-accent-text hover:underline">
                {n}
              </a>
              <span className="ml-auto flex items-center gap-1 text-xs text-muted">
                <input type="checkbox" name={`remove_${field.name}`} value={n} /> remove
              </span>
            </label>
          ))}
          <input type="file" name={name} multiple={multi} className="text-sm" />
        </div>
      );
    default:
      return <input name={name} defaultValue={String(value ?? "")} className="input" />;
  }
}

export function RecordForm({
  projectId,
  table,
  collectionId,
  isAuth,
  fields,
  record,
}: {
  projectId: string;
  table: string;
  collectionId: string;
  isAuth: boolean;
  fields: FieldInput[];
  record: Record<string, unknown> | null;
}) {
  const [state, action, pending] = useActionState(saveRecord, undefined);
  const recordId = record ? String(record.id) : "";
  const fileHref = (n: string) => `/dashboard/${projectId}/files/${collectionId}/${recordId}/${encodeURIComponent(n)}`;

  return (
    <form action={action} className="card space-y-4">
      <input type="hidden" name="projectId" value={projectId} />
      <input type="hidden" name="table" value={table} />
      <input type="hidden" name="recordId" value={recordId} />

      {fields.map((f) => (
        <div key={f.name} className="space-y-1">
          <label className="text-sm font-medium">
            {f.name}
            {f.required && <span className="text-red-600"> *</span>}
            <span className="ml-2 text-xs font-normal text-muted">{FIELD_LABELS[f.type] ?? f.type}</span>
          </label>
          <FieldControl field={f} value={record?.[f.name]} fileHref={fileHref} projectId={projectId} />
        </div>
      ))}

      {isAuth && (
        <div className="space-y-1">
          <label className="text-sm font-medium">
            Password{!record && <span className="text-red-600"> *</span>}
          </label>
          <input
            type="password"
            name="password"
            minLength={8}
            autoComplete="new-password"
            placeholder={record ? "Leave blank to keep current password" : "At least 8 characters"}
            className="input"
          />
        </div>
      )}

      <Err state={state} />
      <button className="btn" disabled={pending}>
        {pending ? "Saving…" : record ? "Save changes" : "Create record"}
      </button>
    </form>
  );
}

export function DeleteRecordButton({ projectId, table, recordId }: { projectId: string; table: string; recordId: string }) {
  const [pending, start] = useTransition();
  const router = useRouter();
  return (
    <button
      className="text-sm text-red-600 hover:underline disabled:opacity-50"
      disabled={pending}
      onClick={() => {
        if (!confirm("Delete this record? This can't be undone.")) return;
        start(async () => {
          const r = await deleteRecord(projectId, table, recordId);
          if (r?.error) alert(r.error);
          router.refresh();
        });
      }}
    >
      {pending ? "…" : "Delete"}
    </button>
  );
}

/* ---------- Settings: unique rules ---------- */

export function UniqueRules({
  projectId,
  table,
  rules,
  candidates,
}: {
  projectId: string;
  table: string;
  rules: UniqueRule[];
  candidates: string[];
}) {
  const [state, action, pending] = useActionState(addUniqueRule, undefined);
  const [busy, start] = useTransition();
  const [error, setError] = useState<string>();
  return (
    <div className="space-y-3">
      {rules.length > 0 && (
        <ul className="divide-y divide-border rounded-md border border-border">
          {rules.map((r) => (
            <li key={r.name} className="flex items-center justify-between gap-3 px-3 py-2 text-sm">
              <span>
                {r.fields.map((f) => (
                  <code key={f} className="mr-1">
                    {f}
                  </code>
                ))}
                <span className="text-xs text-muted">{r.fields.length > 1 ? "unique together" : "unique"}</span>
              </span>
              <button
                className="text-sm text-red-600 hover:underline disabled:opacity-50"
                disabled={busy}
                onClick={() => start(async () => setError((await removeUniqueRule(projectId, table, r.name))?.error))}
              >
                Remove
              </button>
            </li>
          ))}
        </ul>
      )}
      {candidates.length > 0 ? (
        <form action={action} className="space-y-2 rounded-md border border-dashed border-border p-3">
          <input type="hidden" name="projectId" value={projectId} />
          <input type="hidden" name="table" value={table} />
          <p className="text-sm">No two records may share the same value (or combination) of:</p>
          <div className="flex flex-wrap gap-3 text-sm">
            {candidates.map((c) => (
              <label key={c} className="flex items-center gap-1.5">
                <input type="checkbox" name="fields" value={c} /> <code>{c}</code>
              </label>
            ))}
          </div>
          <button className="btn-ghost" disabled={pending}>
            {pending ? "Adding…" : "Add unique rule"}
          </button>
        </form>
      ) : (
        <p className="text-sm text-muted">Add text, number or link fields to create unique rules.</p>
      )}
      <Err state={state} />
      {error && <p className="text-sm text-red-600">{error}</p>}
    </div>
  );
}
