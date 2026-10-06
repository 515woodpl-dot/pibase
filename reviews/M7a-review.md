# M7a review

Commits: `e915a46` (engineer) and `87c3d45` (auditor-applied fixes) on `pibase/m7`
Reviewer: Claude (planner/auditor), 2026-10-05
Environment: Linux x86_64 sandbox, Go 1.24.7, PostgreSQL 16.13. Docker scripts not run;
nothing run on a Pi; the page was not opened in a real browser.

## Verdict: APPROVED at `87c3d45`

`e915a46` on its own would have been blocked: the dashboard could not be opened in a browser.
At the owner's request I fixed the findings directly instead of sending another round.
**Those fixes have had no independent review.**

## Accepted as built (VERIFIED by running the listener locally)

- `admin-serve` refuses a non-loopback address without the override.
- Every `/_pibase/` endpoint returns 401 with no token and with a wrong token.
- Create project returns the keys; the project list does not contain them.
- Delete refuses a mistyped id and works with the right one.
- Migration and SQL endpoints work; a `<script>` value comes back as plain JSON text and the
  page writes results with `textContent`.
- The public gateway returns 404 for `/dashboard` and `/_pibase/projects`.
- Capacity re-run on the Pi 4: 20 projects 1,605 requests, 0 errors; 40 projects 1,815
  requests, 148 errors, **all HTTP 503** (previously 400).

## Findings in `e915a46`, all fixed in `87c3d45`

| ID | Severity | Finding | Evidence |
|---|---|---|---|
| M7a-01 | High | `GET /dashboard` required the bearer token, so a browser got `401` and the page never loaded | VERIFIED |
| M7a-02 | Medium | "Back up now" created a directory named `<id>-<time>.pibase` with the archive inside; "list backups" always returned `[]` | VERIFIED |
| M7a-03 | Medium | Admin service built from the distroless image, which has no `pg_dump`, so backups could not work in the Compose stack | READ |
| M7a-04 | Medium | Page had no delete, backup-list or migration-list control, though the report's checklist described them | READ |
| M7a-05 | Low | Slot-timeout 503 had no `Retry-After` | READ |
| M7a-06 | Medium | Two restores in the same second failed with `file exists` after the database was already restored (older bug, seen as a flaky backup test) | VERIFIED |
| M7a-07 | Medium | `scripts/test.sh` never ran the backup integration suite; no real-database test of the dashboard endpoints | READ |

## Still to do by the engineer before M7b

1. Run `scripts/test.sh` and `scripts/test-supabase-js.sh` at `87c3d45` and paste the last lines.
2. Bring the Compose stack up and confirm the `admin` service starts from the `ops` image and
   "Back up now" produces a `.pib` file in the backups volume.
3. Open the page in a browser and walk the checklist in `reports/M7a.md`.

## Note on capacity

At 40 busy projects about 8% of requests still get 503. They fail in about one second, which
points at PostgreSQL's own connection limit being reached before the gateway's 30-slot queue
does anything: other connections (control pool, delivery worker, realtime listeners) are not
counted against the slots. The error is now the right one and retryable. Raising the ceiling is
a tuning task for a later gate.

## M7b

Unchanged from `reviews/M7-dashboard-gate.md`: table list, row browse/insert/edit/delete, user
management, on the same branch. Report in `reports/M7b.md`.
