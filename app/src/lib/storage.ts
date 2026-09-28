import "server-only";
import {
  CopyObjectCommand,
  DeleteObjectsCommand,
  GetObjectCommand,
  ListObjectsV2Command,
  S3Client,
  type _Object,
} from "@aws-sdk/client-s3";
import { Upload } from "@aws-sdk/lib-storage";
import { createCipheriv, createDecipheriv, hkdfSync, randomBytes } from "node:crypto";
import { createReadStream, createWriteStream } from "node:fs";
import { stat } from "node:fs/promises";
import { PassThrough, Readable, Transform } from "node:stream";
import { pipeline } from "node:stream/promises";

/*
 * Off-server backup storage (DigitalOcean Spaces or any S3-compatible bucket).
 * The keys live ONLY in the control plane; project containers never see them.
 *
 * Every object is encrypted before upload:  "DBK1" | iv(12) | AES-256-GCM ciphertext | tag(16)
 * The key is derived from ENCRYPTION_KEY, so keep a copy of your .env somewhere safe off the server.
 */

const MAGIC = Buffer.from("DBK1");

export function backupsConfigured() {
  return Boolean(process.env.BACKUP_S3_BUCKET && process.env.BACKUP_S3_KEY && process.env.BACKUP_S3_SECRET);
}

let client: S3Client | undefined;
function s3() {
  client ??= new S3Client({
    region: process.env.BACKUP_S3_REGION || "us-east-1",
    endpoint: process.env.BACKUP_S3_ENDPOINT || undefined,
    forcePathStyle: process.env.BACKUP_S3_FORCE_PATH_STYLE === "true",
    credentials: { accessKeyId: process.env.BACKUP_S3_KEY!, secretAccessKey: process.env.BACKUP_S3_SECRET! },
    // DigitalOcean Spaces and other S3-compatible stores reject the newer default checksum headers.
    requestChecksumCalculation: "WHEN_REQUIRED",
    responseChecksumValidation: "WHEN_REQUIRED",
  });
  return client;
}
const bucket = () => process.env.BACKUP_S3_BUCKET!;

function backupKey() {
  const master = Buffer.from(process.env.ENCRYPTION_KEY ?? "", "base64");
  if (master.length !== 32) throw new Error("ENCRYPTION_KEY must be 32 bytes, base64-encoded");
  return Buffer.from(hkdfSync("sha256", master, Buffer.alloc(0), "datbase-backup-v1", 32));
}

/** Encrypt a stream on the fly. */
function encryptStream() {
  const iv = randomBytes(12);
  const cipher = createCipheriv("aes-256-gcm", backupKey(), iv);
  let started = false;
  return new Transform({
    transform(chunk, _enc, cb) {
      if (!started) {
        this.push(Buffer.concat([MAGIC, iv]));
        started = true;
      }
      cb(null, cipher.update(chunk));
    },
    flush(cb) {
      if (!started) this.push(Buffer.concat([MAGIC, iv]));
      this.push(cipher.final());
      this.push(cipher.getAuthTag());
      cb();
    },
  });
}

/** Encrypt + upload a stream. Returns the number of encrypted bytes stored. */
export async function putEncrypted(key: string, source: Readable) {
  let bytes = 0;
  const counter = new Transform({
    transform(chunk, _e, cb) {
      bytes += chunk.length;
      cb(null, chunk);
    },
  });
  const body = new PassThrough();
  const upload = new Upload({ client: s3(), params: { Bucket: bucket(), Key: key, Body: body }, partSize: 16 * 1024 * 1024 });
  await Promise.all([pipeline(source, encryptStream(), counter, body), upload.done()]);
  return bytes;
}

/** Download an object to a local file (still encrypted). */
export async function getToFile(key: string, path: string) {
  const res = await s3().send(new GetObjectCommand({ Bucket: bucket(), Key: key }));
  await pipeline(res.Body as Readable, createWriteStream(path));
}

/** Decrypt a downloaded backup file into another file. Fails if the data was altered. */
export async function decryptFile(src: string, dest: string) {
  const { size } = await stat(src);
  if (size < 32) throw new Error("Backup file is too small");
  const head = await readRange(src, 0, 16);
  if (!head.subarray(0, 4).equals(MAGIC)) throw new Error("Not a DATBase backup file");
  const tag = await readRange(src, size - 16, 16);
  const decipher = createDecipheriv("aes-256-gcm", backupKey(), head.subarray(4, 16));
  decipher.setAuthTag(tag);
  try {
    await pipeline(createReadStream(src, { start: 16, end: size - 17 }), decipher, createWriteStream(dest));
  } catch {
    throw new Error("The backup file is damaged or was altered, so it was not used");
  }
}

async function readRange(path: string, start: number, length: number) {
  const chunks: Buffer[] = [];
  for await (const c of createReadStream(path, { start, end: start + length - 1 })) chunks.push(c as Buffer);
  return Buffer.concat(chunks);
}

export async function listObjects(prefix: string) {
  const out: _Object[] = [];
  let token: string | undefined;
  do {
    const r = await s3().send(new ListObjectsV2Command({ Bucket: bucket(), Prefix: prefix, ContinuationToken: token }));
    out.push(...(r.Contents ?? []));
    token = r.IsTruncated ? r.NextContinuationToken : undefined;
  } while (token);
  return out;
}

export async function removeKeys(keys: string[]) {
  for (let i = 0; i < keys.length; i += 1000) {
    const batch = keys.slice(i, i + 1000);
    if (batch.length)
      await s3().send(
        new DeleteObjectsCommand({ Bucket: bucket(), Delete: { Objects: batch.map((Key) => ({ Key })) } }),
      );
  }
}

export async function removePrefix(prefix: string) {
  await removeKeys((await listObjects(prefix)).map((o) => o.Key!).filter(Boolean));
}

/** Server-side copy within the bucket (used to promote a verified temporary object). */
export async function copyObject(from: string, to: string) {
  await s3().send(
    new CopyObjectCommand({ Bucket: bucket(), Key: to, CopySource: `${bucket()}/${from.split("/").map(encodeURIComponent).join("/")}` }),
  );
}
