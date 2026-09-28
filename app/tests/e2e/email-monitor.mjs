// Email (dashboard + app users) and monitoring/alerts.
// Run via: tests/e2e/run-email.sh email-monitor.mjs   (starts a local SMTP + webhook stand-in)
import { spawn } from "node:child_process";
import { readFileSync, writeFileSync } from "node:fs";
import { pathToFileURL } from "node:url";
import { APP, browser, controlHeaders, linkProject, must, ok, PB, pbAdminToken, psql, sleep, track, WORK_DIR } from "./lib.mjs";

const SINK = process.env.MAIL_SINK;
const PB2 = "http://127.0.0.1:8091";
const J = { "content-type": "application/json" };
const mails = async () => (await fetch(SINK + "/messages")).json();
const hooks = async () => (await fetch(SINK + "/webhooks")).json();
async function waitFor(fn, what, secs = 20) {
  for (let i = 0; i < secs * 4; i++) {
    const v = await fn();
    if (v) return v;
    await sleep(250);
  }
  throw new Error("FAIL: timed out waiting for " + what);
}
const mailTo = (to, re) => waitFor(async () => (await mails()).filter((m) => m.to.includes(to) && re.test(m.subject)).at(-1), `email "${re}" to ${to}`);
const linkIn = (m) => m.text.match(/https?:\/\/\S+/g).at(-1);
const countTo = async (to, re) => (await mails()).filter((m) => m.to.includes(to) && re.test(m.subject)).length;

const b = await browser();
const page = async () => {
  const p = await (await b.newContext()).newPage();
  p.on("dialog", (d) => d.accept());
  return p;
};

// ---------------------------------------------------------------- dashboard: sign up needs email confirmation
const ADMIN = "admin@ops.test";
let a = await page();
await a.goto(APP + "/signup");
await a.fill("input[name=name]", "Ops");
await a.fill("input[name=email]", ADMIN);
await a.fill("input[name=password]", "password123");
await a.click("button:has-text('Create account')");
await a.waitForSelector("[data-testid=check-email]");
await a.goto(APP + "/dashboard");
must(a.url().startsWith(APP + "/login"), "no session before confirming");
const confirmMail = await mailTo(ADMIN, /Confirm your email for DATBase/);
must(confirmMail.from.includes("no-reply@datbase.test"), "sender " + confirmMail.from);
ok("sign-up → 'check your email' screen, no session yet; confirmation email sent from SMTP_FROM");

await a.goto(APP + "/login");
await a.fill("input[name=email]", ADMIN);
await a.fill("input[name=password]", "password123");
await a.click("button:has-text('Sign in')");
await a.waitForSelector("[data-testid=check-email]");
await waitFor(async () => (await countTo(ADMIN, /Confirm your email/)) >= 2, "a second confirmation email");
ok("signing in before confirming shows 'check your email' and re-sends the link");

await a.goto(linkIn(await mailTo(ADMIN, /Confirm your email/)));
await a.waitForURL(APP + "/dashboard", { waitUntil: "commit" });
await a.waitForSelector("text=Server status");
ok("confirmation link signs the user in; ADMIN_EMAIL account sees 'Server status'");

// same email again: identical answer, owner gets a heads-up instead (F-09 closed)
const dup = await page();
await dup.goto(APP + "/signup");
await dup.fill("input[name=name]", "Someone");
await dup.fill("input[name=email]", ADMIN);
await dup.fill("input[name=password]", "otherpass123");
await dup.click("button:has-text('Create account')");
await dup.waitForSelector("[data-testid=check-email]");
await mailTo(ADMIN, /You already have a DATBase account/);
must(psql(`select count(*) from "user" where email='${ADMIN}'`) === "1", "no second account");
ok("F-09: signing up with a registered email shows the same screen; the owner gets 'you already have an account'");

// forgot password
const f = await page();
await f.goto(APP + "/login");
await f.click("text=Forgot your password?");
await f.waitForURL(APP + "/forgot-password");
await f.fill("input[name=email]", "nobody@ops.test");
await f.click("button:has-text('Send reset link')");
await f.waitForSelector("[data-testid=reset-sent]");
await f.goto(APP + "/forgot-password");
await f.fill("input[name=email]", ADMIN);
await f.click("button:has-text('Send reset link')");
await f.waitForSelector("[data-testid=reset-sent]");
const resetMail = await mailTo(ADMIN, /Reset your DATBase password/);
must((await countTo("nobody@ops.test", /./)) === 0, "no mail to unknown address");
await f.goto(linkIn(resetMail));
await f.waitForURL(/\/reset-password\?token=/, { waitUntil: "commit" });
await f.fill("input[name=password]", "newpass12345");
await f.fill("input[name=confirm]", "newpass12345");
await f.click("button:has-text('Save new password')");
await f.waitForURL(/\/login\?reset=1/, { waitUntil: "commit" });
await f.waitForSelector("text=Password changed");
await f.fill("input[name=email]", ADMIN);
await f.fill("input[name=password]", "password123");
await f.click("button:has-text('Sign in')");
await f.waitForSelector("text=Invalid email or password");
await f.fill("input[name=password]", "newpass12345");
await f.click("button:has-text('Sign in')");
await f.waitForURL(APP + "/dashboard", { waitUntil: "commit" });
await f.goto(linkIn(resetMail));
await f.waitForSelector("text=Link expired");
ok("forgot password: same answer for unknown emails; reset link → new password works, old doesn't, link is single-use");
await a.goto(APP + "/dashboard");
must(a.url().startsWith(APP + "/login"), "other sessions revoked");
ok("resetting the password signs out every other session");
a = f; // the admin's browser from here on

// ---------------------------------------------------------------- admin page + /api/monitor
const other = await page();
await other.goto(APP + "/signup");
await other.fill("input[name=name]", "Other");
await other.fill("input[name=email]", "other@ops.test");
await other.fill("input[name=password]", "password123");
await other.click("button:has-text('Create account')");
await other.waitForSelector("[data-testid=check-email]");
await other.goto(linkIn(await mailTo("other@ops.test", /Confirm your email/)));
await other.waitForURL(APP + "/dashboard", { waitUntil: "commit" });
must(!(await other.locator("text=Server status").count()), "no admin link");
must((await other.goto(APP + "/dashboard/admin")).status() === 404, "admin page 404 for others");
ok("other accounts: no 'Server status' link and /dashboard/admin → 404");

must((await fetch(APP + "/api/monitor")).status === 401, "no token");
must((await fetch(APP + "/api/monitor", { headers: { Authorization: "Bearer wrong" } })).status === 401, "wrong token");
const mon = async () => (await fetch(APP + "/api/monitor", { headers: { Authorization: "Bearer test-monitor-token" } })).json();
const m0 = await mon();
must(m0.status === "ok" && typeof m0.disk_used_pct === "number" && typeof m0.memory_used_pct === "number" && m0.projects_total === 0, JSON.stringify(m0).slice(0, 300));
ok(`/api/monitor: 401 without/with a wrong token; with MONITOR_TOKEN → status ok, disk ${m0.disk_used_pct}%, memory ${m0.memory_used_pct}%`);

// ---------------------------------------------------------------- projects + email settings
const P1 = linkProject(ADMIN, { id: "projmail00000001", name: "Corner Shop", slug: "8090" });
linkProject(ADMIN, { id: "projmail00000002", name: "Second", slug: "8091" });
await a.goto(APP + "/dashboard/admin");
await a.click("button:has-text('Apply email settings to all projects')");
await a.waitForSelector("text=applied to 2 of 2");
const T = await pbAdminToken(PB);
const settings = await (await fetch(PB + "/api/settings", { headers: { Authorization: T } })).json();
must(settings.smtp.enabled && settings.smtp.host === "127.0.0.1" && !JSON.stringify(settings).includes('"password":"test"'), "smtp applied, password hidden");
must(settings.meta.appName === "Corner Shop", "app name");
ok("admin 'Apply email settings' → both projects get SMTP + name; the SMTP password is never readable through the project API");

// ---------------------------------------------------------------- app users: DATBASE.md + client
const md = await (await a.request.get(P1 + "/datbase-md")).text();
must(md.includes("requestPasswordReset(email)") && md.includes("page hosted by DATBase") && md.includes("const EMAIL_ENABLED = true"), "guide documents email");
must(!md.includes("Not available yet:** password-reset"), "no 'not available' for reset");
const client = md.match(/```ts\n(\/\/ datbase\.ts:[\s\S]*?)\n```/)[1].replace(/export const DATBASE_URL = "[^"]+";/, `export const DATBASE_URL = "${PB}";`);
const { stripTypeScriptTypes } = await import("node:module");
writeFileSync(`${WORK_DIR}/client-email.mjs`, stripTypeScriptTypes(client));
const db = await import(pathToFileURL(`${WORK_DIR}/client-email.mjs`).href);
ok("DATBASE.md (email on): requestPasswordReset/requestVerification documented, hosted pages explained");

const U = "shopper@app.test";
await db.signUp({ email: U, password: "password123" });
const vmail = await mailTo(U, /Confirm your email for Corner Shop/);
must(vmail.from.includes("no-reply@datbase.test") && vmail.from.includes("Corner Shop"), "from " + vmail.from);
must(linkIn(vmail).startsWith(`${APP}/u/8090/users/verify?token=`), "link " + linkIn(vmail));
must((await db.refreshSession()).verified === false, "not verified yet");
const u = await page();
await u.goto(linkIn(vmail));
await u.waitForSelector("[data-testid=confirm-done]");
must((await db.refreshSession()).verified === true, "verified");
ok("app user sign-up sends 'Confirm your email for Corner Shop' (DATBase sender, app name) → hosted page confirms → verified: true");

await db.requestPasswordReset(U);
const rmail = await mailTo(U, /Reset your Corner Shop password/);
await u.goto(linkIn(rmail));
await u.fill("input[name=password]", "shopnew123");
await u.fill("input[name=passwordConfirm]", "different1");
await u.click("button:has-text('Save new password')");
await u.waitForSelector("text=The passwords don't match.");
await u.fill("input[name=password]", "shopnew123");
await u.fill("input[name=passwordConfirm]", "shopnew123");
await u.click("button:has-text('Save new password')");
await u.waitForSelector("[data-testid=confirm-done]");
db.signOut();
await db.signIn(U, "shopnew123");
await u.goto(linkIn(rmail));
await u.fill("input[name=password]", "again12345");
await u.fill("input[name=passwordConfirm]", "again12345");
await u.click("button:has-text('Save new password')");
await u.waitForSelector("text=invalid or has expired");
await u.goto(`${APP}/u/8090/users/verify?token=garbage`);
await u.waitForSelector("text=Link not valid");
must((await u.goto(`${APP}/u/nosuch/users/verify?token=x`)).status() === 200, "page renders");
await u.waitForSelector("text=Link not valid");
ok("app user password reset on the hosted page: mismatch caught, new password works, link single-use; bad tokens/projects → 'Link not valid'");

// A project admin (or server key) can't turn DATBase's email into phishing
await fetch(PB + "/api/collections/users", { method: "PATCH", headers: { ...J, Authorization: T }, body: JSON.stringify({ resetPasswordTemplate: { subject: "PHISH {APP_NAME}", body: '<a href="https://evil.test/{TOKEN}">x</a>' } }) });
await fetch(PB + "/api/settings", { method: "PATCH", headers: { ...J, Authorization: T }, body: JSON.stringify({ meta: { senderAddress: "ceo@bank.test", senderName: "Bank" } }) });
must((await fetch(PB + "/api/settings/test/email", { method: "POST", headers: { ...J, Authorization: T }, body: JSON.stringify({ email: "victim@else.test", template: "verification" }) })).status === 404, "test email blocked");
await db.requestPasswordReset(U);
await waitFor(async () => (await countTo(U, /Reset your/)) >= 2, "second reset email");
const all = await mails();
must(!all.some((m) => /PHISH|evil\.test|bank\.test/.test(m.subject + m.text + m.from)) && !all.some((m) => m.to.includes("victim@else.test")), "no phishing");
must(linkIn(all.filter((m) => m.to.includes(U)).at(-1)).startsWith(`${APP}/u/8090/`), "link stays on DATBase");
ok("edited templates and sender are ignored (DATBase sender, wording, hosted link); 'send test email' → 404");

// Hourly cap per project (PROJECT_MAIL_PER_HOUR=8)
// (the engine also throttles repeats for the same address, so use many new sign-ups)
for (let i = 0; i < 12; i++) {
  await db.create("users", { email: `cap${i}@app.test`, password: "password123", passwordConfirm: "password123" }, { token: null });
  await db.requestVerification(`cap${i}@app.test`);
}
await sleep(2500);
const appMails = (await mails()).filter((m) => m.to.some((t) => t.endsWith("@app.test"))).length;
must(appMails === 8, `app emails this hour: ${appMails}`);
ok("per-project cap: 12 more sign-up confirmations → exactly 8 emails this hour in total, the API still answers 204");

// "Signed-in users with a confirmed email" access option
await fetch(PB + "/api/collections", { method: "POST", headers: { ...J, Authorization: T }, body: JSON.stringify({ name: "notes", type: "base", fields: [{ name: "text", type: "text" }] }) });
await fetch(PB + "/api/collections/notes/records", { method: "POST", headers: { ...J, Authorization: T }, body: JSON.stringify({ text: "hello" }) });
await a.goto(P1 + "/tables/notes/settings");
await a.selectOption("select[name=list]", "verified");
await a.selectOption("select[name=view]", "verified");
await a.click("button:has-text('Save access')");
await a.waitForSelector("text=Saved");
await db.signIn("cap0@app.test", "password123"); // never confirmed
must((await db.list("notes")).items.length === 0, "unverified sees none");
db.signOut();
await db.signIn(U, "shopnew123");
must((await db.list("notes")).items.length === 1, "verified sees note");
await a.reload();
must((await a.locator("select[name=list]").inputValue()) === "verified", "preset shown");
ok("access 'Signed-in users with a confirmed email': unverified user sees nothing, confirmed user sees the record");

// ---------------------------------------------------------------- monitoring: project down → alert → resolved
await fetch(SINK + "/reset", { method: "POST" });
const pidFile = `${process.env.WORK}/pb8091.pid`;
process.kill(Number(readFileSync(pidFile, "utf8")));
const down = await waitFor(async () => (await hooks()).find((h) => h.level === "problem"), "problem webhook", 40);
must(/Second.*8091.*not responding/.test(down.message) && down.title.startsWith("DATBase: 1 problem") && down.content && down.text, JSON.stringify(down));
const dmail = await mailTo(ADMIN, /DATBase: 1 problem/);
must(/NEW: Project "Second" \(8091\) is not responding/.test(dmail.text), dmail.text);
const m1 = await mon();
must(m1.status === "problem" && m1.projects_down === 1 && m1.problems[0].includes("8091"), JSON.stringify(m1).slice(0, 300));
await a.goto(APP + "/dashboard/admin");
must((await a.locator("[data-testid=server-status]").innerText()).includes("1 problem") && (await a.locator("[data-testid=problems]").innerText()).includes("8091"), "admin page");
ok("project stops responding → after 2 checks: webhook (title/message/text/content) + email to ADMIN_EMAIL; /api/monitor and the status page show it");
must((await hooks()).filter((h) => h.level === "problem").length === 1, "alerted once");
ok("an open problem alerts once (no repeats every check)");

const child = spawn(process.env.PB_BIN, ["serve", "--http=127.0.0.1:8091", `--dir=${process.env.WORK}/pb8091`, "--automigrate=false", `--hooksDir=${process.env.APP_DIR}/../pocketbase/pb_hooks`], {
  detached: true, stdio: "ignore", env: { ...process.env, DATBASE_CONTROL_SECRET: controlHeaders("8091")["X-Datbase-Control"] },
});
child.unref();
writeFileSync(pidFile, String(child.pid));
track(child);
const clear = await waitFor(async () => (await hooks()).find((h) => h.level === "ok"), "all-clear webhook", 40);
must(/RESOLVED: Project "Second"/.test(clear.message), clear.message);
await mailTo(ADMIN, /DATBase: all clear/);
must((await mon()).status === "ok", "ok again");
ok("project back → 'all clear' webhook + email with the resolved item; status ok");

await a.goto(APP + "/dashboard/admin");
await a.click("button:has-text('Send test alert')");
await a.waitForSelector("text=Test alert sent (email + webhook)");
await mailTo(ADMIN, /DATBase: test alert/);
must((await hooks()).some((h) => h.level === "test"), "test webhook");
ok("'Send test alert' reaches email and webhook");

// Home Assistant package reads only fields /api/monitor really returns
const ha = readFileSync(`${process.env.APP_DIR}/../integrations/home-assistant/datbase.yaml`, "utf8");
const live = await mon();
const used = [...new Set([...ha.matchAll(/value_json\.(\w+)/g)].map((x) => x[1]))];
const attrs = [...ha.matchAll(/json_attributes:\n((?:\s+- \w+\n)+)/g)].flatMap((x) => x[1].match(/\w+/g));
const missing = [...used, ...attrs].filter((k) => !(k in live));
must(used.length >= 8 && !missing.length, "HA fields missing from /api/monitor: " + missing.join(", "));
ok(`Home Assistant package: all ${used.length + attrs.length} fields it reads exist in /api/monitor`);

await b.close();
