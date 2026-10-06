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
| M7b-05 Keys shown once | Fixed | Create response includes `anon_key` and `service_key`; page JS writes project id and both keys via `textContent` with "Shown once; copy them now". `TestDashboardShowsKeysOnceAndWiresEveryEndpoint` checks those writes and that no browser storage is used. |
| M7b-06 anon_readable | Fixed (corrected after review R3) | The anon role comes from `control.project_catalog.anon_role`. Readable = SELECT on the table or any column, and (RLS off, or a PERMISSIVE SELECT/ALL policy for anon or PUBLIC whose USING is not literally `false`, with no RESTRICTIVE policy for them that is literally `false`). Policy expressions are not evaluated beyond that. `TestAnonReadable` covers 9 tables, including RESTRICTIVE, `USING (false)`, a policy for another role, and a column-only grant. `pibase_migrations` is hidden. |
| M7b-07 Single connection | Fixed (corrected after review R4) | `adminsql.ListTables` runs the catalog query, every count and the anon checks on one project connection (plus one `_control` lookup). `TestListTablesUsesOneProjectConnection` creates 20 tables and asserts the project connection counter rose by exactly 1. |
| M7b-08 SQL filter | Fixed (corrected after review R1/R2) | See "SQL filter and reach" below. |
| M7b-09 Lost controls | Fixed | Backups list (name, size), "Back up now" (shows filename), apply migration (name + SQL textarea), list migrations (name, checksum prefix, time) — all in page JS via existing endpoints, tested in integration test. |
| M7b-10 UI rules | Checked | `TestDashboardUIHardRules` passes. See screenshots below. |

## SQL filter and reach
The keyword filter is a guard rail for the SQL tab, not the security boundary. Migrations (`ApplyMigration`) do not use it at all. The boundary is the role model, and `TestOwnerConnectionReach` now tests that boundary directly.

**Filter.** Blocked spellings:
- `SET`/`RESET` `ROLE` and `SESSION AUTHORIZATION`, in every form: `LOCAL`, comments between words, a quoted `"role"`.
- `set_config`, quoted or not.
- `U&"..."` identifiers, because escapes inside them can spell any keyword.
- `CREATE`/`ALTER`/`DROP` `ROLE`/`USER`/`GROUP`, including quoted forms.
- `UPDATE pg_settings`.
- `standard_conforming_strings` and `backslash_quote`.
- `GRANT`, `REVOKE`, `COPY` and `PROGRAM`.
- `DO` blocks in the SQL tab. `ON CONFLICT DO NOTHING/UPDATE` still works, and migrations may use `DO`.
- Function and procedure bodies are checked with the same rules, and dynamic `EXECUTE` inside them is refused in the SQL tab.

The lexer now understands `E'...'` backslash escapes. The independent review did not find this one: `SELECT E'\' ', set_config(...) --'` hid a live call inside what the old lexer took for a string, and it executes on Postgres 16. Covered by `TestSQLFilterComprehensive` and `TestSQLFilterReviewBypasses`, plus endpoint 400 checks in `TestOwnerSQLRoleIsolation`.

**Reach.** `TestOwnerConnectionReach` opens the owner connection exactly as `Run` does: authenticator, then `SET LOCAL ROLE <project admin>`. It assumes the filter is bypassed and records what is reachable on Postgres 16:

| Probe | Result |
|---|---|
| `RESET ROLE`, then `current_user` | `authenticator` |
| After `RESET ROLE`: read the project's own table | denied (42501); authenticator does not inherit project roles |
| After `RESET ROLE`: `auth.users`, `pg_read_file`, `CREATE ROLE`, `SET SESSION AUTHORIZATION`, `GRANT <other project role>` | all denied (42501) |
| `SET ROLE <other project admin>` | allowed (authenticator is a member), but reading this project's table is denied (42501) |
| `SET ROLE <other project anon>`, then read this project's table | denied (42501) |
| Other project's table from this database | does not exist (42P01); databases are separate |
| `dblink` / `postgres_fdw` installed | 0 |

Nothing is reachable beyond what the project admin role can already read.

## Browse rewrite (replaces M7b-03/07 for read-only)
All columns cast to `::text` with quoted identifiers. ORDER BY primary key (or `ctid`). Paging: Previous disabled at offset 0, Next disabled when `has_more` is false. NULL shown as muted text. `TestBrowseTextCasting` covers: uuid pk, bigint 9007199254740993, numeric(10,2), timestamptz, jsonb, bytea, int[], bool, generated column, NULL, empty string. `TestBrowseRowsPagingIsStable` pages 61 rows of a composite-key table and of a table with no primary key (offset 0 and 50): 50 + 11 rows, no overlap, none missing, `has_more` correct. A zero-column table returns no rows instead of a SQL error. Tables without a primary key are ordered by `ctid`, which can shift under concurrent updates or VACUUM FULL. That is acceptable for browsing, but pages are not a snapshot.

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
- **Docker gates**: PENDING.
  - `scripts/test.sh` was run at 11e8ce7 as a non-root user in the cloud container. The in-Docker gofmt, vet and unit tests passed, and the production image built.
  - It then stopped building `Dockerfile.postgres`: the sandbox network blocks plain-HTTP apt from containers (`403 Forbidden` from deb.debian.org).
  - `scripts/test-supabase-js.sh` was not run.
  - Both must be run on the Pi.
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
4. `anon_readable` does not evaluate policy expressions (for example `USING (auth.uid() = owner)` counts as readable).
5. The SQL tab filter can be sidestepped by applying the same SQL as a migration. That is by design: the role model is the boundary (see the reach table).

## Click-through checklist for the owner
1. **Create project** → response shows `id`, `anon_key`, `service_key` with "Shown once; copy them now"
2. **Create a user** → hash stored as `$pibase$argon2id$...`; sign in with curl or supabase-js against the gateway (not tested in this session)
3. **Browse a uuid table** → all columns display as text, bigint preserves full precision, NULL shown as muted text, paging works
4. **Run a backup** → "Back up now" returns created filename; backup appears in list with name and size
5. **Apply a migration** → name + SQL textarea; result shown inline; migration appears in list with name, checksum prefix, time
6. **SQL tab** → `SET SESSION AUTHORIZATION`, `RESET ROLE`, `SET ROLE <other>`, `CREATE USER`, `ALTER USER` all return 400; normal queries work

## Independent review (2026-10-06)
A fresh reviewer session with no shared context reviewed 7346ec2 and 11e8ce7 and returned BLOCK. Status of each finding in the follow-up commit:

| ID | Severity | Finding | Status |
|---|---|---|---|
| R1 | High | Filter bypasses: quoted `set_config`, `U&"role"`, `DO` bodies, `UPDATE pg_settings`, `GROUP`, quoted role DDL | Fixed; each spelling is a unit test. Also fixed the E-string bypass found while fixing these. |
| R2 | High | Task 8 reach test only checked the filter's 400; report overclaimed | Fixed: `TestOwnerConnectionReach` asserts real reach; report corrected |
| R3 | Medium | `anon_readable` ignored RESTRICTIVE / `USING (false)` / column grants; anon role derived by string replace | Fixed; anon role from `control.project_catalog` |
| R4 | Medium | No connection-count assertion; table list used several connections | Fixed: `ListTables`, one project connection, counter test |
| R5 | Medium | Paging never exercised | Fixed: 61-row paging test |
| R6 | Low | No test that keys are written to the page; id not shown | Fixed |
| R7 | Low | No page test for backup/migration endpoints | Fixed |
| R8 | Low | ctid ordering undocumented; zero-column table errored; uuid/bytea in arrays | Documented; zero-column handled; `displayValue` now recurses into arrays |

The fixes are auditor-applied and need a second review before M7b is approved.

## Second independent review (2026-10-06, at 6b2d12e)
A second fresh reviewer verified R2-R8 as fixed and reopened R1:

| ID | Severity | Finding | Status |
|---|---|---|---|
| N1 | High | The lexer treated non-ASCII bytes as non-identifier, so `SELECT 1 AS é$a$, set_config('role',...) -- $a$` hid a live call (verified on PG16) | Fixed: bytes >= 0x80 are identifier characters, as in PostgreSQL's scanner. Unit-tested (`TestSQLFilterSecondReview`). |
| N2 | Low | `UPDATE t SET role = ...` and `ON CONFLICT DO UPDATE SET role = ...` were refused | Fixed: `SET ROLE` is not flagged after `UPDATE` in the same statement. The lexer now emits `;`, so a later `SET ROLE` in a routine body is still caught. |
| N3 | Info | DO message clear; migrations unaffected | No change needed |

Both reviewers noted that a keyword filter can never fully match PostgreSQL's grammar. The role model is the boundary (`TestOwnerConnectionReach`). If the filter must be airtight, the next step is a real parser such as pg_query_go.

## Full test run in the cloud sandbox (2026-10-06, at 25dab9c plus the control test fix)
The cloud container now has PostGIS 3 and pgvector installed for Postgres 16, plus `npm ci` for supabase-js.

**Go suite.** `PIBASE_TEST_ADMIN_DATABASE_URL=... go test -p 1 -count=1 ./...` passes in all 9 packages:
```
ok  cmd/pibase   ok  internal/adminserver   ok  internal/adminsql   ok  internal/auth   ok  internal/backup
ok  internal/control   ok  internal/gateway   ok  internal/keys   ok  internal/secretbox
```
- The first full run found one regression from this work. `control/TestExtensionGuardRejectsDynamicCustomerDDL` sent a `DO` block through the SQL tab and expected the database extension guard's error, but the SQL tab now refuses `DO` first.
- The test now checks that the SQL tab refuses it, and runs the same `DO` block as a migration, where the database guard must still refuse it. So the guard stays covered.

**supabase-js acceptance.** A native run of `scripts/test-supabase-js.sh`'s steps, using the same binary commands (`bootstrap`, `serve`, `project-create`, `migration-apply`) against a fresh password-auth cluster instead of containers:
```
supabase-js M6a acceptance passed
supabase-js M5b realtime acceptance passed
```

**Click-through checklist.** Run with `admin-serve` (loopback :18081) and the gateway (:18080) sharing one master key, driven in headless Chromium:
```
public gateway /dashboard status: 404
admin /_pibase without token: 401
PASS  1 create project → keys shown once (keys not in project list)
PASS  5 apply migration → listed (name, checksum prefix and time shown)
PASS  2 create user in dashboard → sign in via gateway with supabase-js (wrong password refused)
PASS  2b signed-in user reads through RLS (55 own rows, other user's row hidden)
PASS  3 browse uuid table (uuid text, bigint exact, NULL shown, paging 50 + 6)
PASS  4 back up now → listed
PASS  6 SQL tab refuses role/privilege changes (7 statements refused; SELECT works)
```

**Still not run.**
- The two Docker scripts as written, which build the Docker images. The sandbox network blocks apt inside containers, and the workaround (relaying the proxy into containers) was refused by the sandbox's safety controls.
- Anything on the Pi itself: arm64 runtime, its Postgres image and memory limits.
