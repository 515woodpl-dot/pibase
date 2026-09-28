import { APP as B, PB, psql, enc, ok, must, sleep, browser, requireApp, fixture, WORK_DIR } from "./lib.mjs";
import { execSync } from "node:child_process";

const b = await browser();
async function signup(email) {
  const p = await (await b.newContext()).newPage();
  p.on("dialog", (d) => d.accept());
  await p.goto(B + "/signup"); await p.fill("input[name=name]", "T"); await p.fill("input[name=email]", email);
  await p.fill("input[name=password]", "password123"); await p.click("button:has-text('Create account')"); await p.waitForURL(B + "/dashboard", { waitUntil: "commit" });
  return p;
}
const a = await signup("a@test.com");
const uid = psql(`select id from "user" where email=\'a@test.com\'`);
psql(`insert into project (id, owner_id, name, slug, status, admin_email, admin_password_enc) values ('proj00000000000a','${uid}','Shop','8090','active','admin@proj.test','${enc("SuperSecret123")}')`);
const P = B + "/dashboard/proj00000000000a";

// Tables tab shows the built-in users table
await a.goto(P + "/tables"); await a.waitForSelector("text=users"); ok("tables list loads from PocketBase (shows 'users')");

// Create table → lands on settings
await a.fill("input[name=name]", "products"); await a.click("button:has-text('Create table')");
await a.waitForURL(P + "/tables/products/settings", { waitUntil: "commit" }); ok("created table products");

async function addField(name, type, opts = {}) {
  const f = a.locator("form:has(button:has-text('Add field'))");
  await f.locator("input[name=name]").fill(name); await f.locator("select[name=type]").selectOption(type);
  if (opts.values) await f.locator("input[name=values]").fill(opts.values);
  if (opts.target) await f.locator("select[name=target]").selectOption({ label: opts.target });
  if (opts.required) await f.locator("input[name=required]").check();
  await f.locator("button:has-text('Add field')").click();
  await a.waitForSelector(`li:has(span.font-mono:text-is("${name}"))`);
}
await addField("name", "text", { required: true });
await addField("price", "number"); await addField("in_stock", "bool"); await addField("size", "select", { values: "S, M, L" });
await addField("meta", "json"); await addField("photo", "file");
ok("added 6 fields (text, number, bool, choice, json, file)");

// Bad field name error
{ const f = a.locator("form:has(button:has-text('Add field'))"); await f.locator("input[name=name]").fill("bad name");
  await f.locator("button:has-text('Add field')").click(); await a.waitForSelector("text=Field names use letters"); ok("invalid field name rejected"); }

// Access: make list+view public
await a.selectOption("select[name=list]", "public"); await a.selectOption("select[name=view]", "public");
await a.click("button:has-text('Save access')"); await a.waitForSelector("text=Saved");
const pub = await fetch(PB + "/api/collections/products/records"); must(pub.status === 200, "public list should be 200");
const pubCreate = await fetch(PB + "/api/collections/products/records", { method: "POST", headers: {"content-type":"application/json"}, body: '{"name":"x"}' });
must(pubCreate.status >= 400, "public create should be denied"); ok(`access rules applied: public read ${pub.status}, anonymous create ${pubCreate.status}`);

// New record with file
await a.goto(P + "/tables/products/new");
await a.fill("input[name=f_name]", "Mug"); await a.fill("input[name=f_price]", "12.5"); await a.check("input[name=f_in_stock]");
await a.selectOption("select[name=f_size]", "M"); await a.fill("textarea[name=f_meta]", '{"color":"blue"}');
await a.setInputFiles("input[name=f_photo]", fixture("photo.txt", "a photo")); await a.click("button:has-text('Create record')");
await a.waitForURL(P + "/tables/products", { waitUntil: "commit" }); await a.waitForSelector("td:has-text('Mug')"); ok("record created with file upload");
await a.screenshot({ path: `${WORK_DIR}/s3-data.png`, fullPage: true });

// Required field validation
await a.goto(P + "/tables/products/new"); await a.fill("input[name=f_price]", "3");
await a.evaluate(() => document.querySelector("input[name=f_name]").removeAttribute("required"));
await a.click("button:has-text('Create record')"); await a.waitForSelector("p.text-red-600"); ok("PocketBase validation error shown:", await a.locator("p.text-red-600").innerText());
await a.fill("input[name=f_name]", "Plate"); await a.click("button:has-text('Create record')"); await a.waitForURL(P + "/tables/products", { waitUntil: "commit" });

// File opens through dashboard proxy
const fileLink = await a.locator("td a[href*='/files/']").first().getAttribute("href");
const fr = await a.request.get(B + fileLink); must((await fr.text()) === "a photo", "file content"); ok("file opens via dashboard");

// Filter + sort
await a.fill("input[name=filter]", "price > 10"); await a.click("button:has-text('Apply')");
await a.waitForSelector("td:has-text('Mug')"); must(!(await a.locator("td:has-text('Plate')").count()), "filter"); ok("filter 'price > 10' works");
await a.fill("input[name=filter]", "price >>> 1"); await a.click("button:has-text('Apply')"); await a.waitForSelector(".card.text-sm"); ok("bad filter shows a message, no crash");
await a.goto(P + "/tables/products?sort=price"); const first = await a.locator("tbody tr td").first().innerText(); must(first === "Plate", "sort asc"); ok("sort by price works");

// Edit: change price, remove file
await a.click("tr:has-text('Mug') a:has-text('Edit')"); await a.fill("input[name=f_price]", "15");
await a.check("input[name=remove_photo]"); await a.click("button:has-text('Save changes')"); await a.waitForURL(P + "/tables/products", { waitUntil: "commit" });
must(await a.locator("td:has-text('15')").count(), "edited"); must(!(await a.locator("td a[href*='/files/']").count()), "file removed"); ok("edit record + remove file works");
await a.screenshot({ path: `${WORK_DIR}/s3-edit.png`, fullPage: true });

// Relation: orders → products
await a.goto(P + "/tables"); await a.fill("input[name=name]", "orders"); await a.click("button:has-text('Create table')"); await a.waitForURL(P + "/tables/orders/settings", { waitUntil: "commit" });
await addField("product", "relation", { target: "products" }); await a.goto(P + "/tables/orders/new");
await a.fill("input[aria-label='Search product']", "Mug"); await a.click("ul button:has-text('Mug')"); await a.click("button:has-text('Create record')"); await a.waitForURL(P + "/tables/orders", { waitUntil: "commit" });
ok("relation field: order linked to 'Mug'");

// Users tab: create app user, who can then sign in via the API
await a.click("nav a:has-text('Users')"); await a.waitForURL(P + "/tables/users", { waitUntil: "commit" }); await a.click("a:has-text('New user')");
await a.fill("input[name=f_email]", "customer@app.com"); await a.fill("input[name=f_name]", "Cust"); await a.fill("input[name=password]", "custpass123");
await a.click("button:has-text('Create record')"); await a.waitForURL(P + "/tables/users", { waitUntil: "commit" }); await a.waitForSelector("td:has-text('customer@app.com')");
const login = await fetch(PB + "/api/collections/users/auth-with-password", { method: "POST", headers: {"content-type":"application/json"}, body: JSON.stringify({ identity: "customer@app.com", password: "custpass123" }) });
must(login.status === 200, "user login"); ok("app user created in dashboard can sign in via API");
await a.screenshot({ path: `${WORK_DIR}/s3-users.png`, fullPage: true });

// Remove a field, delete record, delete table
await a.goto(P + "/tables/products/settings"); await a.click("li:has(span.font-mono:text-is('meta')) button:has-text('Remove')");
await a.waitForSelector("li:has(span.font-mono:text-is('meta'))", { state: "detached" }); ok("field removed");
await a.goto(P + "/tables/products"); await a.click("tr:has-text('Plate') button:has-text('Delete')"); await a.waitForSelector("td:has-text('Plate')", { state: "detached" }); ok("record deleted");
await a.goto(P + "/tables/orders/settings"); await a.fill("input[name=confirm]", "orders"); await a.click("button:has-text('Delete table')");
await a.waitForURL(P + "/tables", { waitUntil: "commit" }); must(!(await a.locator("span.font-mono:text-is('orders')").count()), "table deleted"); ok("table deleted (typed confirmation)");

// Isolation: user B can't reach A's project pages or files
const bp = await signup("b@test.com");
for (const path of ["/tables", "/tables/products", "/tables/products/settings", "/tables/users"]) {
  const r = await bp.goto(P + path); must(r.status() === 404, "isolation " + path);
}
const fb = await bp.request.get(B + fileLink); must(fb.status() === 404, "file isolation");
ok("user B gets 404 on A's tables, settings, users and files");
await b.close();
