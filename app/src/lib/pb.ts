import "server-only";
import type { Project } from "@/db/schema";
import { pbInternalUrl } from "./config";
import { controlSecret, decrypt } from "./crypto";

/* ---------- Types (subset of the PocketBase API we use) ---------- */

export type Field = {
  id?: string;
  name: string;
  type: string;
  required?: boolean;
  system?: boolean;
  hidden?: boolean;
  maxSelect?: number;
  collectionId?: string;
  onCreate?: boolean;
  onUpdate?: boolean;
  [k: string]: unknown;
};

export type Collection = {
  id: string;
  name: string;
  type: "base" | "auth" | "view";
  system: boolean;
  fields: Field[];
  listRule: string | null;
  viewRule: string | null;
  createRule: string | null;
  updateRule: string | null;
  deleteRule: string | null;
  indexes?: string[];
};

export type RecordItem = { id: string; collectionId: string; [k: string]: unknown };
export type Page<T> = { page: number; perPage: number; totalItems: number; totalPages: number; items: T[] };

export class PbError extends Error {
  constructor(message: string, public status: number) {
    super(message);
  }
}

/* ---------- Superuser session per project ---------- */

/** Marks a request as DATBase's own: not counted, not limited, allowed on /api/datbase/*. */
export const controlHeaders = (slug: string) => ({ "X-Datbase-Control": controlSecret(slug) });

const tokens = new Map<string, { token: string; exp: number }>();

async function login(p: Project) {
  if (!p.adminEmail || !p.adminPasswordEnc) throw new PbError("Project is not ready yet", 409);
  const r = await fetch(`${pbInternalUrl(p.slug)}/api/collections/_superusers/auth-with-password`, {
    method: "POST",
    headers: { "content-type": "application/json", ...controlHeaders(p.slug) },
    body: JSON.stringify({ identity: p.adminEmail, password: decrypt(p.adminPasswordEnc) }),
    signal: AbortSignal.timeout(5000),
  });
  if (!r.ok) throw new PbError("Could not sign in to the project backend", r.status);
  const { token } = (await r.json()) as { token: string };
  tokens.set(p.id, { token, exp: Date.now() + 30 * 60_000 });
  return token;
}

/** Drop the cached superuser session (e.g. after a restore replaced the project's data). */
export const forgetToken = (projectId: string) => tokens.delete(projectId);

async function token(p: Project, fresh = false) {
  const t = tokens.get(p.id);
  return !fresh && t && t.exp > Date.now() ? t.token : login(p);
}

/** Turn PocketBase's error body into one readable sentence. */
function errorMessage(body: unknown, status: number) {
  const b = body as { message?: string; data?: Record<string, { message?: string }> } | null;
  const [field, detail] = Object.entries(b?.data ?? {})[0] ?? [];
  if (field && detail?.message) return `${field}: ${detail.message}`;
  return b?.message || `Request failed (${status})`;
}

/**
 * Call a project's PocketBase as its superuser.
 * Callers must pass a project already checked for ownership. The path is always
 * built server-side; never forward a raw path from the browser.
 */
export async function pb<T = unknown>(p: Project, path: string, init: RequestInit = {}): Promise<T> {
  if (p.status !== "active") throw new PbError(`Project is ${p.status}`, 409);
  const send = async (fresh: boolean) =>
    fetch(`${pbInternalUrl(p.slug)}${path}`, {
      ...init,
      headers: { ...(init.headers ?? {}), ...controlHeaders(p.slug), Authorization: await token(p, fresh) },
      signal: AbortSignal.timeout(15000),
      cache: "no-store",
    });

  let r = await send(false).catch(() => {
    throw new PbError("Project backend is not reachable", 503);
  });
  if (r.status === 401) r = await send(true);
  if (r.status === 204) return undefined as T;
  const body = await r.json().catch(() => null);
  if (!r.ok) throw new PbError(errorMessage(body, r.status), r.status);
  return body as T;
}

export const json = (method: string, data: unknown): RequestInit => ({
  method,
  headers: { "content-type": "application/json" },
  body: JSON.stringify(data),
});

/** Short-lived token that lets the superuser read protected files. */
export async function fileToken(p: Project) {
  const { token } = await pb<{ token: string }>(p, "/api/files/token", { method: "POST" });
  return token;
}

export const fileUrl = (p: Project, collectionId: string, recordId: string, name: string) =>
  `${pbInternalUrl(p.slug)}/api/files/${encodeURIComponent(collectionId)}/${encodeURIComponent(recordId)}/${encodeURIComponent(name)}`;

/* ---------- Helpers shared by pages ---------- */

export const enc = encodeURIComponent;

/** Customer-visible collections: hide PocketBase's internal ones (_superusers, _otps, …). */
export async function listCollections(p: Project) {
  const res = await pb<Page<Collection>>(p, "/api/collections?perPage=200&sort=name");
  return res.items.filter((c) => !isInternal(c));
}

/** PocketBase's own internal tables (_superusers, _otps, …): never exposed to customers (F-03). */
export const isInternal = (c: Pick<Collection, "name" | "system">) => c.system || c.name.startsWith("_");

/**
 * Load a customer table by name or id. Internal tables are reported as "not found".
 * Every page and action that touches a table must go through this.
 */
export async function getCollection(p: Project, name: string) {
  if (!name || name.startsWith("_")) throw new PbError("Table not found", 404);
  const c = await pb<Collection>(p, `/api/collections/${enc(name)}`);
  if (isInternal(c)) throw new PbError("Table not found", 404);
  return c;
}

/** Text-like fields used to search and label records (e.g. in relation pickers). */
export function labelFields(c: Collection) {
  return c.fields.filter((f) => (f.type === "text" || f.type === "email") && !f.hidden && f.name !== "id").slice(0, 3);
}

/** A readable label for a record: its first non-empty text/email field, else its id. */
export function recordLabel(c: Collection, r: RecordItem) {
  for (const f of labelFields(c)) if (typeof r[f.name] === "string" && r[f.name]) return `${r[f.name]} · ${r.id}`;
  return r.id;
}

/** Escape a user-typed value for use inside a PocketBase filter string literal. */
export const filterLiteral = (v: string) => `"${v.replace(/\\/g, "\\\\").replace(/"/g, '\\"')}"`;

/** Fields shown/edited in the data browser (skip system internals like password, tokenKey). */
export function editableFields(c: Collection) {
  return c.fields.filter(
    (f) => !f.hidden && f.type !== "password" && f.type !== "autodate" && f.name !== "id" && f.name !== "tokenKey",
  );
}
