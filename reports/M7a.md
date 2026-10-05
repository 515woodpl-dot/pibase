# M7a gate report — isolated operator listener and capacity limits

**Status:** ready for audit

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
