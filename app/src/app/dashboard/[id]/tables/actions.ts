"use server";

import { revalidatePath } from "next/cache";
import { redirect } from "next/navigation";
import {
  editableFields,
  enc,
  filterLiteral,
  getCollection,
  json,
  labelFields,
  listCollections,
  pb,
  PbError,
  recordLabel,
  type Collection,
  type Field,
  type Page,
  type RecordItem,
} from "@/lib/pb";
import { actionProject } from "@/lib/projects";
import { randomId } from "@/lib/crypto";
import { FIELD_TYPES, findOwnerField, ownerRule, RULE_PRESETS, UNIQUE_TYPES, uniqueIndexSql, uniqueRules, type RuleOp } from "./shared";
import { describePlan, planImport, runImport } from "./import-schema";

export type FormState = { error?: string; ok?: boolean } | undefined;

const str = (f: FormData, k: string) => String(f.get(k) ?? "").trim();
const NAME_RE = /^[a-zA-Z][a-zA-Z0-9_]{0,49}$/;
const RECORD_ID_RE = /^[a-zA-Z0-9_-]{1,64}$/;

/** Field names PocketBase uses itself; adding one would break the table (F-02). */
const RESERVED_FIELDS = new Set(
  ["id", "created", "updated", "collectionId", "collectionName", "expand", "fields"].map((n) => n.toLowerCase()),
);
const RESERVED_AUTH_FIELDS = new Set(
  ["email", "emailVisibility", "verified", "password", "passwordConfirm", "oldPassword", "tokenKey"].map((n) =>
    n.toLowerCase(),
  ),
);

const base = (projectId: string, table?: string) =>
  `/dashboard/${projectId}/tables${table ? `/${enc(table)}` : ""}`;

/** Run a PocketBase call and turn its errors into form errors. */
async function attempt(fn: () => Promise<void>): Promise<FormState> {
  try {
    await fn();
    return { ok: true };
  } catch (e) {
    if (e instanceof PbError) return { error: e.message };
    throw e;
  }
}

/* ---------------- Tables ---------------- */

export async function createTable(_: FormState, f: FormData): Promise<FormState> {
  const p = await actionProject(f.get("projectId"));
  const name = str(f, "name");
  if (!NAME_RE.test(name)) return { error: "Use letters, numbers and underscores, starting with a letter." };

  const res = await attempt(async () => {
    await pb(p, "/api/collections", json("POST", {
      name,
      type: "base",
      fields: [
        { name: "created", type: "autodate", onCreate: true, onUpdate: false },
        { name: "updated", type: "autodate", onCreate: true, onUpdate: true },
      ],
    }));
  });
  if (res?.error) return res;
  redirect(`${base(p.id, name)}/settings`);
}

export async function renameTable(_: FormState, f: FormData): Promise<FormState> {
  const p = await actionProject(f.get("projectId"));
  const table = str(f, "table");
  const name = str(f, "name");
  if (!NAME_RE.test(name)) return { error: "Use letters, numbers and underscores, starting with a letter." };
  const res = await attempt(async () => {
    const c = await getCollection(p, table);
    await pb(p, `/api/collections/${enc(c.id)}`, json("PATCH", { name }));
  });
  if (res?.error) return res;
  redirect(`${base(p.id, name)}/settings`);
}

export async function deleteTable(_: FormState, f: FormData): Promise<FormState> {
  const p = await actionProject(f.get("projectId"));
  const table = str(f, "table");
  if (str(f, "confirm") !== table) return { error: "Type the table name exactly to confirm." };
  const res = await attempt(async () => {
    const c = await getCollection(p, table);
    await pb(p, `/api/collections/${enc(c.id)}`, { method: "DELETE" });
  });
  if (res?.error) return res;
  redirect(base(p.id));
}

/* ---------------- Schema import (many tables from one JSON paste) ---------------- */

export type ImportState = { error?: string; ok?: boolean; created?: string[]; notes?: string[] } | undefined;

/**
 * Turn an import failure into one product-level sentence. Most failures already carry a clear message
 * (pb.ts turns PocketBase's validation body into a readable line). The exception is a race: if the same
 * table is imported from two dashboard tabs at once, both plans validate against a project that still
 * lacks the table, then collide when they hit the database. Depending on timing that surfaces either as
 * PocketBase's own "name must be unique" validation or, when the collision is caught at the storage
 * layer, as a raw SQLite line ("UNIQUE constraint failed: _collections.name (2067)"). Either way the
 * import rolled back cleanly (runImport is all-or-nothing); the owner just needs to know it was a
 * duplicate, not an internal error. Both forms map to the same guidance.
 */
function importErrorMessage(e: unknown): string {
  const msg = e instanceof Error ? e.message : String(e);
  const duplicateName =
    /unique constraint failed:\s*_collections\.name/i.test(msg) || // raw SQLite (concurrent commit)
    /collection name must be unique/i.test(msg) || // PocketBase validation
    /^name:\s*collection name must be unique/i.test(msg);
  if (duplicateName) {
    return "One of these tables already exists — another import may have just created it. Refresh the page and try again; anything already there is safe.";
  }
  return msg;
}

/** Create or extend every table in a pasted schema (see import-schema.ts). All-or-nothing. */
export async function importSchema(_: ImportState, f: FormData): Promise<ImportState> {
  const p = await actionProject(f.get("projectId"));
  const text = String(f.get("schema") ?? "");
  try {
    const existing = await listCollections(p);
    const planned = planImport(text, existing);
    if ("error" in planned) return planned;
    const { plan, notes } = planned;
    try {
      await runImport(
        plan,
        existing,
        (method, path, body) => pb(p, path, body === undefined ? { method } : json(method, body)),
        () => randomId(10),
      );
    } catch (e) {
      return { error: `Nothing was imported. ${importErrorMessage(e)}`, notes };
    }
    revalidatePath(base(p.id), "layout");
    return { ok: true, created: describePlan(plan), notes };
  } catch (e) {
    if (e instanceof PbError) return { error: e.message };
    throw e;
  }
}

/* ---------------- Fields ---------------- */

export async function addField(_: FormState, f: FormData): Promise<FormState> {
  const p = await actionProject(f.get("projectId"));
  const table = str(f, "table");
  const name = str(f, "name");
  const type = str(f, "type");
  if (!NAME_RE.test(name))
    return { error: "Field names use letters, numbers and underscores, starting with a letter." };
  if (!(FIELD_TYPES as readonly string[]).includes(type)) return { error: "Unknown field type." };
  if (RESERVED_FIELDS.has(name.toLowerCase())) return { error: `"${name}" is a built-in name. Choose another.` };

  const field: Field = { name, type, required: f.get("required") === "on" };
  const multiple = f.get("multiple") === "on";

  if (type === "select") {
    const raw = str(f, "values").split(",").map((v) => v.trim()).filter(Boolean);
    const values = [...new Set(raw)];
    if (!values.length) return { error: "Add at least one choice, separated by commas." };
    if (values.length !== raw.length) return { error: "Each choice must be different." };
    if (values.some((v) => v.length > 100)) return { error: "Choices can be at most 100 characters." };
    Object.assign(field, { values, maxSelect: multiple ? values.length : 1 });
  }
  if (type === "number") {
    const num = (k: string) => (str(f, k) === "" ? null : Number(str(f, k)));
    const min = num("min");
    const max = num("max");
    if ((min !== null && !Number.isFinite(min)) || (max !== null && !Number.isFinite(max)))
      return { error: "Minimum and maximum must be numbers." };
    if (min !== null && max !== null && min > max) return { error: "Minimum can't be larger than maximum." };
    Object.assign(field, { min, max, onlyInt: f.get("onlyInt") === "on" });
  }
  if (type === "file") {
    const mb = Number(str(f, "maxSizeMb") || 5);
    if (!Number.isFinite(mb) || mb < 1 || mb > 100) return { error: "Max file size must be between 1 and 100 MB." };
    Object.assign(field, { maxSelect: multiple ? 10 : 1, maxSize: Math.round(mb * 1024 * 1024) });
  }
  if (type === "relation") {
    const target = str(f, "target");
    if (!target) return { error: "Choose which table this links to." };
    Object.assign(field, { collectionId: target, maxSelect: multiple ? 999 : 1, cascadeDelete: false });
  }

  return attempt(async () => {
    const c = await getCollection(p, table);
    if (c.fields.some((x) => x.name.toLowerCase() === name.toLowerCase()))
      throw new PbError(`This table already has a field called "${name}".`, 400);
    if (c.type === "auth" && RESERVED_AUTH_FIELDS.has(name.toLowerCase()))
      throw new PbError(`"${name}" is a built-in name for user tables. Choose another.`, 400);
    if (type === "relation" && !(await listCollections(p)).some((x) => x.id === field.collectionId))
      throw new PbError("Choose one of your tables to link to.", 400);

    const unique = f.get("unique") === "on" && (UNIQUE_TYPES as readonly string[]).includes(type) && !(type === "relation" && multiple);
    const indexes = [...(c.indexes ?? [])];
    if (unique) indexes.push(uniqueIndexSql(c.name, [{ name, type }], randomId(10)));
    const saved = await pb<Collection>(
      p,
      `/api/collections/${enc(c.id)}`,
      json("PATCH", { fields: [...c.fields, field], indexes }),
    );
    // Confirm PocketBase stored what we asked for before reporting success.
    if (!saved.fields.some((x) => x.name === name && x.type === type))
      throw new PbError("The field could not be added.", 500);
    revalidatePath(base(p.id, table), "layout");
  });
}

export async function removeField(projectId: string, table: string, fieldId: string): Promise<FormState> {
  const p = await actionProject(projectId);
  return attempt(async () => {
    const c = await getCollection(p, table);
    const target = c.fields.find((x) => x.id === fieldId);
    if (!target || target.system) throw new PbError("This field can't be removed.", 400);
    // Unique rules that use this field go with it.
    const indexes = (c.indexes ?? []).filter((sql) => !uniqueRules([sql]).some((r) => r.fields.includes(target.name)));
    await pb(p, `/api/collections/${enc(c.id)}`, json("PATCH", { fields: c.fields.filter((x) => x.id !== fieldId), indexes }));
    revalidatePath(base(p.id, table), "layout");
  });
}

/* ---------------- Access rules ---------------- */

export async function saveRules(_: FormState, f: FormData): Promise<FormState> {
  const p = await actionProject(f.get("projectId"));
  const table = str(f, "table");
  return attempt(async () => {
    const c = await getCollection(p, table);
    const authIds = (await listCollections(p)).filter((x) => x.type === "auth").map((x) => x.id);
    const ownerField = findOwnerField(c.fields, authIds);
    const body: Record<string, string | null> = {};
    for (const op of ["list", "view", "create", "update", "delete"] as RuleOp[]) {
      const v = str(f, op);
      if (v === "keep") continue; // custom rule set elsewhere; leave untouched
      if (v === "owner") {
        if (!ownerField) throw new PbError("Add a “Link to table” field pointing to users (e.g. owner) first.", 400);
        body[`${op}Rule`] = ownerRule(op, ownerField);
      } else if (v in RULE_PRESETS) {
        body[`${op}Rule`] = RULE_PRESETS[v as keyof typeof RULE_PRESETS];
      } else throw new PbError("Unknown access option.", 400);
    }
    await pb(p, `/api/collections/${enc(c.id)}`, json("PATCH", body));
    revalidatePath(base(p.id, table), "layout");
  });
}

/* ---------------- Records ---------------- */

/** Build the PocketBase payload from the generic record form. */
function readRecord(f: FormData, fields: Field[], isAuth: boolean, isNew: boolean) {
  const data: Record<string, unknown> = {};
  const files: [string, File][] = [];

  for (const field of fields) {
    const k = `f_${field.name}`;
    const multi = (field.maxSelect ?? 1) > 1;
    switch (field.type) {
      case "bool":
        data[field.name] = f.get(k) === "on";
        break;
      case "number": {
        const v = str(f, k);
        if (v !== "" && !Number.isFinite(Number(v))) throw new PbError(`${field.name}: not a valid number`, 400);
        data[field.name] = v === "" ? null : Number(v);
        break;
      }
      case "date": {
        const v = str(f, k);
        data[field.name] = v ? `${v.replace("T", " ")}:00.000Z` : "";
        break;
      }
      case "json": {
        const v = str(f, k);
        try {
          data[field.name] = v ? JSON.parse(v) : null;
        } catch {
          throw new PbError(`${field.name}: not valid JSON`, 400);
        }
        break;
      }
      case "select":
      case "relation":
        data[field.name] = multi ? f.getAll(k).map(String).filter(Boolean) : str(f, k);
        break;
      case "file": {
        for (const file of f.getAll(k)) if (file instanceof File && file.size > 0) files.push([field.name, file]);
        const remove = f.getAll(`remove_${field.name}`).map(String);
        if (remove.length) data[`${field.name}-`] = remove;
        break;
      }
      default:
        data[field.name] = str(f, k);
    }
  }

  if (isAuth) {
    const password = str(f, "password");
    if (isNew && password.length < 8) throw new PbError("Password must be at least 8 characters.", 400);
    if (password) Object.assign(data, { password, passwordConfirm: password });
  }

  const body = new FormData();
  body.set("@jsonPayload", JSON.stringify(data));
  for (const [name, file] of files) body.append(name, file, file.name);
  return body;
}

export async function saveRecord(_: FormState, f: FormData): Promise<FormState> {
  const p = await actionProject(f.get("projectId"));
  const table = str(f, "table");
  const recordId = str(f, "recordId");
  const isNew = !recordId;
  if (!isNew && !RECORD_ID_RE.test(recordId)) return { error: "Record not found." };

  const res = await attempt(async () => {
    const c = await getCollection(p, table);
    const body = readRecord(f, editableFields(c), c.type === "auth", isNew);
    const path = `/api/collections/${enc(c.id)}/records${isNew ? "" : `/${enc(recordId)}`}`;
    await pb(p, path, { method: isNew ? "POST" : "PATCH", body });
  });
  if (res?.error) return res;
  revalidatePath(base(p.id, table), "layout");
  redirect(base(p.id, table));
}

export async function deleteRecord(projectId: string, table: string, recordId: string): Promise<FormState> {
  const p = await actionProject(projectId);
  if (!RECORD_ID_RE.test(recordId)) return { error: "Record not found." };
  return attempt(async () => {
    const c = await getCollection(p, table);
    await pb(p, `/api/collections/${enc(c.id)}/records/${enc(recordId)}`, { method: "DELETE" });
    revalidatePath(base(p.id, table), "layout");
  });
}

/** Relation picker search: up to 20 records of a customer table matching the query (F-05). */
export async function searchRecords(
  projectId: string,
  collectionId: string,
  q: string,
): Promise<{ value: string; label: string }[]> {
  const p = await actionProject(projectId);
  try {
    const c = await getCollection(p, collectionId);
    const term = q.trim().slice(0, 100);
    const query = new URLSearchParams({ perPage: "20" });
    if (term) {
      const lit = filterLiteral(term);
      query.set("filter", ["id", ...labelFields(c).map((f) => f.name)].map((n) => `${n}~${lit}`).join(" || "));
    }
    const res = await pb<Page<RecordItem>>(p, `/api/collections/${enc(c.id)}/records?${query}`);
    return res.items.map((r) => ({ value: r.id, label: recordLabel(c, r) }));
  } catch (e) {
    if (e instanceof PbError) return [];
    throw e;
  }
}

/* ---------------- Unique rules ---------------- */

export async function addUniqueRule(_: FormState, f: FormData): Promise<FormState> {
  const p = await actionProject(f.get("projectId"));
  const table = str(f, "table");
  const names = [...new Set(f.getAll("fields").map(String))];
  if (names.length < 1) return { error: "Choose at least one field." };
  if (names.length > 5) return { error: "Choose at most 5 fields." };
  return attempt(async () => {
    const c = await getCollection(p, table);
    const fields = names.map((n) => c.fields.find((x) => x.name === n));
    if (fields.some((x) => !x || !(UNIQUE_TYPES as readonly string[]).includes(x.type) || (x.maxSelect ?? 1) > 1))
      throw new PbError("Those fields can't be used in a unique rule.", 400);
    const existing = uniqueRules(c.indexes);
    const key = [...names].sort().join(",");
    if (existing.some((r) => [...r.fields].sort().join(",") === key)) throw new PbError("That rule already exists.", 400);
    const indexes = [...(c.indexes ?? []), uniqueIndexSql(c.name, fields as Field[], randomId(10))];
    await pb(p, `/api/collections/${enc(c.id)}`, json("PATCH", { indexes }));
    revalidatePath(base(p.id, table), "layout");
  });
}

export async function removeUniqueRule(projectId: string, table: string, indexName: string): Promise<FormState> {
  const p = await actionProject(projectId);
  return attempt(async () => {
    const c = await getCollection(p, table);
    const indexes = (c.indexes ?? []).filter((sql) => uniqueRules([sql])[0]?.name !== indexName);
    await pb(p, `/api/collections/${enc(c.id)}`, json("PATCH", { indexes }));
    revalidatePath(base(p.id, table), "layout");
  });
}
