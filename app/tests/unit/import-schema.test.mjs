import { test } from "node:test";
import assert from "node:assert/strict";
import { loadTsGroup } from "./load-ts.mjs";

const { planImport, ruleFor, describePlan } = await loadTsGroup("src/app/dashboard/[id]/tables", "import-schema", ["shared"]);

const USERS = { id: "_pb_users_auth_", name: "users", type: "auth", fields: [
  { name: "id", type: "text" }, { name: "email", type: "email" }, { name: "name", type: "text" }, { name: "avatar", type: "file" },
] };
const plan = (doc, existing = [USERS]) => planImport(typeof doc === "string" ? doc : JSON.stringify(doc), existing);
const ok = (doc, existing) => {
  const r = plan(doc, existing);
  assert.ok(!("error" in r), r.error);
  return r;
};
const err = (doc, re, existing) => {
  const r = plan(doc, existing);
  assert.ok("error" in r, "expected an error");
  assert.match(r.error, re);
};

// A typical store: 8 tables, forward links, owner access, in the documented format.
const PANDA = { tables: [
  { name: "categories", fields: [{ name: "name", type: "text", required: true }, { name: "slug", type: "text", unique: true }, { name: "sortOrder", type: "number", integer: true }], access: { read: "public", write: "admin" } },
  { name: "products", fields: [
    { name: "name", type: "text", required: true }, { name: "slug", type: "text", unique: true },
    { name: "category", type: "relation", table: "categories", required: true }, { name: "description", type: "text" },
    { name: "basePrice", type: "number", min: 0, integer: true }, { name: "images", type: "file", multiple: true }, { name: "active", type: "bool" },
  ], access: { read: "public", write: "admin" } },
  { name: "product_variants", fields: [
    { name: "product", type: "relation", table: "products", required: true }, { name: "sku", type: "text", unique: true },
    { name: "size", type: "text" }, { name: "color", type: "text" }, { name: "stock", type: "number", integer: true, min: 0 },
  ], access: { read: "public", write: "admin" } },
  { name: "carts", fields: [{ name: "owner", type: "relation", table: "users", required: true }, { name: "status", type: "select", values: ["active", "converted", "abandoned"] }], access: "owner" },
  { name: "cart_items", fields: [{ name: "cart", type: "relation", table: "carts", required: true }, { name: "variant", type: "relation", table: "product_variants" }, { name: "owner", type: "relation", table: "users" }, { name: "qty", type: "number", integer: true, min: 1 }], access: "owner" },
  { name: "addresses", fields: [{ name: "owner", type: "relation", table: "users" }, { name: "line1", type: "text" }, { name: "isDefault", type: "bool" }], access: "owner" },
  { name: "orders", fields: [{ name: "orderNumber", type: "text", unique: true }, { name: "owner", type: "relation", table: "users" }, { name: "status", type: "select", values: ["pending", "paid", "shipped", "completed", "cancelled"] }, { name: "total", type: "number" }], access: { list: "owner", view: "owner", create: "owner", update: "admin", delete: "admin" } },
  { name: "order_items", fields: [{ name: "order", type: "relation", table: "orders", required: true }, { name: "variant", type: "relation", table: "product_variants" }, { name: "unitPrice", type: "number" }, { name: "qty", type: "number" }] },
] };

test("store schema plans all 8 tables", () => {
  const { plan: p } = ok(PANDA);
  assert.deepEqual(p.tables.map((t) => t.name), ["categories", "products", "product_variants", "carts", "cart_items", "addresses", "orders", "order_items"]);
  const products = p.tables[1];
  assert.equal(products.fields.find((f) => f.name === "category").target, "categories");
  assert.equal(products.fields.find((f) => f.name === "images").options.maxSelect, 10);
  assert.deepEqual(products.access, { list: "public", view: "public", create: "admin", update: "admin", delete: "admin" });
  assert.equal(p.tables[3].access.update, "owner");
  assert.deepEqual(p.tables[7].access, { list: "admin", view: "admin", create: "admin", update: "admin", delete: "admin" });
  assert.equal(describePlan(p)[0], "categories: created with 3 fields");
});

test("forward links to tables later in the import work", () => {
  const { plan: p } = ok({ tables: [{ name: "a", fields: [{ name: "b", type: "relation", table: "b" }] }, { name: "b", fields: [] }] });
  assert.equal(p.tables[0].fields[0].target, "b");
});

test("Supabase/SQL style from an AI is understood", () => {
  const sqlish = "```json\n" + JSON.stringify({ collections: {
    products: { columns: [
      { name: "id", type: "uuid" }, { name: "created_at", type: "timestamptz" },
      { name: "title", type: "varchar(255)", nullable: false }, { name: "price", type: "numeric(10,2)" },
      { name: "qty", type: "integer", default: 0 }, { name: "tags", type: "text[]" },
      { name: "meta", type: "jsonb" }, { name: "is_active", type: "boolean" },
      { name: "status", type: "enum", options: ["draft", "live"] },
      { name: "user_id", type: "uuid", references: "auth.users(id)", onDelete: "cascade" },
      { name: "cover", type: "image" }, { name: "published_at", type: "timestamp" },
    ], permissions: { select: "anyone", insert: "authenticated" } },
  } }) + "\n```";
  const { plan: p, notes } = ok(sqlish);
  const f = Object.fromEntries(p.tables[0].fields.map((x) => [x.name, x]));
  assert.equal(f.id, undefined);
  assert.equal(f.created_at, undefined);
  assert.equal(f.title.type, "text");
  assert.equal(f.title.required, true);
  assert.equal(f.price.type, "number");
  assert.equal(f.qty.options.onlyInt, true);
  assert.equal(f.tags.type, "json");
  assert.equal(f.meta.type, "json");
  assert.equal(f.is_active.type, "bool");
  assert.deepEqual(f.status.options.values, ["draft", "live"]);
  assert.equal(f.user_id.type, "relation");
  assert.equal(f.user_id.target, "users");
  assert.equal(f.user_id.options.cascadeDelete, true);
  assert.equal(f.cover.type, "file");
  assert.equal(f.published_at.type, "date");
  assert.equal(p.tables[0].access.list, "public");
  assert.equal(p.tables[0].access.create, "signedIn");
  assert.ok(notes.some((n) => n.includes('"default"')));
});

test("fields as a name → type map", () => {
  const { plan: p } = ok({ tables: [{ name: "notes", fields: { title: "text", done: "boolean" } }] });
  assert.deepEqual(p.tables[0].fields.map((f) => [f.name, f.type]), [["title", "text"], ["done", "bool"]]);
});

test("existing tables are extended, not recreated", () => {
  const existing = [USERS, { id: "c1", name: "products", type: "base", fields: [{ name: "id", type: "text" }, { name: "title", type: "text" }] }];
  const { plan: p, notes } = ok({ tables: [
    { name: "products", fields: [{ name: "title", type: "number" }, { name: "price", type: "number" }] },
    { name: "users", fields: [{ name: "email", type: "email" }, { name: "phone", type: "text" }, { name: "name", type: "text" }], access: "public" },
  ] }, existing);
  assert.equal(p.tables[0].existingId, "c1");
  assert.deepEqual(p.tables[0].fields.map((f) => f.name), ["price"]);
  assert.deepEqual(p.tables[0].access, {}); // not named → unchanged
  assert.deepEqual(p.tables[1].fields.map((f) => f.name), ["phone"]);
  assert.deepEqual(p.tables[1].access, {});
  assert.ok(notes.some((n) => n.includes("products.title") && n.includes("already exists")));
  assert.ok(notes.some((n) => n.includes("users: already exists, so its access was left as it is")));
  assert.equal(describePlan(p)[0], "products: added 1 field");
});

test("pasting the same schema twice is a no-op error, not a crash", () => {
  const existing = [USERS, { id: "c1", name: "notes", type: "base", fields: [{ name: "title", type: "text" }] }];
  err({ tables: [{ name: "notes", fields: [{ name: "title", type: "text" }] }] }, /Nothing to import/, existing);
});

test("owner access requires a link to users", () => {
  err({ tables: [{ name: "carts", fields: [{ name: "status", type: "text" }], access: "owner" }] }, /owner.*users/);
  ok({ tables: [{ name: "carts", fields: [{ name: "owner", type: "relation", table: "users" }], access: { list: "owner" } }] });
});

test("clear errors for things that can't be mapped", () => {
  err("not json", /Not valid JSON/);
  err({ tables: [] }, /at least one table/);
  err({ tables: [{ name: "1bad" }] }, /table names/);
  err({ tables: [{ name: "_hidden" }] }, /reserved|table names/);
  err({ tables: [{ name: "a" }, { name: "A" }] }, /appears twice/);
  err({ tables: [{ name: "a", fields: [{ name: "x", type: "hologram" }] }] }, /unknown type "hologram"/);
  err({ tables: [{ name: "a", fields: [{ name: "x", type: "select" }] }] }, /values/);
  err({ tables: [{ name: "a", fields: [{ name: "x", type: "relation", table: "ghosts" }] }] }, /ghosts/);
  err({ tables: [{ name: "a", fields: [{ name: "x", type: "text" }, { name: "X", type: "text" }] }] }, /appears twice/);
  err({ tables: [{ name: "a", fields: [{ name: "expand", type: "text" }] }] }, /built-in/);
  err({ tables: [{ name: "a", access: { read: "martians" } }] }, /public, signedIn/);
  err({ tables: [{ name: "a", access: { fly: "public" } }] }, /unknown access action/);
  err({ tables: [{ name: "a", fields: [{ name: "n", type: "number", min: 5, max: 1 }] }] }, /min/);
});

test("rules map to the dashboard presets", () => {
  assert.equal(ruleFor("list", "public", null), "");
  assert.equal(ruleFor("list", "admin", null), null);
  assert.equal(ruleFor("list", "owner", "owner"), "@request.auth.id != '' && owner = @request.auth.id");
});
