/**
 * DATBase mirror client for ehsmanager (EHS Studio Manager — photography studio CRM).
 *
 * Starting table: `clients` — the central table every other table (invoices, bookings,
 * contracts, quotes, questionnaires, messages) references via client_id. Good first pick:
 * simple shape, no jsonb line-item complexity, high write traffic.
 *
 * Same pattern as docs/examples/datbase-mirror-occ-inventory.ts — see docs/SECONDARY-BACKEND.md
 * for the full playbook. Write-only, best-effort, non-blocking: never throws, never slows down
 * or breaks the real app. Auth (Supabase `profiles`/`auth.users`) is deliberately NOT mirrored.
 *
 * Env vars needed:
 *   DATBASE_URL        e.g. https://ehsmanager-xxxxx.<your-domain>   (no trailing slash)
 *   DATBASE_SERVER_KEY the token from Dashboard -> project -> Server keys
 *
 * DATBase collection to create first (fields match supabase-schema.sql's `clients` table):
 *   sourceId (text, unique) — the Supabase clients.id (uuid), carried over as-is
 *   userId   (text)         — the owning studio account's Supabase user_id (not mirrored as
 *                              an auth relation — see docs/SECONDARY-BACKEND.md on why auth
 *                              stays out of the mirror)
 *   name, email, phone, type, status (text)
 *   date     (date)
 *   notes    (text)
 *   archived (bool)
 *
 * To extend to more tables later (invoices, bookings, contracts, quotes, questionnaires,
 * messages, leads), copy the shape of mirrorClient() below — each of those tables also has a
 * client_id, so look up the mirrored client's DATBase record id first, same as
 * datbase-mirror-occ-inventory.ts does for items -> categories.
 */

const BASE = process.env.DATBASE_URL;
const KEY = process.env.DATBASE_SERVER_KEY;
const enabled = Boolean(BASE && KEY);

function log(action: string, err: unknown) {
  console.warn(`[datbase-mirror] ${action} failed (secondary backend, real app unaffected):`, err);
}

async function pbFetch(path: string, init: RequestInit = {}) {
  const res = await fetch(`${BASE}${path}`, {
    ...init,
    headers: { "content-type": "application/json", Authorization: KEY!, ...(init.headers ?? {}) },
    signal: AbortSignal.timeout(5000),
  });
  if (!res.ok) throw new Error(`${res.status} ${await res.text().catch(() => "")}`);
  if (res.status === 204) return undefined;
  return res.json();
}

const esc = (v: string) => `"${v.replace(/\\/g, "\\\\").replace(/"/g, '\\"')}"`;

async function upsertByField(collection: string, field: string, value: string, data: Record<string, unknown>) {
  const found = (await pbFetch(
    `/api/collections/${collection}/records?filter=${encodeURIComponent(`${field}=${esc(value)}`)}&perPage=1`,
  )) as { items: Array<{ id: string }> };
  if (found.items.length > 0) {
    await pbFetch(`/api/collections/${collection}/records/${found.items[0].id}`, {
      method: "PATCH",
      body: JSON.stringify(data),
    });
  } else {
    await pbFetch(`/api/collections/${collection}/records`, { method: "POST", body: JSON.stringify(data) });
  }
}

export async function mirrorClient(c: {
  id: string; // uuid
  user_id: string;
  name: string;
  email: string | null;
  phone: string | null;
  type: string | null;
  status: string;
  date: string | null; // date, ISO
  notes: string | null;
  archived: boolean;
}) {
  if (!enabled) return;
  try {
    await upsertByField("clients", "sourceId", c.id, {
      sourceId: c.id,
      userId: c.user_id,
      name: c.name,
      email: c.email,
      phone: c.phone,
      type: c.type,
      status: c.status,
      date: c.date,
      notes: c.notes,
      archived: c.archived,
    });
  } catch (e) {
    log("mirrorClient", e);
  }
}
