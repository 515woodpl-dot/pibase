// The SQL editor's query checker (pocketbase/pb_hooks/datbase_sql.js), run in Node.
// The same file runs inside every project's engine; tests/e2e/sql-editor.mjs checks it there too.
import { test } from "node:test";
import assert from "node:assert/strict";
import { createRequire } from "node:module";

const S = createRequire(import.meta.url)("../../../pocketbase/pb_hooks/datbase_sql.js");
const rejects = (sql, re) => {
  const r = S.check(sql);
  assert.ok(r.error, `should be rejected: ${sql}`);
  if (re) assert.match(r.error, re, sql);
};
const accepts = (sql) => {
  const r = S.check(sql);
  assert.ok(!r.error, `should be accepted: ${sql} → ${r.error}`);
  return r;
};

test("internal tables are rejected in every spelling SQLite accepts", () => {
  for (const q of [
    `SELECT * FROM _superusers`,
    `SELECT * FROM "_superusers"`,
    "SELECT * FROM `_superusers`",
    `SELECT * FROM [_superusers]`,
    `SELECT * FROM '_superusers'`, // SQLite reads a 'string' as a name here
    `SELECT * FROM "main"."_superusers"`, // the bypass found in review: quoted, schema-qualified
    `SELECT * FROM 'main'.'_superusers'`,
    `SELECT * FROM items, '_superusers'`,
    `SELECT 1 WHERE 1 IN '_superusers'`,
    `SELECT * FROM "_SuperUsers"`,
    `SELECT * FROM _params`,
    `SELECT * FROM sqlite_schema`,
    `SELECT * FROM "sqlite_master"`,
    `SELECT * FROM sqlite_temp_master`,
    `SELECT * FROM sqlite_dbpage`, // raw database pages
    `SELECT * FROM 'sqlite_dbpage'`,
    `SELECT * FROM dbstat`,
    `SELECT * FROM "DBSTAT"`,
    `SELECT * FROM pragma_table_info('items')`,
    `SELECT * FROM 'pragma_table_info'('_superusers')`,
    `SELECT name FROM "pragma_table_list"`,
  ])
    rejects(q);
});

test("schema-qualified names are rejected, so shadowed tables can't be reached", () => {
  rejects(`SELECT * FROM main.users`, /schema prefix/);
  rejects(`SELECT * FROM "main".users`, /schema prefix/);
  rejects(`SELECT * FROM temp.x`, /schema prefix/);
  rejects(`SELECT * FROM 'users'.'id'`, /double quotes/);
});

test("anything that could write, attach or load is rejected", () => {
  for (const q of [
    `INSERT INTO items (label) VALUES ('x')`,
    `REPLACE INTO items (label) VALUES ('x')`,
    `UPDATE items SET qty = 1`,
    `DELETE FROM items`,
    `WITH a AS (SELECT 1) DELETE FROM items`,
    `WITH a AS (SELECT 1) INSERT INTO items SELECT * FROM a`,
    `DROP TABLE items`,
    `CREATE TABLE x (a)`,
    `ALTER TABLE items ADD COLUMN x`,
    `ATTACH 'x.db' AS x`,
    `DETACH x`,
    `VACUUM`,
    `VACUUM INTO '/tmp/copy.db'`,
    `PRAGMA query_only = OFF`,
    `PRAGMA writable_schema = ON`,
    `BEGIN`,
    `SELECT load_extension('x')`,
    `SELECT "load_extension"('x')`,
    `SELECT fts3_tokenizer('x')`,
    `SELECT 1 RETURNING *`,
    `/* hi */ DELETE FROM items`,
    `-- hi\nDELETE FROM items`,
  ])
    rejects(q);
});

test("one statement only; comments can't hide a second one", () => {
  rejects(`SELECT 1; SELECT 2`, /one statement/);
  rejects(`SELECT 1; DELETE FROM items`, /one statement/);
  rejects(`SELECT 1 -- ;\n; DROP TABLE items`, /one statement/);
  rejects(`SELECT 1 /* ; */ ; DROP TABLE items`, /one statement/);
  accepts(`SELECT ';' AS semicolon`);
  accepts(`SELECT 1;`);
  accepts(`SELECT 1 ; -- done`);
});

test("the subquery wrapper can't be escaped", () => {
  rejects(`SELECT 1) UNION SELECT * FROM (SELECT 1`, /parenthesis/);
  rejects(`SELECT (1`, /parenthesis/);
  rejects(`SELECT * FROM items /*`, /comment is not closed/);
  rejects(`SELECT 'abc`, /not closed/);
  rejects(`SELECT "abc`, /not closed/);
});

test("engine query-builder markup and parameters are rejected", () => {
  rejects(`SELECT '[[_superusers]]'`, /Double brackets/);
  rejects(`SELECT * FROM {{users}}`, /Double brackets/);
  rejects(`SELECT {:id}`, /Double brackets/);
  rejects(`SELECT * FROM items WHERE id = ?`, /Parameters/);
  rejects(`SELECT * FROM items WHERE id = :id`, /Parameters/);
  rejects(`SELECT * FROM items WHERE id = @id`, /Parameters/);
  rejects(`SELECT * FROM items WHERE id = $id`, /Parameters/);
});

test("ordinary read queries are accepted", () => {
  for (const q of [
    `SELECT * FROM users`,
    `select label, qty from items order by qty desc limit 5`,
    `WITH t AS (SELECT label FROM items) SELECT * FROM t`,
    `WITH RECURSIVE c(x) AS (SELECT 1 UNION ALL SELECT x + 1 FROM c WHERE x < 10) SELECT x FROM c`,
    `VALUES (1, 'a'), (2, NULL)`,
    `SELECT * FROM items WHERE label LIKE '_a%'`, // LIKE's "_" wildcard is not a name
    `SELECT replace(label, 'a', 'b') FROM items`,
    `SELECT CASE WHEN qty > 1 THEN 'many' END FROM items`,
    `SELECT label AS "delete" FROM items`,
    `SELECT "order", [group] FROM "my table"`,
    `SELECT x'00ff', 1.5e3, 0x1F, -2`,
    `SELECT json_extract(data, '$.a.b') FROM items`,
    `SELECT data ->> '$.a' FROM items`,
    `SELECT value FROM json_each('[1,2]')`,
    `SELECT sqlite_version()`,
    `SELECT date('now'), strftime('%Y', created) FROM users`,
    `SELECT 'it''s' AS quote`,
    `SELECT café FROM menü`,
  ])
    accepts(q);
});

test("EXPLAIN modes and the body that gets wrapped", () => {
  assert.deepEqual(accepts(`SELECT 1;`), { mode: "", body: "SELECT 1" });
  assert.deepEqual(accepts(`EXPLAIN SELECT 1`), { mode: "code", body: "SELECT 1" });
  assert.deepEqual(accepts(`explain query plan select * from items -- note`), { mode: "plan", body: "select * from items" });
  rejects(`EXPLAIN DELETE FROM items`);
  rejects(`EXPLAIN`);
  assert.equal(accepts(`  \n SELECT 1 -- trailing`).body, "SELECT 1");
});

test("empty and oversized input", () => {
  rejects(``, /No SQL/);
  rejects(`   `, /No SQL/);
  rejects(`;`, /No SQL/);
  rejects(`-- only a comment`, /No SQL/);
  rejects(`SELECT '${"x".repeat(110 * 1024)}'`, /too large/);
});

test("shadow CTEs list only visible columns and never leave a table readable", () => {
  assert.equal(S.shadowWith([]), "");
  const w = S.shadowWith([{ name: "users", visible: ["id", "email"] }, { name: 'we"ird', visible: [] }]);
  assert.equal(w, 'WITH "users" AS (SELECT "id", "email" FROM main."users"), "we""ird" AS (SELECT NULL AS "id" WHERE 0)\n');
  assert.equal(S.dataSql("", "SELECT 1", ["a", 'b"c']).includes('"b""c"'), true);
  assert.match(S.dataSql("", "SELECT 1", ["a"]), /LIMIT 1001$/);
  const many = S.dataSql("", "SELECT 1", Array.from({ length: 250 }, (_, i) => "c" + i));
  assert.match(many, /AS r0, json_array\(.*AS r1, json_array\(.*AS r2 FROM/);
  assert.match(S.probeSql("", "SELECT 1 -- x"), /-- x\n\) LIMIT 0$/);
  assert.equal(S.explainSql("plan", "", "SELECT 1"), "EXPLAIN QUERY PLAN SELECT * FROM (\nSELECT 1\n) LIMIT 1001");
});

test("engine access check: internal tables, other schemas and writes fail closed", () => {
  const roots = { 2: "items", 5: "_superusers", 7: "sqlite_stat1", 9: "users" };
  assert.equal(S.accessProblem([{ opcode: "OpenRead", p2: 2, p3: 0 }, { opcode: "OpenRead", p2: 9, p3: 0 }], roots), "");
  assert.match(S.accessProblem([{ opcode: "OpenRead", p2: 5, p3: 0 }], roots), /Internal/);
  assert.match(S.accessProblem([{ opcode: "ReopenIdx", p2: 7, p3: 0 }], roots), /Internal/);
  assert.match(S.accessProblem([{ opcode: "OpenRead", p2: 1, p3: 0 }], roots), /Internal/); // sqlite_schema
  assert.match(S.accessProblem([{ opcode: "OpenRead", p2: 42, p3: 0 }], roots), /Only this project/); // unknown root
  assert.match(S.accessProblem([{ opcode: "OpenRead", p2: 2, p3: 1 }], roots), /Only this project/); // temp / attached
  assert.match(S.accessProblem([{ opcode: "OpenWrite", p2: 2, p3: 0 }], roots), /read-only/);
  assert.equal(S.accessProblem([{ opcode: "OpenEphemeral", p2: 3, p3: 0 }, { opcode: "VOpen", p2: 0, p3: 0 }], roots), "");
});

test("rows: cap, long values shortened, errors made readable", () => {
  const rows = Array.from({ length: 1001 }, (_, i) => ({ r0: JSON.stringify([i, "x"]) }));
  const got = S.collectRows(rows, 1);
  assert.equal(got.rows.length, 1000);
  assert.equal(got.truncated, true);
  assert.deepEqual(S.collectRows([{ r0: "[1]", r1: '["a",null]' }], 2), { rows: [[1, "a", null]], truncated: false });
  const long = S.collectRows([{ r0: JSON.stringify(["y".repeat(20000)]) }], 1).rows[0][0];
  assert.match(long, /^y{10000}… \(10000 more characters\)$/);
  assert.equal(S.friendlyError("GoError: SQL logic error: no such column: password (1)"), "no such column: password");
  assert.match(S.friendlyError("GoError: context canceled"), /stopped/);
  assert.match(S.friendlyError("GoError: attempt to write a readonly database (8)"), /read-only/);
});
