# Planner decision: M6e, table defaults and concurrent capacity (APPROVED to start)

Planner/auditor: Claude, 2026-10-04. Base: `pibase/m6d` at `0fab24f`.
Engineer: Claude Code CLI on the engineering Pi 4. Rules: PLAN.md section 7; push only
`pibase/*` build branches; never `main`; never force-push.

This closes the three items left open by `reviews/M6d-rereview-2.md`. Parts A and B are yours.
Part C is run by the owner on separate hardware; you only prepare for it.

## Part A: default API access for new `public` tables

Problem: a table the project owner creates is invisible to the API until grants are added by
hand (`404 relation not found or not accessible`). Supabase-style code expects the table to be
reachable and protected by row-level security.

Decisions (do not relitigate):

1. **Default privileges.** For objects the project admin role creates in `public`:
   `SELECT, INSERT, UPDATE, DELETE` on tables and `USAGE, SELECT` on sequences to the project's
   `anon`, `authenticated` and `service` roles (`ALTER DEFAULT PRIVILEGES FOR ROLE <admin> IN
   SCHEMA public ...`). Nothing for other schemas.
2. **Row-level security on by default.** An operator-owned event trigger enables RLS on every
   new table created in `public` by a non-superuser session. No policies are added. Result: the
   service key works at once (it bypasses RLS); `anon` and `authenticated` see nothing until
   the owner adds a policy. This is deliberately stricter than Supabase, which leaves RLS off.
3. The owner may later run `ALTER TABLE ... DISABLE ROW LEVEL SECURITY` on their own table.
   Do not block it. Document that this exposes the table to the anon key.
4. Operator sessions (bootstrap, restore, branch) do not trigger the RLS default, so a restore
   or branch reproduces the RLS state stored in the archive or source.
5. **Existing tables are not changed**, neither grants nor RLS. The report lists, per test
   project, which existing `public` tables still lack API grants; `docs/` explains the
   one-line grant the owner can apply.
6. Applied through the canonical upgrade path, schema version 10, idempotent, one project's
   failure never stops bootstrap. Branching keeps working (it already remaps default
   privileges; add the new ones to its allowed set if needed).

Required tests (real Postgres, no mocks):

- New table via `migration-apply` and via `pibase sql`: service key can insert and read with no
  manual grant; anon and authenticated get an empty result or a permission error, never rows.
- After the owner adds a policy, authenticated users see exactly the rows the policy allows;
  two users are isolated from each other.
- `pg_class.relrowsecurity` is true for the new table; false is preserved for a table the owner
  explicitly disabled, across bootstrap, backup then restore, and branch.
- A table created in another schema gets neither grants nor forced RLS.
- Upgrade from version 9: existing tables unchanged (show `\dp` and `relrowsecurity` before and
  after), new tables get the defaults.
- The event trigger cannot be dropped, disabled or replaced by the admin role.
- `tests/supabase-js.mjs` gains a case: create table by migration, use it with the service key
  and with a signed-in user plus policy, with no manual grant.
- Remove the manual `GRANT` lines from `scripts/pi-drill.sh`, `test-pi-drill-local.sh`,
  `measure-memory.sh` and `test-upgrade-retained.sh` only where the table is created by the new
  build; keep them where the old M5b build creates the table.

## Part B: memory and behaviour under concurrent load

Facts from the code: each project's pool has `MaxConns = 1` and a 1 second idle timeout;
Postgres runs with `max_connections = 40`.

Extend `scripts/measure-memory.sh` (or add `scripts/measure-concurrency.sh`):

1. Cases: 20 projects and 40 projects, each with a real table of 1,000 rows.
2. Load: for 60 seconds, run parallel REST reads so that every project has at least one
   request in flight continuously (for example one looping client per project).
3. Sample every 2 seconds: whole-container PSS for Postgres and the gateway (same method as
   M6d), and the number of client backends from `pg_stat_activity`.
4. Report, all labeled measured, with the raw file committed under `reports/raw/`:
   peak and median PSS for both containers; peak backend count; total requests, error count by
   HTTP status, and p50/p95 latency; host free memory before and at peak (`free -m`).
5. State plainly what happens at 40 projects against `max_connections = 40`: do requests queue,
   fail, or succeed? If they fail, describe the error a customer would see. Do not change
   pooling or limits in this gate; put any recommendation under "Questions for the planner".
6. Run on the engineering Pi. Do not touch the live stack or its data root; use disposable
   containers as before.

## Part C: fresh-install drill (owner runs it; you prepare)

1. Write `docs/PI-DRILL.md`: a one-page checklist for the owner, from flashing a clean 64-bit
   Raspberry Pi OS Lite image onto a spare SD card or second Pi, formatting and mounting a USB
   SSD at `/mnt/pibase-ssd`, cloning the branch, running `scripts/pi-drill.sh`, rebooting,
   running it again with `PIBASE_DRILL_POSTREBOOT=1`, to which two files to paste back. Every
   command copy-pasteable. Mark the few commands that need `sudo` (disk format and mount) and
   keep them to that.
2. State in bold that the drill must not be run on the engineering Pi's installed system.
3. Run `scripts/test-pi-drill-local.sh` again after Part A and include its result.

Out of scope: pooling changes, OAuth, point-in-time recovery, image transforms, S3 API,
dashboard changes, any new dependency or service.

## Gate report: `reports/M6e.md`

What was built, commands and results, the `\dp` and RLS before/after evidence, the Part B table
and raw file, what was NOT tested, questions for the planner. Branch `pibase/m6e` from
`pibase/m6d`. Push only `pibase/m6e`. Stop there for audit.
