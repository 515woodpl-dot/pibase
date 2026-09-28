/**
 * DATBase mirror client for gswstore (retail/inventory store).
 *
 * Starting tables: `categories` + `inventory` — the base product data, kept separate from
 * the much larger surface this app has grown (carts, orders, payments, tax rates, card-on-
 * file, walk-in sales — 17 migrations deep per supabase/migrations/). Same reasoning as
 * occ-inventory: start with the simplest, most central write path, not everything at once.
 *
 * Note: supabase/schema/FULL_SCHEMA.sql claims to combine all migrations but is dated before
 * several of them (tax rates, card-on-file, sales docs) — the `inventory` shape below matches
 * that base file; double-check against the actual live schema before trusting field names
 * exactly, especially if inventory itself was touched by a later migration.
 *
 * Same pattern as docs/examples/datbase-mirror-occ-inventory.ts — see docs/SECONDARY-BACKEND.md
 * for the full playbook. Write-only, best-effort, non-blocking. Auth (`profiles`/`admin_users`/
 * `auth.users`) is deliberately NOT mirrored.
 *
 * Env vars needed:
 *   DATBASE_URL        e.g. https://gswstore-xxxxx.<your-domain>   (no trailing slash)
 *   DATBASE_SERVER_KEY the token from Dashboard -> project -> Server keys
 *
 * DATBase collections to create first:
 *   categories: sourceId (text, unique — categories.id, a bigint, stringified),
 *               name, prefix, color (text), sortOrder (number)
 *   inventory:  sourceId (text, unique — inventory.id, already a TEXT business id),
 *               name, category (relation -> categories), categoryName, brand, modelNumber,
 *               voltage, sku, description (text), amount (number), storePrice (number),
 *               imageUrl (text), featured, storeVisible (bool)
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

export async function mirrorCategory(c: {
  id: number; // bigint identity
  name: string;
  prefix: string;
  color: string;
  sort_order: number;
}) {
  if (!enabled) return;
  try {
    await upsertByField("categories", "sourceId", String(c.id), {
      sourceId: String(c.id),
      name: c.name,
      prefix: c.prefix,
      color: c.color,
      sortOrder: c.sort_order,
    });
  } catch (e) {
    log("mirrorCategory", e);
  }
}

export async function mirrorInventory(i: {
  id: string; // TEXT business id, already the primary key
  name: string;
  category_id: number | null;
  category_name: string;
  brand: string;
  model_number: string;
  voltage: string;
  sku: string;
  description: string;
  amount: number;
  store_price: number;
  image_url: string | null;
  featured: boolean;
  store_visible: boolean;
}) {
  if (!enabled) return;
  try {
    let categoryRecordId: string | undefined;
    if (i.category_id != null) {
      const cat = (await pbFetch(
        `/api/collections/categories/records?filter=${encodeURIComponent(`sourceId=${esc(String(i.category_id))}`)}&perPage=1`,
      )) as { items: Array<{ id: string }> };
      if (cat.items.length === 0) {
        log("mirrorInventory", `category ${i.category_id} not mirrored yet — mirror categories before inventory`);
        return;
      }
      categoryRecordId = cat.items[0].id;
    }
    await upsertByField("inventory", "sourceId", i.id, {
      sourceId: i.id,
      name: i.name,
      category: categoryRecordId,
      categoryName: i.category_name,
      brand: i.brand,
      modelNumber: i.model_number,
      voltage: i.voltage,
      sku: i.sku,
      description: i.description,
      amount: i.amount,
      storePrice: i.store_price,
      imageUrl: i.image_url,
      featured: i.featured,
      storeVisible: i.store_visible,
    });
  } catch (e) {
    log("mirrorInventory", e);
  }
}
