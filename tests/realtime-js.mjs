import assert from "node:assert/strict";
import { createClient } from "@supabase/supabase-js";

const url = process.env.PIBASE_TEST_URL;
const anonKey = process.env.PIBASE_TEST_ANON_KEY;
assert(url && anonKey, "PIBASE_TEST_URL and PIBASE_TEST_ANON_KEY are required");

const options = { auth: { persistSession: false, autoRefreshToken: false } };
const first = createClient(url, anonKey, options);
const second = createClient(url, anonKey, options);
const topic = `m5b-${Date.now()}`;

function subscribed(channel) {
  return new Promise((resolve, reject) => {
    const timer = setTimeout(() => reject(new Error("subscribe timed out")), 5000);
    channel.subscribe((status, error) => {
      if (status === "SUBSCRIBED") {
        clearTimeout(timer);
        resolve();
      } else if (status === "CHANNEL_ERROR" || status === "TIMED_OUT") {
        clearTimeout(timer);
        reject(error ?? new Error(status));
      }
    });
  });
}

function nextEvent(register, label) {
  return new Promise((resolve, reject) => {
    const timer = setTimeout(() => reject(new Error(`${label} timed out`)), 5000);
    register((payload) => {
      clearTimeout(timer);
      resolve(payload);
    });
  });
}

const a = first.channel(topic, {
  config: { broadcast: { self: true, ack: true }, presence: { key: "js-a" } },
});
const b = second.channel(topic, {
  config: { broadcast: { self: false, ack: true }, presence: { key: "js-b" } },
});

const broadcast = nextEvent(
  (done) => b.on("broadcast", { event: "cursor" }, done),
  "broadcast",
);
const joined = nextEvent(
  (done) => b.on("presence", { event: "join" }, done),
  "presence join",
);
const left = nextEvent(
  (done) => b.on("presence", { event: "leave" }, done),
  "presence leave",
);

try {
  await Promise.all([subscribed(a), subscribed(b)]);
  assert.equal(await a.send({ type: "broadcast", event: "cursor", payload: { x: 42 } }), "ok");
  assert.deepEqual((await broadcast).payload, { x: 42 });
  assert.equal(await a.track({ online_at: "now" }), "ok");
  const join = await joined;
  assert.equal(join.key, "js-a");
  assert.equal(join.newPresences[0].online_at, "now");
  assert.equal(await a.untrack(), "ok");
  const leave = await left;
  assert.equal(leave.key, "js-a");
} finally {
  await Promise.all([first.removeAllChannels(), second.removeAllChannels()]);
}

console.log("supabase-js M5b realtime acceptance passed");
