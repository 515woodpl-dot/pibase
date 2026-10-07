# Planner decision: M7, functional operator dashboard (APPROVED; starts after M6e is approved)

Planner/auditor: Claude, 2026-10-05. Base: `pibase/m6e` once its fixes are approved.
Owner's instruction: unstyled, functional, smallest possible build. Looks do not matter yet.

## Decisions (do not relitigate)

1. **Plain HTML.** No CSS beyond browser defaults, no framework, no build step, no Node, no new
   dependency. One page with sections, small inline script, `fetch` to JSON endpoints. Always
   put values into the page with `textContent`, never `innerHTML`.
2. **Separate admin listener.** The public gateway must not hold superuser database
   credentials. Add `pibase admin-serve`: same binary, its own Compose service, bound to
   `127.0.0.1` (default port 18081), holding `PIBASE_ADMIN_DATABASE_URL` and the backup key.
   It serves the dashboard and its endpoints. Move the existing `/dashboard` and `/_pibase/*`
   routes there; the public gateway stops serving them. Refuse to start if the listen address is
   not loopback unless `PIBASE_ADMIN_ALLOW_NONLOCAL=1` is set, and document reaching it through
   SSH forwarding or Tailscale.
3. **Auth:** the existing operator token, sent in a header on every request. No cookies.
   Constant-time compare. Every endpoint returns 401 without it.
4. **Reuse.** Endpoints call the functions the CLI already uses (`CreateProject`,
   `DeleteProject`, `ApplyMigration`, owner SQL, backup). No second implementation.
5. Row access runs as the project's admin role through the same boundary as owner SQL.
   Table and column names come from the catalog and are quoted; values are parameters.

## Features

| # | Feature | Notes |
|---|---|---|
| 1 | Projects: list with usage and `schema_error`; create; delete | Keys shown once after create. Delete requires typing the project id. |
| 2 | Tables: list `public` relations with row count, RLS on/off, and whether the anon role can read | Read-only summary. |
| 3 | Rows: browse 50 per page; insert, edit, delete one row | Edit and delete only for tables with a primary key; others are read-only. |
| 4 | Migrations: paste SQL plus a name, apply; list applied migrations | |
| 5 | SQL box | Already exists; keep. |
| 6 | Users: list, create, delete for a project | |
| 7 | Backups: "back up now", list files with size and time | Restore stays on the command line. |
| 8 | Extensions: enable PostGIS / pgvector buttons | |

Out of scope: styling, policy editor, file browser, restore from the page, customer accounts,
anything in Tier 2.

## Also in this gate: connection limit behaviour (from `reviews/M6e-review.md`)

The public gateway limits concurrently open project connections to
`PIBASE_MAX_PROJECT_CONNECTIONS` (default: 30 with `max_connections=40`), waits up to 5 seconds
for a slot, then answers `503` with `Retry-After: 1` and a clear JSON message. PostgreSQL's
`53300` must also map to that `503`, never `400`. Re-run `scripts/measure-concurrency.sh` at 40
projects and report the status codes.

## Tests (real Postgres, no mocks)

- Every endpoint: 401 without the token; works with it.
- Create project then delete it; keys returned once and not retrievable afterwards.
- Row insert, edit, delete on a table with a primary key; refused on one without.
- A table or column name containing quotes, spaces or SQL is handled safely.
- A value containing `<script>` is stored and shown as text.
- The public gateway no longer serves `/dashboard` or `/_pibase/*`.
- `admin-serve` refuses a non-loopback address without the override.
- Connection limit: queued requests succeed; overflow gets `503` with `Retry-After`.

## Gate report: `reports/M7.md`

What was built, tests run, what was NOT tested, a 10-line manual checklist the owner can click
through, questions for the planner. Branch `pibase/m7` from the approved `pibase/m6e`. Push only
`pibase/m7`. Stop there for audit.
