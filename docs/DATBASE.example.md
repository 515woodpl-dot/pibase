# DATBase backend: Shop

> Generated 2026-09-27 19:28 UTC from the live project. **If tables, fields or access change in the DATBase dashboard, download this file again.**

This file is written for AI coding assistants and for developers. It is the complete, authoritative description of this app's backend: database tables, user accounts, file storage and the HTTP API. If you are an AI assistant, **read the whole file before writing code** and follow the rules below.

## Rules for AI assistants

1. **Use only what this file documents.** Table names, field names, types and access rules below are exact. Don't invent tables, fields or endpoints.
2. **You cannot change the schema or access rules.** If the app needs a new table, field or different access, stop and tell the human exactly what to add in the DATBase dashboard (Tables → the table → Settings). Then continue once they confirm, using a freshly downloaded DATBASE.md.
3. **Never put a server key in browser, mobile or public code.** Server keys ignore all access rules and are for the app owner's servers only (see *Server-side access*). App users sign in with their own email and password (see *Users & sign-in*).
4. **Respect the access table of each table.** If an action is "Server only", the website can't do it directly. If it says "Any signed-in user", send the user's token.
5. **Use the `datbase.ts` client at the end of this file.** Copy it into the project (for example `src/lib/datbase.ts`). It has no dependencies and works in browsers, Node 18+, Next.js, Vite, React Native and Deno.
6. **Build filters with the `q()` helper** for any user-typed value, so it's quoted safely.
7. **Show API error messages to the user.** They are human-readable (see *Errors*).

## Connection

- **API base URL:** `https://8090.example.test`
- **Format:** JSON over HTTPS. File uploads use `multipart/form-data`.
- **CORS:** requests are allowed from any website origin.
- **Auth header (when signed in):** `Authorization: Bearer <token>`.
- **No API key is needed** for actions allowed to *Anyone*.

## Tables

This project has 2 data tables plus the users table. Every record also has `id`, `collectionId` and `collectionName`.

### `products`

| Action | Who is allowed |
|---|---|
| List / search | Anyone, no sign-in |
| View one | Anyone, no sign-in |
| Create | 🔒 Server only (never from a browser or app) |
| Edit | 🔒 Server only (never from a browser or app) |
| Delete | 🔒 Server only (never from a browser or app) |

| Field | Type | Required | Notes |
|---|---|---|---|
| `id` | text (15 chars) | auto | Set by DATBase. Never send it when creating. |
| `created` | date-time, set automatically (read-only) | no | Read-only |
| `updated` | date-time, set automatically (read-only) | no | Read-only |
| `name` | text | yes |  |

```ts
export interface Product {
  id: string;
  collectionId: string;
  collectionName: string;
  created: string;
  updated: string;
  name: string;
}
```

Examples (with the `datbase.ts` client below):

```ts
import { list, getOne, create, update, remove } from "./datbase";

const page = await list<Product>("products", { perPage: 20, sort: "-created" });
const one = await getOne<Product>("products", page.items[0].id);
const created = await create<Product>("products", {"name":"example name"});
await update<Product>("products", created.id, { name: "example name" });
await remove("products", created.id);
```

<details><summary>Example record as returned by the API</summary>

```json
{
  "id": "RECORD_ID",
  "collectionId": "TABLE_ID",
  "collectionName": "products",
  "created": "2026-09-27 14:30:00.000Z",
  "updated": "2026-09-27 14:30:00.000Z",
  "name": "example name"
}
```
</details>

### `reviews`

| Action | Who is allowed |
|---|---|
| List / search | Only the record's owner (`owner` = signed-in user) |
| View one | Only the record's owner (`owner` = signed-in user) |
| Create | Signed-in users, and `owner` must be their own user id |
| Edit | Only the record's owner (`owner` = signed-in user) |
| Delete | Only the record's owner (`owner` = signed-in user) |

| Field | Type | Required | Notes |
|---|---|---|---|
| `id` | text (15 chars) | auto | Set by DATBase. Never send it when creating. |
| `created` | date-time, set automatically (read-only) | no | Read-only |
| `updated` | date-time, set automatically (read-only) | no | Read-only |
| `owner` | id of a record in `users` | no | **Owner.** On create, set it to the signed-in user's id |
| `product` | id of a record in `products` | no |  |
| `stars` | whole number from 1 to 5 | yes |  |
| `code` | text | no | **Unique**: no two records may have the same value |

**Unique together:** `owner` + `product`. Only one record may have each combination; creating or editing into a duplicate fails with `400` and `validation_not_unique` on those fields. Enforced by the database, even for simultaneous requests.

```ts
export interface Review {
  id: string;
  collectionId: string;
  collectionName: string;
  created: string;
  updated: string;
  owner: string;
  product: string;
  /** whole number from 1 to 5 */
  stars: number;
  code: string;
  /** Linked records, present when requested with `expand` */
  expand?: { owner?: User; product?: Product };
}
```

Examples (with the `datbase.ts` client below):

```ts
import { list, getOne, create, update, remove, currentUser } from "./datbase";

const page = await list<Review>("reviews", { perPage: 20, sort: "-created", expand: "owner" });
const one = await getOne<Review>("reviews", page.items[0].id);
const created = await create<Review>("reviews", { ...{"product":"RECORD_ID","stars":1,"code":"example code"}, owner: currentUser()!.id });
await update<Review>("reviews", created.id, { stars: 1 });
await remove("reviews", created.id);
```

<details><summary>Example record as returned by the API</summary>

```json
{
  "id": "RECORD_ID",
  "collectionId": "TABLE_ID",
  "collectionName": "reviews",
  "created": "2026-09-27 14:30:00.000Z",
  "updated": "2026-09-27 14:30:00.000Z",
  "owner": "RECORD_ID",
  "product": "RECORD_ID",
  "stars": 1,
  "code": "example code"
}
```
</details>

### `users` (your app's users)

| Action | Who is allowed |
|---|---|
| List / search | Only the signed-in user, for their own account |
| View one | Only the signed-in user, for their own account |
| Create | Anyone can sign up |
| Edit | Only the signed-in user, for their own account |
| Delete | Only the signed-in user, for their own account |

| Field | Type | Required | Notes |
|---|---|---|---|
| `id` | text (15 chars) | auto | Set by DATBase. Never send it when creating. |
| `email` | email address | yes | Unique. Hidden from other users unless `emailVisibility` is true |
| `emailVisibility` | true / false | no |  |
| `verified` | true / false | no | Read-only for app users |
| `name` | text | no |  |
| `avatar` | 1 file (stored value = file name) | no |  |
| `created` | date-time, set automatically (read-only) | no | Read-only |
| `updated` | date-time, set automatically (read-only) | no | Read-only |
| `password` | text, min 8 characters | on create | Write-only. Never returned |

```ts
export interface User {
  id: string;
  collectionId: string;
  collectionName: string;
  email: string;
  emailVisibility: boolean;
  verified: boolean;
  name: string;
  avatar: string;
  created: string;
  updated: string;
}
```

<details><summary>Example record as returned by the API</summary>

```json
{
  "id": "RECORD_ID",
  "collectionId": "TABLE_ID",
  "collectionName": "users",
  "email": "someone@example.com",
  "emailVisibility": true,
  "verified": true,
  "name": "example name",
  "avatar": "photo_x7k2p.jpg",
  "created": "2026-09-27 14:30:00.000Z",
  "updated": "2026-09-27 14:30:00.000Z"
}
```
</details>

## What "not allowed" looks like

Access rules don't always produce an error. Know these cases, or you'll chase phantom bugs:

| Situation | Response |
|---|---|
| List a table the user may not see (signed-out, or not the owner) | **`200` with only the allowed records, which may be an empty `items: []`**. No error. |
| View / edit / delete a record the user may not access | `404` *"The requested resource wasn't found."* (same as a missing record) |
| Create where the rule isn't met (e.g. not signed in, or `owner` isn't yourself) | `400` *"Failed to create record."* with empty `data` |
| Any action marked *Server only* | `403` *"Only superusers can perform this action."* |
| No token sent | Treated as **signed out**: lists get emptier, protected creates fail. |
| Expired, invalid or revoked token (user session or server key) | `401` *"The token is invalid, expired or revoked…"* on **every** call. Never an empty result. The client signs the user out automatically; show the sign-in screen. |

So: an empty list on a signed-in-only table usually means **the token wasn't sent**. A 404 right after creating usually means **the user isn't allowed to view it**.

## Users & sign-in

App users live in the `users` table. They're separate from the DATBase dashboard account.

| Task | HTTP | Client |
|---|---|---|
| Sign up | `POST /api/collections/users/records` with `{email, password, passwordConfirm, ...fields}` | `signUp({ email, password, name })` (also signs in) |
| Sign in | `POST /api/collections/users/auth-with-password` with `{identity: email, password}` → `{token, record}` | `signIn(email, password)` |
| Check / renew session | `POST /api/collections/users/auth-refresh` with the token → `{token, record}`, or `401` if invalid | `refreshSession()` |
| Sign out | Forget the token (nothing to call) | `signOut()` |
| Current user | The `record` from sign-in or refresh | `currentUser()` |
| Change own details | `PATCH /api/collections/users/records/{id}` | `update("users", id, {...})` |
| Change own password | `PATCH …/records/{id}` with `{oldPassword, password, passwordConfirm}` (then sign in again) | `update("users", id, {...})` |

- Tokens last about **7 days**. `refreshSession()` renews them, so call it on app start. Any call with an expired token returns `401`, and the client then signs out by itself.
- Sign-in errors are deliberately vague (*"Failed to authenticate."*). Show a generic "wrong email or password".
- Sign-up errors are per field, e.g. `data.email.message = "Value must be unique."`, `data.password.message = "Must be at least 8 character(s)."`.
- **Not available yet:** password-reset emails, email verification and social login (Google, GitHub). Don't build UI for them.
- Browsers: the client keeps the token in `localStorage`. For server-side rendering, pass `{ token }` explicitly (see the client).

## Reading data

`GET /api/collections/{table}/records` returns a page:

```json
{ "page": 1, "perPage": 30, "totalItems": 57, "totalPages": 2, "items": [ /* records */ ] }
```

| Query parameter | Meaning | Example |
|---|---|---|
| `page` | Page number, starts at 1 | `page=2` |
| `perPage` | Items per page (default 30, max 1000) | `perPage=50` |
| `sort` | Comma-separated fields; `-` for descending. Only existing fields (unknown → `400`). | `sort=-created,name` |
| `filter` | Filter expression (below) | `filter=(price > 10 && name ~ "mug")` |
| `expand` | Include linked records (link fields), comma-separated, nested with `.` | `expand=product` |
| `fields` | Return only these fields | `fields=id,name,price` |
| `skipTotal` | `1` = faster, but `totalItems`/`totalPages` become `-1` | `skipTotal=1` |

One record: `GET /api/collections/{table}/records/{id}` (also accepts `expand` and `fields`).

**Filter syntax.** Compare fields with `=` `!=` `>` `>=` `<` `<=`. `~` means *contains* (case-insensitive) and `!~` *doesn't contain*. Combine with `&&` `||` and parentheses. Text and dates go in double quotes. Examples:

```
status = "paid" && total >= 100
name ~ "mug" || description ~ "mug"
created >= "2026-09-01 00:00:00"
owner = "USER_ID"
product.name ~ "mug"          (filter through a link field)
```

Always build filters with user input through `q()`: `` `name ~ ${q(search)}` ``. URL-encode the whole filter (the client does it).

**Linked records.** With `expand=product`, each item gets `item.expand.product` (the full linked record, if the user may view it).

## Writing data

| Action | HTTP | Success |
|---|---|---|
| Create | `POST /api/collections/{table}/records` with JSON (or FormData for files) | `200` + the new record |
| Edit | `PATCH /api/collections/{table}/records/{id}` with only the changed fields | `200` + the updated record |
| Delete | `DELETE /api/collections/{table}/records/{id}` | `204`, no body |

- **Dates:** send ISO strings (`"2026-09-27T14:30:00Z"` or `"2026-09-27"`). Offsets are converted to UTC. They come back as `"2026-09-27 14:30:00.000Z"` (space, not `T`); use `toDate()` from the client to parse them (Safari can't parse the space form directly).
- **Numbers** may be sent as numbers or numeric strings. **Choices** must be one of the listed values.
- **Links:** send the linked record's id (or an array of ids for multiple).
- **Files:** use `FormData`. Append each non-file field as a string (JSON values as `JSON.stringify(...)`) and each file under its field name. Don't set `Content-Type`; the browser does. A stored file value is its **file name**. Build the URL with `fileUrl(record, name)` → `https://8090.example.test/api/files/{collectionId}/{recordId}/{fileName}`. Images work directly in `<img src>`.
- **Remove a file:** `update(table, id, { field: "" })` for single-file fields. For multi-file fields, `update(table, id, { "field-": ["name.jpg"] })` removes specific files, and appending files under `field+` in FormData adds files without replacing the existing ones (appending under `field` replaces them all).
- **IDs** are generated for you (15 lowercase letters/digits).

## Errors

Every error has this shape:

```json
{ "status": 400, "message": "Failed to create record.", "data": { "name": { "code": "validation_required", "message": "Cannot be blank." } } }
```

| Status | Meaning | What to do |
|---|---|---|
| `400` | Invalid input (see `data.<field>.message`), a create not allowed by the rules, a bad filter/sort, or a sign-in that failed | Show field messages next to inputs |
| `403` | *Server only* action. Or **plan limit**: *"…reached its storage limit…"* / *"…reached its user limit…"* | Show the message. Storage full still allows reading and deleting |
| `404` | Record missing **or not visible to this user**, or unknown table | Check the table's access rules |
| `429` | **Monthly API request limit reached**: *"…used all API requests included in its plan this month."* | Show a friendly "service temporarily unavailable"; it resets next month |
| `5xx` / network error | Temporary problem | Retry once after a short delay |

Common `data.<field>.code` values: `validation_required`, `validation_not_unique` (duplicate of a unique field or combination), `validation_min_number_constraint` / `validation_max_number_constraint` (number out of range), `validation_only_int_constraint` (decimal in a whole-number field), `validation_invalid_value` (not one of the choices).

The client throws `DatbaseError` with `.status`, `.message` and `.fieldErrors` (`{ field: message }`).

## Server-side access (server keys)

For code that runs **only on the app owner's servers** (a backend, an admin script, a cron job, a Next.js server route), use a **server key**. It can read and change every table, **ignoring the access rules above**, so it must never reach a browser, mobile app or public repository.

- **Getting one:** the human creates it in the DATBase dashboard (project → Overview → *Server keys* → Create key) and gives it to the server as the environment variable `DATBASE_SERVER_KEY`. If it isn't set, **stop and ask the human** to create one. Never try other ways to obtain admin access.
- **Using it:** send it like a user token: `Authorization: Bearer <key>`. With the client, pass it per call:

```ts
// server-only file (never imported by browser code)
import { checkServerKey, listAll } from "./datbase";
await checkServerKey(process.env.DATBASE_SERVER_KEY); // throws (401/403) if missing, expired or revoked
const asServer = { token: process.env.DATBASE_SERVER_KEY! };
const everything = await listAll("products", { sort: "-created" }, 5000, asServer);
```

- **Check the key first and fail loudly.** Start every server job with `await checkServerKey(process.env.DATBASE_SERVER_KEY)`. It throws unless the key is a live server key. Don't catch errors and fall back to zeros or empty lists: let the job exit non-zero.
- Without the client: `GET /api/session` with `Authorization: Bearer <key>` returns `200 {"type":"server"}` for a live key and `401` otherwise.
- Keys **expire** (30 days to 1 year, shown in the dashboard) and can be **revoked** at any time. After that, **every** call with the key returns `401`. You never get a silent empty result.
- Requests made with a key **count toward the project's plan** like any other.
- Don't call `signIn()` with a key. Keys aren't user accounts and don't need refreshing.

## Not available (don't build against these)

- Creating or changing tables, fields or access rules through the API (use the dashboard).
- Realtime subscriptions, server functions or webhooks.
- Password reset, email verification and social login (no email provider yet).
- Server keys in client code. There is no "public API key" to add either: public access is controlled by each table's access rules.

## `datbase.ts`: the client

Copy this file into the project unchanged, for example as `src/lib/datbase.ts`. For plain JavaScript, remove the type annotations.

```ts
// datbase.ts: DATBase client. No dependencies. Works in browsers, Node 18+, Next.js, Vite, React Native, Deno.
export const DATBASE_URL = "https://8090.example.test";
const USERS_TABLE = "users";
const EMAIL_ENABLED = false; // this project can send sign-in emails (reset password, confirm email)

export interface BaseRecord {
  id: string;
  collectionId: string;
  collectionName: string;
  [field: string]: unknown;
}
export interface ListResult<T> {
  page: number;
  perPage: number;
  totalItems: number;
  totalPages: number;
  items: T[];
}
export interface ListOptions {
  page?: number;
  perPage?: number;
  sort?: string;
  filter?: string;
  expand?: string;
  fields?: string;
  skipTotal?: boolean;
}
/** Pass { token } to act as a specific user (e.g. server-side rendering). Defaults to the signed-in user. */
export interface RequestOptions {
  token?: string | null;
  signal?: AbortSignal;
}

export class DatbaseError extends Error {
  status: number;
  fieldErrors: Record<string, string>;
  constructor(status: number, message: string, fieldErrors: Record<string, string> = {}) {
    super(message);
    this.name = "DatbaseError";
    this.status = status;
    this.fieldErrors = fieldErrors;
  }
}

/* ---------- session storage (localStorage in browsers, memory elsewhere) ---------- */
const memory = new Map<string, string>();
const storage = {
  get(k: string): string | null {
    try {
      return globalThis.localStorage?.getItem(k) ?? memory.get(k) ?? null;
    } catch {
      return memory.get(k) ?? null;
    }
  },
  set(k: string, v: string) {
    memory.set(k, v);
    try {
      globalThis.localStorage?.setItem(k, v);
    } catch {}
  },
  remove(k: string) {
    memory.delete(k);
    try {
      globalThis.localStorage?.removeItem(k);
    } catch {}
  },
};
const TOKEN_KEY = "datbase_token";
const USER_KEY = "datbase_user";

export function getToken(): string | null {
  return storage.get(TOKEN_KEY);
}
export function currentUser<T extends BaseRecord = BaseRecord>(): T | null {
  const raw = storage.get(USER_KEY);
  return raw ? (JSON.parse(raw) as T) : null;
}
function saveSession(token: string, user: BaseRecord) {
  storage.set(TOKEN_KEY, token);
  storage.set(USER_KEY, JSON.stringify(user));
}
export function signOut() {
  storage.remove(TOKEN_KEY);
  storage.remove(USER_KEY);
}

/* ---------- low-level request ---------- */
export async function request<T>(path: string, init: RequestInit = {}, opts: RequestOptions = {}): Promise<T> {
  const token = opts.token === undefined ? getToken() : opts.token;
  const headers = new Headers(init.headers);
  if (token) headers.set("Authorization", "Bearer " + token);
  if (init.body && !(init.body instanceof FormData) && !headers.has("Content-Type")) {
    headers.set("Content-Type", "application/json");
  }
  const res = await fetch(DATBASE_URL + path, { ...init, headers, signal: opts.signal });
  if (res.status === 401 && opts.token === undefined) signOut(); // stored session expired or revoked
  if (res.status === 204) return undefined as T;
  const body = await res.json().catch(() => null);
  if (!res.ok) {
    const fieldErrors: Record<string, string> = {};
    for (const [k, v] of Object.entries((body?.data ?? {}) as Record<string, { message?: string }>)) {
      if (v?.message) fieldErrors[k] = v.message;
    }
    throw new DatbaseError(res.status, body?.message ?? "Request failed (" + res.status + ")", fieldErrors);
  }
  return body as T;
}

const tablePath = (table: string) => "/api/collections/" + encodeURIComponent(table) + "/records";
const bodyOf = (data: Record<string, unknown> | FormData) => (data instanceof FormData ? data : JSON.stringify(data));

/* ---------- records ---------- */
export function list<T = BaseRecord>(table: string, o: ListOptions = {}, opts?: RequestOptions) {
  const q = new URLSearchParams();
  if (o.page) q.set("page", String(o.page));
  if (o.perPage) q.set("perPage", String(o.perPage));
  if (o.sort) q.set("sort", o.sort);
  if (o.filter) q.set("filter", o.filter);
  if (o.expand) q.set("expand", o.expand);
  if (o.fields) q.set("fields", o.fields);
  if (o.skipTotal) q.set("skipTotal", "1");
  const qs = q.toString();
  return request<ListResult<T>>(tablePath(table) + (qs ? "?" + qs : ""), {}, opts);
}

/** Every matching record (fetches page by page, up to maxItems). */
export async function listAll<T = BaseRecord>(table: string, o: Omit<ListOptions, "page" | "perPage"> = {}, maxItems = 5000, opts?: RequestOptions) {
  const out: T[] = [];
  for (let page = 1; out.length < maxItems; page++) {
    const r = await list<T>(table, { ...o, page, perPage: 200, skipTotal: true }, opts);
    out.push(...r.items);
    if (r.items.length < 200) break;
  }
  return out.slice(0, maxItems);
}

export function getOne<T = BaseRecord>(table: string, id: string, o: { expand?: string; fields?: string } = {}, opts?: RequestOptions) {
  const q = new URLSearchParams();
  if (o.expand) q.set("expand", o.expand);
  if (o.fields) q.set("fields", o.fields);
  const qs = q.toString();
  return request<T>(tablePath(table) + "/" + encodeURIComponent(id) + (qs ? "?" + qs : ""), {}, opts);
}

export function create<T = BaseRecord>(table: string, data: Record<string, unknown> | FormData, opts?: RequestOptions) {
  return request<T>(tablePath(table), { method: "POST", body: bodyOf(data) }, opts);
}

export function update<T = BaseRecord>(table: string, id: string, data: Record<string, unknown> | FormData, opts?: RequestOptions) {
  return request<T>(tablePath(table) + "/" + encodeURIComponent(id), { method: "PATCH", body: bodyOf(data) }, opts);
}

export function remove(table: string, id: string, opts?: RequestOptions) {
  return request<void>(tablePath(table) + "/" + encodeURIComponent(id), { method: "DELETE" }, opts);
}

/* ---------- users & sessions ---------- */
type AuthResponse = { token: string; record: BaseRecord };

export async function signIn<T extends BaseRecord = BaseRecord>(email: string, password: string): Promise<T> {
  const r = await request<AuthResponse>(
    "/api/collections/" + USERS_TABLE + "/auth-with-password",
    { method: "POST", body: JSON.stringify({ identity: email, password }) },
    { token: null },
  );
  saveSession(r.token, r.record);
  return r.record as T;
}

/** Create an account, then sign in. Extra fields (e.g. name) are saved on the user record. */
export async function signUp<T extends BaseRecord = BaseRecord>(
  fields: { email: string; password: string } & Record<string, unknown>,
): Promise<T> {
  await request(
    tablePath(USERS_TABLE),
    { method: "POST", body: JSON.stringify({ ...fields, passwordConfirm: fields.password }) },
    { token: null },
  );
  if (EMAIL_ENABLED) await requestVerification(fields.email).catch(() => {}); // "confirm your email" link
  return signIn<T>(fields.email, fields.password);
}

/** Email a "choose a new password" link. Resolves the same way whether or not the account exists. */
export async function requestPasswordReset(email: string): Promise<void> {
  if (!EMAIL_ENABLED) throw new DatbaseError(400, "Password reset emails are not available for this project.");
  await request<void>("/api/collections/" + USERS_TABLE + "/request-password-reset", { method: "POST", body: JSON.stringify({ email }) }, { token: null });
}

/** Email a "confirm your email" link (again). */
export async function requestVerification(email: string): Promise<void> {
  if (!EMAIL_ENABLED) throw new DatbaseError(400, "Email confirmation is not available for this project.");
  await request<void>("/api/collections/" + USERS_TABLE + "/request-verification", { method: "POST", body: JSON.stringify({ email }) }, { token: null });
}

/** Validate and renew the stored session. Returns the user, or null (and signs out) if it's no longer valid. */
export async function refreshSession<T extends BaseRecord = BaseRecord>(): Promise<T | null> {
  if (!getToken()) return null;
  try {
    const r = await request<AuthResponse>("/api/collections/" + USERS_TABLE + "/auth-refresh", { method: "POST" });
    saveSession(r.token, r.record);
    return r.record as T;
  } catch (e) {
    if (e instanceof DatbaseError && (e.status === 401 || e.status === 403 || e.status === 404)) signOut();
    return null;
  }
}

/** Server code only: throws unless `key` is a live server key (401 if expired/revoked, 403 if it's a user token). */
export async function checkServerKey(key: string | undefined): Promise<void> {
  if (!key) throw new DatbaseError(401, "DATBASE_SERVER_KEY is not set.");
  const me = await request<{ type: string }>("/api/session", {}, { token: key });
  if (me.type !== "server") throw new DatbaseError(403, "This is a user token, not a server key.");
}

/* ---------- helpers ---------- */
/** URL of a stored file, usable directly in <img src>. */
export function fileUrl(record: Pick<BaseRecord, "id" | "collectionId">, fileName: string): string {
  if (!fileName) return "";
  return DATBASE_URL + "/api/files/" + record.collectionId + "/" + record.id + "/" + encodeURIComponent(fileName);
}

/** Safely quote a value for use inside a filter: `name ~ ${q(search)}`. */
export function q(value: string | number | boolean | null): string {
  if (value === null) return "null";
  if (typeof value !== "string") return String(value);
  return '"' + value.replace(/\\/g, "\\\\").replace(/"/g, '\\"') + '"';
}

/** Parse a DATBase date string ("2026-09-27 14:30:00.000Z") into a Date. Works in all browsers. */
export function toDate(value: string): Date | null {
  return value ? new Date(value.replace(" ", "T")) : null;
}

```

<details><summary>Plain JavaScript version (for pages without a build step: <code>&lt;script type="module"&gt;</code>)</summary>

Same client with the types removed. Save it as `datbase.js` and `import { list } from "./datbase.js"`.

```js
// datbase.js: DATBase client. No dependencies. Works in browsers, Node 18+, Next.js, Vite, React Native, Deno.
export const DATBASE_URL = "https://8090.example.test";
const USERS_TABLE = "users";
const EMAIL_ENABLED = false; // this project can send sign-in emails(reset password, confirm email)

/** Pass { token } to act as a specific user (e.g. server-side rendering). Defaults to the signed-in user. */

export class DatbaseError extends Error {
  status;
  fieldErrors;
  constructor(status, message, fieldErrors = {}) {
    super(message);
    this.name = "DatbaseError";
    this.status = status;
    this.fieldErrors = fieldErrors;
  }
}

/* ---------- session storage (localStorage in browsers, memory elsewhere) ---------- */
const memory = new Map();
const storage = {
  get(k) {
    try {
      return globalThis.localStorage?.getItem(k) ?? memory.get(k) ?? null;
    } catch {
      return memory.get(k) ?? null;
    }
  },
  set(k, v) {
    memory.set(k, v);
    try {
      globalThis.localStorage?.setItem(k, v);
    } catch {}
  },
  remove(k) {
    memory.delete(k);
    try {
      globalThis.localStorage?.removeItem(k);
    } catch {}
  },
};
const TOKEN_KEY = "datbase_token";
const USER_KEY = "datbase_user";

export function getToken() {
  return storage.get(TOKEN_KEY);
}
export function currentUser() {
  const raw = storage.get(USER_KEY);
  return raw ? (JSON.parse(raw)) : null;
}
function saveSession(token, user) {
  storage.set(TOKEN_KEY, token);
  storage.set(USER_KEY, JSON.stringify(user));
}
export function signOut() {
  storage.remove(TOKEN_KEY);
  storage.remove(USER_KEY);
}

/* ---------- low-level request ---------- */
export async function request(path, init = {}, opts = {}) {
  const token = opts.token === undefined ? getToken() : opts.token;
  const headers = new Headers(init.headers);
  if (token) headers.set("Authorization", "Bearer " + token);
  if (init.body && !(init.body instanceof FormData) && !headers.has("Content-Type")) {
    headers.set("Content-Type", "application/json");
  }
  const res = await fetch(DATBASE_URL + path, { ...init, headers, signal: opts.signal });
  if (res.status === 401 && opts.token === undefined) signOut(); // stored session expired or revoked
  if (res.status === 204) return undefined;
  const body = await res.json().catch(() => null);
  if (!res.ok) {
    const fieldErrors = {};
    for (const [k, v] of Object.entries((body?.data ?? {}))) {
      if (v?.message) fieldErrors[k] = v.message;
    }
    throw new DatbaseError(res.status, body?.message ?? "Request failed(" + res.status + ")", fieldErrors);
  }
  return body;
}

const tablePath = (table) => "/api/collections/" + encodeURIComponent(table) + "/records";
const bodyOf = (data) => (data instanceof FormData ? data : JSON.stringify(data));

/* ---------- records ---------- */
export function list(table, o = {}, opts) {
  const q = new URLSearchParams();
  if (o.page) q.set("page", String(o.page));
  if (o.perPage) q.set("perPage", String(o.perPage));
  if (o.sort) q.set("sort", o.sort);
  if (o.filter) q.set("filter", o.filter);
  if (o.expand) q.set("expand", o.expand);
  if (o.fields) q.set("fields", o.fields);
  if (o.skipTotal) q.set("skipTotal", "1");
  const qs = q.toString();
  return request(tablePath(table) + (qs ? "?" + qs : ""), {}, opts);
}

/** Every matching record (fetches page by page, up to maxItems). */
export async function listAll(table, o = {}, maxItems = 5000, opts) {
  const out = [];
  for (let page = 1; out.length < maxItems; page++) {
    const r = await list(table, { ...o, page, perPage: 200, skipTotal: true }, opts);
    out.push(...r.items);
    if (r.items.length < 200) break;
  }
  return out.slice(0, maxItems);
}

export function getOne(table, id, o = {}, opts) {
  const q = new URLSearchParams();
  if (o.expand) q.set("expand", o.expand);
  if (o.fields) q.set("fields", o.fields);
  const qs = q.toString();
  return request(tablePath(table) + "/" + encodeURIComponent(id) + (qs ? "?" + qs : ""), {}, opts);
}

export function create(table, data, opts) {
  return request(tablePath(table), { method: "POST", body: bodyOf(data) }, opts);
}

export function update(table, id, data, opts) {
  return request(tablePath(table) + "/" + encodeURIComponent(id), { method: "PATCH", body: bodyOf(data) }, opts);
}

export function remove(table, id, opts) {
  return request(tablePath(table) + "/" + encodeURIComponent(id), { method: "DELETE" }, opts);
}

/* ---------- users & sessions ---------- */

export async function signIn(email, password) {
  const r = await request (
    "/api/collections/" + USERS_TABLE + "/auth-with-password",
    { method: "POST", body: JSON.stringify({ identity: email, password }) },
    { token: null },
 );
  saveSession(r.token, r.record);
  return r.record;
}

/** Create an account, then sign in. Extra fields (e.g. name) are saved on the user record. */
export async function signUp (
  fields,
) {
  await request(
    tablePath(USERS_TABLE),
    { method: "POST", body: JSON.stringify({ ...fields, passwordConfirm: fields.password }) },
    { token: null },
 );
  if (EMAIL_ENABLED) await requestVerification(fields.email).catch(() => {}); // "confirm your email" link
  return signIn(fields.email, fields.password);
}

/** Email a "choose a new password" link. Resolves the same way whether or not the account exists. */
export async function requestPasswordReset(email) {
  if (!EMAIL_ENABLED) throw new DatbaseError(400, "Password reset emails are not available for this project.");
  await request("/api/collections/" + USERS_TABLE + "/request-password-reset", { method: "POST", body: JSON.stringify({ email }) }, { token: null });
}

/** Email a "confirm your email" link (again). */
export async function requestVerification(email) {
  if (!EMAIL_ENABLED) throw new DatbaseError(400, "Email confirmation is not available for this project.");
  await request("/api/collections/" + USERS_TABLE + "/request-verification", { method: "POST", body: JSON.stringify({ email }) }, { token: null });
}

/** Validate and renew the stored session. Returns the user, or null (and signs out) if it's no longer valid. */
export async function refreshSession() {
  if (!getToken()) return null;
  try {
    const r = await request("/api/collections/" + USERS_TABLE + "/auth-refresh", { method: "POST" });
    saveSession(r.token, r.record);
    return r.record;
  } catch (e) {
    if (e instanceof DatbaseError && (e.status === 401 || e.status === 403 || e.status === 404)) signOut();
    return null;
  }
}

/** Server code only: throws unless `key` is a live server key (401 if expired/revoked, 403 if it's a user token). */
export async function checkServerKey(key) {
  if (!key) throw new DatbaseError(401, "DATBASE_SERVER_KEY is not set.");
  const me = await request("/api/session", {}, { token: key });
  if (me.type !== "server") throw new DatbaseError(403, "This is a user token, not a server key.");
}

/* ---------- helpers ---------- */
/** URL of a stored file, usable directly in <img src>. */
export function fileUrl(record, fileName) {
  if (!fileName) return "";
  return DATBASE_URL + "/api/files/" + record.collectionId + "/" + record.id + "/" + encodeURIComponent(fileName);
}

/** Safely quote a value for use inside a filter: `name ~ ${q(search)}`. */
export function q(value) {
  if (value === null) return "null";
  if (typeof value !== "string") return String(value);
  return '"' + value.replace(/\\/g, "\\\\").replace(/"/g, '\\"') + '"';
}

/** Parse a DATBase date string ("2026-09-27 14:30:00.000Z") into a Date. Works in all browsers. */
export function toDate(value) {
  return value ? new Date(value.replace(" ", "T")) : null;
}
```
</details>

## Troubleshooting checklist

- Empty list but data exists? → The table needs sign-in or ownership, and no token was sent (check `getToken()`).
- `401` → the session or server key expired or was revoked. Users: sign in again. Servers: ask the human for a new key.
- `400 Failed to create record.` with empty `data` → the create rule isn't met (not signed in, or the owner field isn't the current user's id).
- `404` on a record you just saw → the view rule differs from the list rule, or the user isn't the owner.
- `400` on list → a typo in `filter` or `sort` (field names are case-sensitive).
- Images not showing → use `fileUrl(record, record.field)`, not the bare file name.
- Field or table "doesn't exist" → this file is out of date. Download it again from the dashboard.
