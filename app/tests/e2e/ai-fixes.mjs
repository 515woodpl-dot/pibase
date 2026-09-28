// "Build with AI" review fixes: AI-02 (number limits, unique rules), AI-03 (server keys), AI-04 (examples),
// AI-05 (typed links), gap 7 (plain JS client), plus the limit-bypass hardening.
// Run via: tests/e2e/run-backups.sh ai-fixes.mjs   (uses backups for the key-reconcile check)
import { execSync } from "node:child_process";
import { writeFileSync } from "node:fs";
import { pathToFileURL } from "node:url";
import { APP, browser, controlHeaders, linkProject, must, ok, PB, psql, requireApp, signup, sleep, WORK_DIR } from "./lib.mjs";

const ts = requireApp("typescript");
const J = { "content-type": "application/json" };

// ---------------------------------------------------------------- dashboard setup (UI only)
const b = await browser();
const a = await signup(b, "a@test.com");
const P = linkProject("a@test.com", { id: "projaaaaaaaaaaaa", name: "Shop", slug: "8090" });
const form = a.locator("form:has(button:has-text('Add field'))");
async function table(name) {
  await a.goto(P + "/tables");
  await a.fill("input[name=name]", name);
  await a.click("button:has-text('Create table')");
  await a.waitForURL(P + `/tables/${name}/settings`, { waitUntil: "commit" });
}
async function field(name, type, o = {}) {
  await form.locator("input[name=name]").fill(name);
  await form.locator("select[name=type]").selectOption(type);
  if (o.target) await form.locator("select[name=target]").selectOption({ label: o.target });
  if (o.min != null) await form.locator("input[name=min]").fill(String(o.min));
  if (o.max != null) await form.locator("input[name=max]").fill(String(o.max));
  if (o.onlyInt) await form.locator("input[name=onlyInt]").check();
  if (o.required) await form.locator("input[name=required]").check();
  if (o.unique) await form.locator("input[name=unique]").check();
  await form.locator("button:has-text('Add field')").click();
  await a.waitForSelector(`li:has(span.font-mono:text-is("${name}"))`);
}
await table("products");
await field("name", "text", { required: true });
await a.selectOption("select[name=list]", "public"); await a.selectOption("select[name=view]", "public");
await a.click("button:has-text('Save access')"); await a.waitForSelector("text=Saved");
await table("reviews");
await field("owner", "relation", { target: "users" });
await field("product", "relation", { target: "products" });
await field("stars", "number", { min: 1, max: 5, onlyInt: true, required: true });
await field("code", "text", { unique: true });
await a.locator("form:has(button:has-text('Add unique rule')) input[value=owner]").check();
await a.locator("form:has(button:has-text('Add unique rule')) input[value=product]").check();
await a.click("button:has-text('Add unique rule')");
await a.waitForSelector("text=unique together");
await a.reload();
for (const op of ["list", "view", "create", "update", "delete"]) await a.selectOption(`select[name=${op}]`, "owner");
await a.click("button:has-text('Save access')"); await a.waitForSelector("text=Saved");
const settings = await a.locator("main").innerText();
must(/whole, min 1, max 5/.test(settings) && /code[\s\S]*unique/.test(settings), "settings show limits + unique");
ok("dashboard: number limits (whole, 1–5, required), unique field and 'owner + product' unique rule set through the UI");
await a.screenshot({ path: `${WORK_DIR}/ai-fixes-settings.png`, fullPage: true });

// ---------------------------------------------------------------- server key (AI-03)
await a.goto(P);
await a.fill("form:has(button:has-text('Create key')) input[name=name]", "production server");
await a.click("button:has-text('Create key')");
const KEY = (await a.locator("[data-testid=new-server-key]").innerText()).trim();
must(KEY.length > 100, "key shown once");
await a.reload();
must(!(await a.content()).includes(KEY), "key not shown again after reload");
ok("server key created in the dashboard; shown once, not visible after reload");
await a.screenshot({ path: `${WORK_DIR}/ai-fixes-keys.png`, fullPage: true });

// ---------------------------------------------------------------- DATBASE.md content (AI-02/03/04/05, gap 7)
const md = await (await a.request.get(P + "/datbase-md")).text();
writeFileSync(`${WORK_DIR}/DATBASE-fixes.md`, md);
must(md.includes("| `stars` | whole number from 1 to 5 | yes |"), "stars documented");
must(md.includes("**Unique together:** `owner` + `product`"), "unique together documented");
must(/\| `code` \| text \| no \| \*\*Unique\*\*/.test(md), "unique field documented");
must(md.includes("validation_not_unique") && md.includes("validation_max_number_constraint"), "error codes");
ok("DATBASE.md: 'whole number from 1 to 5', 'Unique together: owner + product', unique `code`, error codes");
must(!md.includes("12.5") && /"stars":1\b/.test(md), "in-range example");
ok("AI-04: examples use in-range values (stars: 1), no arbitrary 12.5");
must(md.includes("expand?: { owner?: User; product?: Product }"), "typed expand");
ok("AI-05: `expand?: { owner?: User; product?: Product }`");
must(md.includes("## Server-side access (server keys)") && md.includes("DATBASE_SERVER_KEY"), "server section");
must(!/pocketbase|_superusers|impersonate/i.test(md), "no engine names");
must(md.includes("await checkServerKey(process.env.DATBASE_SERVER_KEY)") && /returns `401`\. You never get a silent empty result/.test(md) && md.includes("Expired, invalid or revoked token"), "fail-closed documented");
ok("AI-V4-01: guide says validate the key first, 401 after expiry/revoke, never a silent empty result");
ok("AI-03: server-key section documented (env var, usage, expiry, revoke); still no engine names");

// Strict type-check: interfaces + client + code that uses a linked record without casts
const tsBlocks = [...md.matchAll(/```ts\n([\s\S]*?)\n```/g)].map((m) => m[1]);
const client = md.split("## `datbase.ts`: the client")[1].split("```ts\n")[1].split("\n```")[0];
const interfaces = tsBlocks.filter((x) => x.startsWith("export interface")).join("\n\n");
writeFileSync(`${WORK_DIR}/typecheck.ts`, `${client}\n\n${interfaces}\n\nexport async function demo() {\n  const page = await list<Review>("reviews", { expand: "product" });\n  const productName: string | undefined = page.items[0]?.expand?.product?.name;\n  const stars: number = page.items[0].stars;\n  return { productName, stars };\n}\n`);
execSync(`npx tsc --noEmit --strict --target es2022 --module esnext --moduleResolution bundler --lib es2022,dom ${WORK_DIR}/typecheck.ts`, { cwd: process.env.APP_DIR, stdio: "pipe" });
ok("generated client + interfaces pass `tsc --strict`, including `review.expand.product.name` without casts");

// Plain JS client (gap 7) runs as-is
const jsBlock = md.split("```js\n")[1].split("\n```")[0];
must(!/:\s*(string|number|boolean)\b|interface |<T/.test(jsBlock), "no types left");
writeFileSync(`${WORK_DIR}/datbase.js`, jsBlock.replace(/export const DATBASE_URL = "[^"]+";/, `export const DATBASE_URL = "${PB}";`));
const plain = await import(pathToFileURL(`${WORK_DIR}/datbase.js`).href + "?v=1");
ok("gap 7: plain JavaScript client included and importable without a build step");

// ---------------------------------------------------------------- behaviour with the generated TS client
const js = ts.transpileModule(client, { compilerOptions: { module: ts.ModuleKind.ESNext, target: ts.ScriptTarget.ES2022 } }).outputText
  .replace(/export const DATBASE_URL = "[^"]+";/, `export const DATBASE_URL = "${PB}";`);
writeFileSync(`${WORK_DIR}/client-fixes.mjs`, js);
const db = await import(pathToFileURL(`${WORK_DIR}/client-fixes.mjs`).href);
const asServer = { token: KEY };
const code = async (p) => { try { await p; return "ok"; } catch (e) { return `${e.status} ${Object.entries(e.fieldErrors).map(([k, v]) => k + ":" + v).join(",")}`; } };
const mug = await db.create("products", { name: "Mug" }, asServer);
const plate = await db.create("products", { name: "Plate" }, asServer);
must((await plain.list("products")).items.length === 2, "plain client lists");
ok("server key creates records in a server-only table; plain JS client reads them");

const u1 = await db.signUp({ email: "u1@app.co", password: "password123" });
const t1 = db.getToken();
const u2 = await db.signUp({ email: "u2@app.co", password: "password123" });
const t2 = db.getToken();
must((await code(db.checkServerKey(KEY))) === "ok", "live key passes");
must((await code(db.checkServerKey(t2))).startsWith("403"), "user token is not a server key");
must((await code(db.checkServerKey(undefined))).startsWith("401"), "missing key");
ok("checkServerKey(): live key passes; user token → 403; missing key → 401");
const rv = (u, product, stars, extra = {}) => ({ owner: u.id, product: product.id, stars, ...extra });
must((await code(db.create("reviews", rv(u1, mug, 6), { token: t1 }))).startsWith("400 stars:Must be less or equal than 5"), "max");
must((await code(db.create("reviews", rv(u1, mug, 2.5), { token: t1 }))).startsWith("400 stars:Decimal"), "int");
must((await code(db.create("reviews", rv(u1, mug, 0), { token: t1 }))).startsWith("400 stars:Cannot be blank"), "required 0");
must((await code(db.create("reviews", rv(u1, mug, 5, { code: "A1" }), { token: t1 }))) === "ok", "valid");
const dup = await code(db.create("reviews", rv(u1, mug, 4), { token: t1 }));
must(dup.startsWith("400") && dup.includes("owner:Value must be unique") && dup.includes("product:Value must be unique"), "dup " + dup);
must((await code(db.create("reviews", rv(u2, plate, 3, { code: "A1" }), { token: t2 }))).includes("code:Value must be unique"), "unique code");
ok("backend enforces stars 1–5 whole (6, 2.5 and 0 rejected), unique code, and one review per owner + product");
const race = await Promise.all(Array.from({ length: 10 }, () => code(db.create("reviews", rv(u2, mug, 4), { token: t2 }))));
must(race.filter((r) => r === "ok").length === 1, "race: " + race.join(" | "));
ok("10 simultaneous duplicate reviews → exactly 1 created, 9 rejected by the database");

const all = await db.listAll("reviews", { expand: "product" }, 5000, asServer);
must(all.length === 2 && all.every((r) => r.expand?.product?.name), "server sees all owners");
must((await db.list("reviews", {}, { token: t2 })).items.length === 1, "user sees own only");
ok("server key reads every user's reviews (owner-only table); a user still sees only their own");

// ---------------------------------------------------------------- limit-bypass hardening
for (const [m, path] of [["POST", "/api/datbase/limits"], ["GET", "/api/datbase/usage"], ["POST", "/api/datbase/requests/take"]]) {
  const r = await fetch(PB + path, { method: m, headers: { ...J, Authorization: KEY }, body: m === "POST" ? "{}" : undefined });
  must(r.status === 404, `${path} with server key → ${r.status}`);
}
ok("server key (full admin) can't reach DATBase's limit/usage endpoints (404)");
// Turning off request logs no longer stops counting
await fetch(PB + "/api/settings", { method: "PATCH", headers: { ...J, Authorization: KEY }, body: JSON.stringify({ logs: { maxDays: 0 } }) });
const meterValue = async () => {
  await a.goto(P + "/usage");
  await a.click("button:has-text('Refresh')");
  await a.waitForSelector("button:has-text('Refresh'):not([disabled])");
  await a.goto(P + "/usage");
  const t = await a.locator('div.space-y-2:has(span.font-medium:text-matches("API requests"))').innerText();
  return Number(t.match(/([\d,]+)\s*\//)[1].replace(/,/g, ""));
};
const before = await meterValue();
for (let i = 0; i < 15; i++) await db.list("products", {}, asServer);
const after = await meterValue();
must(after - before === 15, `counted ${after - before} of 15`);
ok(`with request logs switched off by the project admin, 15 server-key requests were still counted exactly (${before} → ${after})`);
await fetch(PB + "/api/datbase/limits", { method: "POST", headers: { ...J, ...controlHeaders() }, body: '{"blockAll":true}' });
must((await code(db.list("products", {}, asServer))).startsWith("429"), "key blocked");
await fetch(PB + "/api/datbase/limits", { method: "POST", headers: { ...J, ...controlHeaders() }, body: "{}" });
ok("monthly limit reached → server-key requests get 429 too");

// ---------------------------------------------------------------- revoke + restore can't resurrect a key
await a.goto(P + "/backups");
await a.click("button:has-text('Back up now')");
for (let i = 0; i < 60 && psql(`select count(*) from backup where status='done'`) === "0"; i++) await sleep(1000);
must(psql(`select count(*) from backup where status='done'`) === "1", "backup with the key inside");
const backupId = psql(`select id from backup where status='done'`);
await a.goto(P);
await a.click("li:has-text('production server') button:has-text('Revoke')");
for (let i = 0; i < 20 && psql(`select count(*) from server_key`) !== "0"; i++) await sleep(500);
must((await code(db.create("products", { name: "x" }, asServer))).startsWith("401"), "revoked create");
must((await code(db.list("reviews", {}, asServer))).startsWith("401"), "revoked key on owner-filtered list must not return an empty 200");
must((await code(db.list("products", {}, asServer))).startsWith("401"), "revoked key on public list");
must((await code(db.checkServerKey(KEY))).startsWith("401"), "checkServerKey after revoke");
ok("revoked key fails closed: 401 on create, owner-filtered list, public list and checkServerKey() (no silent empty result)");
await a.goto(P + "/backups");
await a.click(`li[data-backup-id="${backupId}"] button:has-text('Restore')`);
await a.locator(`li[data-backup-id="${backupId}"] form input[name=confirm]`).fill("Shop");
await a.locator(`li[data-backup-id="${backupId}"] form button.btn-danger`).click();
// Wait for the restore to START (its safety backup appears), then for it to finish.
for (let i = 0; i < 30 && psql(`select count(*) from backup where kind='pre-restore'`) === "0"; i++) await sleep(500);
for (let i = 0; i < 90 && psql(`select status from project where id='projaaaaaaaaaaaa'`) !== "active"; i++) await sleep(1000);
must(psql(`select status from project where id='projaaaaaaaaaaaa'`) === "active", "restored");
must((await code(db.list("reviews", {}, asServer))).startsWith("401"), "key must stay revoked after restore");
ok("restoring a backup taken before the revoke does NOT bring the key back (reconciled after restore)");

// ---------------------------------------------------------------- removing a field removes its unique rule
await a.goto(P + "/tables/reviews/settings");
await a.click("li:has(span.font-mono:text-is('product')) button:has-text('Remove')");
await a.waitForSelector("li:has(span.font-mono:text-is('product'))", { state: "detached" });
must(!(await a.locator("text=unique together").count()), "rule removed with field");
ok("removing a field also removes the unique rule that used it");
await b.close();
