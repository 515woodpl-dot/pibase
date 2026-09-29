// Schema import (Tables → Import schema): paste one JSON document, get many tables.
// Drives the real dashboard UI, then checks what PocketBase actually holds and how the tables behave.
import { readFileSync } from "node:fs";
import { APP as B, PB, ok, must, browser, signup, linkProject, pbAdminToken, controlHeaders, WORK_DIR } from "./lib.mjs";

const fx = (n) => readFileSync(new URL(`../fixtures/schema-import/${n}`, import.meta.url), "utf8");
const b = await browser();
const a = await signup(b, "a@test.com");
const P = linkProject("a@test.com");
const token = await pbAdminToken();
const H = { Authorization: token, "content-type": "application/json" };
const collection = async (n) => {
  const r = await fetch(`${PB}/api/collections/${n}`, { headers: H });
  return r.status === 200 ? r.json() : null;
};
const names = async () =>
  (await (await fetch(`${PB}/api/collections?perPage=200`, { headers: H })).json()).items
    .filter((c) => !c.system && !c.name.startsWith("_")).map((c) => c.name).sort();
const rec = (path, method, body, auth) =>
  fetch(PB + path, { method, headers: { "content-type": "application/json", ...(auth ? { Authorization: auth } : {}) }, body: JSON.stringify(body) });

/** Open Tables, open the import box, paste, click Import. */
async function paste(text) {
  await a.goto(P + "/tables");
  await a.click("summary:has-text('Import schema')");
  await a.fill("textarea[name=schema]", text);
  await a.click("button:text-is('Import')");
  await a.waitForSelector("p.text-red-600, p:has-text('Imported:')");
}
const errorText = () => a.locator("p.text-red-600").first().innerText();

// 0. The import box is there and the example loads.
await a.goto(P + "/tables");
must(await a.locator("summary:has-text('Import schema')").count(), "import box missing on Tables page");
await a.click("summary:has-text('Import schema')");
await a.click("button:has-text('Load example')");
must((await a.inputValue("textarea[name=schema]")).includes('"products"'), "example not loaded");
ok("Tables page has an Import schema box; 'Load example' fills it");
await a.screenshot({ path: `${WORK_DIR}/schema-import-box.png`, fullPage: true });

// 1. Bad input never creates anything, and says what to fix.
const before = await names();
for (const [file, re] of [
  ["invalid-json.json", /Not valid JSON/],
  ["invalid-type.json", /unknown type "hologram"/],
  ["invalid-link.json", /ghosts/],
  ["invalid-owner.json", /owner.*users/],
]) {
  await paste(fx(file));
  const e = await errorText();
  must(re.test(e), `${file}: expected ${re}, got "${e}"`);
}
must(JSON.stringify(await names()) === JSON.stringify(before), "a rejected import created tables");
ok("invalid JSON / unknown type / link to missing table / owner without users link: clear errors, nothing created");

// 2. An 8-table store in one paste.
await paste(fx("store.json"));
await a.waitForSelector("li:has-text('categories: created with 3 fields')");
const want = ["addresses", "cart_items", "carts", "categories", "order_items", "orders", "product_variants", "products"];
const have = await names();
must(want.every((n) => have.includes(n)), `missing tables: ${want.filter((n) => !have.includes(n))}`);
ok("store schema: 8 tables created in one paste");
await a.screenshot({ path: `${WORK_DIR}/schema-import-done.png`, fullPage: true });

const products = await collection("products");
const cats = await collection("categories");
must(products.fields.map((f) => f.name).join() === "id,name,slug,category,description,basePrice,imageUrl,images,featured,active,created,updated", "products field order/set: " + products.fields.map((f) => f.name));
must(products.fields.find((f) => f.name === "category").collectionId === cats.id, "relation points at categories");
must(products.fields.find((f) => f.name === "images").maxSelect === 10, "multiple files");
must(products.fields.find((f) => f.name === "basePrice").onlyInt === true, "integer flag");
must(products.listRule === "" && products.viewRule === "" && products.createRule === null, "products access: public read, server-only write");
must(products.indexes.some((i) => /UNIQUE/i.test(i) && i.includes("`slug`")), "unique slug index");
must((await collection("carts")).listRule === "@request.auth.id != '' && owner = @request.auth.id", "carts owner rule");
must((await collection("orders")).createRule === null && (await collection("orders")).listRule.includes("owner"), "orders: owner reads, server writes");
ok("fields, links, integer/multiple options, unique index and access rules are what the JSON asked for");

// 3. Behaviour: the rules really work.
const cat = await (await rec("/api/collections/categories/records", "POST", { name: "Shirts", slug: "shirts" }, token)).json();
must(cat.id, "server can create category");
must((await rec("/api/collections/products/records", "POST", { name: "Tee", slug: "tee", category: cat.id, basePrice: 1999, active: true }, token)).status === 200, "server creates product");
must((await (await fetch(PB + "/api/collections/products/records")).json()).items.length === 1, "anyone can read products");
must((await rec("/api/collections/products/records", "POST", { name: "Hack", slug: "hack", category: cat.id })).status >= 400, "anonymous create must fail");
must((await rec("/api/collections/products/records", "POST", { name: "Dup", slug: "tee", category: cat.id }, token)).status >= 400, "unique slug enforced");
must((await rec("/api/collections/products/records", "POST", { name: "Neg", slug: "neg", category: cat.id, basePrice: -5 }, token)).status >= 400, "min 0 enforced");
const mk = async (email) => {
  const u = await (await rec("/api/collections/users/records", "POST", { email, password: "Passw0rd!123", passwordConfirm: "Passw0rd!123" }, token)).json();
  const t = (await (await rec("/api/collections/users/auth-with-password", "POST", { identity: email, password: "Passw0rd!123" })).json()).token;
  return { id: u.id, t };
};
const u1 = await mk("one@shop.test"), u2 = await mk("two@shop.test");
must((await rec("/api/collections/carts/records", "POST", { owner: u1.id, status: "active" }, u1.t)).status === 200, "user creates own cart");
must((await rec("/api/collections/carts/records", "POST", { owner: u2.id, status: "active" }, u1.t)).status >= 400, "user can't create a cart for someone else");
must((await (await fetch(PB + "/api/collections/carts/records", { headers: { Authorization: u2.t } })).json()).items.length === 0, "user 2 can't see user 1's cart");
must((await rec("/api/collections/orders/records", "POST", { owner: u1.id, orderNumber: "1" }, u1.t)).status >= 400, "customers can't create orders themselves");
ok("public read, server-only write, unique + min enforced, owner-only carts, server-only orders");

// 4. DATBASE.md now lists the new tables and teaches AIs the import format.
const md = await (await a.request.get(P + "/datbase-md")).text();
must(md.includes("product_variants") && md.includes("cart_items"), "DATBASE.md lists new tables");
must(md.includes("Adding tables: schema import"), "DATBASE.md explains schema import");
must(!/pocketbase/i.test(md), "DATBASE.md must not name the engine");
ok("DATBASE.md lists the new tables, includes the schema-import instructions, never names the engine");

// 5. Pasting the same thing again is harmless.
await paste(fx("store.json"));
must(/Nothing to import/.test(await errorText()), "re-paste should say nothing to import");
ok("pasting the same schema twice: 'Nothing to import', no changes");

// 6. The app grows: extend existing tables, keep data.
await paste(fx("growth.json"));
await a.waitForSelector("li:has-text('products: added')");
const grown = await collection("products");
must(["brand", "weightGrams", "tags"].every((n) => grown.fields.some((f) => f.name === n)), "new fields added");
must(grown.fields.find((f) => f.name === "tags").type === "json", "text[] stored as json");
must(grown.listRule === "" && grown.createRule === null, "existing access untouched (import named none)");
must((await (await fetch(PB + "/api/collections/products/records")).json()).items.length === 1, "existing product survived");
must((await collection("users")).fields.some((f) => f.name === "phone"), "users table extended");
const rv = await collection("reviews");
must(rv.listRule === "" && rv.createRule.includes("@request.body.owner = @request.auth.id"), "reviews rules");
ok("growth paste: new fields added to products and users, new reviews table, data and access kept");
must((await a.locator("text=Notes:").count()) > 0, "notes shown");

// 7. Supabase-style JSON from an AI is understood.
await paste(fx("supabase-style.json"));
await a.waitForSelector("li:has-text('blog_posts: created')");
const post = await collection("blog_posts");
const ft = Object.fromEntries(post.fields.map((f) => [f.name, f]));
must(!("created_at" in ft) && ft.title.type === "text" && ft.title.required === true, "varchar/nullable");
must(ft.views.onlyInt === true && ft.rating.type === "number", "integer/numeric");
must(ft.tags.type === "json" && ft.meta.type === "json" && ft.published.type === "bool", "array/jsonb/boolean");
must(ft.status.type === "select" && ft.status.values.join() === "draft,live", "enum");
must(ft.author_id.type === "relation" && ft.author_id.cascadeDelete === true, "uuid references users");
must(ft.cover.type === "file" && ft.published_at.type === "date", "image/timestamp");
must(post.listRule === "" && post.createRule === "@request.auth.id != ''", "select→list+view public, insert→signed in");
ok("Supabase/SQL-style types and permissions map to the right DATBase fields and access");
await a.screenshot({ path: `${WORK_DIR}/schema-import-notes.png`, fullPage: true });

// 8. Mid-way failure leaves nothing behind (PocketBase rejects the third table's name).
const beforeFail = await names();
await paste(JSON.stringify({ tables: [
  { name: "rollback_a", fields: [{ name: "x", type: "text" }] },
  { name: "rollback_b", fields: [{ name: "a", type: "relation", table: "rollback_a" }] },
  { name: "sqlite_bad", fields: [{ name: "x", type: "text" }] },
] }));
const failMsg = await errorText();
must(/Nothing was imported/.test(failMsg), `expected rollback message, got: ${failMsg}`);
must(JSON.stringify(await names()) === JSON.stringify(beforeFail), "failed import left tables behind: " + (await names()));
ok("import that fails half-way (PocketBase rejects a table) is fully undone:", failMsg.slice(0, 90));

// 9. A server key (superuser token) can read and write data but can NOT change tables or access.
const uid = (await (await fetch(`${PB}/api/collections/_superusers/records`, { headers: H })).json()).items[0].id;
const control = controlHeaders();
const key = (await (await fetch(`${PB}/api/collections/_superusers/impersonate/${uid}`, { method: "POST", headers: { ...H, ...control }, body: JSON.stringify({ duration: 3600 }) })).json()).token;
must(key, "could not mint a server-key-like token");
const withKey = (path, method, body) => fetch(PB + path, { method, headers: { "content-type": "application/json", Authorization: key }, body: body ? JSON.stringify(body) : undefined });
must((await withKey("/api/collections/products/records", "GET")).status === 200, "server key reads data");
must((await withKey("/api/collections/categories/records", "POST", { name: "ViaKey", slug: "via-key" })).status === 200, "server key writes data");
const pid = products.id;
for (const [label, r] of [
  ["create table", await withKey("/api/collections", "POST", { name: "pwned", type: "base", fields: [{ name: "x", type: "text" }], listRule: "" })],
  ["change access", await withKey(`/api/collections/${pid}`, "PATCH", { createRule: "" })],
  ["import schema", await withKey("/api/collections/import", "PUT", { collections: [], deleteMissing: true })],
  ["delete table", await withKey(`/api/collections/${pid}`, "DELETE")],
  ["truncate table", await withKey(`/api/collections/${pid}/truncate`, "DELETE")],
  ["mint another token", await withKey(`/api/collections/_superusers/impersonate/${uid}`, "POST", { duration: 31536000 })],
]) must(r.status === 403, `server key must not ${label}: got ${r.status}`);
must(!(await names()).includes("pwned") && (await collection("products")).createRule === null, "schema unchanged after the attempts");
ok("server key: reads and writes data, but create table / change access / import / delete / truncate / mint tokens are all 403; schema unchanged");

// 10. Other accounts can't reach the import.
const bp = await signup(b, "b@test.com");
must((await bp.goto(P + "/tables")).status() === 404, "another account must get 404");
ok("another account gets 404 on this project's Tables page (and so can't import into it)");

await b.close();
