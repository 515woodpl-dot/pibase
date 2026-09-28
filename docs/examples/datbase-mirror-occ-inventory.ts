/**
 * DATBase mirror client for occ-inventory.
 *
 * Write-only, best-effort mirror of Postgres writes into DATBase (a secondary PocketBase
 * backend), so DATBase gets exercised by real app traffic without ever being able to affect
 * the real app: every function here catches its own errors and never throws, has a hard
 * timeout, and is meant to be called without awaiting (or awaited but ignored) right after the
 * real Postgres write succeeds.
 *
 * Env vars needed:
 *   DATBASE_URL        e.g. https://occ-inventory-emzw7o.<your-domain>   (no trailing slash)
 *   DATBASE_SERVER_KEY the token from Dashboard -> project -> Server keys
 *
 * If either is unset, every function becomes a silent no-op — safe to deploy this file before
 * the env vars exist.
 */

const BASE = process.env.DATBASE_URL;
const KEY = process.env.DATBASE_SERVER_KEY;
const enabled = Boolean(BASE && KEY);

function log(action: string, err: unknown) {
  // Intentionally console.warn, not console.error: a mirror failure is not a real app error.
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

/**
 * Find a record by one unique field and PATCH it, or POST a new one if none exists.
 * This is the "pass the same computed value to both" pattern: sourceId carries the real
 * Postgres id/number so re-mirroring an update finds the same DATBase record instead of
 * creating duplicates.
 */
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

/* ---------- One function per table, matching schema.ts's shape ---------- */

export async function mirrorCategory(c: { id: number; name: string; prefix: string; color: string; nextItemNumber: number }) {
  if (!enabled) return;
  try {
    await upsertByField("categories", "sourceId", String(c.id), {
      sourceId: String(c.id),
      name: c.name,
      prefix: c.prefix,
      color: c.color,
      nextItemNumber: c.nextItemNumber,
    });
  } catch (e) {
    log("mirrorCategory", e);
  }
}

export async function mirrorItem(i: {
  id: string; // business id, e.g. "C001"
  name: string;
  categoryId: number;
  description: string;
  brand: string;
  modelNumber: string;
  serialNumber: string;
  location: string;
  warranty: string;
  voltage: string;
  amount: number;
  priceCents: number;
  purchaseDate: Date | null;
  addedBy: string;
}) {
  if (!enabled) return;
  try {
    // Item's category field is a DATBase relation, which needs the *DATBase* record id, not
    // the Postgres category id — look it up by the same sourceId convention used above.
    const cat = (await pbFetch(
      `/api/collections/categories/records?filter=${encodeURIComponent(`sourceId=${esc(String(i.categoryId))}`)}&perPage=1`,
    )) as { items: Array<{ id: string }> };
    if (cat.items.length === 0) {
      log("mirrorItem", `category ${i.categoryId} not mirrored yet — mirror categories before items`);
      return;
    }
    await upsertByField("items", "sourceId", i.id, {
      sourceId: i.id,
      itemCode: i.id,
      name: i.name,
      category: cat.items[0].id,
      description: i.description,
      brand: i.brand,
      modelNumber: i.modelNumber,
      serialNumber: i.serialNumber,
      location: i.location,
      warranty: i.warranty,
      voltage: i.voltage,
      amount: i.amount,
      priceCents: i.priceCents,
      purchaseDate: i.purchaseDate?.toISOString() ?? null,
      addedBy: i.addedBy,
    });
  } catch (e) {
    log("mirrorItem", e);
  }
}

export async function mirrorQuote(q: {
  id: number;
  quoteNumber: string;
  title: string;
  description: string;
  status: string;
  createdBy: string;
  totalCents: number;
  purchasedAt: Date | null;
}) {
  if (!enabled) return;
  try {
    await upsertByField("quotes", "sourceId", String(q.id), {
      sourceId: String(q.id),
      quoteNumber: q.quoteNumber,
      title: q.title,
      description: q.description,
      status: q.status,
      createdBy: q.createdBy,
      totalCents: q.totalCents,
      purchasedAt: q.purchasedAt?.toISOString() ?? null,
    });
  } catch (e) {
    log("mirrorQuote", e);
  }
}

export async function mirrorQuoteItem(qi: {
  id: number;
  quoteId: number;
  itemId: string | null;
  name: string;
  qty: number;
  unitPriceCents: number;
}) {
  if (!enabled) return;
  try {
    const quote = (await pbFetch(
      `/api/collections/quotes/records?filter=${encodeURIComponent(`sourceId=${esc(String(qi.quoteId))}`)}&perPage=1`,
    )) as { items: Array<{ id: string }> };
    if (quote.items.length === 0) {
      log("mirrorQuoteItem", `quote ${qi.quoteId} not mirrored yet — mirror the quote before its line items`);
      return;
    }
    await upsertByField("quote_items", "sourceId", String(qi.id), {
      sourceId: String(qi.id),
      quote: quote.items[0].id,
      itemId: qi.itemId,
      name: qi.name,
      qty: qi.qty,
      unitPriceCents: qi.unitPriceCents,
    });
  } catch (e) {
    log("mirrorQuoteItem", e);
  }
}

export async function mirrorSettings(s: {
  orgName: string;
  orgTagline: string;
  logoBase64: string;
  accentColor: string;
  lowStockThreshold: number;
}) {
  if (!enabled) return;
  try {
    // Single-row table: sourceId is always "1", matching your Postgres id=1 convention.
    await upsertByField("settings", "sourceId", "1", { sourceId: "1", ...s });
  } catch (e) {
    log("mirrorSettings", e);
  }
}
