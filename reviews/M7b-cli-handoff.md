# M7b handoff for the Claude CLI session (auditor-applied fixes)

You are Claude, working as planner/auditor, and for this one task also as the fixer. The engineer (Codex) is paused on this branch. Read `reviews/M7b-review.md` first (on `origin/pibase/review-m7`); it lists every finding with evidence. This file says what to do about them.

## Why this exists
Three review rounds on the dashboard each ended in blockers because code was pushed without a green real-Postgres run. To stop the loop: cut M7b to what is needed now, fix the remaining blockers directly, test each fix before pushing, and label the result honestly.

## Ground rules (do not break)
- Work on `pibase/m7`, starting from `origin/pibase/m7`. As of 2026-10-06 06:37 (America/New_York) its tip is **66d99a6** (`feat: add operator data dashboard`), nothing newer, and `pibase/review-m7` holds this file (f12c77e or later). Run `git fetch` first. If the tip is not 66d99a6, read every newer commit before editing and say so in the report. The owner must tell Codex not to push `pibase/m7` while you work.
- Create a local branch from the tip (`git switch -c m7-fix origin/pibase/m7`), commit there, and push with `git push origin m7-fix:pibase/m7` (fast-forward only). Do not work on `main`; `main` is the older PocketBase product and is unrelated.
- Push `pibase/m7` only. Never `main`. Never force-push. Fast-forward only.
- No `sudo`. Never print `.env`, tokens, keys or hashes in output, logs or the report.
- Nothing is pushed with a red test. Before every commit: `gofmt -l cmd internal` empty, `go vet ./...`, `go build ./...`, and the real-Postgres tests below.
- Customer-visible text never names internals beyond "Postgres".
- Commits end with the attribution lines given by your session's system reminder.
- Everything you fix is "auditor-applied, not independently reviewed". Say so in the report. After you finish, the owner should have Codex (or a fresh session) review it.

## State of play at handoff (verified by the auditor)
- 66d99a6 builds, vets and is gofmt-clean; compose parses; the checkpoint findings C01 to C03 and C05 to C07 are fixed.
- `go test ./internal/adminsql` passes. `go test ./internal/adminserver` **fails** in `TestOperatorDashboardEndpoints` at the users call (task 1). With task 1 patched locally the rest of that test passed.
- Not run by the auditor: Docker gates, the Pi, a real browser.
- Do not trust this list over the code: re-run the tests first and record the starting result in the report.

## Test environment
- Real Postgres 16 is required. In the cloud sandbox: `pg_ctlcluster 16 main start`, then `su postgres -c "psql -c \"alter user postgres password 'pw'\""` and `export PIBASE_TEST_ADMIN_DATABASE_URL='postgres://postgres:pw@127.0.0.1:5432/postgres'`. Run `go test -p 1 -count=1 ./internal/adminsql ./internal/adminserver ./internal/control ./internal/gateway ./internal/backup`.
- If Go modules cannot be downloaded (proxy blocked), use an offline module file with `GOFLAGS=-mod=mod GOPROXY=off go test -modfile=<file>`; do not disable TLS verification, ever.
- Docker gates (`scripts/test.sh`, `scripts/test-supabase-js.sh`) need Docker. If this machine has no Docker or cannot pull, mark them PENDING in the report with the exact error, and do not claim them. The owner runs them on the Pi.
- Screenshots: headless Chromium (`/opt/pw-browsers/chromium` in the cloud sandbox) via Playwright or `--screenshot`.

## Scope decision (owner-approved cut)
M7b becomes:
- **Keep and fix:** table list, read-only row browse, users (list, create, delete), keys shown once after project create, and the M7a controls that were lost.
- **Remove for now:** row insert, edit and delete (endpoints, UI, tests). Row edits are done with the SQL tab. This drops findings M7b-03, M7b-04 and most of M7b-07. Row editing returns later as its own gate with a type matrix.

## Tasks, in order (each with its acceptance test)

1. **M7b-01 Users lookup.** `projectDB` must find the project in `_control` (reuse the `control` package lookup instead of querying `control.project_catalog` on the default database). Acceptance: `TestOperatorDashboardEndpoints` passes unchanged for the users calls.
2. **M7b-02 Password hash.** Create users with `auth.HashPassword` (argon2id), normalise email the same way the gateway does, return a clear 400 on a duplicate email. Acceptance: a test that creates a user through the dashboard endpoint and then verifies the stored hash with `auth.VerifyPassword`; if the gateway auth handler can be driven in the test harness, also sign in through `/auth/v1/token`. Say which of the two you did.
3. **M7b-05 Keys.** After `POST /_pibase/projects`, render the id, anon key and service key once in the Project pane, with the text "shown once; copy them now". They are never stored in the browser and never listed afterwards (the existing test that the list contains no keys stays). Acceptance: Playwright or an assertion that the page script writes both keys into the DOM from the create response via `textContent`.
4. **M7b-09 Lost controls.** Restore in the page: list backups (file name and size), "Back up now" showing the created file name, apply a migration (name + SQL textarea, inline result), list applied migrations (name, checksum prefix, time). All via the existing endpoints. Acceptance: page test that each endpoint path is referenced, and the integration test calls each endpoint.
5. **Browse rewrite (replaces M7b-03/07 for read-only).** Rows are fetched with every column cast to text (`col::text`, identifiers quoted from the catalog) so uuid, bigint, numeric, jsonb, bytea (`\x...`), arrays and timestamps display exactly and never lose precision; NULL is returned as null and shown as a muted "NULL". `ORDER BY` the primary key (or `ctid` when there is none). Paging: Previous disabled at offset 0, Next disabled when fewer than 50 rows came back. Acceptance: a test table with uuid pk, bigint 9007199254740993, numeric(10,2), timestamptz, jsonb, bytea, int[], bool, a generated column, a NULL and an empty string; assert the exact text of each value in the JSON response and that paging is stable across two pages.
6. **M7b-06 anon readable.** Define it as: `has_table_privilege(<the project's actual anon role>, table, 'SELECT')` AND (RLS off OR a `pg_policies` SELECT/ALL policy that applies to that role or to PUBLIC). Use the real role name from the `control` package, not `LIKE 'p_%_anon'`. Hide `pibase_migrations` from the table list. Acceptance: a test with four tables: granted + RLS off (true), granted + RLS on + no policy (false), granted + RLS on + public SELECT policy (true), ungranted (false).
7. **M7b-07 remaining.** Table list uses one connection for all counts (add a helper in `adminsql` that runs several statements on one connection and keep it parameterised); drop the unused `owner_can_select`. Acceptance: connection count during a 20-table list is 1 (assert via `pg_stat_activity` or a counter).
8. **M7b-08 SQL filter.** Block `SET/RESET SESSION AUTHORIZATION` (also `LOCAL`, with comments or extra whitespace between words), keep `set_config`, `RESET ALL`, `SET ROLE`, `GRANT/REVOKE`, `COPY`, role DDL blocked, and `SET "role"` blocked when the quoted name follows `SET`/`RESET [LOCAL|SESSION]`. A quoted identifier counts as a keyword only in that position, so `select "copy" from t` and `select "grant" from t` are allowed. Add unit tests for every case. Then add one real-Postgres test that opens the owner connection exactly as `adminsql` does and records what is reachable after `RESET ROLE` and after `SET ROLE <another project's admin or anon role>`: it must not read another project's data, `_control`, or `auth` secrets beyond what the project admin can already read. Put the observed result in the report even if it is "nothing reachable". If something is reachable, stop and report it to the owner before continuing.
9. **M7a-10 Gates.** Run `scripts/test.sh` and `scripts/test-supabase-js.sh` where Docker works, saving raw output under `reports/raw/`. If Docker is unavailable, mark PENDING.
10. **UI rules still hold** (all are enforced by `TestDashboardUIHardRules`, extend it if needed): no animation, no modal, no dialogs, left menu + left-to-right chain, text `#333`, tap highlight off, `:focus-visible` kept, no `innerHTML`, left click only. Take screenshots at 1280px and 390px with three panes open: `reports/m7b-1280.png`, `reports/m7b-390.png`, and look at them; list anything ugly or overflowing in the report.

## Report (`reports/M7b.md`)
Sections: Built; Auditor-applied fixes (list finding IDs); Tests run (command + pasted summary line each); Not tested (be specific: Docker gates if pending, Pi, real browser clicking); Known gaps; Click-through checklist for the owner (create project → copy keys; create a user → sign in with it from curl or supabase-js; browse a uuid table; run a backup → see it listed; apply a migration → see it listed). No claim without a command behind it.

## Finish
Push `pibase/m7`, report the commit hash to the owner and say: "auditor-applied, needs independent review". Then stop. Do not start Tier 2 in this session.

## After M7b (next session, not this one)
Write `reviews/T2a-gate.md`: customer accounts (email/password login, session cookie, password reset), project ownership per account, every dashboard call authorised against the owner, operator token reduced to an admin-only fallback. Include a typed test matrix and a "do not regress earlier controls" list in the gate.
