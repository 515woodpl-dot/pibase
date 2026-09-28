import { APP as B, PB, psql, enc, ok, must, sleep, browser, requireApp, fixture, WORK_DIR, controlHeaders } from "./lib.mjs";
import { execSync } from "node:child_process";
const J = { "content-type": "application/json" };
const C = controlHeaders(); // setup below is "DATBase-internal", so it isn't counted

// Superuser prep straight on PocketBase: a public "notes" table, open sign-ups
const { token: T } = await (await fetch(PB + "/api/collections/_superusers/auth-with-password", { method: "POST", headers: { ...J, ...C }, body: JSON.stringify({ identity: "admin@proj.test", password: "SuperSecret123" }) })).json();
await fetch(PB + "/api/collections", { method: "POST", headers: { ...J, ...C, Authorization: T }, body: JSON.stringify({ name: "notes", type: "base", listRule: "", createRule: "", fields: [{ name: "t", type: "text" }, { name: "f", type: "file", maxSelect: 1, maxSize: 1000000 }] }) });
await fetch(PB + "/api/collections/users", { method: "PATCH", headers: { ...J, ...C, Authorization: T }, body: JSON.stringify({ createRule: "" }) });
const signup = (email) => fetch(PB + "/api/collections/users/records", { method: "POST", headers: J, body: JSON.stringify({ email, password: "12345678", passwordConfirm: "12345678" }) });

const b = await browser();
const a = await (await b.newContext()).newPage(); a.on("dialog", (d) => d.accept());
await a.goto(B + "/signup"); await a.fill("input[name=name]", "T"); await a.fill("input[name=email]", "a@test.com");
await a.fill("input[name=password]", "password123"); await a.click("button:has-text('Create account')"); await a.waitForURL(B + "/dashboard", { waitUntil: "commit" });
const uid = psql(`select id from "user" where email=\'a@test.com\'`);
psql(`insert into project (id, owner_id, name, slug, status, admin_email, admin_password_enc, created_at) values ('proj00000000000a','${uid}','Shop','8090','active','admin@proj.test','${enc("SuperSecret123")}', now() - interval '5 minutes')`);
const P = B + "/dashboard/proj00000000000a";
const refresh = async () => { await a.goto(P + "/usage"); await a.click("button:has-text('Refresh')"); await a.waitForSelector("button:has-text('Refresh'):not([disabled])"); await a.goto(P + "/usage"); };
const meter = async (label) => (await a.locator(`div.space-y-2:has(span.font-medium:text-is("${label}"))`).innerText()).replace(/\n/g, " ");

// 1. First measurement, nothing blocked
await refresh(); ok("usage measured:", await meter("Database"), "|", await meter("App users"));

// 2. 25 anonymous API calls + dashboard browsing (must NOT count)
for (let i = 0; i < 25; i++) await fetch(PB + "/api/collections/notes/records");
for (const t of ["/tables", "/tables/notes", "/tables/users"]) await a.goto(P + t);
// Counting happens in the project itself (no log delay), so no waiting is needed.
// F-06: two collectors at the same moment (two Refresh clicks) must not double-count
const a2 = await a.context().newPage(); await a2.goto(P + "/usage"); await a.goto(P + "/usage");
await Promise.all([a.click("button:has-text('Refresh')"), a2.click("button:has-text('Refresh')")]);
await Promise.all([a, a2].map((x) => x.waitForSelector("button:has-text('Refresh'):not([disabled])")));
await a2.close(); await a.goto(P + "/usage");
const req = await meter(`API requests in ${new Date().toLocaleString("en-US", { month: "long", timeZone: "UTC" })}`);
must(req.includes("25 / 20"), "expected exactly 25 requests counted, got " + req); ok("F-06 two simultaneous Refreshes → still exactly 25 counted:", req);
must(await a.locator("text=Plan limit reached").count(), "banner"); ok("banner shown:", (await a.locator("a:has-text('Plan limit reached')").innerText()).slice(0, 90) + "…");
const r1 = await fetch(PB + "/api/collections/notes/records"); must(r1.status === 429, "api blocked"); ok("customer API now returns", r1.status);
await a.goto(P + "/tables/notes"); must(await a.locator("text=No records yet").count(), "dashboard still works"); ok("dashboard still works while API is blocked");
await a.goto(B + "/dashboard"); must(await a.locator("text=Limit reached").count(), "list badge"); ok("'Limit reached' badge on project list");
await a.screenshot({ path: `${WORK_DIR}/s4-list.png` });

// 3. Users limit (2): dashboard creates users (superuser, allowed even while API is blocked)
for (const e of ["u1@x.co", "u2@x.co"]) {
  await a.goto(P + "/tables/users/new"); await a.fill("input[name=f_email]", e); await a.fill("input[name=password]", "12345678");
  await a.click("button:has-text('Create record')"); await a.waitForURL(P + "/tables/users", { waitUntil: "commit" }); await a.waitForSelector(`td:has-text('${e}')`);
}
await refresh(); await a.goto(P + "/tables/users/new"); await a.fill("input[name=f_email]", "u3@x.co"); await a.fill("input[name=password]", "12345678");
await a.click("button:has-text('Create record')"); await a.waitForSelector("p.text-red-600");
ok("user limit: 3rd user blocked →", await a.locator("p.text-red-600").innerText());

// 4. Storage limit (300 KB): upload 350 KB, then writes are blocked, deletes still work
await a.goto(P + "/tables/notes/new"); await a.fill("input[name=f_t]", "big"); await a.setInputFiles("input[name=f_f]", fixture("big.bin", Buffer.alloc(350000, 7)));
await a.click("button:has-text('Create record')"); await a.waitForURL(P + "/tables/notes", { waitUntil: "commit" });
await refresh(); ok("after upload:", await meter("File storage"));
await a.goto(P + "/tables/notes/new"); await a.fill("input[name=f_t]", "another"); await a.click("button:has-text('Create record')"); await a.waitForSelector("p.text-red-600");
ok("storage limit: new record blocked →", await a.locator("p.text-red-600").innerText());
await a.goto(P + "/usage"); await a.screenshot({ path: `${WORK_DIR}/s4-usage.png`, fullPage: true });

// 5. Free space + remove a user → writes and sign-ups unblock (monthly request block stays)
await a.goto(P + "/tables/notes"); await a.click("tr:has-text('big') button:has-text('Delete')"); await a.waitForSelector("td:has-text('big')", { state: "detached" });
await a.goto(P + "/tables/users"); await a.click("tr:has-text('u2@x.co') button:has-text('Delete')"); await a.waitForSelector("td:has-text('u2@x.co')", { state: "detached" });
await refresh();
const u = psql("select block_all, block_writes, block_signups from project_usage");
must(u === "t|f|f", "expected only blockAll, got " + u); ok("after cleanup: writes + sign-ups unblocked, monthly request block kept:", u);
await a.goto(P + "/tables/notes/new"); await a.fill("input[name=f_t]", "fits now"); await a.click("button:has-text('Create record')");
await a.waitForURL(P + "/tables/notes", { waitUntil: "commit" }); ok("record creation works again");

// 6. Isolation: another account can't trigger refresh on this project's usage page
const bctx = await (await b.newContext()).newPage();
await bctx.goto(B + "/signup"); await bctx.fill("input[name=name]", "B"); await bctx.fill("input[name=email]", "b@test.com");
await bctx.fill("input[name=password]", "password123"); await bctx.click("button:has-text('Create account')"); await bctx.waitForURL(B + "/dashboard", { waitUntil: "commit" });
must((await bctx.goto(P + "/usage")).status() === 404, "usage isolation"); ok("user B gets 404 on A's usage page");
await b.close();
