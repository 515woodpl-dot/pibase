# M7b final verdict — `pibase/m7` at 25dab9c

Auditor: Claude, 2026-10-06.

## Verdict: APPROVED, CONDITIONAL on the Pi gates below

Tier 1 (M0 to M7) closes when the owner has run the three Pi items and they pass. Until then M7 is approved in code only.

## Commits reviewed
| Commit | What |
|---|---|
| 7346ec2 | Handoff tasks 1–10: SQL filter, browse rewrite, anon_readable, scope cut (rows read-only), restored controls, keys once |
| 11e8ce7 | UI fixes found by driving the page in headless Chromium at 1280px and 390px |
| 6b2d12e | First independent review (BLOCK), findings R1–R8 fixed |
| 25dab9c | Second independent review, N1 (non-ASCII lexer bypass, High) and N2 (`UPDATE … SET role`, Low) fixed |

Details and the test tables are in `reports/M7b.md` on `pibase/m7`.

## Review history
- **First review** (fresh session, no shared context): BLOCK on 7346ec2/11e8ce7. The SQL-filter bypasses were real, the task 8 reach test only checked the filter, and anon_readable was wrong for RESTRICTIVE and false policies. It also found missing paging and connection-count tests. All fixed in 6b2d12e.
- **Second review** (fresh session): R2–R8 verified fixed. R1 reopened by N1, a non-ASCII byte before a `$tag$`, which ran `set_config` on PG16. Fixed in 25dab9c.
- **Third review**, aimed only at lexer bypasses, did not complete: the reviewer stopped early and produced no result. **25dab9c's filter has therefore not been re-reviewed.**

## Decision on the SQL-tab filter (owner, 2026-10-06)
The keyword filter in `validateOwnerSQL` is accepted as a **best-effort guard rail**, not a security boundary:
- Migrations (`ApplyMigration`) do not use it, by design.
- The boundary is the role model. `TestOwnerConnectionReach` opens the owner connection exactly as the dashboard does and assumes the filter is bypassed:
  - after `RESET ROLE`, the connection is `authenticator` and is denied this project's tables, `auth.users`, server files, `CREATE ROLE`, `SET SESSION AUTHORIZATION`, and `GRANT` of other projects' roles;
  - `SET ROLE` to another project's admin or anon role succeeds, but reads nothing in this project;
  - other projects' tables are in other databases;
  - dblink and postgres_fdw are not installed.
- A future filter bypass is therefore a Low finding unless it also breaks the reach table.

**Follow-up (not M7 scope):** replace the hand-written lexer with PostgreSQL's own parser (pg_query_go), or drop the filter and keep only the role boundary. Decide before Tier 2 exposes SQL to customers.

## Conditions to close Tier 1 (owner, on the Pi)
1. `scripts/test.sh` passes. In the cloud sandbox it got as far as building `Dockerfile.postgres` (gofmt, vet, unit tests in Docker and the production image all passed), then the sandbox blocked apt.
2. `scripts/test-supabase-js.sh` passes. Not run anywhere yet.
3. The click-through checklist in `reports/M7b.md` passes. Item 2 (sign in as a dashboard-created user through the gateway) has never been run.

When all three pass, append "Tier 1 closed" with the date and the test output summary to this file.

## Accepted gaps
- Row insert, edit and delete were cut (owner-approved); edits go through the SQL tab.
- anon_readable does not evaluate policy expressions.
- Tables without a primary key page by `ctid`.
- At 40 busy projects about 8% of requests get 503 (from the M7a review).
- The 390px layout is usable but cramped.

## Next
Tier 2 starts with `reviews/T2a-gate.md` (customer accounts), in a new session, after Tier 1 closes.

## Update 2026-10-06: tested in the cloud sandbox at 3b90f92
3b90f92 is 25dab9c plus a test-only fix: `control/TestExtensionGuardRejectsDynamicCustomerDDL` broke when the SQL tab began refusing `DO`. It now also runs the `DO` block as a migration, so the database guard stays covered. Details are in `reports/M7b.md` → "Full test run in the cloud sandbox".

What ran:
- **Full Go suite:** 9 of 9 packages pass against real Postgres 16 with PostGIS and pgvector installed.
- **supabase-js acceptance:** the steps of `scripts/test-supabase-js.sh`, run natively against a fresh cluster. M6a passed and M5b realtime passed.
- **Click-through checklist:** all items pass against real `admin-serve` and the gateway, driven in Chromium. This includes item 2, never run before: a user created in the dashboard signs in through the gateway with supabase-js, and reads only their own rows through RLS. The public gateway returns 404 for `/dashboard`, and admin endpoints return 401 without the token.

That satisfies conditions 2 and 3 above in substance. **Remaining condition to close Tier 1:** run `scripts/test.sh` and `scripts/test-supabase-js.sh` as written on the Pi. That is the only place the Docker images are built and run end to end, on arm64 with the Pi's memory limits. The sandbox blocked apt inside containers.
