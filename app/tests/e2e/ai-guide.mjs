import { APP as B, PB, psql, enc, ok, must, sleep, browser, requireApp, fixture, WORK_DIR, controlHeaders } from "./lib.mjs";
import { execSync } from "node:child_process";
import { writeFileSync } from "node:fs";
const ts = requireApp("typescript");
const J = { "content-type": "application/json" };
async function expectErr(p, status, re) { try { await p; } catch (e) { must(e.name === "DatbaseError" && e.status === status && (!re || re.test(e.message + JSON.stringify(e.fieldErrors))), `expected ${status} ${re}, got ${e.status} ${e.message} ${JSON.stringify(e.fieldErrors)}`); return e; } throw new Error(`FAIL: expected ${status}, got success`); }

// ---------- dashboard setup (owner-only preset through the real UI) ----------
const b = await browser();
const a = await (await b.newContext()).newPage(); a.on("dialog", (d) => d.accept());
await a.goto(B + "/signup"); await a.fill("input[name=name]", "T"); await a.fill("input[name=email]", "a@test.com");
await a.fill("input[name=password]", "password123"); await a.click("button:has-text('Create account')"); await a.waitForURL(B + "/dashboard", { waitUntil: "commit" });
const uid = psql(`select id from "user" where email=\'a@test.com\'`);
psql(`insert into project (id, owner_id, name, slug, status, admin_email, admin_password_enc) values ('proj00000000000a','${uid}','Shop','8090','active','admin@proj.test','${enc("SuperSecret123")}')`);
const P = B + "/dashboard/proj00000000000a";
const form = a.locator("form:has(button:has-text('Add field'))");
async function table(name) { await a.goto(P + "/tables"); await a.fill("input[name=name]", name); await a.click("button:has-text('Create table')"); await a.waitForURL(P + `/tables/${name}/settings`, { waitUntil: "commit" }); }
async function field(name, type, o = {}) {
  await form.locator("input[name=name]").fill(name); await form.locator("select[name=type]").selectOption(type);
  if (o.values) await form.locator("input[name=values]").fill(o.values);
  if (o.target) await form.locator("select[name=target]").selectOption({ label: o.target });
  if (o.multiple) await form.locator("input[name=multiple]").check();
  if (o.required) await form.locator("input[name=required]").check();
  await form.locator("button:has-text('Add field')").click(); await a.waitForSelector(`li:has(span.font-mono:text-is("${name}"))`);
}
await table("products");
await field("name", "text", { required: true }); await field("price", "number"); await field("photo", "file");
await field("gallery", "file", { multiple: true }); await field("tags", "select", { values: "new,sale", multiple: true }); await field("released", "date");
await a.selectOption("select[name=list]", "public"); await a.selectOption("select[name=view]", "public"); await a.click("button:has-text('Save access')"); await a.waitForSelector("text=Saved");
await table("orders");
must(await a.locator("text=“Only its owner” option appears").count(), "owner hint before owner field");
await field("owner", "relation", { target: "users" }); await field("product", "relation", { target: "products" }); await field("qty", "number");
await a.reload();
const opts = await a.locator("select[name=list] option").allInnerTexts(); must(opts.includes("Only its owner (owner)"), "owner option: " + opts);
for (const op of ["list", "view", "create", "update", "delete"]) await a.selectOption(`select[name=${op}]`, "owner");
await a.click("button:has-text('Save access')"); await a.waitForSelector("text=Saved"); await a.reload();
must((await a.locator("select[name=update]").inputValue()) === "owner", "owner persisted");
ok("dashboard: 'Only its owner' option appears once a users link exists, saves and reloads as owner");
await a.screenshot({ path: `${WORK_DIR}/s7-access.png`, fullPage: true });

// ---------- DATBASE.md ----------
const resp = await a.request.get(P + "/datbase-md"); must(resp.status() === 200, "download");
must(/attachment; filename="DATBASE.md"/.test(resp.headers()["content-disposition"]), "attachment");
const md = await resp.text(); writeFileSync(`${WORK_DIR}/DATBASE.md`, md);
const leaks = md.match(/pocketbase|_superusers|pb_|superuser auth|\/_\//gi); must(!leaks, "engine names leaked: " + leaks);
must(md.includes("Only the record's owner (`owner` = signed-in user)") && md.includes("| `owner` | id of a record in `users`"), "owner documented");
must(md.includes("export interface Product {") && md.includes("export interface Order {"), "types");
ok(`DATBASE.md downloaded (${md.length} chars, ${md.split("\n").length} lines); no engine names; owner rules + types documented`);
const bp = await (await b.newContext()).newPage();
await bp.goto(B + "/signup"); await bp.fill("input[name=name]", "B"); await bp.fill("input[name=email]", "b@test.com");
await bp.fill("input[name=password]", "password123"); await bp.click("button:has-text('Create account')"); await bp.waitForURL(B + "/dashboard", { waitUntil: "commit" });
must((await bp.request.get(P + "/datbase-md")).status() === 404, "guide isolation"); ok("another account gets 404 for this project's DATBASE.md");
await a.goto(P); await a.waitForSelector("text=Build with AI"); await a.screenshot({ path: `${WORK_DIR}/s7-overview.png`, fullPage: true });

// ---------- extract the client exactly as an AI would, compile, run ----------
const block = md.split("## `datbase.ts`: the client")[1].split("```ts\n")[1].split("\n```")[0];
const js = ts.transpileModule(block, { compilerOptions: { module: ts.ModuleKind.ESNext, target: ts.ScriptTarget.ES2022 } }).outputText
  .replace(/export const DATBASE_URL = "[^"]+";/, `export const DATBASE_URL = "${PB}";`);
writeFileSync(`${WORK_DIR}/client.mjs`, js);
const db = await import(`${WORK_DIR}/client.mjs`);
ok("datbase.ts extracted from DATBASE.md and compiled cleanly");

// seed a product with photo + gallery as the server admin
const { token: T } = await (await fetch(PB + "/api/collections/_superusers/auth-with-password", { method: "POST", headers: J, body: JSON.stringify({ identity: "admin@proj.test", password: "SuperSecret123" }) })).json();
const fd = new FormData(); fd.append("name", 'Mug "special"'); fd.append("price", "12.5"); fd.append("tags", JSON.stringify(["new"]));
fd.append("released", "2026-09-27T10:00:00-04:00"); fd.append("photo", new Blob(["PHOTO-BYTES"]), "mug.jpg");
fd.append("gallery", new Blob(["G1"]), "g1.jpg"); fd.append("gallery", new Blob(["G2"]), "g2.jpg");
const mug = await db.create("products", fd, { token: T });
await db.create("products", { name: "Plate", price: 5 }, { token: T });

// anonymous reads
const pub = await db.list("products", { sort: "-price", filter: `name ~ ${db.q('Mug "special"')}` });
must(pub.items.length === 1 && pub.items[0].name === 'Mug "special"', "q() quoting + filter");
must(db.toDate(pub.items[0].released).toISOString() === "2026-09-27T14:00:00.000Z", "toDate + offset→UTC");
must((await (await fetch(db.fileUrl(mug, mug.photo))).text()) === "PHOTO-BYTES", "fileUrl");
ok("anonymous: list + filter with quotes via q(), dates via toDate(), fileUrl serves the image");
await expectErr(db.create("products", { name: "x" }), 403, /superusers/); ok("anonymous create on server-only table → 403 as documented");
await expectErr(db.list("products", { sort: "nope" }), 400); ok("unknown sort → 400 as documented");

// users
const u1 = await db.signUp({ email: "u1@app.co", password: "password123", name: "U1" });
must(db.currentUser().id === u1.id && db.getToken(), "session saved"); ok("signUp() creates the account and signs in:", u1.email);
const tokU1 = db.getToken();
const e1 = await expectErr(db.signUp({ email: "u1@app.co", password: "password123" }), 400);
must(e1.fieldErrors.email === "Value must be unique.", "dup email field error"); ok("duplicate sign-up → fieldErrors.email =", e1.fieldErrors.email);
const e2 = await expectErr(db.signUp({ email: "short@app.co", password: "123" }), 400); ok("short password → fieldErrors.password =", e2.fieldErrors.password);
db.signOut(); must(!db.currentUser() && !db.getToken(), "signOut"); await expectErr(db.signIn("u1@app.co", "wrong"), 400, /Failed to authenticate/);
ok("signOut() clears session; wrong password → 400 'Failed to authenticate.'");
const u2 = await db.signUp({ email: "u2@app.co", password: "password123" }); const tokU2 = db.getToken();

// owner-only orders
const asU1 = { token: tokU1 }, asU2 = { token: tokU2 };
const o1 = await db.create("orders", { owner: u1.id, product: mug.id, qty: 2 }, asU1);
await expectErr(db.create("orders", { owner: u2.id, product: mug.id, qty: 1 }, asU1), 400, /Failed to create record/);
await expectErr(db.create("orders", { product: mug.id, qty: 1 }, asU1), 400);
ok("owner: create with own id OK; spoofed or missing owner → 400 'Failed to create record.' as documented");
must((await db.list("orders", {}, asU2)).items.length === 0, "u2 sees none"); must((await db.list("orders", {}, { token: null })).items.length === 0, "anon sees none");
await expectErr(db.getOne("orders", o1.id, {}, asU2), 404); await expectErr(db.update("orders", o1.id, { qty: 9 }, asU2), 404); await expectErr(db.remove("orders", o1.id, asU2), 404);
await expectErr(db.update("orders", o1.id, { owner: u2.id }, asU1), 404);
ok("owner: other user/anonymous see empty lists; view/edit/delete/reassign → 404 as documented");
const mine = await db.list("orders", { expand: "product" }, asU1);
must(mine.items.length === 1 && mine.items[0].expand.product.name === 'Mug "special"', "expand");
ok("owner lists own order with expand=product →", mine.items[0].expand.product.name);
must((await db.update("orders", o1.id, { qty: 3 }, asU1)).qty === 3, "update own"); await db.remove("orders", o1.id, asU1);
await expectErr(db.getOne("orders", o1.id, {}, asU1), 404); ok("owner updates and deletes own order");
await expectErr(db.list("orders", {}, { token: "garbage" }), 401); await expectErr(db.list("products", {}, { token: "garbage" }), 401);
must((await db.list("orders", {}, { token: null })).items.length === 0, "no token = signed out");
ok("invalid token → 401 on every call (even public tables); no token → signed out, as documented");

// stored session that stopped being valid → 401 and the client signs out by itself
db.signOut(); const me = await db.signIn("u1@app.co", "password123");
await db.update("users", me.id, { password: "password999", passwordConfirm: "password999", oldPassword: "password123" }); // invalidates the session token
await expectErr(db.list("orders"), 401); must(db.getToken() === null && db.currentUser() === null, "client signed out on 401");
ok("session invalidated (password changed elsewhere) → next call 401 and the client signs out by itself");
await db.signIn("u1@app.co", "password999"); await db.update("users", me.id, { password: "password123", passwordConfirm: "password123", oldPassword: "password999" });
// session refresh
db.signOut(); await db.signIn("u1@app.co", "password123"); const again = await db.refreshSession(); must(again && again.id === u1.id, "refresh");
ok("refreshSession() renews and returns the user");

// files: multi-file replace / append / remove, single remove
let g = await db.update("products", mug.id, { "gallery-": [mug.gallery[0]] }, { token: T }); must(g.gallery.length === 1, "gallery- removes");
const add = new FormData(); add.append("gallery+", new Blob(["G3"]), "g3.jpg"); g = await db.update("products", mug.id, add, { token: T }); must(g.gallery.length === 2, "gallery+ appends");
const rep = new FormData(); rep.append("gallery", new Blob(["G4"]), "g4.jpg"); g = await db.update("products", mug.id, rep, { token: T }); must(g.gallery.length === 1, "gallery replaces: " + g.gallery.length);
g = await db.update("products", mug.id, { photo: "" }, { token: T }); must(g.photo === "", "photo removed");
ok("files: field- removes, field+ appends, field replaces, single file cleared with \"\" (all as documented)");
must((await db.listAll("products")).length === 2, "listAll"); ok("listAll() pages through everything");

// plan limits surface as documented
await fetch(PB + "/api/datbase/limits", { method: "POST", headers: { ...J, ...controlHeaders() }, body: '{"blockAll":true}' });
const e429 = await expectErr(db.list("products"), 429, /used all API requests/); ok("monthly limit → 429:", e429.message);
await fetch(PB + "/api/datbase/limits", { method: "POST", headers: { ...J, ...controlHeaders() }, body: '{"blockWrites":true}' });
await expectErr(db.create("orders", { owner: u1.id, qty: 1 }, { token: db.getToken() }), 403, /storage limit/); ok("storage limit → 403 with the documented message");
await fetch(PB + "/api/datbase/limits", { method: "POST", headers: { ...J, ...controlHeaders() }, body: "{}" });
await b.close();
