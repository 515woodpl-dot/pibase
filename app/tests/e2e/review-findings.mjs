import { APP as B, PB, psql, enc, ok, must, sleep, browser, requireApp, fixture, WORK_DIR } from "./lib.mjs";
import { execSync } from "node:child_process";
const J = { "content-type": "application/json" };
const cspErrors = [];

const b = await browser();
const ctx = await b.newContext();
const a = await ctx.newPage(); a.on("dialog", (d) => d.accept());
a.on("console", (m) => { if (/Content Security Policy|CSP/i.test(m.text())) cspErrors.push(m.text()); });
await a.goto(B + "/signup"); await a.fill("input[name=name]", "T"); await a.fill("input[name=email]", "a@test.com");
await a.fill("input[name=password]", "password123"); await a.click("button:has-text('Create account')"); await a.waitForURL(B + "/dashboard", { waitUntil: "commit" });
const uid = psql(`select id from "user" where email=\'a@test.com\'`);
psql(`insert into project (id, owner_id, name, slug, status, admin_email, admin_password_enc) values ('proj00000000000a','${uid}','Shop','8090','active','admin@proj.test','${enc("SuperSecret123")}')`);
const P = B + "/dashboard/proj00000000000a";

// ---- F-01: 5 parallel creates with 1 existing project, plan allows 2
await a.goto(B + "/dashboard");
const pages = await Promise.all([1,2,3,4,5].map(async (i) => { const p = await ctx.newPage(); await p.goto(B + "/dashboard"); await p.fill("input[name=name]", "Race " + i); return p; }));
await Promise.all(pages.map((p) => p.click("button:has-text('Create project')")));
await new Promise((r) => setTimeout(r, 4000));
const n = Number(psql(`select count(*) from project where owner_id='${uid}'`));
must(n === 2, `expected 2 projects, got ${n}`); ok(`F-01 5 parallel creates → account has ${n} projects (limit 2)`);
for (const p of pages) await p.close();

// ---- F-02 / F-07 / F-08: field & value validation
await a.goto(P + "/tables"); await a.fill("input[name=name]", "products"); await a.click("button:has-text('Create table')");
await a.waitForURL(P + "/tables/products/settings", { waitUntil: "commit" });
const form = a.locator("form:has(button:has-text('Add field'))");
async function tryField(name, type, extra = async () => {}) {
  await form.locator("input[name=name]").fill(name); await form.locator("select[name=type]").selectOption(type); await extra();
  await form.locator("button:has-text('Add field')").click();
  await a.waitForSelector("form button:has-text('Adding…')", { timeout: 2000 }).catch(() => {});
  await a.waitForSelector("form:has(button:has-text('Add field')) button:has-text('Add field'):not([disabled])"); // action finished
  await a.waitForTimeout(300);
  return (await form.locator("p.text-red-600").count()) ? await form.locator("p.text-red-600").innerText() : "OK";
}
for (const nm of ["id", "ID", "created", "collectionId", "expand"]) { const r = await tryField(nm, "text"); must(r !== "OK", "reserved " + nm); }
ok("F-02 reserved names rejected (id, ID, created, collectionId, expand):", await tryField("id", "text"));
must((await tryField("qty", "number")) === "OK", "qty add"); await a.waitForSelector("li:has(span.font-mono:text-is('qty'))");
ok("F-02 duplicate name rejected:", await tryField("QTY", "text"));
must((await tryField("name", "text")) === "OK", "name add");
ok("F-07 duplicate choices:", await tryField("size", "select", () => form.locator("input[name=values]").fill("alpha,beta,beta")));
ok("F-07 101 MB:", await tryField("pic", "file", () => form.locator("input[name=maxSizeMb]").fill("101")));
ok("F-07 0 MB:", await tryField("pic", "file", () => form.locator("input[name=maxSizeMb]").fill("0")));
// id field still healthy → records can be created
await a.goto(P + "/tables/products/new"); await a.fill("input[name=f_name]", "Mug"); await a.fill("input[name=f_qty]", "3");
await a.click("button:has-text('Create record')"); await a.waitForURL(P + "/tables/products", { waitUntil: "commit" }); await a.waitForSelector("td:has-text('Mug')");
ok("F-02 table still creates records normally");
await a.goto(P + "/tables/products/new"); await a.fill("input[name=f_name]", "Inf");
await a.evaluate(() => { const i = document.querySelector("input[name=f_qty]"); i.type = "text"; i.value = "1e309"; });
await a.click("button:has-text('Create record')"); await a.waitForSelector("p.text-red-600");
ok("F-08 1e309 rejected:", await a.locator("p.text-red-600").innerText());

// ---- F-03: internal collections
for (const path of ["/tables/_superusers", "/tables/_superusers/settings", "/tables/_otps", "/tables/_superusers/abc123"]) {
  const r = await a.goto(P + path); must(r.status() === 404, path + " → " + r.status());
}
const pbcSuper = (await (await fetch(PB + "/api/collections/_superusers/auth-with-password", { method: "POST", headers: J, body: JSON.stringify({ identity: "admin@proj.test", password: "SuperSecret123" }) })).json()).record.collectionId;
must((await a.request.get(`${P}/files/${pbcSuper}/abc/x.txt`)).status() === 404, "file proxy internal");
ok("F-03 _superusers/_otps pages, settings, record and file routes → 404");
// Replay the real deleteRecord server action with table rewritten to _superusers
const superId = JSON.parse(execSync(`curl -s -X POST ${PB}/api/collections/_superusers/auth-with-password -H 'content-type: application/json' -d '{"identity":"admin@proj.test","password":"SuperSecret123"}'`).toString()).record.id;
await a.goto(P + "/tables/products");
let rewritten = false;
await a.route("**/dashboard/**", async (route) => {
  const req = route.request();
  if (req.method() === "POST" && req.headers()["next-action"] && req.postData()?.includes('"products"')) {
    rewritten = true;
    const body = req.postData().replace('"products"', '"_superusers"').replace(/"[a-z0-9]{15}"\]/, `"${superId}"]`);
    return route.continue({ postData: body });
  }
  return route.continue();
});
await a.click("tr:has-text('Mug') button:has-text('Delete')"); await a.waitForTimeout(2500); await a.unroute("**/dashboard/**");
const still = await fetch(PB + "/api/collections/_superusers/auth-with-password", { method: "POST", headers: J, body: JSON.stringify({ identity: "admin@proj.test", password: "SuperSecret123" }) });
must(rewritten && still.status === 200, "superuser must still exist");
ok("F-03 replayed deleteRecord with table=_superusers + real superuser id → blocked, superuser intact");

// ---- F-05: relation picker with 250 target records
const { token: T } = await still.json();
for (let i = 0; i < 250; i += 25) await Promise.all(Array.from({ length: 25 }, (_, k) => fetch(PB + "/api/collections/products/records", { method: "POST", headers: { ...J, Authorization: T }, body: JSON.stringify({ name: `Item ${i + k}` }) })));
await a.goto(P + "/tables"); await a.fill("input[name=name]", "orders"); await a.click("button:has-text('Create table')"); await a.waitForURL(P + "/tables/orders/settings", { waitUntil: "commit" });
await form.locator("input[name=name]").fill("product"); await form.locator("select[name=type]").selectOption("relation");
await form.locator("select[name=target]").selectOption({ label: "products" }); await form.locator("button:has-text('Add field')").click();
await a.waitForSelector("li:has(span.font-mono:text-is('product'))");
await a.goto(P + "/tables/orders/new"); await a.fill("input[aria-label='Search product']", "Item 249");
await a.click("ul button:has-text('Item 249')"); await a.click("button:has-text('Create record')"); await a.waitForURL(P + "/tables/orders", { waitUntil: "commit" });
await a.click("a:has-text('Edit')"); await a.waitForSelector("span:has-text('Item 249')");
ok("F-05 relation picker finds record #250 by search, saves it, shows it on edit");

// ---- F-04: headers + nonce CSP; no CSP violations while using the app
const h = (await a.request.get(P + "/tables")).headers();
must(/nonce-/.test(h["content-security-policy"]) && /frame-ancestors 'none'/.test(h["content-security-policy"]), "csp");
must(h["x-frame-options"] === "DENY" && h["referrer-policy"] === "strict-origin-when-cross-origin" && h["x-content-type-options"] === "nosniff", "headers");
must(!h["x-powered-by"], "x-powered-by");
const h2 = (await a.request.get(B + "/login")).headers(); must(/nonce-/.test(h2["content-security-policy"] ?? ""), "login csp");
must(cspErrors.length === 0, "CSP violations: " + cspErrors.join(" | "));
ok("F-04 CSP with nonce + frame-ancestors, X-Frame-Options, Referrer-Policy, nosniff; 0 CSP violations during this run");

// ---- F-09: sign-up rate limit per IP (production mode)
const codes = [];
for (let i = 0; i < 7; i++) {
  const r = await fetch(B + "/api/auth/sign-up/email", { method: "POST", headers: { ...J, origin: B, "x-forwarded-for": "203.0.113.9" }, body: JSON.stringify({ email: `spam${i}@x.co`, password: "password123", name: "s" }) });
  codes.push(r.status); await new Promise((r) => setTimeout(r, 3500));
}
must(codes.slice(0, 5).every((c) => c === 200) && codes.slice(5).every((c) => c === 429), "signup limit " + codes);
ok("F-09 sign-ups from one IP:", codes.join(","), "(6th+ blocked for the hour)");
await b.close();
