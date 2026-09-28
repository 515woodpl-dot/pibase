export const FIELD_TYPES = ["text", "number", "bool", "email", "url", "date", "select", "json", "file", "relation"] as const;

export const FIELD_LABELS: Record<string, string> = {
  text: "Text",
  number: "Number",
  bool: "Yes / No",
  email: "Email",
  url: "URL",
  date: "Date & time",
  select: "Choice",
  json: "JSON",
  file: "File",
  relation: "Link to table",
  autodate: "Auto date",
  editor: "Rich text",
  password: "Password",
  geoPoint: "Location",
};

export const RULE_PRESETS = {
  admin: null, // only server/admin credentials
  public: "", // anyone, no sign-in
  signedIn: "@request.auth.id != ''",
  verified: "@request.auth.id != '' && @request.auth.verified = true",
} as const;

export type RuleOp = "list" | "view" | "create" | "update" | "delete";
export type RulePreset = keyof typeof RULE_PRESETS | "owner";

export const RULE_LABELS: Record<RulePreset, string> = {
  public: "Anyone",
  signedIn: "Any signed-in user",
  verified: "Signed-in users with a confirmed email",
  owner: "Only its owner",
  admin: "Server only (admin)",
};

/**
 * "Only its owner": the record's owner field (a link to the users table) must be the signed-in user.
 * Create requires setting yourself as owner; update can't hand the record to someone else.
 */
export function ownerRule(op: RuleOp, field: string) {
  const me = "@request.auth.id != ''";
  switch (op) {
    case "create":
      return `${me} && @request.body.${field} = @request.auth.id`;
    case "update":
      return `${me} && ${field} = @request.auth.id && (@request.body.${field}:isset = false || @request.body.${field} = @request.auth.id)`;
    default:
      return `${me} && ${field} = @request.auth.id`;
  }
}

/** Map a stored rule back to a preset, or "custom" if it was hand-written. */
export function presetOf(rule: string | null, op: RuleOp, ownerField: string | null): RulePreset | "custom" {
  for (const [k, v] of Object.entries(RULE_PRESETS)) if (v === rule) return k as RulePreset;
  if (ownerField && rule === ownerRule(op, ownerField)) return "owner";
  return "custom";
}

type FieldLike = { name: string; type: string; collectionId?: string; maxSelect?: number };

/** The field that marks a record's owner: a single link to a users (auth) table. Prefers common names. */
export function findOwnerField(fields: FieldLike[], authTableIds: string[]) {
  const candidates = fields.filter(
    (f) => f.type === "relation" && (f.maxSelect ?? 1) <= 1 && authTableIds.includes(f.collectionId ?? ""),
  );
  const preferred = ["owner", "user", "author", "createdBy", "created_by"];
  return candidates.find((f) => preferred.includes(f.name))?.name ?? candidates[0]?.name ?? null;
}

/* ---------- Unique rules (unique indexes) ---------- */

/** Field types that can be made unique. */
export const UNIQUE_TYPES = ["text", "email", "url", "number", "relation"] as const;
const TEXT_LIKE = ["text", "email", "url", "relation"];

export type UniqueRule = { name: string; fields: string[] };

/** Read unique rules from a table's index definitions (others are ignored). */
export function uniqueRules(indexes: string[] = []): UniqueRule[] {
  const out: UniqueRule[] = [];
  for (const sql of indexes) {
    const m = sql.match(/^\s*CREATE\s+UNIQUE\s+INDEX\s+(?:IF\s+NOT\s+EXISTS\s+)?[`"]?(\w+)[`"]?\s+ON\s+[`"]?\w+[`"]?\s*\(([^)]*)\)/i);
    if (!m) continue;
    const fields = m[2].split(",").map((c) => c.trim().replace(/[`"]/g, "").split(/\s+/)[0]).filter(Boolean);
    out.push({ name: m[1], fields });
  }
  return out;
}

/**
 * SQL for a unique rule. A single text-like field ignores empty values (many records may leave it blank).
 */
export function uniqueIndexSql(table: string, fields: { name: string; type: string }[], id: string) {
  const cols = fields.map((f) => "`" + f.name + "`").join(", ");
  const where = fields.length === 1 && TEXT_LIKE.includes(fields[0].type) ? ` WHERE \`${fields[0].name}\` != ''` : "";
  return `CREATE UNIQUE INDEX \`idx_u_${id}\` ON \`${table}\` (${cols})${where}`;
}
