// Backup scheduler fixes R-03 (due check), R-05 (failed pg_dump), R-07 (overlapping sweeps).
// Run via: BACKUP_FIRST_DELAY_SEC=20 BACKUP_CHECK_MIN=1 tests/e2e/run-backups.sh backup-scheduler.mjs
import { execSync, spawn } from "node:child_process";
import { readFileSync, writeFileSync } from "node:fs";
import { browser, linkProject, must, ok, psql, requireApp, signup, sleep } from "./lib.mjs";
import { track } from "./lib.mjs";

const { S3Client, ListObjectsV2Command } = requireApp("@aws-sdk/client-s3");
const s3 = new S3Client({ region: "us-east-1", endpoint: process.env.S3_DIRECT, forcePathStyle: true, credentials: { accessKeyId: "test", secretAccessKey: "test" } });
const objects = async (Prefix) => ((await s3.send(new ListObjectsV2Command({ Bucket: process.env.BACKUP_S3_BUCKET, Prefix }))).Contents ?? []).map((o) => o.Key);
const pgdump = (m) => writeFileSync(process.env.FAKE_PGDUMP_MODE, m);

pgdump("fail");
// A second DATBase instance on the same database, started at nearly the same time (R-07).
const app2 = track(spawn("node", ["server.js"], { cwd: `${process.env.APP_DIR}/.next/standalone`, env: { ...process.env, PORT: "3001" }, stdio: ["ignore", "pipe", "pipe"] }));
let log2 = "";
app2.stdout.on("data", (d) => (log2 += d));
app2.stderr.on("data", (d) => (log2 += d));

const b = await browser();
const a = await signup(b, "a@test.com");
const P = linkProject("a@test.com", { id: "projaaaaaaaaaaaa", name: "Shop", slug: "8090" });
await a.goto(P + "/backups");
await a.click("button:has-text('Back up now')");
for (let i = 0; i < 30 && psql(`select count(*) from backup where kind='manual' and status='done'`) !== "1"; i++) await sleep(1000);
must(psql(`select count(*) from backup where kind='manual' and status='done'`) === "1", "manual backup");

// R-03: a recent manual backup doesn't count as the daily backup; the first sweep still makes an "auto" one.
for (let i = 0; i < 60 && psql(`select count(*) from backup where kind='auto' and status='done'`) === "0"; i++) await sleep(1000);
must(psql(`select count(*) from backup where kind='auto' and status='done'`) === "1", "auto backup despite manual");
ok("R-03: the scheduler still made the daily (auto) backup although a manual backup existed");

// R-05: failing pg_dump never produces a control backup, and leaves no temp objects behind.
await sleep(8000);
const appLog = () => readFileSync(`${process.env.WORK}/app.log`, "utf8") + log2;
must(/control database: pg_dump failed/.test(appLog()), "failure logged");
must((await objects("control/")).length === 0 && (await objects("control-tmp/")).length === 0, "nothing stored");
ok("R-05: failed pg_dump → nothing under control/ (and no temp leftovers); the failure is logged");

// R-05 + R-07: next sweeps with a slow, successful pg_dump on BOTH instances → exactly one control backup.
pgdump("slow");
for (let i = 0; i < 120 && (await objects("control/")).length === 0; i++) await sleep(1000);
await sleep(40_000); // give the other instance's sweep time to (not) run a second dump
const control = await objects("control/");
must(control.length === 1, "exactly one control backup, got " + control.length);
must((await objects("control-tmp/")).length === 0, "temp promoted and removed");
must(psql(`select count(*) from backup where kind='auto'`) === "1", "no duplicate auto backups");
ok(`R-07: two instances, overlapping sweeps → exactly 1 control backup (${control[0]}) and 1 daily backup`);

// The documented recovery command picks the newest valid control backup and restores it.
psql("drop database if exists datbase_e2e_restored", "postgres");
psql("create database datbase_e2e_restored", "postgres");
const restoredUrl = process.env.DATABASE_URL.replace(/\/[^/?]+\?/, "/datbase_e2e_restored?");
execSync(`node scripts/backup-tool.mjs restore-control`, { cwd: process.env.APP_DIR, env: { ...process.env, DATABASE_URL: restoredUrl }, stdio: "pipe" });
must(psql(`select count(*) from project`, "datbase_e2e_restored") === "1", "restored control db");
ok("backup-tool restore-control (default = newest) restored a complete control database");

app2.kill();
await b.close();
