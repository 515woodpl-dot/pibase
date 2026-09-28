import { APP as B, PB, psql, enc, ok, must, sleep, browser, requireApp, fixture, WORK_DIR } from "./lib.mjs";
import { execSync } from "node:child_process";
const req = requireApp;
const { S3Client, ListObjectsV2Command, GetObjectCommand, PutObjectCommand } = req("@aws-sdk/client-s3");
const s3 = new S3Client({ region: "us-east-1", endpoint: process.env.S3_DIRECT, forcePathStyle: true, credentials: { accessKeyId: "test", secretAccessKey: "test" } });
const Bucket = process.env.BACKUP_S3_BUCKET;
const list = async (Prefix) => ((await s3.send(new ListObjectsV2Command({ Bucket, Prefix }))).Contents ?? []).map((o) => o.Key);

const J = { "content-type": "application/json" };
const su = async () => (await (await fetch(PB + "/api/collections/_superusers/auth-with-password", { method: "POST", headers: J, body: JSON.stringify({ identity: "admin@proj.test", password: "SuperSecret123" }) })).json()).token;
const notes = async () => { const T = await su(); const r = await (await fetch(PB + "/api/collections/notes/records?sort=t", { headers: { Authorization: T } })).json();
  return r.items.map((x) => x.t + (x.f ? "+file" : "")).join(","); };

// Seed: table + 2 records (one with a file)
let T = await su();
await fetch(PB + "/api/collections", { method: "POST", headers: { ...J, Authorization: T }, body: JSON.stringify({ name: "notes", type: "base", fields: [{ name: "t", type: "text" }, { name: "f", type: "file", maxSelect: 1 }] }) });
const fd = new FormData(); fd.set("t", "v1-a"); fd.set("f", new Blob(["original file"]), "orig.txt");
const recA = await (await fetch(PB + "/api/collections/notes/records", { method: "POST", headers: { Authorization: T }, body: fd })).json();
await fetch(PB + "/api/collections/notes/records", { method: "POST", headers: { ...J, Authorization: T }, body: JSON.stringify({ t: "v1-b" }) });
const v1 = await notes(); ok("seeded:", v1);

const b = await browser();
const a = await (await b.newContext()).newPage(); a.on("dialog", (d) => d.accept());
await a.goto(B + "/signup"); await a.fill("input[name=name]", "T"); await a.fill("input[name=email]", "a@test.com");
await a.fill("input[name=password]", "password123"); await a.click("button:has-text('Create account')"); await a.waitForURL(B + "/dashboard", { waitUntil: "commit" });
const uid = psql(`select id from "user" where email=\'a@test.com\'`);
psql(`insert into project (id, owner_id, name, slug, status, admin_email, admin_password_enc) values ('proj00000000000a','${uid}','Shop','8090','active','admin@proj.test','${enc("SuperSecret123")}')`);
const P = B + "/dashboard/proj00000000000a";
const waitRow = async (sel, ms = 60000) => { const until = Date.now() + ms; while (Date.now() < until) { await a.goto(P + "/backups"); if (await a.locator(sel).count()) return; await sleep(1500); } throw new Error("timeout waiting for " + sel); };
const waitActive = async () => { for (let i = 0; i < 60; i++) { if (psql("select status from project where id='proj00000000000a'") !== "restoring") return psql("select status||'|'||coalesce(status_message,'') from project where id='proj00000000000a'"); await sleep(1000); } throw new Error("restore did not finish"); };

// 1. Manual backup
await a.goto(P + "/backups"); await a.click("button:has-text('Back up now')");
await waitRow("li:has-text('Manual') button:has-text('Restore')");
const keys = await list("projects/proj00000000000a/"); must(keys.length === 1, "one object");
const obj = await (await s3.send(new GetObjectCommand({ Bucket, Key: keys[0] }))).Body.transformToByteArray();
must(Buffer.from(obj.slice(0, 4)).toString() === "DBK1" && !Buffer.from(obj).includes(Buffer.from("v1-a")), "encrypted");
ok(`manual backup stored off-server, encrypted (${obj.length} bytes, no plaintext inside):`, keys[0]);
await a.screenshot({ path: `${WORK_DIR}/s6-backups.png`, fullPage: true });

// 2. Cooldown
await a.click("button:has-text('Back up now')"); await a.waitForSelector("text=once every 10 minutes"); ok("manual backup cooldown enforced");

// 3. Change data, then restore
T = await su();
await fetch(`${PB}/api/collections/notes/records/${recA.id}`, { method: "DELETE", headers: { Authorization: T } });
await fetch(PB + "/api/collections/notes/records", { method: "POST", headers: { ...J, Authorization: T }, body: JSON.stringify({ t: "v2-new" }) });
const v2 = await notes(); ok("changed data:", v2);
await a.goto(P + "/backups"); await a.click("li:has-text('Manual') button:has-text('Restore')");
await a.fill("form:has(input[name=backupId]) input[name=confirm]", "Shop"); await a.click("form:has(input[name=backupId]) button.btn-danger");
await a.waitForSelector("text=Restoring", { timeout: 15000 }).catch(() => {}); ok("restore started, status:", psql("select status from project where id='proj00000000000a'"));
const st = await waitActive(); must(st === "active|", "status after restore " + st);
const after = await notes(); must(after === v1, `expected ${v1}, got ${after}`);
T = await su(); const r1 = await (await fetch(PB + "/api/collections/notes/records?filter=" + encodeURIComponent('t="v1-a"'), { headers: { Authorization: T } })).json();
const ft = (await (await fetch(PB + "/api/files/token", { method: "POST", headers: { Authorization: T } })).json()).token;
const fileBody = await (await fetch(`${PB}/api/files/notes/${r1.items[0].id}/${r1.items[0].f}?token=${ft}`)).text();
must(fileBody === "original file", "file restored");
ok("restore brought back exact data + file:", after, `| file: "${fileBody}"`);
await a.goto(P + "/tables/notes"); must(await a.locator("td:has-text('v1-a')").count(), "dashboard works"); ok("dashboard works after restore");
await waitRow("li:has-text('Before restore') button:has-text('Restore')"); ok("safety backup 'Before restore' was taken");

// 4. Undo the restore using the safety backup
await a.click("li:has-text('Before restore') button:has-text('Restore')");
await a.fill("form:has(input[name=backupId]) input[name=confirm]", "Shop"); await a.click("form:has(input[name=backupId]) button.btn-danger");
await sleep(2000); await waitActive(); must((await notes()) === v2, "undo"); ok("undo via safety backup →", await notes());

// 5. Tampered backup is refused; data untouched
const manualKey = psql("select storage_key from backup where kind='manual'");
const bad = Buffer.from(obj); bad[bad.length - 40] ^= 0xff;
await s3.send(new PutObjectCommand({ Bucket, Key: manualKey, Body: bad }));
await a.goto(P + "/backups"); await a.click("li:has-text('Manual') button:has-text('Restore')");
await a.fill("form:has(input[name=backupId]) input[name=confirm]", "Shop"); await a.click("form:has(input[name=backupId]) button.btn-danger");
await sleep(2000); const st2 = await waitActive();
must(st2.startsWith("active|Restore failed"), "tamper status " + st2); must((await notes()) === v2, "data untouched");
await a.goto(P + "/backups"); must(await a.locator("text=Your data was not changed").count(), "banner");
ok("tampered backup refused →", st2.split("|")[1], "| data unchanged");
await s3.send(new PutObjectCommand({ Bucket, Key: manualKey, Body: obj })); // put the good one back

// 6. Control database backup by the scheduler (first run 60s after boot)
let control = []; for (let i = 0; i < 40 && !control.length; i++) { control = await list("control/"); if (!control.length) await sleep(3000); }
must(control.length, "control backup"); ok("scheduler backed up DATBase's own database:", control[0]);

// 7. Retention: fake 12 older backups spread over 40 days, then trigger one more backup
for (let i = 0; i < 12; i++) {
  const days = 2 + i * 3.3; const id = "fake" + String(i).padStart(12, "0"); const k = `projects/proj00000000000a/old-${id}.dbk`;
  await s3.send(new PutObjectCommand({ Bucket, Key: k, Body: "x" }));
  psql(`insert into backup (id, project_id, kind, status, storage_key, size_bytes, created_at, finished_at) values ('${id}','proj00000000000a','auto','done','${k}',1, now() - interval '${days} days', now())`);
}
psql("update backup set created_at = created_at - interval '11 minutes' where kind='manual'");
const before = Number(psql("select count(*) from backup where status='done'"));
await a.goto(P + "/backups"); await a.click("button:has-text('Back up now')"); await sleep(1000);
for (let i = 0; i < 30 && psql("select count(*) from backup where status='running'") !== "0"; i++) await sleep(1000);
const kept = psql("select count(*) from backup where status='done'"); const objs = (await list("projects/proj00000000000a/")).length;
must(Number(kept) < before + 1 && Number(kept) === objs, `kept ${kept} rows / ${objs} objects`);
ok(`retention: ${before + 1} backups → kept ${kept} (1 per day ×7 + weekly + recent manual/pre-restore extras); bucket objects match: ${objs}`);

// 8. Isolation
const bp = await (await b.newContext()).newPage();
await bp.goto(B + "/signup"); await bp.fill("input[name=name]", "B"); await bp.fill("input[name=email]", "b@test.com");
await bp.fill("input[name=password]", "password123"); await bp.click("button:has-text('Create account')"); await bp.waitForURL(B + "/dashboard", { waitUntil: "commit" });
must((await bp.goto(P + "/backups")).status() === 404, "isolation"); ok("user B gets 404 on A's Backups tab");

// 9. Delete project removes its backups
await a.goto(P); await a.fill("form:has(input[name=confirm]) input[name=confirm]", "Shop"); await a.click("button:has-text('Delete project')");
await a.waitForURL(B + "/dashboard", { waitUntil: "commit" }).catch(() => {}); await sleep(1500);
const left = await list("projects/proj00000000000a/"); must(left.length === 0, "backups deleted with project"); ok("project deleted → all its backups removed from the bucket");
await b.close();
