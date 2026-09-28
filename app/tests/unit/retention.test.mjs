import { test } from "node:test";
import assert from "node:assert/strict";
import { loadTs } from "./load-ts.mjs";

const { pickExpired } = await loadTs("src/lib/retention.ts");
const DAY = 86_400_000;
const NOW = new Date("2026-09-27T12:00:00Z");
const at = (daysAgo, hour = 3) => new Date(Date.UTC(2026, 8, 27 - daysAgo, hour));
const row = (id, kind, date) => ({ id, kind, createdAt: date });
const kept = (rows, now = NOW, prot = []) => {
  const gone = new Set(pickExpired(rows, now, prot).map((r) => r.id));
  return rows.filter((r) => !gone.has(r.id)).map((r) => r.id);
};

test("30 daily backups → 7 daily + 4 weekly", () => {
  const rows = Array.from({ length: 30 }, (_, i) => row(`d${i}`, "auto", at(i)));
  const k = kept(rows);
  assert.deepEqual(k.slice(0, 7), ["d0", "d1", "d2", "d3", "d4", "d5", "d6"]);
  assert.equal(k.length, 11);
});

test("R-03: a burst of manual backups can't evict daily restore points", () => {
  const rows = [
    ...Array.from({ length: 7 }, (_, i) => row(`d${i}`, "auto", at(i))),
    ...Array.from({ length: 8 }, (_, i) => row(`m${i}`, "manual", new Date(NOW.getTime() - i * 60_000))),
  ];
  const k = kept(rows);
  for (let i = 1; i < 7; i++) assert.ok(k.includes(`d${i}`), `daily d${i} kept`);
  for (let i = 0; i < 8; i++) assert.ok(k.includes(`m${i}`), `manual m${i} kept as extra`);
});

test("R-04: the backup being restored is never expired by the safety backup", () => {
  const rows = Array.from({ length: 7 }, (_, i) => row(`d${i}`, "auto", at(i)));
  rows.push(row("pre", "pre-restore", NOW));
  const selected = "d6"; // the 7th-newest, which the old logic deleted
  assert.ok(kept(rows, NOW, [selected]).includes(selected));
});

test("several backups on one day: only that day's newest counts as the daily point", () => {
  const rows = [row("a", "auto", at(1, 3)), row("b", "auto", at(1, 9)), row("c", "auto", at(0))];
  assert.deepEqual(kept(rows).sort(), ["b", "c"]);
});

test("extras are bounded (newest 10) and only for 7 days", () => {
  const rows = [
    row("d0", "auto", at(0)),
    ...Array.from({ length: 15 }, (_, i) => row(`m${i}`, "manual", new Date(NOW.getTime() - (i + 1) * 3_600_000))),
    row("old-manual", "manual", at(20)),
    ...Array.from({ length: 30 }, (_, i) => row(`x${i}`, "auto", at(i + 1))),
  ];
  const k = kept(rows);
  const manualsKept = k.filter((id) => id.startsWith("m"));
  assert.ok(manualsKept.length <= 11, `bounded: ${manualsKept.length}`); // 10 extras (+ possibly today's daily point)
  assert.ok(!k.includes("m14"), "oldest manual in burst expired");
});

test("the newest backup is always kept, and an empty list is fine", () => {
  assert.deepEqual(kept([row("only", "manual", at(40))]), ["only"]);
  assert.deepEqual(pickExpired([], NOW), []);
});

test("gaps between backups: daily tier uses the 7 most recent days that have backups", () => {
  const rows = [0, 3, 6, 9, 12, 15, 18, 21].map((d) => row(`g${d}`, "auto", at(d)));
  const k = kept(rows);
  for (const d of [0, 3, 6, 9, 12, 15, 18]) assert.ok(k.includes(`g${d}`));
});
