// Backup fixes R-01, R-02, R-04, R-06 (review 2026-09-27).
// Run via: tests/e2e/run-backups.sh   (starts S3 stand-in, flaky proxy, fake Docker, 2 PocketBases)
import { execSync, spawn } from "node:child_process";
import { readFileSync, writeFileSync } from "node:fs";
import { APP, browser, controlHeaders, linkProject, must, ok, pbAdminToken, psql, requireApp, signup, sleep, step, track } from "./lib.mjs";

const { S3Client, ListObjectsV2Command, PutObjectCommand } = requireApp("@aws-sdk/client-s3");
const s3 = new S3Client({
  region: "us-east-1",
  endpoint: process.env.S3_DIRECT ?? "http://127.0.0.1:5055",
  forcePathStyle: true,
  credentials: { accessKeyId: "test", secretAccessKey: "test" },
});
const Bucket = process.env.BACKUP_S3_BUCKET;
const objects = async (Prefix) => ((await s3.send(new ListObjectsV2Command({ Bucket, Prefix }))).Contents ?? []).map((o) => o.Key);
const s3mode = (m) => writeFileSync(process.env.S3_MODE_FILE, m);
const DOCKER = process.env.DOCKER_HOST.replace("tcp://", "http://");
const dockerLog = async () => (await fetch(DOCKER + "/_log")).json();
const forgetContainers = () => fetch(DOCKER + "/_forget", { method: "POST" });
const J = { "content-type": "application/json" };

async function seedNotes(base, texts) {
  const T = await pbAdminToken(base);
  await fetch(base + "/api/collections", { method: "POST", headers: { ...J, Authorization: T }, body: JSON.stringify({ name: "notes", type: "base", fields: [{ name: "t", type: "text" }] }) });
  for (const t of texts) await fetch(base + "/api/collections/notes/records", { method: "POST", headers: { ...J, Authorization: T }, body: JSON.stringify({ t }) });
}
async function notes(base) {
  const T = await pbAdminToken(base);
  const r = await (await fetch(base + "/api/collections/notes/records?sort=t", { headers: { Authorization: T } })).json();
  return (r.items ?? []).map((x) => x.t).join(",");
}
async function setNotes(base, texts) {
  const T = await pbAdminToken(base);
  const r = await (await fetch(base + "/api/collections/notes/records?perPage=500", { headers: { Authorization: T } })).json();
  for (const x of r.items) await fetch(`${base}/api/collections/notes/records/${x.id}`, { method: "DELETE", headers: { Authorization: T } });
  for (const t of texts) await fetch(base + "/api/collections/notes/records", { method: "POST", headers: { ...J, Authorization: T }, body: JSON.stringify({ t }) });
}
const status = (id) => psql(`select status||'|'||coalesce(status_message,'') from project where id='${id}'`);
async function waitStatus(id, not = "restoring", ms = 90_000) {
  const until = Date.now() + ms;
  while (Date.now() < until) {
    const s = status(id);
    if (!s.startsWith(not)) return s;
    await sleep(1000);
  }
  throw new Error(`still ${not}`);
}
async function manualBackup(page, P) {
  const pid = P.split("/").pop();
  psql(`update backup set created_at = created_at - interval '11 minutes' where kind='manual'`); // skip cooldown
  const since = psql(`select now()`);
  await page.goto(P + "/backups");
  await page.click("button:has-text('Back up now')");
  for (let i = 0; i < 60; i++) {
    const s = psql(`select status from backup where project_id='${pid}' and kind='manual' and created_at >= '${since}' order by created_at desc limit 1`);
    if (s === "done") return;
    if (s === "failed") break;
    await sleep(1000);
  }
  const msg = await page.locator("span.text-red-600").allInnerTexts().catch(() => []);
  throw new Error("manual backup did not finish. UI: " + msg.join(" | ") + "\nrows:\n" +
    psql(`select id, kind, status, coalesce(error,''), created_at from backup order by created_at desc limit 20`));
}
async function restore(page, P, backupId) {
  await page.goto(P + "/backups");
  await page.click(`li[data-backup-id="${backupId}"] button:has-text('Restore')`);
  const form = page.locator(`li[data-backup-id="${backupId}"] form`);
  await form.locator("input[name=confirm]").fill("Shop");
  await form.locator("button.btn-danger").click();
  await sleep(1500);
}
function restartPocketBase(port) {
  const { WORK, PB_BIN, APP_DIR } = process.env;
  const pidFile = `${WORK}/pb${port}.pid`; // written by harness.sh (PID-based, no process-name matching)
  const old = Number(readFileSync(pidFile, "utf8"));
  try { process.kill(old); } catch {}
  for (let i = 0; i < 50; i++) { try { process.kill(old, 0); execSync("sleep 0.2"); } catch { break; } }
  execSync(`rm -rf "${WORK}/pb${port}" && "${PB_BIN}" superuser upsert admin@proj.test SuperSecret123 --dir="${WORK}/pb${port}" >/dev/null`);
  const child = spawn(PB_BIN, ["serve", `--http=127.0.0.1:${port}`, `--dir=${WORK}/pb${port}`, "--automigrate=false", `--hooksDir=${APP_DIR}/../pocketbase/pb_hooks`], {
    detached: true, stdio: "ignore", env: { ...process.env, DATBASE_CONTROL_SECRET: controlHeaders(String(port))["X-Datbase-Control"] },
  });
  child.unref();
  writeFileSync(pidFile, String(child.pid));
  track(child); // stopped at the end of the suite (and by harness.sh)
}
async function spawnApp(port, env) {
  const log = `${process.env.WORK}/app-${port}.log`;
  const child = track(spawn("node", ["server.js"], {
    cwd: `${process.env.APP_DIR}/.next/standalone`,
    env: { ...process.env, PORT: String(port), ...env },
    stdio: ["ignore", "pipe", "pipe"],
  }));
  let out = "";
  child.stdout.on("data", (d) => (out += d));
  child.stderr.on("data", (d) => (out += d));
  child.logs = () => out;
  for (let i = 0; i < 30; i++) { try { if ((await fetch(`http://127.0.0.1:${port}/login`, { signal: AbortSignal.timeout(3000) })).ok) break; } catch {} await sleep(1000); }
  return child;
}

// ---------------------------------------------------------------- setup
s3mode("ok");
const b = await browser();
const a = await signup(b, "a@test.com");
const PA = linkProject("a@test.com", { id: "projaaaaaaaaaaaa", name: "Shop", slug: "8090" });
const PBB = "http://127.0.0.1:8091";
const PBA = "http://127.0.0.1:8090";
const PB2 = linkProject("a@test.com", { id: "projbbbbbbbbbbbb", name: "Shop", slug: "8091" });
await seedNotes(PBA, ["a1", "a2"]);
await seedNotes(PBB, ["b1", "b2"]);

// ---------------------------------------------------------------- R-04
step("R-04: manual backup of A");
await manualBackup(a, PA);
const M = psql(`select id from backup where project_id='projaaaaaaaaaaaa' and status='done' order by created_at desc limit 1`);
const mState = await notes(PBA); // "a1,a2"
psql(`update backup set created_at = now() - interval '7.5 days' where id='${M}'`);
for (let i = 0; i < 7; i++) {
  const k = `projects/projaaaaaaaaaaaa/fake-d${i}.dbk`;
  await s3.send(new PutObjectCommand({ Bucket, Key: k, Body: "x" }));
  psql(`insert into backup (id, project_id, kind, status, storage_key, size_bytes, created_at, finished_at) values ('faked${i}aaaaaaaaaa','projaaaaaaaaaaaa','auto','done','${k}',1, ${i === 0 ? "now() - interval '1 minute'" : `date_trunc('day', now()) - interval '${i} days' + interval '12 hours'`}, now())`); // midday of past days, today's just now: never in the future, any time of day
}
await s3.send(new PutObjectCommand({ Bucket, Key: "projects/projaaaaaaaaaaaa/fake-w0.dbk", Body: "x" }));
psql(`insert into backup (id, project_id, kind, status, storage_key, size_bytes, created_at, finished_at) values ('fakew0aaaaaaaaaaa','projaaaaaaaaaaaa','auto','done','projects/projaaaaaaaaaaaa/fake-w0.dbk',1, now() - interval '7.4 days', now())`);
await setNotes(PBA, ["changed"]);
await restore(a, PA, M);
const r04 = await waitStatus("projaaaaaaaaaaaa");
must(r04 === "active|", "restore status " + r04);
must((await notes(PBA)) === mState, `restored data ${await notes(PBA)} vs ${mState}`);
ok(`R-04: restoring a backup that retention would otherwise expire works (data back to "${mState}"); its safety backup didn't delete it`);

// ---------------------------------------------------------------- R-06
step("R-06: backups while storage deletes fail");
// Three extra backups on an older day that already has a newer one → retention must expire them.
for (let h = 0; h < 3; h++) {
  const k = `projects/projaaaaaaaaaaaa/extra-${h}.dbk`;
  await s3.send(new PutObjectCommand({ Bucket, Key: k, Body: "x" }));
  psql(`insert into backup (id, project_id, kind, status, storage_key, size_bytes, created_at, finished_at) values ('extra${h}aaaaaaaaaaa','projaaaaaaaaaaaa','auto','done','${k}',1, date_trunc('day', now()) - interval '2 days' + interval '${h} hours', now())`);
}
s3mode("no-delete");
await manualBackup(a, PA);
must(psql(`select count(*) from backup where id like 'extra%'`) === "3", "expired rows kept for retry while deletes fail");
must((await objects("projects/projaaaaaaaaaaaa/extra-")).length === 3, "objects still there");
s3mode("ok");
await manualBackup(a, PA);
for (let i = 0; i < 15 && psql(`select count(*) from backup where id like 'extra%'`) !== "0"; i++) await sleep(1000); // retention runs right after "done"
must(psql(`select count(*) from backup where id like 'extra%'`) === "0", "retention retried");
must((await objects("projects/projaaaaaaaaaaaa/extra-")).length === 0, "objects removed on retry");
ok("R-06: while storage deletes fail, the new backup still ends 'done' and expired backups wait; next run removes them (3 → 0)");

// ---------------------------------------------------------------- R-01
step("R-01: manual backup of B");
await manualBackup(a, PB2);
const bState = await notes(PBB);
step("R-01: pause B");
await a.goto(PB2); await a.click("button:has-text('Pause')"); await sleep(1500);
must(status("projbbbbbbbbbbbb").startsWith("paused"), "B paused");
// Host lost: both projects' data and containers are gone
step("R-01: wipe both backends (restart PocketBase 8090/8091 with empty data)");
restartPocketBase(8090); restartPocketBase(8091); await sleep(2000);
step("R-01: fake Docker forgets all containers");
await forgetContainers();
step("R-01: read wiped data");
must((await notes(PBA)) === "" && (await notes(PBB)) === "", "data wiped");
// The newest backup of A is damaged: recovery must fall back to the previous one.
await s3.send(new PutObjectCommand({ Bucket, Key: "projects/projaaaaaaaaaaaa/damaged.dbk", Body: "not a backup" }));
psql(`insert into backup (id, project_id, kind, status, storage_key, size_bytes, created_at, finished_at) values ('damagedaaaaaaaaa','projaaaaaaaaaaaa','manual','done','projects/projaaaaaaaaaaaa/damaged.dbk',12, now(), now())`);
step("R-01: start a recovery instance (RECOVER_ON_START=1) on :3001");
const rec = await spawnApp(3001, { RECOVER_ON_START: "1", BACKUP_FIRST_DELAY_SEC: "3600" });
step("R-01: wait for '[recover] done'");
for (let i = 0; i < 90 && !rec.logs().includes("[recover] done"); i++) await sleep(1000);
if (!rec.logs().includes("[recover] done")) console.error("recovery instance log:\n" + rec.logs().slice(-4000));
rec.kill();
must(rec.logs().includes("[recover] done"), "recovery finished:\n" + rec.logs());
must((await notes(PBA)) !== "", "A restored. Recovery log:\n" + rec.logs().slice(-3000));
must(/8090: backup damagedaaaaaaaaa unusable .*trying an older one/.test(rec.logs()), "fallback logged:\n" + rec.logs().slice(-2000));
must((await notes(PBB)) === bState, `B restored ${await notes(PBB)} vs ${bState}`);
step("R-01: check restored data and Docker calls");
const log = await dockerLog();
must(log.includes("POST /containers/dbp-8091/stop"), "paused project stopped again: " + log.join(" | "));
must(status("projbbbbbbbbbbbb").startsWith("paused") && status("projaaaaaaaaaaaa").startsWith("active"), "statuses kept");
ok(`R-01: recovery rebuilt the active AND the paused project from backups (B="${bState}"); B was stopped again and is still paused; A's damaged newest backup was skipped for the previous one`);
step("R-01: resume B with its container missing");
await forgetContainers(); // B's container is gone again (e.g. moved servers)
await a.goto(PB2); await a.click("button:has-text('Resume')");
for (let i = 0; i < 30 && !status("projbbbbbbbbbbbb").startsWith("active"); i++) await sleep(1000);
must(status("projbbbbbbbbbbbb").startsWith("active"), "resume recreated: " + status("projbbbbbbbbbbbb"));
must((await dockerLog()).filter((c) => c === "POST /containers/create").length >= 3, "container recreated on resume");
ok("R-01: Resume on a project whose container is missing recreates it instead of failing");

// ---------------------------------------------------------------- R-02
step("R-02: delete A while storage is down");
const before = (await objects("projects/projaaaaaaaaaaaa/")).length;
s3mode("down");
await a.goto(PA);
await a.locator("form:has(button:has-text('Delete project')) input[name=confirm]").fill("Shop");
await a.click("button:has-text('Delete project')");
await sleep(3000);
must(status("projaaaaaaaaaaaa").startsWith("deleting|Removing backups"), "pending deletion: " + status("projaaaaaaaaaaaa"));
ok(`R-02: storage outage during delete → project stays 'deleting' (${before} backups kept, nothing orphaned)`);
s3mode("ok");
psql(`update project set updated_at = now() - interval '2 minutes' where id='projaaaaaaaaaaaa'`);
step("R-02: sweeper instance finishes the deletion");
const sweeper = await spawnApp(3002, { BACKUP_FIRST_DELAY_SEC: "2", BACKUP_CHECK_MIN: "60" });
for (let i = 0; i < 60 && psql(`select count(*) from project where id='projaaaaaaaaaaaa'`) !== "0"; i++) await sleep(1000);
sweeper.kill();
must(psql(`select count(*) from project where id='projaaaaaaaaaaaa'`) === "0", "row deleted by retry");
must((await objects("projects/projaaaaaaaaaaaa/")).length === 0, "objects deleted by retry");
ok("R-02: once storage is back, the scheduler finished the deletion: project row and all its backups gone");
await b.close();
