// supabase.functions.invoke against a project whose functions address points at
// a site's server. Started by TestSupabaseFunctionsInvoke.
import assert from "node:assert/strict";
import { createClient } from "@supabase/supabase-js";

const { PIBASE_TEST_URL: url, PIBASE_TEST_ANON_KEY: anonKey, PIBASE_TEST_EMAIL: email, PIBASE_TEST_PASSWORD: password } = process.env;
assert(url && anonKey && email && password, "test address, key and account are required");

const db = createClient(url, anonKey, { auth: { persistSession: false, autoRefreshToken: false } });

// Signed out: the anon key authorizes the call.
let out = await db.functions.invoke("send-quote", { body: { to: "a@b.c" } });
assert.equal(out.error, null);
assert.deepEqual(out.data, { sent: true });

// Signed in: the user's token travels with the call.
const signedIn = await db.auth.signInWithPassword({ email, password });
assert.equal(signedIn.error, null);
out = await db.functions.invoke("nested/route", { method: "GET" });
assert.equal(out.error, null);

// After sign-out the gateway refuses the old token. supabase-js reports it as an error.
const stale = createClient(url, anonKey, {
  auth: { persistSession: false, autoRefreshToken: false },
  global: { headers: { Authorization: `Bearer ${signedIn.data.session.access_token}` } },
});
await db.auth.signOut();
out = await stale.functions.invoke("send-quote", { body: {} });
assert(out.error, "a signed-out token must be refused");
assert.equal(out.error.context.status, 401);
console.log("supabase-js functions.invoke: ok");
