# M6e re-review (fix commit)

Commit reviewed: `31f3c19` on `pibase/m6e`
Previous review: `reviews/M6e-review.md` (BLOCK at `e0661ee`)
Reviewer: Claude (planner/auditor), 2026-10-05
Environment: Linux x86_64 sandbox, Go 1.24.7, PostgreSQL 16.13, gateway built from this commit.
Docker scripts not run; nothing run on a Pi.

## Verdict: BLOCK (one new Critical, introduced by the fix)

The four original findings are fixed. The fix for "table moved into `public`" made the trigger
run on **every** `ALTER TABLE`, which opens a new leak and removes the owner's opt-out.

## Original findings

| ID | Now | Evidence (VERIFIED unless noted) |
|---|---|---|
| M6e-01 anon reads query-made tables, views, materialized views | **Fixed** | Same four migrations as before. `ctas` and `sel_into` now have RLS on; the view has `security_invoker=true`; the materialized view has no anon grant. Anon key: `[]`, `[]`, `[]`, `404`. Service key reads all five. |
| M6e-02 branch exposes private tables | **Fixed** | Untouched private table in the source: branch keeps RLS off and no API grants; branch anon key gets `404`. |
| M6e-03 red suite, missing tests | **Fixed** | `go vet` and `go test -p 1 -count=1 ./internal/backup ./internal/control ./internal/gateway` pass on a fresh real PostgreSQL 16. New `TestM6ePartATableDrivenRelations` and branch/restore cases exist (READ). |
| M6e-04 drill checklist | **Fixed (READ)** | `DISK` chosen by the owner with typed confirmation, fstab entry by UUID with `nofail`, bundle route for the private repo. Small note: `findmnt` prints the device path, not `UUID=...`; reword that line. |

## New finding

### M6e-05 (Critical): any `ALTER TABLE` re-enables RLS and grants the API roles  — VERIFIED

The trigger now lists the tag `ALTER TABLE` and, for every table in `public` touched by one,
runs `ENABLE ROW LEVEL SECURITY` and `GRANT SELECT, INSERT, UPDATE, DELETE ... TO anon,
authenticated, service`. Three consequences, all reproduced:

1. **Leak.** A table created before version 10, readable by signed-in users only (grant to
   `authenticated`, policy `USING (true)`, no anon grant). Anon key: `404`. Then a routine
   migration:

   ```
   ALTER TABLE public.old_policy ADD COLUMN extra text;
   ```

   Afterwards the table has an anon grant, and the anon key returns
   `[{"note": "members-only"}]`. Adding a column made a members-only table public.

2. **The owner cannot switch RLS off.** `ALTER TABLE public.plain DISABLE ROW LEVEL SECURITY`
   succeeds and the table still has `relrowsecurity = true`. This contradicts gate decision 3.

3. **Existing tables are changed.** `ALTER TABLE public.old_private ADD COLUMN extra text`
   turned RLS on and added API grants to a table that had neither. This contradicts gate
   decision 5, and for an app that relied on RLS being off it means rows silently disappear for
   its users after an unrelated migration.

Planner decision:

- Remove `ALTER TABLE` from the trigger. The trigger acts only when a relation is **created**
  (`CREATE TABLE`, `CREATE TABLE AS`, `SELECT INTO`, `CREATE VIEW`, `CREATE MATERIALIZED VIEW`).
- A table moved into `public` with `ALTER TABLE ... SET SCHEMA` gets nothing automatically: no
  grants, no RLS change. It is unreachable by the API roles until the owner grants access, so
  it is safe. Document this in `docs/PI-OPERATIONS.md`. Change the test's expectation for the
  moved table to "anon and service both get 404 until granted".
- Remove the `pibase.public_relation_trigger` setting. With `ALTER TABLE` gone the trigger no
  longer re-enters itself, and a customer-settable switch that skips the trigger should not
  exist.

Tests to add: the three reproductions above (members-only table stays `404` for anon after
`ADD COLUMN`; `DISABLE ROW LEVEL SECURITY` sticks; an older table's ACL and RLS flag are
identical before and after `ADD COLUMN`, `ADD CONSTRAINT` and `RENAME COLUMN`).

## Low

- The materialized-view branch loops over every non-superuser role in the cluster and issues a
  `REVOKE` for each. With 100 projects that is about 400 statements per materialized view.
  Revoke only from the grantees actually present in the relation's ACL.

## Fix list

1. M6e-05 as decided, with the tests.
2. Materialized-view revoke by ACL grantees.
3. Reword the `findmnt` line in `docs/PI-DRILL.md`.

Append to "Audit fixes" in `reports/M6e.md`. I will re-check these items only.
