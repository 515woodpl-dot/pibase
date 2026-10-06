# M7b review (operator data dashboard)

Commit reviewed: 66d99a6 on pibase/m7 (follows checkpoint 8c2f2aa)
Environment: Linux x86_64 cloud sandbox, PostgreSQL 16 (local cluster, real), Go with offline module file. No Docker, no Pi, no browser.
Verdict: **BLOCK.** The feature set exists, but Users is broken, dashboard-made users cannot sign in, rows with uuid keys cannot be edited or deleted, and editing a row can silently corrupt bytea data. The gate is also incomplete (no report, no pasted gate output, no screenshots).

## Checkpoint findings from 8c2f2aa: status
| ID | Status |
|---|---|
| C01 build error | FIXED, VERIFIED (`go build ./...`, `go vet`, `gofmt -l` clean) |
| C02 tab in compose.yaml | FIXED, VERIFIED (YAML parses) |
| C03 PIBASE_UID/GID missing from .env.example | FIXED, READ (lines present; `init-env.sh` sed now applies) |
| C05 named volumes owned by 65532 | FIXED by design, READ: `user: "${PIBASE_UID:-65532}:..."`; Docker run still UNVERIFIED |
| C06 old Pi files keep old owner | FIXED, READ: `chown -R` in setup-pi.sh, and the mock-sudo test asserts it |
| C07 uid 0, comment | FIXED, READ |
| C04 SQL filter | **PARTLY FIXED, regression found (M7b-08)**; the real-Postgres "what can owner SQL reach" test is still missing |

## What I ran
- VERIFIED `go test ./internal/adminsql ./internal/adminserver` with `PIBASE_TEST_ADMIN_DATABASE_URL` set: adminsql passes; **adminserver `TestOperatorDashboardEndpoints` FAILS** at `POST /_pibase/projects/<id>/users`: `status=400 want=201 body=project not found: ERROR: relation "control.project_catalog" does not exist`.
- VERIFIED probe tests (written by me, run against real Postgres with a one-line local fix for M7b-01, then discarded) for types, paging, users, the hash, and the filter.
- READ the page: no animation/transition, no dialog/alert/confirm/prompt, no innerHTML, `#333` text, left menu plus left-to-right chain, inline delete confirmations, tap highlight off with `:focus-visible` kept. The new `TestDashboardUIHardRules` test enforces the forbidden list. The look and layout were NOT viewed (no browser here); the screenshots were not delivered.
- UNVERIFIED: Docker gates (`scripts/test.sh`, `scripts/test-supabase-js.sh`), Pi run.

## Findings

**M7b-01, High: Users endpoints cannot work.** `projectDB` runs `SELECT ... FROM control.project_catalog` on `AdminURL`'s default database (`postgres`), but the `control` schema lives in `_control`. All three user endpoints return 400. The engineer's own integration test fails here, so the commit was pushed with a red test. (With a local one-line fix the endpoints work: create 201, list 200.)

**M7b-02, High: users created from the dashboard can never sign in.** They are stored with bcrypt; the gateway accepts only `$pibase$argon2id$...`. VERIFIED: `auth.VerifyPassword` returns false for the dashboard's hash and true for `auth.HashPassword`'s. Fix: call `auth.HashPassword`, keep the same email normalisation as the gateway, and add a test that creates a user from the dashboard and signs in through `/auth/v1/token`.

**M7b-03, High: uuid primary keys are unusable.** pgx returns uuid as `[16]byte`, so rows show `"id":[77,129,202,...]`. Edit and delete then fail with `unable to encode []interface{}{...} into binary format for uuid`, and the delete-confirmation text would be a list of numbers. uuid is the normal primary key in Supabase-style apps. With a string key the update and delete work, so the fix is in how values are read out and shown.

**M7b-04, High: editing a row can silently corrupt or fail, and can't set NULL.** The editor sends every non-empty column back as text.
- bytea: shown as base64 (`3q2+7w==`), sent back, stored as the 8 literal bytes of that text (`hex 3371322b37773d3d`). Silent data loss, VERIFIED.
- jsonb: shown as `[object Object]`, update fails with invalid JSON. int[]: shown as `1,2,3`, fails with malformed array. Generated columns: insert/update fails. All VERIFIED.
- Setting a column to empty or NULL is impossible (empty fields are dropped).
- bigint above 2^53 loses precision in the browser (`9007199254740993` is serialised by the server as a JSON number).
Required: send only changed columns; render and parse each type explicitly (jsonb as JSON text, arrays as JSON, bytea read-only or hex, large ints and numerics as strings, an explicit NULL control); skip generated columns.

**M7b-05, High: new-project keys are no longer shown.** M7a displayed the anon and service keys once after create. The new page says "Save the keys returned below" but renders nothing, the project list correctly omits keys, and no other endpoint returns them. The operator cannot obtain the keys of a project they just made. Show the keys once in the pane after create.

**M7b-06, Medium: "anon readable" is misleading.** It is computed only from table grants. Owner-created tables get default API grants and RLS (v10), so VERIFIED output shows `anon_readable: true` for all six tables including `priv` (RLS on, no policy, returns no rows) and the internal `pibase_migrations`. Compute effective readability: grant AND (RLS off OR a SELECT/ALL policy that applies to anon or public). Hide `pibase_migrations` or mark it internal.

**M7b-07, Medium: browse and write behaviour.**
- `SELECT * ... LIMIT 50 OFFSET $1` has no `ORDER BY`; paging is unstable. Order by the primary key.
- "Next" has no end; shows `Rows 51-50` on an empty page.
- Update or delete matching no row returns 200 (`UPDATE 0`) and the UI says "Saved." Return 404 and show "no row matched".
- The table list opens one new database connection per table plus a full `count(*)` per table. Fine for 6 tables, costly for many or large ones; use one connection and consider estimated counts above a threshold.
- `owner_can_select` is selected and never used.

**M7b-08, Medium: SQL filter regression.** The rewrite dropped `SET SESSION AUTHORIZATION` and `RESET SESSION AUTHORIZATION` (the old substring filter blocked them). VERIFIED not blocked now, including with comments between the words. Unit tests only cover `session_authorization` as one word. Role setup means this is not a privilege escalation by itself (`authenticator` is not a superuser), which is why I rate it Medium, but the stated purpose of the filter was to block these. Also: quoted identifiers named `grant` or `copy` (for example `select "copy" from t`) are now rejected as false positives. And the real-Postgres test of what owner SQL can reach after `RESET ROLE` / `SET ROLE <other project role>` requested in the checkpoint review is still missing. Required before Tier 2.

**M7b-09, Medium: M7a controls were lost from the page.** The new page has no list of backups (the endpoint exists, the page never calls it), no way to apply a migration, and no list of applied migrations (the Migrations pane is a one-line placeholder). "Back up now" prints raw JSON in the status line. These worked in the approved M7a page.

**M7b-10, Low:** delete confirmation for composite keys joins values with `:`; fine, but document it. Table names containing `/` were not tested. Nothing in the repo pins the page against horizontal overflow at 390px (no screenshots were delivered).

## Gate completeness
- No `reports/M7b.md` (not in the commit).
- No pasted output of `scripts/test.sh` or `scripts/test-supabase-js.sh` (M7a-10 still open).
- No 1280px / 390px screenshots as required by the UI rules.
- The failing integration test above shows the suite was not run green before the push.

## Fix order for the engineer
1. M7b-01, M7b-02 (and the sign-in test).
2. M7b-03, M7b-04 with a per-type round-trip test: uuid, bigint above 2^53, numeric, timestamptz, jsonb, bytea, int[], bool, generated column, NULL, empty string, run through browse, edit, delete.
3. M7b-05, M7b-09 (restore keys display, backups list, migrations apply and list).
4. M7b-06, M7b-07, M7b-08 (with the real-Postgres reach test).
5. Report, pasted gate output, screenshots. Do not push with a red test.
