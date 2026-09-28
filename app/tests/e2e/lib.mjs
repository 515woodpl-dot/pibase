// Shared helpers for the end-to-end suites. Configure with environment variables (see tests/README.md).
import { execSync } from "node:child_process";
import { createCipheriv, createHmac, randomBytes } from "node:crypto";
import { appendFileSync as _append } from "node:fs";
import { createRequire } from "node:module";
import { chromium } from "playwright";

export const APP = process.env.APP_URL ?? "http://localhost:3000";
export const PB = process.env.PB_URL ?? "http://127.0.0.1:8090";
export const PB_ADMIN = { email: "admin@proj.test", password: "SuperSecret123" }; // created by harness.sh
export const DB = process.env.TEST_DB ?? "datbase_e2e";
export const PSQL = process.env.PSQL ?? "psql -h /tmp -p 5433 -U postgres";
export const requireApp = createRequire(new URL("../../package.json", import.meta.url));

/* ---------- Every suite is bounded: no silent hangs (MAC-V5-02) ----------
 * - fetch() gets a default timeout (FETCH_TIMEOUT_SEC, 60) unless the caller passes its own signal.
 * - The whole suite gets SUITE_TIMEOUT_SEC (900); on timeout it prints the last step() and exits 1.
 * - Processes started with track() are stopped when the suite ends (and by harness.sh as a fallback).
 */
let lastStep = "start";
export function step(name) {
  lastStep = name;
  if (process.env.TEST_VERBOSE === "1") console.log("  …", name);
}
const _fetch = globalThis.fetch;
globalThis.fetch = (url, opts = {}) =>
  _fetch(url, opts.signal ? opts : { ...opts, signal: AbortSignal.timeout(Number(process.env.FETCH_TIMEOUT_SEC ?? 60) * 1000) });
const tracked = new Set();
export function track(child) {
  tracked.add(child);
  try {
    _append(`${process.env.WORK ?? "/tmp/datbase-e2e"}/harness.pids`, `${child.pid}\n`);
  } catch {}
  return child;
}
const killTracked = () => {
  for (const c of tracked) {
    try {
      c.kill();
    } catch {}
  }
};
process.on("exit", (code) => {
  killTracked();
  if (code) console.error(`   (last step: ${lastStep})`);
});
const suiteLimit = Number(process.env.SUITE_TIMEOUT_SEC ?? 900);
setTimeout(() => {
  console.error(`FAIL: suite timed out after ${suiteLimit}s at step: ${lastStep}`);
  process.exit(1);
}, suiteLimit * 1000).unref();

export const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
export const ok = (...a) => console.log("✔", ...a);
export function must(cond, msg) {
  if (!cond) throw new Error("FAIL: " + msg);
}
// psql runs with the caller's original PATH/DOCKER_HOST: the backup suites point the app at a fake
// Docker and a fake pg_dump, which must not reach a PSQL like "docker exec -i pg psql …".
const hostEnv = () => {
  const env = { ...process.env, PGTZ: "UTC" };
  if (process.env.HARNESS_ORIG_PATH) env.PATH = process.env.HARNESS_ORIG_PATH;
  if (process.env.HARNESS_ORIG_DOCKER_HOST) env.DOCKER_HOST = process.env.HARNESS_ORIG_DOCKER_HOST;
  else delete env.DOCKER_HOST;
  return env;
};
export const psql = (q, db = DB) => execSync(`${PSQL} ${db} -tAc "${q.replace(/"/g, '\\"')}"`, { env: hostEnv() }).toString().trim();

/** Encrypt like the app does (lib/crypto.ts), to link a local PocketBase as a project without Docker. */
export function enc(s) {
  const iv = randomBytes(12);
  const c = createCipheriv("aes-256-gcm", Buffer.from(process.env.ENCRYPTION_KEY, "base64"), iv);
  const d = Buffer.concat([c.update(s), c.final()]);
  return Buffer.concat([iv, c.getAuthTag(), d]).toString("base64");
}

export async function browser() {
  return chromium.launch(process.env.CHROMIUM_PATH ? { executablePath: process.env.CHROMIUM_PATH } : {});
}

/** Sign up a dashboard account in a fresh browser context. Returns the page. */
export async function signup(b, email, password = "password123") {
  const p = await (await b.newContext()).newPage();
  p.on("dialog", (d) => d.accept());
  await p.goto(APP + "/signup");
  await p.fill("input[name=name]", "Test");
  await p.fill("input[name=email]", email);
  await p.fill("input[name=password]", password);
  await p.click("button:has-text('Create account')");
  await p.waitForURL(APP + "/dashboard", { waitUntil: "commit" });
  return p;
}

/** Link a running local PocketBase (port = slug) to an account as an active project. Returns the dashboard URL. */
export function linkProject(ownerEmail, { id = "proj00000000000a", name = "Shop", slug = "8090", status = "active" } = {}) {
  const uid = psql(`select id from "user" where email='${ownerEmail}'`);
  psql(
    `insert into project (id, owner_id, name, slug, status, admin_email, admin_password_enc) values ('${id}','${uid}','${name}','${slug}','${status}','${PB_ADMIN.email}','${enc(PB_ADMIN.password)}')`,
  );
  return `${APP}/dashboard/${id}`;
}

/** Superuser token straight from a PocketBase (test setup only). */
export async function pbAdminToken(base = PB) {
  const r = await fetch(base + "/api/collections/_superusers/auth-with-password", {
    method: "POST",
    headers: { "content-type": "application/json" },
    body: JSON.stringify({ identity: PB_ADMIN.email, password: PB_ADMIN.password }),
  });
  return (await r.json()).token;
}

export const WORK_DIR = process.env.WORK ?? "/tmp/datbase-e2e";
/** Write a small test file (upload fixture) and return its path. */
import { writeFileSync as _write, mkdirSync as _mkdir } from "node:fs";
export function fixture(name, content) {
  _mkdir(WORK_DIR, { recursive: true });
  const path = `${WORK_DIR}/${name}`;
  _write(path, content);
  return path;
}

/** Same as the app's controlSecret(slug): header value that marks a request as DATBase's own. */
export function controlHeaders(slug = "8090") {
  const secret = createHmac("sha256", Buffer.from(process.env.ENCRYPTION_KEY, "base64")).update(`datbase-control:${slug}`).digest("base64url");
  return { "X-Datbase-Control": secret };
}
