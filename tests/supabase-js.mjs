import assert from "node:assert/strict";
import { createClient } from "@supabase/supabase-js";

const url = process.env.PIBASE_TEST_URL;
const anonKey = process.env.PIBASE_TEST_ANON_KEY;
const serviceKey = process.env.PIBASE_TEST_SERVICE_KEY;
assert(url && anonKey && serviceKey, "test URL and keys are required");

const options = { auth: { persistSession: false, autoRefreshToken: false } };
const anon = createClient(url, anonKey, options);
const service = createClient(url, serviceKey, options);
let result;

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

console.log("supabase-js M2 acceptance passed");
