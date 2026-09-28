// AI-01 regression: a project backend must survive realistic concurrent traffic (signed-in lists,
// image downloads, token refreshes, admin calls) without crashing.
//
// Local (via harness):   tests/e2e/harness.sh pb-stress.mjs
// Against a deployed project (e.g. to vet a new PocketBase version on your real server/arch).
// Use a throwaway test project: these requests count toward its plan.
//   PB_URL=https://<project>.<domain> DATBASE_SERVER_KEY=<key> STRESS_SECONDS=120 node tests/e2e/pb-stress.mjs
import { existsSync, readFileSync } from "node:fs";
import { must, ok, PB, pbAdminToken, WORK_DIR } from "./lib.mjs";

const SECS = Number(process.env.STRESS_SECONDS ?? 45);
const WORKERS = Number(process.env.STRESS_WORKERS ?? 60);
const J = { "content-type": "application/json" };
const T = process.env.DATBASE_SERVER_KEY ?? (await pbAdminToken(PB));
const H = { Authorization: T };
const table = `stress_${Date.now().toString(36)}`;

await fetch(PB + "/api/collections", {
  method: "POST",
  headers: { ...J, ...H },
  body: JSON.stringify({ name: table, type: "base", listRule: "", viewRule: "", fields: [{ name: "name", type: "text" }, { name: "photo", type: "file", maxSelect: 1 }] }),
});
const recs = [];
for (let i = 0; i < 12; i++) {
  const fd = new FormData();
  fd.append("name", "item " + i);
  fd.append("photo", new Blob([Buffer.alloc(40_000, i)]), `p${i}.jpg`);
  recs.push(await (await fetch(`${PB}/api/collections/${table}/records`, { method: "POST", headers: H, body: fd })).json());
}
// A few app users with tokens (like signed-in shoppers)
const users = [];
for (let i = 0; i < 5; i++) {
  const email = `stress${i}_${Date.now()}@example.com`;
  await fetch(PB + "/api/collections/users/records", { method: "POST", headers: { ...J, ...H }, body: JSON.stringify({ email, password: "password123", passwordConfirm: "password123" }) });
  const r = await (await fetch(PB + "/api/collections/users/auth-with-password", { method: "POST", headers: J, body: JSON.stringify({ identity: email, password: "password123" }) })).json();
  users.push(r.token);
}

let n = 0;
let errors = 0;
const until = Date.now() + SECS * 1000;
async function worker(w) {
  while (Date.now() < until) {
    const r = Math.random();
    const tok = r < 0.3 ? T : users[w % users.length];
    try {
      let res;
      if (r < 0.35) res = await fetch(`${PB}/api/collections/${table}/records?page=${1 + (n % 2)}&perPage=12&filter=${encodeURIComponent('name ~ "item"')}`, { headers: { Authorization: tok } });
      else if (r < 0.7) { const p = recs[n % recs.length]; res = await fetch(`${PB}/api/files/${p.collectionId}/${p.id}/${p.photo}`); }
      else if (r < 0.85) res = await fetch(PB + "/api/collections/users/auth-refresh", { method: "POST", headers: { Authorization: users[w % users.length] } });
      else res = await fetch(PB + "/api/collections", { headers: H });
      await res.arrayBuffer();
      if (res.status >= 500) errors++;
      n++;
    } catch {
      errors++;
      if (errors > 50) return;
    }
  }
}
await Promise.all(Array.from({ length: WORKERS }, (_, i) => worker(i)));

const alive = await fetch(PB + "/api/health").then((r) => r.ok).catch(() => false);
await fetch(`${PB}/api/collections/${table}`, { method: "DELETE", headers: H }).catch(() => {});
const log = existsSync(`${WORK_DIR}/pb8090.log`) && !process.env.DATBASE_SERVER_KEY ? readFileSync(`${WORK_DIR}/pb8090.log`, "utf8") : "";
must(alive, "backend died under load");
must(!/fatal error/.test(log), "fatal error in backend log");
must(errors === 0, `${errors} failed requests`);
ok(`${n} requests in ${SECS}s from ${WORKERS} concurrent clients (lists, images, token refresh, admin): 0 errors, backend healthy`);
