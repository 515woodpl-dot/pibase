# M7a gate report — isolated operator listener and capacity limits

**Status:** approved after auditor-applied fixes (see `reviews/M7a-review.md` on `pibase/review-m7`)

## What was built

- Added `pibase admin-serve`, a separately deployed operator listener. It
  requires the operator token on every request with a constant-time bearer
  comparison, defaults to `127.0.0.1:18081`, and refuses non-loopback bind
  addresses unless `PIBASE_ADMIN_ALLOW_NONLOCAL=1` is set.
- Removed `/dashboard` and `/_pibase/*` from the public gateway. The Compose
  gateway no longer receives the superuser database URL, backup key, or
  operator token; the new `admin` service receives them instead.
- M7a dashboard API: project list/create/delete (typed-id confirmation), owner
  SQL, migrations, backup-now and backup listing, plus PostGIS/pgvector enable.
  These call the existing control, owner-SQL, and backup package functions.
- Added `PIBASE_MAX_PROJECT_CONNECTIONS` (default 30). The gateway waits five
  seconds for a slot, then returns 503; PostgreSQL `53300` also maps to 503
  with `Retry-After: 1`.
- Re-ran the disposable Pi capacity harness. At 20 projects it recorded 1,605
  requests / 0 errors; at 40 it recorded 1,815 requests / 148 errors. The
  harness records status codes but not error bodies, so these failures cannot
  yet be attributed to the new 503 behavior.

## Tests run

- `scripts/test.sh`: passed. Last lines:

```
ok   github.com/515woodpl-dot/pibase/internal/control  34.271s
ok   github.com/515woodpl-dot/pibase/internal/gateway  27.295s
```

- `scripts/test-supabase-js.sh`: passed. Last lines:

```
> node tests/supabase-js.mjs
supabase-js M6a acceptance passed
supabase-js M5b realtime acceptance passed
```
- Unit coverage verifies loopback-address refusal, unauthenticated admin
  requests receive 401, and the public gateway returns 404 for dashboard and
  `/_pibase` routes.
- `scripts/measure-concurrency.sh`: passed on the engineering Pi; complete raw
  output is in `reports/raw/m6e-concurrency.txt`.

## What was NOT tested

- Physical browser click-through of the operator listener.
- Capacity harness response bodies at 40 projects; it has status/latency only.
- M7b tables, row editor, and user management were intentionally not started.

## Manual click-through checklist

1. Start the stack and browse `http://127.0.0.1:18081/dashboard`.
2. Confirm the page rejects requests until the operator token is entered.
3. Load projects and verify usage and `schema_error` values.
4. Create a project and copy its keys from the one returned response.
5. Confirm a project delete requires typing its exact id.
6. Paste SQL for the selected project and inspect the returned result.
7. Apply a named migration, then confirm it appears in owner SQL results.
8. Press each extension button only for a disposable project.
9. Press **Back up now** and confirm the new encrypted file appears in backups.
10. Confirm `http://127.0.0.1:18080/dashboard` and `/_pibase/projects` return 404.

## Questions for the planner

1. The 40-project harness recorded 148 non-2xx responses but cannot preserve
   their response bodies. Should the harness record a bounded sample of bodies
   before tuning the connection limit further?

## Auditor-applied fixes (2026-10-05)

Applied by the auditor at the owner's request, so these changes have **not** had an
independent review.

- **Page could not load in a browser.** Every route, including `GET /dashboard`, required the
  bearer token, which a browser cannot send on navigation. The static page (no data, no
  secrets) now loads without the token; every `/_pibase/` endpoint and every non-GET request
  still returns 401 without it.
- **Backup wrote a directory and the list was always empty.** The handler passed a file name
  where the backup package expects a directory. It now writes into the backup root and returns
  the real file name; the list shows that project's `pibase-<id>-*.pib` and
  `pre-restore-<id>-*.pib` files.
- **Admin service image.** `compose.yaml` built the admin service from the distroless
  `gateway` target, which has no `pg_dump`. It now uses the `ops` target.
- **Page controls.** Added delete (typed id), list backups, and list applied migrations.
- **`Retry-After: 1`** is now sent on the slot-timeout 503 as well as on PostgreSQL `53300`.
- **Restore name collision.** Two restores of one project within the same second failed with
  `file exists` after the database had already been restored. The saved-copy name now has
  nanosecond precision. This predates M7a and showed up as a flaky backup test.
- **Gate script.** `scripts/test.sh` now also runs `./internal/backup` and
  `./internal/adminserver` in the real-Postgres step; the backup suite was never part of it.
- **Tests.** `TestOperatorDashboardEndpoints` drives create, list (no keys), migration, SQL,
  backup, backup list, mistyped delete and delete against real PostgreSQL.

Capacity question answered from the committed raw file: at 40 projects all 148 failures are
HTTP `503` (none are `400`), so no response-body sampling is needed.

Run on Linux x86_64, Go 1.24.7, fresh PostgreSQL 16.13, twice in a row
(`go vet ./...` clean, arm64 cross-build passes):

```
$ go test -p 1 -count=1 ./internal/backup ./internal/control ./internal/gateway ./internal/adminserver ./cmd/...
ok  	github.com/515woodpl-dot/pibase/internal/backup	3.144s
ok  	github.com/515woodpl-dot/pibase/internal/control	7.471s
ok  	github.com/515woodpl-dot/pibase/internal/gateway	20.013s
ok  	github.com/515woodpl-dot/pibase/internal/adminserver	0.523s
ok  	github.com/515woodpl-dot/pibase/cmd/pibase	0.005s
```

Not run for this commit: `scripts/test.sh`, `scripts/test-supabase-js.sh`, the Compose stack
with the `ops`-based admin service, and the page in a real browser.
