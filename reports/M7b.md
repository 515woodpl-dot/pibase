# M7b report — auditor-applied fixes

## Built
`go build ./...` clean, `gofmt -l cmd internal` empty, `go vet ./...` clean.

## Auditor-applied fixes

| Finding | Status | What was done |
|---------|--------|---------------|
| M7b-01 Users lookup | Fixed in bf1c868, confirmed | Test passes unchanged |
| M7b-02 Password hash | Fixed in bf1c868, **test added** | `TestOperatorDashboardEndpoints` verifies stored hash with `auth.VerifyPassword` (correct password succeeds, wrong password fails, prefix is `$pibase$argon2id$`). Gateway sign-in test not added (requires full gateway wiring in test harness). |
| M7b-03 Row insert | **Removed (scope cut)** | Endpoints, handlers, UI and tests for row insert/edit/delete removed |
| M7b-04 Row update/delete | **Removed (scope cut)** | Same as M7b-03 |
| M7b-05 Keys shown once | Fixed | Create response includes `anon_key` and `service_key`; page JS writes them via `textContent` with "Shown once; copy them now" |
| M7b-06 anon_readable | Fixed | Uses `has_table_privilege(anon_role, oid, 'SELECT')` with real role name from control package + RLS/policy check. `pibase_migrations` hidden from table list. Four-table test matrix passes (`TestAnonReadable`). |
| M7b-07 Single connection | Fixed | `adminsql.TableMeta` runs all counts + anon checks on one connection. `owner_can_select` removed. |
| M7b-08 SQL filter | **Fixed and tested** | `SET [LOCAL] SESSION AUTHORIZATION` blocked (with comments/whitespace between words). `CREATE/ALTER/DROP USER` blocked. `SET "role"` blocked. `select "copy" from t` and `select "grant" from t` allowed. 30+ unit test cases in `TestSQLFilterComprehensive`. Real-Postgres reach test (`TestOwnerSQLRoleIsolation`): RESET ROLE, SET ROLE, SET SESSION AUTHORIZATION, RESET ALL all return 400. Nothing reachable beyond the project admin role. |
| M7b-09 Lost controls | Fixed | Backups list (name, size), "Back up now" (shows filename), apply migration (name + SQL textarea), list migrations (name, checksum prefix, time) — all in page JS via existing endpoints, tested in integration test. |
| M7b-10 UI rules | Checked | `TestDashboardUIHardRules` passes. See screenshots below. |

## Browse rewrite (replaces M7b-03/07 for read-only)
All columns cast to `::text` with quoted identifiers. ORDER BY primary key (or `ctid`). Paging: Previous disabled at offset 0, Next disabled when `has_more` is false. NULL shown as muted text. `TestBrowseTextCasting` covers: uuid pk, bigint 9007199254740993, numeric(10,2), timestamptz, jsonb, bytea, int[], bool, generated column, NULL, empty string — all exact text values asserted.

## Tests run

```
$ gofmt -l cmd internal
(empty — clean)

$ go vet ./...
(clean)

$ go build ./...
(clean)

$ PIBASE_TEST_ADMIN_DATABASE_URL="..." go test -p 1 -count=1 ./internal/adminsql ./internal/adminserver
ok  github.com/515woodpl-dot/pibase/internal/adminsql    0.005s
ok  github.com/515woodpl-dot/pibase/internal/adminserver 2.263s

$ PIBASE_TEST_ADMIN_DATABASE_URL="..." go test -p 1 -count=1 ./internal/control ./internal/gateway ./internal/backup
FAIL (pre-existing failures only):
- control: PostGIS extension binaries not installed (TestBranchProjectRemapsCatalogSecurity, TestOperatorOptionalExtensions)
- gateway: @supabase/supabase-js npm module missing (TestM5bBroadcastPresenceIsolation)
- backup: PostGIS extension binaries not installed (TestRestorePreM6cBackupReappliesManagedOwnership)
None of these are related to M7b changes.
```

## Not tested
- **Docker gates** (`scripts/test.sh`, `scripts/test-supabase-js.sh`): PENDING — `init-env.sh` refuses to run as root in this cloud container. Owner should run on the Pi.
- **Pi deployment**: not tested (no Pi access).
- **Real browser clicking**: screenshots taken programmatically; no manual click-through.

## Screenshots
Taken with headless Chromium driving the real page against real Postgres (token entered, project opened, Tables → customers → Browse rows, then Users, SQL, Backups, Migrations at both widths; no page errors).
- `reports/m7b-1280.png`: rows pane (wide pane, table scrolls inside it).
- `reports/m7b-390.png`: rows page 2 at phone width, Next disabled.

### UI fixes after the first screenshots (follow-up commit)
The first screenshots were of the empty landing page (wrong URL), so they missed these. All found by driving the page and fixed:
1. Opening a pane scrolled the chain to its far right, slicing the left-most visible pane mid-word (both widths). Now `alignChain()` scrolls so the left-most visible pane starts on its own edge.
2. At 390px panes were 330px in a 270px column, so no pane was ever fully visible. Panes now take exactly the width beside the menu on phones.
3. The rows table pushed its pane wider instead of scrolling. Rows and SQL panes are now wide panes (760px) with the table scrolling inside.
4. SQL results were raw JSON and a uuid showed as an array of 16 numbers. Results now render as the same table as Browse; uuid and bytea are converted to text server-side (`displayValue`, unit-tested).
5. "Back up now" and "Apply migration" reloaded their pane and wiped the "Created: …" / "Applied: …" message. The message now survives the reload (checked in the browser).
6. Disabled Previous/Next looked enabled; pager buttons were misaligned; project action buttons and the user form had no spacing; migration times were raw ISO strings; favicon request returned 401 in the console.

## Known gaps
1. The 390px layout is usable but cramped: the left menu keeps 120px (the UI rules require a left menu).
2. Gateway sign-in test not added for M7b-02 (would need full gateway wiring).
3. Docker gates pending.
4. Connection count assertion for M7b-07 not added (would need `pg_stat_activity` query in test); the implementation uses a single `TableMeta` call so it is structurally one connection.

## Click-through checklist for the owner
1. **Create project** → response shows `id`, `anon_key`, `service_key` with "Shown once; copy them now"
2. **Create a user** → hash stored as `$pibase$argon2id$...`; sign in with curl or supabase-js against the gateway (not tested in this session)
3. **Browse a uuid table** → all columns display as text, bigint preserves full precision, NULL shown as muted text, paging works
4. **Run a backup** → "Back up now" returns created filename; backup appears in list with name and size
5. **Apply a migration** → name + SQL textarea; result shown inline; migration appears in list with name, checksum prefix, time
6. **SQL tab** → `SET SESSION AUTHORIZATION`, `RESET ROLE`, `SET ROLE <other>`, `CREATE USER`, `ALTER USER` all return 400; normal queries work
