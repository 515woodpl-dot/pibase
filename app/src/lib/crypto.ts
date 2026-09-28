import { createCipheriv, createDecipheriv, createHmac, randomBytes } from "node:crypto";

// ENCRYPTION_KEY: 32 random bytes, base64. Generate with: openssl rand -base64 32
function key() {
  const k = Buffer.from(process.env.ENCRYPTION_KEY ?? "", "base64");
  if (k.length !== 32) throw new Error("ENCRYPTION_KEY must be 32 bytes, base64-encoded");
  return k;
}

/** AES-256-GCM. Output: base64(iv[12] | tag[16] | ciphertext) */
export function encrypt(plain: string) {
  const iv = randomBytes(12);
  const c = createCipheriv("aes-256-gcm", key(), iv);
  const data = Buffer.concat([c.update(plain, "utf8"), c.final()]);
  return Buffer.concat([iv, c.getAuthTag(), data]).toString("base64");
}

export function decrypt(enc: string) {
  const buf = Buffer.from(enc, "base64");
  const d = createDecipheriv("aes-256-gcm", key(), buf.subarray(0, 12));
  d.setAuthTag(buf.subarray(12, 28));
  return Buffer.concat([d.update(buf.subarray(28)), d.final()]).toString("utf8");
}

export function randomPassword(len = 24) {
  const chars = "ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnpqrstuvwxyz23456789";
  const bytes = randomBytes(len);
  return Array.from(bytes, (b) => chars[b % chars.length]).join("");
}

export function randomId(len = 8) {
  const chars = "abcdefghijklmnopqrstuvwxyz0123456789";
  return Array.from(randomBytes(len), (b) => chars[b % chars.length]).join("");
}

/**
 * Per-project secret that unlocks DATBase's control endpoints inside the project (usage, limits).
 * Derived from ENCRYPTION_KEY + the project's slug, so it's reproducible after recovery and never stored.
 * Passed to the container as DATBASE_CONTROL_SECRET; customers (even with admin rights) can't read it.
 */
export function controlSecret(slug: string) {
  return createHmac("sha256", key()).update(`datbase-control:${slug}`).digest("base64url");
}
