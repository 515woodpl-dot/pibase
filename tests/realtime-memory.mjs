import assert from "node:assert/strict";
import { createClient } from "@supabase/supabase-js";

const url = process.env.PIBASE_TEST_URL;
const anonKey = process.env.PIBASE_TEST_ANON_KEY;
const count = Number(process.env.PIBASE_REALTIME_CONNECTIONS ?? "32");
assert(url && anonKey && Number.isInteger(count) && count > 0);

const clients = [];
const joins = [];
for (let i = 0; i < count; i += 1) {
  const client = createClient(url, anonKey, {
    auth: { persistSession: false, autoRefreshToken: false },
  });
  clients.push(client);
  const channel = client.channel(`memory-${i}`, {
    config: { broadcast: { self: false, ack: false } },
  });
  joins.push(new Promise((resolve, reject) => {
    const timer = setTimeout(() => reject(new Error(`socket ${i} timed out`)), 10000);
    channel.subscribe((status, error) => {
      if (status === "SUBSCRIBED") {
        clearTimeout(timer);
        resolve();
      } else if (status === "CHANNEL_ERROR" || status === "TIMED_OUT") {
        clearTimeout(timer);
        reject(error ?? new Error(`${status} for socket ${i}`));
      }
    });
  }));
}

await Promise.all(joins);
console.log(`READY ${count}`);
await new Promise(() => {});
