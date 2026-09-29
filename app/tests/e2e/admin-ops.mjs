// Operator & owner tools: limit warning emails, "Update all projects", suspend project/account,
// data export, account deletion.
// Run via: WITH_MAIL=1 PLAN_LIMITS_OVERRIDE='{"requestsPerMonth":20}' tests/e2e/run-backups.sh admin-ops.mjs
import { execSync } from "node:child_process";
import { writeFileSync } from "node:fs";
import { APP, browser, controlHeaders, linkProject, must, ok, pbAdminToken, psql, requireApp, sleep, step, WORK_DIR } from "./lib.mjs";

const SINK = process.env.MAIL_SINK;
const DOCKER = process.env.DOCKER_HOST.replace("tcp://", "http://");
const PBA = "http://127.0.0.1:8090";
const J = { "content-type": "application/json" };
const mails = async () => (await fetch(SINK + "/messages")).json();
const dockerLog = async () => (await fetch(DOCKER + "/_log")).json();
async function waitFor(fn, what, secs = 20) {
  for (let i = 0; i < secs * 4; i++) {
    const v = await fn();
    if (v) return v;
    await sleep(250);
  }
  throw new Error("FAIL: timed out waiting for " + what);
}
const mailTo = (to, re) => waitFor(async () => (await mails()).filter((m) => m.to.includes(to) && re.test(m.subject)).at(-1), `email "${re}" to ${to}`);
const countTo = async (to, re) => (await mails()).filter((m) => m.to.includes(to) && re.test(m.subject)).length;
const linkIn = (m) => m.text.match(/https?:\/\/\S+/g).at(-1);
const status = (id) => psql(`select status||'|'||coalesce(status_message,'') from project where id='${id}'`);

const b = await browser();
async function account(email) {
  const p = await (await b.newContext()).newPage();
  p.on("dialog", (d) => (d.type() === "prompt" ? d.accept("Phishing pages reported") : d.accept()));
  await p.goto(APP + "/signup");
  await p.fill("input[name=name]", email.split("@")[0]);
  await p.fill("input[name=email]", email);
  await p.fill("input[name=password]", "password123");
  await p.click("button:has-text('Create account')");
  await p.waitForSelector("[data-testid=check-email]");
  await p.goto(linkIn(await mailTo(email, /Confirm your email/)));
  await p.waitForURL(APP + "/dashboard", { waitUntil: "commit" });
  return p;
}

step("setup: admin + owner accounts, two projects");
const admin = await account("admin@ops.test");
const owner = await account("owner@ops.test");
const PA = linkProject("owner@ops.test", { id: "projopsaaaaaaaaa", name: "Shop", slug: "8090" });
linkProject("owner@ops.test", { id: "projopsbbbbbbbbb", name: "Blog", slug: "8091" });

// ---------------------------------------------------------------- limit warnings (requestsPerMonth = 20)
step("limit warnings");
const refresh = async () => {
  await owner.goto(PA + "/usage");
  await owner.click("button:has-text('Refresh')");
  await owner.waitForSelector("button:has-text('Refresh'):not([disabled])");
};
for (let i = 0; i < 17; i++) await fetch(PBA + "/api/collections/users/records");
await refresh();
const w80 = await mailTo("owner@ops.test", /"Shop" is close to a plan limit/);
must(/8\d% used: API requests this month: 17 of 20/.test(w80.text) && w80.text.includes("/usage"), w80.text);
await refresh();
await sleep(1000);
must((await countTo("owner@ops.test", /plan limit/)) === 1, "80% email sent once");
ok("80% of monthly requests → one 'close to a plan limit' email to the owner (with numbers, effect and link); not repeated");
for (let i = 0; i < 5; i++) await fetch(PBA + "/api/collections/users/records");
await refresh();
const w100 = await mailTo("owner@ops.test", /"Shop" reached a plan limit/);
must(/Limit reached: API requests this month/.test(w100.text) && /error until the 1st of next month/.test(w100.text), w100.text);
await refresh();
await sleep(1000);
must((await countTo("owner@ops.test", /plan limit/)) === 2, "100% email once");
must(psql(`select count(*) from usage_notice where project_id='projopsaaaaaaaaa'`) === "2", "notices recorded");
ok("100% → one 'reached a plan limit' email explaining what stops; no repeats");

// ---------------------------------------------------------------- update all projects
step("update all projects");
await owner.goto(`${APP}/dashboard/projopsbbbbbbbbb`);
await owner.click("button:has-text('Pause')");
await waitFor(() => status("projopsbbbbbbbbb").startsWith("paused"), "B paused");
// Prove limits are re-applied by the update: clear them inside the project first.
await fetch(PBA + "/api/datbase/limits", { method: "POST", headers: { ...J, ...controlHeaders("8090") }, body: "{}" });
must((await fetch(PBA + "/api/collections/users/records")).status === 200, "unblocked before update");
await admin.goto(APP + "/dashboard/admin");
must((await admin.locator("[data-testid=image-status]").innerText()).includes("2 of 2 projects run an older version"), "outdated shown");
await admin.click("button:has-text('Update all projects')");
await waitFor(async () => {
  await admin.goto(APP + "/dashboard/admin");
  return /Last update: 2 of 2 done/.test(await admin.locator("[data-testid=image-status]").innerText());
}, "update finished", 60);
const log = await dockerLog();
for (const s of ["8090", "8091"]) must(log.includes(`POST /containers/create`) && log.includes(`POST /containers/dbp-${s}/start`), "recreated " + s);
must(log.includes("POST /containers/dbp-8091/stop"), "paused project stopped again");
must((await admin.locator("[data-testid=image-status]").innerText()).includes("All 2 projects run the current version"), "current");
must((await fetch(PBA + "/api/collections/users/records")).status === 429, "limits re-applied after update");
must(status("projopsaaaaaaaaa").startsWith("active") && status("projopsbbbbbbbbb").startsWith("paused"), "statuses kept");
await fetch(DOCKER + "/_image", { method: "POST" });
await admin.reload();
must((await admin.locator("[data-testid=image-status]").innerText()).includes("2 of 2 projects run an older version"), "new build detected");
ok("admin sees outdated projects; 'Update all projects' recreates each one (paused stays stopped), re-applies limits, then reports all current; a new build shows them outdated again");
await fetch(PBA + "/api/datbase/limits", { method: "POST", headers: { ...J, ...controlHeaders("8090") }, body: "{}" });
psql(`update project_usage set block_all=false`);

// ---------------------------------------------------------------- suspend a project
step("suspend project");
await admin.goto(APP + "/dashboard/admin/accounts");
await admin.click("[data-testid=suspend-8090]");
await waitFor(() => status("projopsaaaaaaaaa").startsWith("suspended|Phishing pages reported"), "suspended");
must((await dockerLog()).includes("POST /containers/dbp-8090/stop"), "stopped");
await owner.goto(PA);
must((await owner.locator("[data-testid=suspended]").innerText()).includes("Phishing pages reported"), "owner sees reason");
must(!(await owner.locator("button:has-text('Resume')").count()) && !(await owner.locator("button:has-text('Pause')").count()), "owner can't resume");
await admin.goto(APP + "/dashboard/admin/accounts");
await admin.click("li[data-account='owner@ops.test'] tr[data-project='8090'] button:has-text('Lift suspension')");
await waitFor(() => status("projopsaaaaaaaaa").startsWith("active"), "active again");
ok("admin suspends a project with a reason → stopped, owner sees the reason and can't resume it; lifting brings it back");

// ---------------------------------------------------------------- suspend an account
step("suspend account");
must(!(await admin.locator("li[data-account='admin@ops.test'] button:has-text('Suspend account')").count()), "no self-suspend");
await admin.click("li[data-account='owner@ops.test'] button:has-text('Suspend account')");
await waitFor(() => status("projopsaaaaaaaaa").startsWith("suspended"), "projects suspended");
must(status("projopsbbbbbbbbb").startsWith("suspended|The account is suspended (project was paused)"), status("projopsbbbbbbbbb"));
await owner.goto(APP + "/dashboard");
must(owner.url().startsWith(APP + "/login"), "owner signed out");
await owner.fill("input[name=email]", "owner@ops.test");
await owner.fill("input[name=password]", "password123");
await owner.click("button:has-text('Sign in')");
await owner.waitForSelector("text=This account is suspended");
ok("suspending an account signs it out, blocks sign-in with a clear message and suspends its projects; admin can't suspend itself");
await admin.goto(APP + "/dashboard/admin/accounts");
await admin.click("li[data-account='owner@ops.test'] button:has-text('Lift account suspension')");
await waitFor(() => status("projopsaaaaaaaaa").startsWith("active") && status("projopsbbbbbbbbb").startsWith("paused"), "restored", 30);
await owner.fill("input[name=password]", "password123");
await owner.click("button:has-text('Sign in')");
await owner.waitForURL(APP + "/dashboard", { waitUntil: "commit" });
ok("lifting the account suspension: sign-in works again; the active project is back online, the paused one stays paused");

// ---------------------------------------------------------------- export
step("export");
const T = await pbAdminToken(PBA);
await fetch(PBA + "/api/collections", { method: "POST", headers: { ...J, ...controlHeaders("8090"), Authorization: T }, body: JSON.stringify({ name: "notes", type: "base", fields: [{ name: "text", type: "text" }, { name: "photo", type: "file", maxSelect: 1 }] }) });
for (let i = 0; i < 3; i++) {
  const fd = new FormData();
  fd.append("text", `note ${i} "quoted"`);
  if (i === 0) fd.append("photo", new Blob([Buffer.from("PNGDATA-123")]), "pic.png");
  await fetch(PBA + "/api/collections/notes/records", { method: "POST", headers: { Authorization: T }, body: fd });
}
await owner.goto(APP + "/dashboard/account");
const res = await owner.request.get(`${PA}/export`);
must(res.status() === 200 && /attachment; filename="8090-data-/.test(res.headers()["content-disposition"]), "download");
writeFileSync(`${WORK_DIR}/export.zip`, await res.body());
execSync(`rm -rf "${WORK_DIR}/export" && mkdir -p "${WORK_DIR}/export" && cd "${WORK_DIR}/export" && unzip -q ../export.zip`);
const { readFileSync, readdirSync } = await import("node:fs");
const notes = JSON.parse(readFileSync(`${WORK_DIR}/export/tables/notes.json`, "utf8"));
must(notes.length === 3 && notes.some((n) => n.text === 'note 0 "quoted"'), "records");
const withPhoto = notes.find((n) => n.photo);
must(readFileSync(`${WORK_DIR}/export/files/notes/${withPhoto.id}/${withPhoto.photo}`, "utf8") === "PNGDATA-123", "file bytes");
const tables = JSON.parse(readFileSync(`${WORK_DIR}/export/tables.json`, "utf8"));
must(tables.some((t) => t.name === "users" && t.kind === "users") && !JSON.stringify(tables).includes('"password"'), "schema");
must(!/pocketbase|_superusers/i.test(execSync(`cd "${WORK_DIR}/export" && cat README.txt tables.json tables/*.json`).toString()), "no engine names");
must(!readdirSync(`${WORK_DIR}/export/tables`).some((f) => f.startsWith("_")), "no internal tables");
const other = await account("other@ops.test");
must((await other.request.get(`${PA}/export`)).status() === 404, "other account 404");
ok("owner downloads a zip: README, tables.json, every table as JSON (quotes intact), uploaded files byte-exact; no internal tables or engine names; other accounts get 404");

// ---------------------------------------------------------------- delete account
step("delete account");
await owner.goto(PA + "/backups");
await owner.click("button:has-text('Back up now')");
await waitFor(() => psql(`select count(*) from backup where project_id='projopsaaaaaaaaa' and status='done'`) === "1", "backup", 60);
const { S3Client, ListObjectsV2Command } = requireApp("@aws-sdk/client-s3");
const s3 = new S3Client({ region: "us-east-1", endpoint: process.env.S3_DIRECT, forcePathStyle: true, credentials: { accessKeyId: "test", secretAccessKey: "test" } });
const objs = async () => ((await s3.send(new ListObjectsV2Command({ Bucket: process.env.BACKUP_S3_BUCKET, Prefix: "projects/projopsaaaaaaaaa/" }))).Contents ?? []).length;
must((await objs()) === 1, "backup object exists");
await owner.goto(APP + "/dashboard/account");
await owner.fill("input[name=confirm]", "wrong@ops.test");
await owner.click("button:has-text('Delete my account')");
await owner.waitForSelector("text=Type your email address exactly");
await owner.fill("input[name=confirm]", "OWNER@ops.test");
await owner.click("button:has-text('Delete my account')");
await owner.waitForURL(/\/login\?deleted=1/, { waitUntil: "commit" });
await owner.waitForSelector("text=Your account and all its projects were deleted");
must(psql(`select count(*) from "user" where email='owner@ops.test'`) === "0", "user gone");
must(psql(`select count(*) from project where id in ('projopsaaaaaaaaa','projopsbbbbbbbbb')`) === "0", "projects gone");
must((await objs()) === 0, "backups gone");
const dl = await dockerLog();
must(dl.some((c) => c.startsWith("DELETE /containers/dbp-8090")) && dl.some((c) => c.startsWith("DELETE /volumes/dbp-8091")), "containers + volumes removed");
await mailTo("owner@ops.test", /Your DATBase account was deleted/);
await owner.fill("input[name=email]", "owner@ops.test");
await owner.fill("input[name=password]", "password123");
await owner.click("button:has-text('Sign in')");
await owner.waitForSelector("text=Invalid email or password");
ok("delete account: wrong confirmation refused; then account, both projects, containers, volumes and backups are gone; goodbye email; can't sign in");

await b.close();
