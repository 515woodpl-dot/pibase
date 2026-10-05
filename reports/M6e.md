# M6e gate report — table defaults and concurrent capacity

**Status:** audit complete

## What was built

### Part A — new public-table defaults

Schema version 10 applies idempotent default privileges only for each project
admin in `public`: table `SELECT, INSERT, UPDATE, DELETE`, and sequence
`USAGE, SELECT`, to anon, authenticated, and service roles. An operator-owned
event trigger enables RLS for tables created in `public` by non-superuser
sessions. It adds no policy; service bypasses RLS while anon and authenticated
roles require owner policies. Operator-created restore and branch objects are
not changed by the trigger. Existing tables are untouched.

The normal branch default-privilege remapper already permits the added TABLE
and SEQUENCE privileges. `docs/PI-OPERATIONS.md` documents the one-line grant
for existing tables; `docs/PI-DRILL.md` prepares the owner’s fresh-install
checklist and explicitly prohibits using the engineering Pi’s installed system.

Part A validation used a disposable real PostgreSQL stack through
`scripts/test-pi-drill-local.sh`: an ops-created project, migration-created
public table, service-key REST insert/read, and encrypted backup all passed.

### Part B — concurrent capacity

`scripts/measure-concurrency.sh` creates disposable PostgreSQL and gateway
containers, then 20 and 40 disposable projects. Each project has a real
1,000-row table. It runs a continuous service-key REST read per project for
60 seconds, samples whole-container PSS and client backends every two seconds,
and writes all samples and request status/latency records to
`reports/raw/m6e-concurrency.txt`.

| Case | Postgres PSS median / peak | Gateway PSS median / peak | Peak backends | Requests / errors | p50 / p95 | Free memory at peak |
|---|---:|---:|---:|---:|---:|---:|
| 20 projects | 199,755 / 213,049 kB measured | 17,717 / 18,115 kB measured | 21 measured | 1,300 / 0 measured | 0.334 / 0.863 s measured | 1,299 MiB measured |
| 40 projects | 232,226 / 254,531 kB measured | 18,189 / 18,645 kB measured | 38 measured | 1,375 / 101 measured | 0.412 / 1.682 s measured | 1,202 MiB measured |

At 40 projects against `max_connections=40`, requests did not all succeed:
101 responses were HTTP 400 (**measured**). Successful requests were visibly
slower at p95. Pooling and connection limits were not changed in this gate.

### Part C preparation

`docs/PI-DRILL.md` supplies the owner’s copy-pasteable fresh-install and
post-reboot checklist. `scripts/test-pi-drill-local.sh` was rerun after Part A
and passed against a disposable Compose stack.

## Evidence for existing tables

The Part A upgrade intentionally does not alter existing public tables:
their `\dp` ACL entries and `pg_class.relrowsecurity` values are preserved.
New owner-created public tables show default API grants and
`relrowsecurity = true`; a table in another schema receives neither behavior.
The raw capacity tables and the disposable drill table were created after
version 10 and therefore are not evidence about pre-version-10 tables.

## Tests run

- `sh -n scripts/pi-drill.sh scripts/measure-memory.sh scripts/measure-concurrency.sh scripts/test-pi-drill-local.sh`: passed.
- `scripts/test-pi-drill-local.sh`: passed on a disposable Compose stack after
  Part A.
- `scripts/measure-concurrency.sh`: passed on the engineering Pi, with the
  complete raw file committed.

## What was NOT tested

- A fresh physical Raspberry Pi OS install and reboot; this is owner-run Part C.

## Audit fixes

### What changed

- Extended the operator-owned public-relation trigger to cover query-created
  tables, moved tables, views, and materialized views. Tables receive RLS;
  views use `security_invoker`; materialized-view API grants are revoked from
  non-bypass roles. Owner changes remain explicit opt-in exposure decisions.
- Branch restore now clears API-role relation and sequence privileges before
  applying the mapped source ACLs, preserving private source objects.
- Added real-Postgres coverage for the relation matrix, service reads,
  anonymous and signed-user denial, policy isolation, branch private ACLs,
  RLS-disabled backup restore, and the Supabase client migration case. The
  backup expectation follows the current schema version rather than the old
  value.
- Corrected the Pi drill’s safe disk selection, UUID fstab mount, reboot mount
  check, and private-repository bundle route. The capacity harness now sets
  `work_mem=4MB`; no capacity remeasurement was requested.

### Tests run

- `scripts/test.sh`: passed on a disposable PostgreSQL stack, including the
  control and gateway real-Postgres suites.
- `scripts/test-supabase-js.sh`: passed on a disposable PostgreSQL and gateway
  stack; the Supabase client and realtime acceptance cases passed.

### Re-review closure

- Removed `ALTER TABLE` from the event-trigger scope so owner changes to
  existing relations neither enable RLS nor alter their ACLs. The trigger no
  longer changes per-session configuration.
- A table moved into `public` stays inaccessible to API roles until its owner
  grants access explicitly. Materialized-view privilege revocation now targets
  only roles that actually hold an ACL entry.
- Added regression coverage for moved-table access and existing-table `ALTER`
  operations. Both full verification gates above were rerun after this change.

### What was NOT tested

- A physical Raspberry Pi fresh install, disk formatting, and reboot remain an
  owner-run drill.
- Capacity was not remeasured after adding the prescribed `work_mem=4MB`
  setting; the committed raw measurement remains the prior measured run.

## Questions for the planner

1. At 40 projects the measured request failures are HTTP 400. The capacity
   harness records status and latency but not response bodies, so the exact
   customer-visible error text needs a follow-up measurement before changing
   pooling or limits.
