// DATBase backup tool: disaster recovery and manual inspection.
// Uses the same settings as the app (.env): ENCRYPTION_KEY, BACKUP_S3_*, DATABASE_URL.
//
//   node scripts/backup-tool.mjs list [prefix]              list stored backups (default: everything)
//   node scripts/backup-tool.mjs get <key> <out-file>       download + decrypt one backup (a project backup is a .zip)
//   node scripts/backup-tool.mjs restore-control [key]      restore DATBase's own database (default: newest)
//
// In Docker:  docker compose run --rm app node scripts/backup-tool.mjs list
import { GetObjectCommand, ListObjectsV2Command, S3Client } from "@aws-sdk/client-s3";
import { spawnSync } from "node:child_process";
import { createDecipheriv, hkdfSync } from "node:crypto";
import { createReadStream, createWriteStream } from "node:fs";
import { mkdtemp, rm, stat } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { pipeline } from "node:stream/promises";

const need = (k) => process.env[k] || (console.error(`Missing ${k} (use the same .env as the app)`), process.exit(1));
const s3 = new S3Client({
  region: process.env.BACKUP_S3_REGION || "us-east-1",
  endpoint: process.env.BACKUP_S3_ENDPOINT || undefined,
  forcePathStyle: process.env.BACKUP_S3_FORCE_PATH_STYLE === "true",
  credentials: { accessKeyId: need("BACKUP_S3_KEY"), secretAccessKey: need("BACKUP_S3_SECRET") },
  requestChecksumCalculation: "WHEN_REQUIRED",
  responseChecksumValidation: "WHEN_REQUIRED",
});
const Bucket = need("BACKUP_S3_BUCKET");
const master = Buffer.from(need("ENCRYPTION_KEY"), "base64");
const key = Buffer.from(hkdfSync("sha256", master, Buffer.alloc(0), "datbase-backup-v1", 32));

async function list(prefix = "") {
  const out = [];
  let ContinuationToken;
  do {
    const r = await s3.send(new ListObjectsV2Command({ Bucket, Prefix: prefix, ContinuationToken }));
    out.push(...(r.Contents ?? []));
    ContinuationToken = r.IsTruncated ? r.NextContinuationToken : undefined;
  } while (ContinuationToken);
  return out.sort((a, b) => (a.Key < b.Key ? 1 : -1)); // newest first (keys start with a timestamp)
}

async function readRange(path, start, length) {
  const chunks = [];
  for await (const c of createReadStream(path, { start, end: start + length - 1 })) chunks.push(c);
  return Buffer.concat(chunks);
}

async function get(objKey, outFile) {
  const dir = await mkdtemp(join(tmpdir(), "dbk-"));
  const enc = join(dir, "enc");
  try {
    const res = await s3.send(new GetObjectCommand({ Bucket, Key: objKey }));
    await pipeline(res.Body, createWriteStream(enc));
    const { size } = await stat(enc);
    const head = await readRange(enc, 0, 16);
    if (head.subarray(0, 4).toString() !== "DBK1") throw new Error("Not a DATBase backup");
    const d = createDecipheriv("aes-256-gcm", key, head.subarray(4, 16));
    d.setAuthTag(await readRange(enc, size - 16, 16));
    await pipeline(createReadStream(enc, { start: 16, end: size - 17 }), d, createWriteStream(outFile));
  } finally {
    await rm(dir, { recursive: true, force: true });
  }
}

const [cmd, a, b] = process.argv.slice(2);
if (cmd === "list") {
  for (const o of await list(a)) console.log(`${o.LastModified?.toISOString()}  ${String(o.Size).padStart(12)}  ${o.Key}`);
} else if (cmd === "get" && a && b) {
  await get(a, b);
  console.log(`Decrypted ${a} → ${b}`);
} else if (cmd === "restore-control") {
  const objKey = a ?? (await list("control/"))[0]?.Key;
  if (!objKey) throw new Error("No control database backups found");
  const file = join(await mkdtemp(join(tmpdir(), "dbk-")), "control.dump");
  await get(objKey, file);
  console.log(`Restoring ${objKey} into DATABASE_URL …`);
  const r = spawnSync(process.env.PG_RESTORE || "pg_restore", ["--clean", "--if-exists", "--no-owner", "--dbname", need("DATABASE_URL"), file], {
    stdio: "inherit",
  });
  await rm(file, { force: true });
  if (r.status !== 0) process.exit(r.status ?? 1);
  console.log("Control database restored. Next: set RECOVER_ON_START=1 and start the app (see README).");
} else {
  console.log("usage: backup-tool.mjs list [prefix] | get <key> <out-file> | restore-control [key]");
  process.exit(1);
}
