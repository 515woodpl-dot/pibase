/**
 * Schema import: create or extend many tables (fields, links, unique rules, access) from one JSON paste.
 *
 * This is DATBase's answer to "paste a SQL script into Supabase": an AI assistant (or a person)
 * writes one JSON document for whatever product is being built, the owner pastes it into
 * Tables → Import schema, and every table is created in one go. It is dashboard-only on purpose:
 * the public API and server keys still can't change the schema (see ai-guide.ts rule 2).
 *
 * It is deliberately forgiving, because the JSON usually comes from an AI that knows Supabase/SQL
 * or PocketBase better than DATBase: common type names (varchar, integer, boolean, timestamptz,
 * enum, uuid references, jsonb, image…), key spellings ("collections", "columns", "options",
 * "references"…) and system columns (id, created_at…) are understood or skipped with a note.
 * Anything it can't safely map is rejected with a message that says exactly what to change.
 *
 * Tables that already exist are extended, never rewritten: new fields are added, existing fields
 * and data are left alone, and access is only changed for actions the import names. So an updated
 * schema can be pasted again after the app grows.
 *
 * Pure validation/planning lives here (no I/O) so it can be unit-tested; actions.ts executes it.
 * The canonical format is documented for AI assistants in ai-guide.ts (schemaImportSection).
 */
import { FIELD_TYPES, findOwnerField, ownerRule, RULE_PRESETS, UNIQUE_TYPES, uniqueIndexSql, type RuleOp, type RulePreset } from "./shared";

export const IMPORT_LIMITS = { tables: 50, fields: 100, bytes: 300_000 } as const;

const NAME_RE = /^[a-zA-Z][a-zA-Z0-9_]{0,49}$/;
/** Columns every DATBase table already has. An import that lists them is fine; they're skipped. */
const SYSTEM_COLUMNS = new Set(["id", "created", "updated", "created_at", "updated_at", "createdat", "updatedat", "inserted_at"]);
/** Names PocketBase reserves; a field can't use them. */
const RESERVED_FIELDS = new Set(["collectionid", "collectionname", "expand", "fields"]);
/** Built-in fields of the users (auth) table. */
const AUTH_FIELDS = new Set(["email", "emailvisibility", "verified", "password", "passwordconfirm", "oldpassword", "tokenkey"]);
const OPS: RuleOp[] = ["list", "view", "create", "update", "delete"];

/** Type names AIs commonly write, mapped to DATBase field types. Parenthesised sizes are stripped first. */
const TYPE_ALIASES: Record<string, { type: string; integer?: boolean; multiple?: boolean }> = {
  text: { type: "text" }, string: { type: "text" }, varchar: { type: "text" }, char: { type: "text" },
  character: { type: "text" }, "character varying": { type: "text" }, longtext: { type: "text" },
  textarea: { type: "text" }, editor: { type: "text" }, richtext: { type: "text" }, citext: { type: "text" },
  slug: { type: "text" }, phone: { type: "text" }, uuid: { type: "text" }, color: { type: "text" },
  number: { type: "number" }, float: { type: "number" }, double: { type: "number" }, "double precision": { type: "number" },
  decimal: { type: "number" }, numeric: { type: "number" }, real: { type: "number" }, money: { type: "number" },
  currency: { type: "number" }, price: { type: "number" },
  int: { type: "number", integer: true }, integer: { type: "number", integer: true }, int4: { type: "number", integer: true },
  int8: { type: "number", integer: true }, bigint: { type: "number", integer: true }, smallint: { type: "number", integer: true },
  serial: { type: "number", integer: true }, bigserial: { type: "number", integer: true },
  bool: { type: "bool" }, boolean: { type: "bool" }, checkbox: { type: "bool" },
  email: { type: "email" }, url: { type: "url" }, link: { type: "url" }, uri: { type: "url" },
  date: { type: "date" }, datetime: { type: "date" }, timestamp: { type: "date" }, timestamptz: { type: "date" },
  "timestamp with time zone": { type: "date" }, "timestamp without time zone": { type: "date" }, time: { type: "date" },
  select: { type: "select" }, enum: { type: "select" }, choice: { type: "select" }, option: { type: "select" },
  multiselect: { type: "select", multiple: true },
  json: { type: "json" }, jsonb: { type: "json" }, object: { type: "json" }, array: { type: "json" }, map: { type: "json" },
  file: { type: "file" }, files: { type: "file", multiple: true }, image: { type: "file" }, images: { type: "file", multiple: true },
  photo: { type: "file" }, attachment: { type: "file" }, upload: { type: "file" },
  relation: { type: "relation" }, reference: { type: "relation" }, ref: { type: "relation" }, foreignkey: { type: "relation" },
  foreign_key: { type: "relation" }, fk: { type: "relation" }, belongsto: { type: "relation" }, belongs_to: { type: "relation" },
};

const ACCESS_ALIASES: Record<string, RulePreset> = {
  public: "public", anyone: "public", everyone: "public", all: "public", open: "public", guest: "public", anon: "public",
  signedin: "signedIn", signed_in: "signedIn", authenticated: "signedIn", auth: "signedIn", user: "signedIn",
  users: "signedIn", loggedin: "signedIn", logged_in: "signedIn", member: "signedIn",
  verified: "verified",
  owner: "owner", own: "owner", self: "owner", ownerOnly: "owner", owner_only: "owner",
  admin: "admin", server: "admin", serveronly: "admin", server_only: "admin", private: "admin", none: "admin",
  nobody: "admin", adminonly: "admin", admin_only: "admin", staff: "admin",
};

/** Keys that describe a column but have no DATBase equivalent: ignored with a note. */
const IGNORED_FIELD_KEYS = new Set(["default", "defaultvalue", "primary", "primarykey", "primary_key", "index", "indexed",
  "check", "ondelete", "on_delete", "onupdate", "on_update", "generated", "autoincrement", "precision", "scale", "length",
  "maxlength", "max_length", "minlength", "pattern", "format"]);

export type ExistingField = { name: string; type: string; collectionId?: string; maxSelect?: number };
export type ExistingTable = { id: string; name: string; type: string; fields?: ExistingField[] };

export type PlannedField = {
  name: string;
  type: string;
  required: boolean;
  unique: boolean;
  /** PocketBase field options, except collectionId for relations (resolved at execution). */
  options: Record<string, unknown>;
  /** Relation target table name, as it will exist (relations only). */
  target?: string;
};

export type PlannedTable = {
  name: string;
  /** Set when the table already exists: it is extended, not created. */
  existingId?: string;
  isAuth: boolean;
  /** Fields to add (for an existing table: only the ones it doesn't have yet). */
  fields: PlannedField[];
  /** Access to set. For existing tables only the actions the import named. */
  access: Partial<Record<RuleOp, RulePreset>>;
};

export type ImportPlan = { tables: PlannedTable[] };
export type ImportResult = { plan: ImportPlan; notes: string[] } | { error: string };

const isObj = (v: unknown): v is Record<string, unknown> => typeof v === "object" && v !== null && !Array.isArray(v);
const lc = (s: string) => s.toLowerCase();
const str = (v: unknown) => (typeof v === "string" ? v.trim() : "");

/** Pull the list of tables out of the shapes AIs tend to produce. */
function tableList(doc: unknown): unknown[] | undefined {
  if (Array.isArray(doc)) return doc;
  if (!isObj(doc)) return undefined;
  for (const k of ["tables", "collections", "schema", "models", "entities"]) {
    const v = doc[k];
    if (Array.isArray(v)) return v;
    // { "tables": { "products": { fields… } } } → named map
    if (isObj(v)) return Object.entries(v).map(([name, t]) => (isObj(t) ? { name, ...t } : t));
  }
  return undefined;
}

/** Fields may be a list of objects, or a map of name → type string / field object. */
function fieldList(raw: unknown): unknown[] | undefined {
  if (raw === undefined || raw === null) return [];
  if (Array.isArray(raw)) return raw;
  if (isObj(raw)) return Object.entries(raw).map(([name, f]) => (typeof f === "string" ? { name, type: f } : isObj(f) ? { name, ...f } : f));
  return undefined;
}

/**
 * Validate the pasted JSON against the project's current tables.
 * All-or-nothing: any problem returns an error and nothing is changed.
 */
export function planImport(text: string, existing: ExistingTable[]): ImportResult {
  if (!text.trim()) return { error: "Paste the schema JSON first." };
  if (text.length > IMPORT_LIMITS.bytes) return { error: "That schema is too large (300 KB max). Split it into two imports." };

  let doc: unknown;
  try {
    // AIs often wrap JSON in a ```json fence; accept that.
    doc = JSON.parse(text.trim().replace(/^```(?:json)?\s*/i, "").replace(/\s*```$/, ""));
  } catch (e) {
    return { error: `Not valid JSON: ${(e as Error).message}` };
  }
  const list = tableList(doc);
  if (!list || list.length === 0) return { error: 'Expected { "tables": [ ... ] } with at least one table.' };
  if (list.length > IMPORT_LIMITS.tables) return { error: `At most ${IMPORT_LIMITS.tables} tables per import.` };

  const notes: string[] = [];
  const existingByName = new Map(existing.map((t) => [lc(t.name), t]));
  const authIds = existing.filter((t) => t.type === "auth").map((t) => t.id);
  const names = new Map<string, string>(); // lower → name as written

  // First pass: table names, so links can point forward to tables later in the list.
  for (const [i, t] of list.entries()) {
    if (!isObj(t)) return { error: `Table #${i + 1} must be an object.` };
    const name = str(t.name) || str(t.table) || str(t.collection);
    if (!NAME_RE.test(name))
      return { error: `Table #${i + 1} (${JSON.stringify(name || null)}): table names use letters, numbers and underscores, start with a letter, max 50 characters.` };
    if (name.startsWith("_")) return { error: `Table "${name}": names starting with "_" are reserved.` };
    if (names.has(lc(name))) return { error: `Table "${name}" appears twice in the import.` };
    const ex = existingByName.get(lc(name));
    if (ex && ex.type === "view") return { error: `Table "${name}" is a view and can't be changed by an import.` };
    names.set(lc(name), ex?.name ?? name);
  }
  /** The real name of a table a link points to (import or project), or undefined. */
  const resolve = (t: string) => names.get(lc(t)) ?? existingByName.get(lc(t))?.name;

  const tables: PlannedTable[] = [];
  for (const raw of list as Record<string, unknown>[]) {
    const name = names.get(lc(str(raw.name) || str(raw.table) || str(raw.collection)))!;
    const ex = existingByName.get(lc(name));
    const isAuth = ex?.type === "auth";
    const where = `Table "${name}"`;

    const rawFields = fieldList(raw.fields ?? raw.columns ?? raw.schema ?? raw.properties);
    if (!rawFields) return { error: `${where}: "fields" must be a list of fields.` };
    if (rawFields.length > IMPORT_LIMITS.fields) return { error: `${where}: at most ${IMPORT_LIMITS.fields} fields.` };

    const have = new Map((ex?.fields ?? []).map((f) => [lc(f.name), f]));
    const fields: PlannedField[] = [];
    const seen = new Set<string>();
    for (const [j, rf] of rawFields.entries()) {
      const r = checkField(rf, `${where}, field #${j + 1}`, name, resolve, notes);
      if ("error" in r) return r;
      if (!r.field) continue; // skipped system column
      const f = r.field;
      const key = lc(f.name);
      if (seen.has(key)) return { error: `${where}: field "${f.name}" appears twice.` };
      seen.add(key);
      if (isAuth && AUTH_FIELDS.has(key)) {
        notes.push(`${name}.${f.name}: built into the users table already, skipped.`);
        continue;
      }
      const cur = have.get(key);
      if (cur) {
        if (cur.type !== f.type)
          notes.push(`${name}.${f.name}: already exists as ${cur.type} (import says ${f.type}). Left unchanged; change it in the table's settings if needed.`);
        continue;
      }
      fields.push(f);
    }

    const access = readAccess(raw.access ?? raw.rules ?? raw.permissions, where);
    if ("error" in access) return access;
    let chosen = access.access;
    if (!ex) {
      for (const op of OPS) chosen[op] ??= "admin"; // new tables: unnamed actions are server-only
    } else if (isAuth && Object.keys(chosen).length) {
      notes.push(`${name}: access of the users table is managed by DATBase and was not changed.`);
      chosen = {};
    }

    if (Object.values(chosen).includes("owner")) {
      const combined = [
        ...(ex?.fields ?? []),
        ...fields
          .filter((f) => f.type === "relation")
          .map((f) => ({
            name: f.name,
            type: "relation",
            maxSelect: f.options.maxSelect as number,
            collectionId: existingByName.get(lc(f.target!))?.id ?? `new:${f.target}`,
          })),
      ];
      if (!findOwnerField(combined, authIds))
        return { error: `${where}: "owner" access needs a single link to the users table, e.g. { "name": "owner", "type": "relation", "table": "users" }.` };
    }

    if (ex && !fields.length && !Object.keys(chosen).length) {
      notes.push(`${name}: already up to date, nothing to add.`);
      continue;
    }
    tables.push({ name, existingId: ex?.id, isAuth, fields, access: chosen });
  }

  if (!tables.length) return { error: "Nothing to import: every table and field already exists." };
  return { plan: { tables }, notes };
}

function readAccess(raw: unknown, where: string): { access: Partial<Record<RuleOp, RulePreset>> } | { error: string } {
  const access: Partial<Record<RuleOp, RulePreset>> = {};
  if (raw === undefined || raw === null) return { access };
  const pick = (v: unknown, label: string): RulePreset | { error: string } => {
    const p = ACCESS_ALIASES[str(v)] ?? ACCESS_ALIASES[lc(str(v)).replace(/[\s-]/g, "")];
    return p ?? { error: `${where}: access "${label}" is ${JSON.stringify(v)}; use public, signedIn, verified, owner or admin.` };
  };
  if (typeof raw === "string") {
    // One word for everything, e.g. "access": "owner"
    const p = pick(raw, "all");
    if (typeof p !== "string") return p;
    for (const op of OPS) access[op] = p;
    return { access };
  }
  if (!isObj(raw)) return { error: `${where}: "access" must be an object like { "list": "public", "create": "admin" }.` };
  const groups: Record<string, RuleOp[]> = {
    list: ["list"], view: ["view"], create: ["create"], update: ["update"], delete: ["delete"],
    read: ["list", "view"], select: ["list", "view"], get: ["view"], insert: ["create"], write: ["create", "update", "delete"],
    edit: ["update"], remove: ["delete"], all: OPS,
  };
  // Groups first, then single actions, so { "read": "public", "view": "owner" } does what it says.
  const entries = Object.entries(raw).sort(([a], [b]) => (groups[lc(b)]?.length ?? 0) - (groups[lc(a)]?.length ?? 0));
  for (const [k, v] of entries) {
    const ops = groups[lc(k)];
    if (!ops) return { error: `${where}: unknown access action "${k}" (use list, view, create, update, delete, read or write).` };
    const p = pick(v, k);
    if (typeof p !== "string") return p;
    for (const op of ops) access[op] = p;
  }
  return { access };
}

function checkField(
  rf: unknown,
  where: string,
  table: string,
  resolve: (table: string) => string | undefined,
  notes: string[],
): { field: PlannedField | null } | { error: string } {
  if (!isObj(rf)) return { error: `${where} must be an object like { "name": "title", "type": "text" }.` };

  const name = str(rf.name) || str(rf.field) || str(rf.column);
  if (!name) return { error: `${where}: every field needs a "name".` };
  if (SYSTEM_COLUMNS.has(lc(name))) return { field: null }; // DATBase adds id/created/updated itself
  if (!NAME_RE.test(name))
    return { error: `${where} ("${name}"): field names use letters, numbers and underscores, start with a letter, max 50 characters.` };
  const at = `${where} ("${name}")`;
  if (RESERVED_FIELDS.has(lc(name))) return { error: `${at}: "${name}" is a built-in name. Choose another.` };

  // Type: accept aliases, SQL sizes ("varchar(255)"), array suffixes ("text[]") and FK hints.
  let rawType = lc(str(rf.type) || str(rf.kind) || str(rf.dataType) || str(rf.data_type));
  const isArray = rawType.endsWith("[]");
  rawType = rawType.replace(/\[\]$/, "").replace(/\(.*\)$/, "").trim();
  const targetRaw = str(rf.table) || str(rf.to) || str(rf.target) || str(rf.collection) || str(rf.references) ||
    str(rf.ref) || str(rf.relation) || str(rf.foreignKey) || str(rf.foreign_key) || (isObj(rf.references) ? str(rf.references.table) : "");
  let alias = TYPE_ALIASES[rawType];
  if (!alias && targetRaw) alias = TYPE_ALIASES.relation; // e.g. { "type": "uuid", "references": "users(id)" }
  if (alias && targetRaw && alias.type === "text" && rawType === "uuid") alias = TYPE_ALIASES.relation;
  if (!alias) {
    if (!rawType) return { error: `${at}: needs a "type" (${FIELD_TYPES.join(", ")}).` };
    return { error: `${at}: unknown type "${rawType}". Use one of ${FIELD_TYPES.join(", ")}.` };
  }
  let type = alias.type;
  if (isArray && type !== "select" && type !== "file" && type !== "relation") {
    notes.push(`${table}.${name}: list of ${rawType} stored as json.`);
    type = "json";
  }
  if (!(FIELD_TYPES as readonly string[]).includes(type)) return { error: `${at}: unsupported type "${type}".` };

  // Other keys (description, label…) are documentation and ignored silently.
  for (const k of Object.keys(rf))
    if (IGNORED_FIELD_KEYS.has(lc(k))) notes.push(`${table}.${name}: "${k}" isn't supported and was ignored.`);

  const bool = (v: unknown) => v === true || v === "true";
  const required = bool(rf.required) || rf.nullable === false || bool(rf.notNull) || bool(rf.not_null);
  const multiple = bool(rf.multiple) || bool(rf.many) || bool(rf.array) || !!alias.multiple || (isArray && ["select", "file", "relation"].includes(type)) ||
    ["many", "hasmany", "has_many", "many-to-many"].includes(lc(str(rf.cardinality) || str(rf.relationType)));
  const options: Record<string, unknown> = {};
  let target: string | undefined;

  if (type === "select") {
    const vals = rf.values ?? rf.options ?? rf.choices ?? rf.enum;
    if (!Array.isArray(vals) || vals.some((v) => typeof v !== "string" && typeof v !== "number"))
      return { error: `${at}: choice fields need "values": a list of choices, e.g. ["small", "medium", "large"].` };
    const list = vals.map((v) => String(v).trim()).filter(Boolean);
    const values = [...new Set(list)];
    if (!values.length) return { error: `${at}: add at least one choice in "values".` };
    if (values.length !== list.length) return { error: `${at}: each choice must be different.` };
    if (values.some((v) => v.length > 100)) return { error: `${at}: choices can be at most 100 characters.` };
    Object.assign(options, { values, maxSelect: multiple ? values.length : 1 });
  }
  if (type === "number") {
    const num = (v: unknown) => (v === undefined || v === null ? null : typeof v === "number" && Number.isFinite(v) ? v : NaN);
    const min = num(rf.min ?? rf.minimum);
    const max = num(rf.max ?? rf.maximum);
    if (Number.isNaN(min) || Number.isNaN(max)) return { error: `${at}: "min" and "max" must be numbers.` };
    if (min !== null && max !== null && min > max) return { error: `${at}: "min" can't be larger than "max".` };
    const integer = bool(rf.integer) || bool(rf.onlyInt) || !!alias.integer;
    Object.assign(options, { min, max, onlyInt: integer });
  }
  if (type === "file") {
    const mb = rf.maxSizeMb ?? rf.maxSizeMB ?? 5;
    if (typeof mb !== "number" || !Number.isFinite(mb) || mb < 1 || mb > 100)
      return { error: `${at}: "maxSizeMb" must be between 1 and 100.` };
    Object.assign(options, { maxSelect: multiple ? 10 : 1, maxSize: Math.round(mb * 1024 * 1024) });
  }
  if (type === "relation") {
    // "users(id)", "users.id", "public.users" → users
    const t = targetRaw.replace(/^public\./i, "").replace(/[.(].*$/, "").trim();
    if (!t) return { error: `${at}: link fields need "table": the table it links to.` };
    const real = t === "auth" || lc(t) === "auth.users" ? resolve("users") : resolve(t);
    if (!real) return { error: `${at}: links to "${t}", which isn't in this import or the project. Add that table to the import.` };
    target = real;
    Object.assign(options, { maxSelect: multiple ? 999 : 1, cascadeDelete: bool(rf.cascadeDelete) || lc(str(rf.onDelete ?? rf.on_delete)) === "cascade" });
  }

  let unique = bool(rf.unique);
  if (unique && (!(UNIQUE_TYPES as readonly string[]).includes(type) || (type === "relation" && multiple))) {
    notes.push(`${table}.${name}: "unique" only works on text, email, url, number and single links; ignored.`);
    unique = false;
  }

  return { field: { name, type, required, unique, options, target } };
}

/** The PocketBase rule string for one access choice. */
export function ruleFor(op: RuleOp, preset: RulePreset, ownerField: string | null): string | null {
  if (preset === "owner") return ownerRule(op, ownerField!);
  return RULE_PRESETS[preset];
}

/** A short, human summary of the plan, shown after a successful import. */
export function describePlan(plan: ImportPlan) {
  return plan.tables.map((t) => {
    const n = `${t.fields.length} field${t.fields.length === 1 ? "" : "s"}`;
    return t.existingId ? `${t.name}: added ${n}${Object.keys(t.access).length ? ", updated access" : ""}` : `${t.name}: created with ${n}`;
  });
}

/* ---------------- Execution ---------------- */

type PbField = { name: string; type: string; [k: string]: unknown };
type PbCollection = {
  id: string;
  name: string;
  type: string;
  fields: PbField[];
  indexes?: string[];
  listRule: string | null;
  viewRule: string | null;
  createRule: string | null;
  updateRule: string | null;
  deleteRule: string | null;
};
/** Calls the project's PocketBase as superuser. Throws on errors. */
export type PbCall = <T>(method: "GET" | "POST" | "PATCH" | "DELETE", path: string, body?: unknown) => Promise<T>;

const AUTODATES: PbField[] = [
  { name: "created", type: "autodate", onCreate: true, onUpdate: false },
  { name: "updated", type: "autodate", onCreate: true, onUpdate: true },
];
const toPbField = (x: PlannedField): PbField => ({ name: x.name, type: x.type, required: x.required, ...x.options });
const enc = encodeURIComponent;

/**
 * Apply a plan. Two passes so links can point at tables created in the same import. If anything fails,
 * tables this import created are deleted and tables it extended are restored, then the error is rethrown,
 * so a failed import leaves the project exactly as it was.
 */
export async function runImport(plan: ImportPlan, existing: ExistingTable[], call: PbCall, newId: () => string) {
  const created: { id: string; name: string }[] = [];
  const snapshots: PbCollection[] = [];
  try {
    // Pass 1: new tables with their plain fields. Access stays "server only" until pass 2.
    for (const t of plan.tables) {
      if (t.existingId) continue;
      const c = await call<PbCollection>("POST", "/api/collections", {
        name: t.name,
        type: "base",
        fields: [...t.fields.filter((x) => x.type !== "relation").map(toPbField), ...AUTODATES],
      });
      created.push({ id: c.id, name: c.name });
    }

    // Pass 2: links (every target now exists), fields for existing tables, unique rules and access.
    const ids = new Map([...existing, ...created].map((c) => [lc(c.name), c.id]));
    const authIds = existing.filter((c) => c.type === "auth").map((c) => c.id);
    for (const t of plan.tables) {
      const c = await call<PbCollection>("GET", `/api/collections/${enc(t.existingId ?? ids.get(lc(t.name))!)}`);
      const link = (x: PlannedField): PbField =>
        x.type === "relation" ? { ...toPbField(x), collectionId: ids.get(lc(x.target!)) } : toPbField(x);

      let fields: PbField[];
      if (t.existingId) {
        snapshots.push(c);
        fields = [...c.fields, ...t.fields.map(link)];
      } else {
        // Keep the order the import listed: id, the fields, then created/updated.
        const byName = new Map(c.fields.map((x) => [x.name, x]));
        fields = [
          ...c.fields.filter((x) => x.name === "id"),
          ...t.fields.map((x) => (x.type === "relation" ? link(x) : byName.get(x.name)!)),
          ...c.fields.filter((x) => x.name === "created" || x.name === "updated"),
        ];
      }
      const indexes = [...(c.indexes ?? [])];
      for (const x of t.fields) if (x.unique) indexes.push(uniqueIndexSql(c.name, [x], newId()));
      const owner = findOwnerField(fields, authIds);
      const rules: Record<string, string | null> = {};
      for (const op of OPS) {
        const preset = t.access[op];
        if (preset) rules[`${op}Rule`] = ruleFor(op, preset, owner);
      }

      const saved = await call<PbCollection>("PATCH", `/api/collections/${enc(c.id)}`, { fields, indexes, ...rules });
      const missing = t.fields.find((x) => !saved.fields.some((s) => s.name === x.name && s.type === x.type));
      if (missing) throw new Error(`${t.name}: field "${missing.name}" could not be added.`);
    }
  } catch (e) {
    await rollback(call, created, snapshots);
    throw e;
  }
}

/** Undo a failed import: restore extended tables, then delete created ones (retrying, since links can block a delete). */
async function rollback(call: PbCall, created: { id: string }[], snapshots: PbCollection[]) {
  for (const c of snapshots) {
    const { fields, indexes, listRule, viewRule, createRule, updateRule, deleteRule } = c;
    await call("PATCH", `/api/collections/${enc(c.id)}`, { fields, indexes, listRule, viewRule, createRule, updateRule, deleteRule }).catch(() => {});
  }
  let left = [...created].reverse();
  while (left.length) {
    const failed: typeof left = [];
    for (const c of left) await call("DELETE", `/api/collections/${enc(c.id)}`).catch(() => failed.push(c));
    if (failed.length === left.length) break;
    left = failed;
  }
}
