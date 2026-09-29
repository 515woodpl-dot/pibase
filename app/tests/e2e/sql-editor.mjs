// SQL editor ("DATBase SQL"): read-only queries, internal data hidden, owner-only, snippets and folders.
// Needs two engines: PB_PORTS="8090 8091" (run-all.sh sets it). Project A = 8090 (a@sql.test),
// project B = 8091 (b@sql.test), project C = paused (a@sql.test, no engine).
import { execSync } from "node:child_process";
import { readFileSync, writeFileSync } from "node:fs";
import { APP, PB, ok, must, browser, signup, linkProject, pbAdminToken, controlHeaders, psql, sleep, step, WORK_DIR } from "./lib.mjs";

const PBB = "http://127.0.0.1:8091";
const b = await browser();

/* ---------- setup ---------- */
step("setup: accounts, projects, data");
const a = await signup(b, "a@sql.test");
const P = linkProject("a@sql.test", { id: "projsqlaaaaaaaaa", slug: "8090" });
const other = await signup(b, "b@sql.test");
const PB2 = linkProject("b@sql.test", { id: "projsqlbbbbbbbbb", slug: "8091" });
const PC = linkProject("a@sql.test", { id: "projsqlccccccccc", slug: "8092", name: "Paused", status: "paused" });

const token = await pbAdminToken();
const H = { Authorization: token, "content-type": "application/json", ...controlHeaders("8090") };
const pbPost = async (path, body) => {
  const r = await fetch(PB + path, { method: "POST", headers: H, body: JSON.stringify(body) });
  const j = await r.json();
  must(r.ok, `setup ${path}: ${r.status} ${JSON.stringify(j).slice(0, 200)}`);
  return j;
};
await pbPost("/api/collections", {
  name: "items",
  type: "base",
  fields: [{ name: "label", type: "text" }, { name: "qty", type: "number" }, { name: "note", type: "text", hidden: true }],
});
for (const [label, qty] of [["apple", 5], ["banana", 3], ["cherry", 12]]) await pbPost("/api/collections/items/records", { label, qty, note: "hidden-" + label });
await pbPost("/api/collections/users/records", { email: "app.user@sql.test", password: "AppUserPass123", passwordConfirm: "AppUserPass123", name: "Ann" });
const itemCount = async () => (await (await fetch(PB + "/api/collections/items/records?perPage=1", { headers: H })).json()).totalItems;
must((await itemCount()) === 3, "setup created 3 items");
ok("setup: items (with a hidden field) and an app user created with the control header");

// CSP violations are recorded on every page of both accounts.
for (const pg of [a, other]) await pg.addInitScript(() => document.addEventListener("securitypolicyviolation", (e) => ((window.__csp ||= []).push(`${e.violatedDirective} ${e.blockedURI}`))));
const cspViolations = (pg) => pg.evaluate(() => window.__csp || []);

/** POST a query through the dashboard route as a signed-in page (its cookies). */
const run = async (pg, sql, project = P) => {
  const r = await pg.request.post(`${project}/sql/run`, { data: { sql }, headers: { "content-type": "application/json" }, failOnStatusCode: false, maxRedirects: 0 });
  let body = {};
  try {
    body = await r.json();
  } catch {}
  return { status: r.status(), ...body };
};
const leaks = (r) => /\$2a\$|pbkdf|hidden-|tokenKey|"password"|admin@proj\.test/i.test(JSON.stringify(r.rows ?? []) + JSON.stringify(r.columns ?? []));

/* ---------- 1. page ---------- */
step("page loads");
await a.goto(P);
must(await a.locator("nav a:text-is('SQL')").count(), "SQL tab missing from the project tabs");
await a.click("nav a:text-is('SQL')");
await a.waitForSelector(".cm-content[data-testid=sql-input]");
await a.waitForSelector("[data-testid=snippet]");
const pageText = await a.locator("main").innerText();
must(!/pocketbase/i.test(pageText), "the SQL page names the engine");
await a.screenshot({ path: `${WORK_DIR}/sql-editor.png`, fullPage: true });
ok("SQL tab opens the editor (CodeMirror) with the snippets sidebar; the page never names the engine");

/* ---------- 2. starter snippets ---------- */
step("starter snippets");
const starters = psql(`select s.name from sql_snippet s join sql_folder f on f.id = s.folder_id where s.project_id = 'projsqlaaaaaaaaa' and f.name = 'Getting started' order by s.name`).split("\n");
must(starters.length === 5, `expected 5 starter snippets, got ${starters.length}: ${starters}`);
for (const b64 of psql(`select translate(encode(convert_to(sql_text, 'UTF8'), 'base64'), chr(10), '') from sql_snippet where project_id = 'projsqlaaaaaaaaa'`).split("\n")) {
  const sqlText = Buffer.from(b64, "base64").toString("utf8");
  const r = await run(a, sqlText);
  must(r.status === 200, `starter snippet fails: ${sqlText.slice(0, 60)} → ${r.error}`);
}
await a.reload();
await a.waitForSelector("[data-testid=snippet]");
must(psql(`select count(*) from sql_snippet where project_id = 'projsqlaaaaaaaaa'`) === "5", "starters duplicated on a second visit");
// Two first visits at the same moment (project B) still create them once.
const other2 = await (await b.newContext({ storageState: await other.context().storageState() })).newPage();
await Promise.all([other.goto(PB2 + "/sql"), other2.goto(PB2 + "/sql")]);
must(psql(`select count(*) from sql_snippet where project_id = 'projsqlbbbbbbbbb'`) === "5", "concurrent first visits duplicated the starters");
must(psql(`select count(*) from sql_folder where project_id = 'projsqlbbbbbbbbb'`) === "1", "concurrent first visits duplicated the folder");
await other2.close();
ok("starter snippets: 5 in 'Getting started', all run as-is, created once (also with two tabs opening at the same moment)");

/* ---------- 3. running from the UI ---------- */
step("run from the editor");
const type = async (sql) => {
  await a.locator(".cm-content").click();
  await a.keyboard.press("ControlOrMeta+a");
  await a.keyboard.insertText(sql);
};
await type("SELECT label, qty FROM items ORDER BY qty DESC");
await a.keyboard.press("ControlOrMeta+Enter");
await a.waitForSelector("[data-testid=sql-results]");
must((await a.locator("[data-testid=sql-rowcount]").innerText()).startsWith("3 rows"), "Ctrl/Cmd+Enter didn't run the typed query");
must((await a.locator("[data-testid=sql-results] tbody tr").first().innerText()).includes("cherry"), "results not sorted as asked");
// A second, different query with Ctrl/Cmd+Enter (the old editor always re-ran the first one).
await type("SELECT count(*) AS n FROM items WHERE qty > 4");
await a.keyboard.press("ControlOrMeta+Enter");
await a.waitForFunction(() => document.querySelector("[data-testid=sql-rowcount]")?.textContent?.startsWith("1 row"));
must((await a.locator("[data-testid=sql-results] tbody").innerText()).trim() === "2", "second query result wrong");
// With text selected, only the selection runs.
await type("SELECT 'first' AS x;\nSELECT 'second' AS x");
await a.keyboard.press("ControlOrMeta+End");
await a.keyboard.press("Shift+Home");
await a.click("[data-testid=sql-run]");
await a.waitForFunction(() => document.querySelector("[data-testid=sql-results] tbody")?.textContent === "second");
ok("Run and Ctrl/Cmd+Enter run what's in the editor (each time the current text); a selection runs on its own");

/* ---------- 4. read-only and internal data, through the real route ---------- */
step("attacks through the route");
const attacks = [
  `SELECT * FROM "main"."_superusers"`, `SELECT * FROM '_superusers'`, `SELECT * FROM 'main'.'_superusers'`, "SELECT * FROM `_superusers`", `SELECT * FROM [_superusers]`,
  `SELECT * FROM items, '_superusers'`, `SELECT 1 WHERE 1 IN '_superusers'`, `SELECT * FROM sqlite_dbpage`, `SELECT * FROM 'sqlite_dbpage'`, `SELECT * FROM dbstat`,
  `SELECT * FROM 'pragma_table_info'('_superusers')`, `SELECT * FROM pragma_table_list`, `SELECT * FROM sqlite_schema`, `SELECT password FROM users`,
  `SELECT tokenKey FROM users`, `SELECT substr(password, 1, 20) FROM users`, `SELECT * FROM main.users`, `SELECT note FROM items`, `SELECT '[[_superusers]]'`,
  `INSERT INTO items (label) VALUES ('x')`, `UPDATE items SET qty = 0`, `DELETE FROM items`, `WITH a AS (SELECT 1) DELETE FROM items`, `DROP TABLE items`,
  `CREATE TABLE pwned (a)`, `ALTER TABLE items ADD COLUMN x`, `ATTACH 'x.db' AS x`, `VACUUM`, `VACUUM INTO '/tmp/x.db'`, `PRAGMA query_only = OFF`,
  `SELECT 1; DELETE FROM items`, `SELECT 1) UNION SELECT * FROM (SELECT 1`, `SELECT load_extension('x')`, `EXPLAIN SELECT * FROM '_superusers'`,
];
for (const q of attacks) {
  const r = await run(a, q);
  must(r.status !== 200 && r.error && !leaks(r), `not blocked: ${q} → ${r.status} ${JSON.stringify(r).slice(0, 160)}`);
  must(!/pocketbase/i.test(r.error), `error names the engine: ${r.error}`);
}
must((await itemCount()) === 3, "an attack changed data");
must(!(await (await fetch(PB + "/api/collections?perPage=200", { headers: H })).json()).items.some((c) => c.name === "pwned"), "an attack created a table");
ok(`${attacks.length} attacks blocked (quoted/'string'/[bracket] names, raw pages, schema tables, hidden columns, writes, DDL, ATTACH, VACUUM, PRAGMA, 2 statements); data unchanged`);

const users = await run(a, "SELECT * FROM users");
must(users.status === 200 && users.rows.length === 1, "SELECT * FROM users should work: " + users.error);
must(!users.columns.includes("password") && !users.columns.includes("tokenKey") && !leaks(users), "users results expose password/tokenKey: " + JSON.stringify(users));
must(users.columns.includes("email") && JSON.stringify(users.rows).includes("app.user@sql.test"), "users email missing");
const quotedUsers = await run(a, `SELECT * FROM 'users' u JOIN "USERS" v ON u.id = v.id`);
must(quotedUsers.status === 200 && !leaks(quotedUsers), "quoted users spellings leak");
const items = await run(a, "SELECT * FROM items");
must(items.status === 200 && !items.columns.includes("note") && !leaks(items), "a hidden field of a data table is readable");
ok("SELECT * FROM users (any spelling) shows email etc. but no password hash or tokenKey; hidden fields of data tables stay hidden");

for (const [q, check] of [
  ["WITH t AS (SELECT label FROM items WHERE qty > 3) SELECT * FROM t", (r) => r.rows.length === 2],
  ["EXPLAIN QUERY PLAN SELECT * FROM items WHERE qty > 1", (r) => r.columns.join() === "id,parent,detail" && /SCAN/.test(JSON.stringify(r.rows))],
  ["EXPLAIN SELECT 1", (r) => r.columns.includes("opcode")],
  ["SELECT * FROM items WHERE label LIKE '_a%'", (r) => r.rows.length === 1],
  ["SELECT a.id, b.id FROM items a JOIN items b ON a.id = b.id", (r) => r.columns.join() === "id,id:1" && r.rows.length === 3],
  ["VALUES (1, NULL, x'00ff', 1.5)", (r) => JSON.stringify(r.rows) === '[[1,null,"(binary, 2 bytes)",1.5]]'],
  ["SELECT * FROM items LIMIT 0", (r) => r.rows.length === 0 && r.columns.length === 3],
]) {
  const r = await run(a, q);
  must(r.status === 200 && check(r), `allowed query wrong: ${q} → ${JSON.stringify(r).slice(0, 200)}`);
}
ok("WITH, EXPLAIN [QUERY PLAN], LIKE '_…', duplicate column names, NULL/binary/real values and empty results work");

/* ---------- 5. limits ---------- */
step("row cap and timeout");
const big = await run(a, "WITH RECURSIVE c(x) AS (SELECT 1 UNION ALL SELECT x + 1 FROM c LIMIT 5000) SELECT x FROM c");
must(big.status === 200 && big.rows.length === 1000 && big.truncated === true, `row cap: ${big.rows?.length} truncated=${big.truncated}`);
const own = await run(a, "WITH RECURSIVE c(x) AS (SELECT 1 UNION ALL SELECT x + 1 FROM c LIMIT 5000) SELECT x FROM c LIMIT 4000");
must(own.rows.length === 1000 && own.truncated === true, "a query's own LIMIT must not lift the cap");
const t0 = Date.now();
const slow = await run(a, "WITH RECURSIVE c(x) AS (SELECT 1 UNION ALL SELECT x + 1 FROM c) SELECT count(*) FROM c");
const took = Date.now() - t0;
must(slow.status !== 200 && /10 s/.test(slow.error) && took < 13_000, `timeout: ${took} ms, ${JSON.stringify(slow)}`);
await sleep(500);
must(/\[datbase-sql\] query stopped after/.test(readFileSync(`${WORK_DIR}/pb8090.log`, "utf8")), "the engine didn't report stopping the timed-out query");
ok(`1,000-row cap (also against the query's own LIMIT); an endless query stops at 10 s (${took} ms) and the engine really interrupts it`);

step("cancel from the UI");
const logBefore = readFileSync(`${WORK_DIR}/pb8090.log`, "utf8").split("[datbase-sql] query stopped").length;
await type("WITH RECURSIVE c(x) AS (SELECT 1 UNION ALL SELECT x + 1 FROM c) SELECT count(*) FROM c");
await a.click("[data-testid=sql-run]");
await a.waitForSelector("[data-testid=sql-cancel]");
await sleep(800);
const c0 = Date.now();
await a.click("[data-testid=sql-cancel]");
await a.waitForFunction(() => document.querySelector("[data-testid=sql-error]")?.textContent?.includes("Cancelled"));
let stopped = false;
for (let i = 0; i < 30 && !stopped; i++) {
  stopped = readFileSync(`${WORK_DIR}/pb8090.log`, "utf8").split("[datbase-sql] query stopped").length > logBefore;
  if (!stopped) await sleep(100);
}
must(stopped && Date.now() - c0 < 4000, `Cancel didn't stop the query in the engine (${Date.now() - c0} ms)`);
const after = await run(a, "SELECT count(*) AS n FROM items");
must(after.status === 200 && after.rows[0][0] === 3, "a query after Cancel failed");
must((await (await fetch(PB + "/api/collections/items/records", { method: "POST", headers: H, body: JSON.stringify({ label: "after-cancel" }) })).ok), "writes fail after the SQL editor used the engine (query_only left on?)");
must((await itemCount()) === 4, "write after cancel not stored");
ok(`Cancel stops the query in the engine within ${Date.now() - c0} ms; the next query and normal writes work`);

/* ---------- 6. who can reach it ---------- */
step("access");
const superuser = await pbAdminToken();
const su = (await (await fetch(`${PB}/api/collections/_superusers/records`, { headers: H })).json()).items[0].id;
const serverKey = (await (await fetch(`${PB}/api/collections/_superusers/impersonate/${su}`, { method: "POST", headers: H, body: JSON.stringify({ duration: 3600 }) })).json()).token;
const appUser = (await (await fetch(`${PB}/api/collections/users/auth-with-password`, { method: "POST", headers: { "content-type": "application/json" }, body: JSON.stringify({ identity: "app.user@sql.test", password: "AppUserPass123" }) })).json()).token;
must(serverKey && appUser, "could not get a server key and an app-user token");
for (const [who, auth] of [["server key", serverKey], ["superuser token", superuser], ["app user", appUser], ["nobody", null]]) {
  const r = await fetch(`${PB}/api/datbase/sql`, { method: "POST", headers: { "content-type": "application/json", ...(auth ? { Authorization: auth } : {}) }, body: JSON.stringify({ sql: "SELECT 1" }) });
  must(r.status === 404, `${who} reached the engine's SQL endpoint: ${r.status}`);
}
const otherPage = await other.goto(P + "/sql");
must(otherPage.status() === 404, `another account got ${otherPage.status()} for this project's SQL page`);
must((await run(other, "SELECT 1", P)).status === 404, "another account can run queries on this project");
const anon = await (await b.newContext()).request.post(`${P}/sql/run`, { data: { sql: "SELECT 1" }, headers: { "content-type": "application/json" }, maxRedirects: 0, failOnStatusCode: false });
must(anon.status() !== 200, "signed-out request ran a query");
const cross = await a.request.post(`${P}/sql/run`, { data: { sql: "SELECT 1" }, headers: { "content-type": "application/json", "sec-fetch-site": "cross-site" }, failOnStatusCode: false });
const form = await a.request.post(`${P}/sql/run`, { data: "sql=SELECT 1", headers: { "content-type": "text/plain" }, failOnStatusCode: false });
must(cross.status() === 403 && form.status() === 403, `cross-site/form posts must be 403: ${cross.status()} ${form.status()}`);
ok("engine SQL endpoint: 404 for server key, superuser token, app user and no token; other accounts 404; signed-out and cross-site calls refused");

/* ---------- 7. paused project ---------- */
step("paused project");
await a.goto(PC + "/sql");
must(/paused/i.test(await a.locator("main").innerText()), "paused project: no clear message on the SQL page");
const paused = await run(a, "SELECT 1", PC);
must(paused.status === 409 && /paused/i.test(paused.error), `paused project run: ${paused.status} ${paused.error}`);
must(psql(`select count(*) from sql_run where project_id = 'projsqlccccccccc'`) === "0", "a run was recorded for the paused project");
ok("paused project: the SQL page says it is paused, and a run is refused with 409 and a clear message");

/* ---------- 8. queries and folders in the UI: + button and right-click menus ---------- */
step("snippets and folders");
await a.goto(P + "/sql");
await a.waitForSelector("[data-testid=snippet]");
const answers = [];
a.removeAllListeners("dialog");
a.on("dialog", (d) => (d.type() === "prompt" ? d.accept(answers.shift() ?? "") : d.accept()));
const menu = (pg = a) => pg.locator("[data-testid=context-menu]");
const item = (label, pg = a) => menu(pg).locator(`[role=menuitem]:text-is("${label}")`);
const folderRow = (name, pg = a) => pg.locator(`[data-testid=folder][data-name="${name}"]`);
const snippetRow = (name, pg = a) => pg.locator(`[data-testid=snippet][data-name="${name}"]`).first();
const rightClick = async (row, label, pg = a) => {
  await row.click({ button: "right" });
  await item(label, pg).click();
};
const moveTo = async (row, target, pg = a) => {
  await row.click({ button: "right" });
  await menu(pg).locator("[data-testid=move-to]").selectOption({ label: target });
};
const plus = async (label) => {
  await a.click("[data-testid=sql-new]");
  await item(label).click();
};
const noteSays = (text) => a.waitForFunction((t) => document.querySelector("[data-testid=sql-note]")?.textContent?.includes(t), text);

answers.push("Reports");
await plus("New folder");
await folderRow("Reports").waitFor();
answers.push("reports");
await plus("New folder");
await noteSays("already a folder");
ok("+ → New folder creates a folder; a second top-level folder with the same name (any case) is refused");

// Right-click on empty space in the panel offers the same menu as +.
await a.locator("[data-testid=snippets-panel]").click({ button: "right", position: { x: 20, y: 400 } });
must((await item("New query").count()) && (await item("New folder").count()), "right-click on the panel doesn't offer New query / New folder");
await a.keyboard.press("Escape");
must(!(await menu().count()), "Escape doesn't close the menu");

let parent = "Reports";
for (const name of ["L2", "L3", "L4", "L5"]) {
  answers.push(name);
  await rightClick(folderRow(parent), "New subfolder");
  await folderRow(name).waitFor();
  parent = name;
}
answers.push("L6");
await rightClick(folderRow("L5"), "New subfolder");
await noteSays("up to 5 levels");
ok("right-click → New subfolder nests folders 5 levels deep; a 6th level is refused");

answers.push("Scratch");
await plus("New query");
await snippetRow("Scratch").waitFor();
must((await a.locator("[data-testid=sql-open-name]").innerText()).trim() === "Scratch", "+ → New query doesn't open the new query");
must(psql(`select sql_text from sql_snippet where project_id = 'projsqlaaaaaaaaa' and name = 'Scratch'`) === "", "a new query should start empty");
await type("SELECT label FROM items ORDER BY label");
must((await a.locator("[data-testid=sql-open-name]").innerText()).includes("•"), "unsaved change not shown");
await a.click("[data-testid=sql-save]");
await a.waitForFunction(() => !document.querySelector("[data-testid=sql-open-name]")?.textContent?.includes("•"));
must(psql(`select sql_text from sql_snippet where name = 'Scratch'`) === "SELECT label FROM items ORDER BY label", "Save didn't store the query");
answers.push("In L2");
await rightClick(folderRow("L2"), "New query here");
await snippetRow("In L2").waitFor();
must(psql(`select f.name from sql_snippet s join sql_folder f on f.id = s.folder_id where s.name = 'In L2'`) === "L2", "New query here didn't use the folder");
ok("+ → New query and right-click → New query here create a named empty query, open it, and Save stores the text");

await type("SELECT label FROM items ORDER BY label DESC");
answers.push("All labels");
await a.click("[data-testid=sql-save-as]");
await snippetRow("All labels").waitFor();
await moveTo(snippetRow("All labels"), "Reports / L2");
await a.waitForFunction(() => [...document.querySelectorAll("[data-testid=snippet][data-name='All labels']")].some((e) => parseInt(e.style.paddingLeft) >= 32));
must(psql(`select f.name from sql_snippet s join sql_folder f on f.id = s.folder_id where s.name = 'All labels'`) === "L2", "snippet not moved into L2");
answers.push("Labels Z-A");
await rightClick(snippetRow("All labels"), "Rename");
await snippetRow("Labels Z-A").waitFor();
await rightClick(snippetRow("Labels Z-A"), "Duplicate");
await snippetRow("Labels Z-A (copy)").waitFor();
await rightClick(snippetRow("Labels Z-A"), "Add to favorites");
await a.waitForFunction(() => document.querySelectorAll("[data-testid=snippet][data-name='Labels Z-A']").length === 2); // also under Favorites
answers.push("Old reports");
await rightClick(folderRow("Reports"), "Rename");
await folderRow("Old reports").waitFor();
answers.push("Reports");
await folderRow("Old reports").locator("button").first().dblclick();
await folderRow("Reports").waitFor();
await rightClick(snippetRow("Labels Z-A (copy)"), "Delete");
await a.waitForFunction(() => !document.querySelector("[data-testid=snippet][data-name='Labels Z-A (copy)']"));
await a.fill("input[placeholder='Search queries']", "label DESC");
await a.waitForFunction(() => document.querySelectorAll("[data-testid=snippet]").length === 1);
await a.fill("input[placeholder='Search queries']", "");
await a.screenshot({ path: `${WORK_DIR}/sql-snippets.png`, fullPage: true });
ok("right-click menus: rename (and double-click), duplicate, favorite, move, delete queries; rename folders; search by text");

// The move menu never offers a folder's own subtree.
await folderRow("Reports").click({ button: "right" });
const reportsOptions = await menu().locator("[data-testid=move-to] option").allInnerTexts();
await a.screenshot({ path: `${WORK_DIR}/sql-context-menu.png` });
await a.keyboard.press("Escape");
must(!reportsOptions.some((o) => o.startsWith("Reports")), "move menu offers the folder's own subtree: " + reportsOptions);

/** Rewrite the next server-action requests of page pg (replace text in the body) to fake a tampered client. */
async function tampered(pg, from, to, actionFn) {
  const handler = async (route) => {
    const req = route.request();
    if (req.method() === "POST" && req.headers()["next-action"]) return route.continue({ postData: (req.postData() ?? "").split(from).join(to) });
    return route.continue();
  };
  await pg.route("**/dashboard/**", handler);
  try {
    await actionFn();
    await sleep(800);
  } finally {
    await pg.unroute("**/dashboard/**", handler);
  }
}
const idOf = (where) => psql(`select id from ${where}`);
const reportsId = idOf(`sql_folder where project_id = 'projsqlaaaaaaaaa' and name = 'Reports'`);
const l4 = idOf(`sql_folder where name = 'L4'`);
// A legal move (L4 under L2), rewritten into "move Reports under L2", its own subfolder.
await tampered(a, `"${l4}"`, `"${reportsId}"`, () => moveTo(folderRow("L4"), "Reports / L2"));
must(psql(`select parent_id is null from sql_folder where id = '${reportsId}'`) === "t", "server allowed moving a folder into its own subfolder (cycle)");
must(psql(`select p.name from sql_folder f join sql_folder p on p.id = f.parent_id where f.id = '${l4}'`) === "L3", "tamper didn't take effect (L4 was moved), so the cycle check wasn't tested");
ok("move menu never offers a folder's own subfolders, and the server refuses such a move (tampered request)");

/* ---------- 9. other projects' folders and queries (tampered requests from account B) ---------- */
step("IDOR");
const aSnippet = idOf(`sql_snippet where project_id = 'projsqlaaaaaaaaa' and name = 'Labels Z-A'`);
await other.goto(PB2 + "/sql");
await other.waitForSelector("[data-testid=snippet]");
other.removeAllListeners("dialog");
other.on("dialog", (d) => d.accept(d.type() === "prompt" ? "Hijacked" : undefined));
const bName = psql(`select name from sql_snippet where project_id = 'projsqlbbbbbbbbb' order by name limit 1`);
const bSnippet = idOf(`sql_snippet where project_id = 'projsqlbbbbbbbbb' and name = '${bName}'`);
const bRow = () => snippetRow(bName, other);
const favA = () => psql(`select favorite from sql_snippet where id = '${aSnippet}'`);
const favBefore = favA();
await tampered(other, bSnippet, aSnippet, () => bRow().locator("button[aria-label=Favorite]").click());
must(favA() === favBefore, "account B toggled a favorite on account A's query");
await tampered(other, bSnippet, aSnippet, () => rightClick(bRow(), "Rename", other));
must(psql(`select name from sql_snippet where id = '${aSnippet}'`) === "Labels Z-A", "account B renamed account A's query");
await tampered(other, bSnippet, aSnippet, () => rightClick(bRow(), "Delete", other));
must(psql(`select count(*) from sql_snippet where id = '${aSnippet}'`) === "1", "account B deleted account A's query");
await tampered(other, `"${bSnippet}"`, `"${aSnippet}"`, () => moveTo(bRow(), "Top level", other));
must(psql(`select f.name from sql_snippet s join sql_folder f on f.id = s.folder_id where s.id = '${aSnippet}'`) === "L2", "account B moved account A's query");
const bFolder = idOf(`sql_folder where project_id = 'projsqlbbbbbbbbb'`);
await tampered(other, `"${bFolder}"`, `"${reportsId}"`, () => rightClick(other.locator("[data-testid=folder]").first(), "Delete", other));
must(psql(`select count(*) from sql_folder where id = '${reportsId}'`) === "1", "account B deleted account A's folder");
await tampered(other, "projsqlbbbbbbbbb", "projsqlaaaaaaaaa", () => bRow().locator("button[aria-label=Favorite]").click());
must(favA() === favBefore, "account B changed project A through a swapped project id");
must(psql(`select count(*) from sql_snippet where project_id = 'projsqlaaaaaaaaa' and name = 'Hijacked'`) === "0", "a rename leaked into project A");
// Proof the requests really were rewritten: B's own query was never the target, so it is untouched.
const bState = () => psql(`select name || '|' || favorite::int || '|' || coalesce(folder_id, '') from sql_snippet where id = '${bSnippet}'`);
must(bState() === `${bName}|0|${bFolder}`, `tampering didn't take effect: B's own query changed (${bState()})`);
must(psql(`select count(*) from sql_folder where id = '${bFolder}'`) === "1", "tampering didn't take effect: B's own folder was deleted");
ok("account B can't favorite, rename, move or delete A's queries, delete A's folders, or reach A's project with tampered requests");

/* ---------- 10. deleting a folder removes everything inside ---------- */
step("folder delete cascade");
await a.goto(P + "/sql");
await folderRow("Reports").waitFor();
await rightClick(folderRow("Reports"), "Delete");
await a.waitForFunction(() => !document.querySelector("[data-testid=folder][data-name='Reports']"));
must(psql(`select count(*) from sql_folder where project_id = 'projsqlaaaaaaaaa' and name in ('L2','L3','L4','L5')`) === "0", "subfolders survived deleting their parent");
must(psql(`select count(*) from sql_snippet where project_id = 'projsqlaaaaaaaaa' and name in ('Labels Z-A', 'In L2')`) === "0", "queries inside survived deleting the folder");
ok("deleting a folder (after confirming) removes its subfolders and the queries in them");

/* ---------- 11. export ---------- */
step("export");
answers.push("Deep");
await plus("New folder");
await folderRow("Deep").waitFor();
answers.push("Answer");
await rightClick(folderRow("Deep"), "New query here");
await snippetRow("Answer").waitFor();
await type("SELECT 42 AS answer");
await a.click("[data-testid=sql-save]");
await a.waitForFunction(() => !document.querySelector("[data-testid=sql-open-name]")?.textContent?.includes("•"));
const zip = await a.request.get(P + "/export");
must(zip.status() === 200, "export failed");
writeFileSync(`${WORK_DIR}/sql-export.zip`, await zip.body());
const py = (code) => execSync(`python3 -c "${code}" ${WORK_DIR}/sql-export.zip`).toString();
const entries = py("import zipfile,sys; print(chr(10).join(zipfile.ZipFile(sys.argv[1]).namelist()))").split("\n");
must(entries.includes("sql-snippets/Getting started/Newest sign-ups.sql"), "starter query missing from export: " + entries.filter((e) => e.startsWith("sql")));
must(entries.includes("sql-snippets/Deep/Answer.sql"), "query in a folder missing from export");
must(entries.includes("sql-snippets/Scratch.sql"), "top-level query missing from export");
must(py("import zipfile,sys; print(zipfile.ZipFile(sys.argv[1]).read('sql-snippets/Deep/Answer.sql').decode())").trim() === "SELECT 42 AS answer", "exported query text wrong");
ok("Download data includes saved queries as sql-snippets/<folder>/<name>.sql");

/* ---------- 12. history and rate limit ---------- */
step("history and rate limit");
await a.click("[data-testid=tab-history]");
await a.waitForSelector("[data-testid=history-item]");
must((await a.locator("[data-testid=history-item]").count()) === 20, "history panel should list the last 20 runs");
must(psql(`select count(*) from sql_run where project_id = 'projsqlaaaaaaaaa'`) === "20", "history must keep exactly the last 20 runs");
await a.locator("[data-testid=history-item]").first().click();
must((await a.locator("[data-testid=sql-open-name]").innerText()).includes("Untitled"), "a history entry should open as an unsaved query");
// Burst: the per-minute limit kicks in (earlier runs this minute count too). run-all.sh raises it
// from the product default (30) with SQL_RUNS_PER_MINUTE so the rest of this suite fits in a minute.
const LIMIT = Number(process.env.SQL_RUNS_PER_MINUTE) || 30;
let limited = null;
for (let i = 0; i < LIMIT + 5 && !limited; i++) {
  const r = await run(a, `SELECT ${i} AS n`);
  if (r.status === 429) limited = r;
}
must(limited && new RegExp(`at most ${LIMIT} queries per minute`).test(limited.error), "rate limit never kicked in");
must(psql(`select count(*) from sql_run where project_id = 'projsqlaaaaaaaaa'`) === "20", "history grew past 20");
must((await run(other, "SELECT 1", PB2)).status === 200, "project B was rate-limited by project A's runs");
ok(`history keeps the last 20 runs; ${LIMIT} runs/minute per project (product default 30), other projects unaffected`);

/* ---------- 13. CSP ---------- */
const csp = [...(await cspViolations(a)), ...(await cspViolations(other))];
must(csp.length === 0, "CSP violations on the SQL pages: " + csp.join(" | "));
ok("no Content-Security-Policy violations on the SQL pages (CodeMirror's styles carry the nonce)");

await b.close();
