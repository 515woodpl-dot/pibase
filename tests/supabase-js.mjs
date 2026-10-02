import assert from "node:assert/strict";
import { createHmac } from "node:crypto";
import { createClient } from "@supabase/supabase-js";

const url = process.env.PIBASE_TEST_URL;
const anonKey = process.env.PIBASE_TEST_ANON_KEY;
const serviceKey = process.env.PIBASE_TEST_SERVICE_KEY;
assert(url && anonKey && serviceKey, "test URL and keys are required");

const options = { auth: { persistSession: false, autoRefreshToken: false } };
const anon = createClient(url, anonKey, options);
const service = createClient(url, serviceKey, options);
let result;

function totp(secret) {
  const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZ234567";
  let bits = "";
  for (const char of secret.replaceAll("=", "").toUpperCase()) {
    const value = alphabet.indexOf(char);
    assert(value >= 0, "invalid base32 TOTP secret");
    bits += value.toString(2).padStart(5, "0");
  }
  const key = Buffer.alloc(Math.floor(bits.length / 8));
  for (let i = 0; i < key.length; i++) key[i] = Number.parseInt(bits.slice(i * 8, i * 8 + 8), 2);
  const counter = Buffer.alloc(8);
  counter.writeBigUInt64BE(BigInt(Math.floor(Date.now() / 30000)));
  const digest = createHmac("sha1", key).update(counter).digest();
  const offset = digest[digest.length - 1] & 15;
  const value = (digest.readUInt32BE(offset) & 0x7fffffff) % 1000000;
  return value.toString().padStart(6, "0");
}

const email = `m2-${Date.now()}@example.test`;
let raw = await fetch(`${url}/auth/v1/signup`, {
  method: "POST", headers: { apikey: anonKey, "content-type": "application/json" },
  body: JSON.stringify({ email, password: "correct horse battery staple", data: { plan: "m2" } }),
});
assert.equal(raw.status, 200);
let signup = await raw.json();
assert(signup.confirmation_token);
raw = await fetch(`${url}/auth/v1/verify?token=${encodeURIComponent(signup.confirmation_token)}&type=signup`, { headers: { apikey: anonKey } });
assert.equal(raw.status, 200);
result = await anon.auth.signInWithPassword({ email, password: "correct horse battery staple" });
assert.ifError(result.error);
assert(result.data.session);
result = await anon.auth.refreshSession({ refresh_token: result.data.session.refresh_token });
assert.ifError(result.error);
assert(result.data.session);
raw = await fetch(`${url}/auth/v1/recover`, {
  method: "POST", headers: { apikey: anonKey, "content-type": "application/json" },
  body: JSON.stringify({ email }),
});
assert.equal(raw.status, 200);
const recovery = await raw.json();
assert(recovery.reset_token);
raw = await fetch(`${url}/auth/v1/verify?token=${encodeURIComponent(recovery.reset_token)}&type=recovery`, { headers: { apikey: anonKey } });
assert.equal(raw.status, 200);
const recoverySession = await raw.json();
assert(recoverySession.access_token);
raw = await fetch(`${url}/auth/v1/user`, {
  method: "PATCH", headers: { apikey: anonKey, authorization: `Bearer ${recoverySession.access_token}`, "content-type": "application/json" },
  body: JSON.stringify({ password: "new correct horse battery" }),
});
assert.equal(raw.status, 200);
result = await anon.auth.signInWithPassword({ email, password: "new correct horse battery" });
assert.ifError(result.error);
result = await anon.auth.mfa.enroll({ factorType: "totp", friendlyName: "phone" });
assert.ifError(result.error);
const factor = result.data;
assert(factor.id && factor.totp.secret && factor.totp.qr_code.startsWith("data:image/svg+xml;utf-8,<svg"));
result = await anon.auth.mfa.listFactors();
assert.ifError(result.error);
assert.equal(result.data.all.length, 1);
assert.equal(result.data.all[0].status, "unverified");
result = await anon.auth.mfa.challenge({ factorId: factor.id });
assert.ifError(result.error);
result = await anon.auth.mfa.verify({ factorId: factor.id, challengeId: result.data.id, code: totp(factor.totp.secret) });
assert.ifError(result.error);
result = await anon.auth.mfa.getAuthenticatorAssuranceLevel();
assert.ifError(result.error);
assert.equal(result.data.currentLevel, "aal2");
assert.equal(result.data.nextLevel, "aal2");
result = await anon.auth.mfa.unenroll({ factorId: factor.id });
assert.ifError(result.error);
await anon.auth.signOut();

result = await service.from("groups").insert({ name: "work" }).select().single();
assert.ifError(result.error);
const groupID = result.data.id;

result = await service.from("items").insert([
  { group_id: groupID, name: "private", visible: false },
  { group_id: groupID, name: "public", visible: true },
]).select();
assert.ifError(result.error);
assert.equal(result.data.length, 2);

result = await anon.from("items").select("id,name,groups(name)", { count: "exact" })
  .eq("visible", true).order("id", { ascending: true }).range(0, 9);
assert.ifError(result.error);
assert.equal(result.count, 1);
assert.equal(result.data[0].name, "public");
assert.equal(result.data[0].groups.name, "work");

result = await service.from("items").update({ visible: true }).eq("name", "private").select();
assert.ifError(result.error);
assert.equal(result.data.length, 1);

result = await service.from("items").upsert(
  { group_id: groupID, name: "public", visible: true },
  { onConflict: "name" },
).select();
assert.ifError(result.error);

result = await anon.from("items").select("*", { count: "exact", head: true });
assert.ifError(result.error);
assert.equal(result.count, 2);

result = await anon.rpc("add_numbers", { a: 20, b: 22 });
assert.ifError(result.error);
assert.equal(result.data, 42);

result = await service.from("items").delete().eq("name", "private").select();
assert.ifError(result.error);
assert.equal(result.data.length, 1);

raw = await fetch(`${url}/auth/v1/admin/users`, {
  method: "POST", headers: { apikey: serviceKey, "content-type": "application/json" },
  body: JSON.stringify({ email: `admin-${Date.now()}@example.test`, password: "admin password 123", email_confirm: true, ban_duration: "1h" }),
});
assert.equal(raw.status, 201);
const adminCreated = await raw.json();
assert(adminCreated.user.id);
raw = await fetch(`${url}/auth/v1/admin/users/${adminCreated.user.id}`, { method: "DELETE", headers: { apikey: serviceKey } });
assert.equal(raw.status, 204);

result = await service.storage.createBucket("m3-public", { public: true, fileSizeLimit: 1024 });
assert.ifError(result.error);
result = await service.storage.from("m3-public").upload("hello.txt", new Blob(["hello storage"]), { contentType: "text/plain", upsert: true });
assert.ifError(result.error);
result = await anon.storage.from("m3-public").download("hello.txt");
assert.ifError(result.error);
assert.equal(await result.data.text(), "hello storage");
result = await anon.storage.from("m3-public").list();
assert.ifError(result.error);
assert.equal(result.data[0].name, "hello.txt");
result = await service.storage.from("m3-public").remove(["hello.txt"]);
assert.ifError(result.error);
result = await service.storage.deleteBucket("m3-public");
assert.ifError(result.error);

console.log("supabase-js M6a acceptance passed");
