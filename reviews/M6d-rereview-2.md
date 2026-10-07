# M6d second re-review

Commit reviewed: `0fab24f` on `pibase/m6d`
Previous: `reviews/M6d-rereview.md` (blocked on item 6 at `7717d73`)
Reviewer: Claude (planner/auditor), 2026-10-04
Environment: Linux x86_64 sandbox, Go 1.24.7, PostgreSQL 16.13, Node 22. Docker scripts could
not be run here; nothing was run on a Pi.

## Verdict: APPROVE

All six scope items are accepted. M6d is closed.

## Findings status

| ID | Now | Evidence |
|---|---|---|
| M6d-04a endpoint that does not exist | Fixed | READ: project is created with `ops project-create`. VERIFIED: the same call with the CLI returns an id and a service key. |
| M6d-04b table not reachable through REST | Fixed | VERIFIED: I replayed the drill's exact migration (table plus grant to the service role), insert and read against a locally built gateway. Insert and read both return `[{"note": "hello from drill"}]`, the script's whitespace-insensitive comparison passes, and backup exits 0. |
| M6d-04c hygiene | Fixed | READ: both blocks run in a subshell whose status is propagated; state and output files are in `.gitignore`; `scripts/test-pi-drill-local.sh` covers steps 5 to 7 on a disposable Compose stack. That self-test also exposed a missing `PIBASE_DATABASE_URL` on the `ops` service in `compose.yaml`, now added. Not run by me (Docker). |
| M6d-06 served-request memory case | Fixed | READ: each of the 20 projects gets a real table and a service-key REST read; the backend count is recorded in the raw file. |

## What I ran

- `go vet ./...`: pass.
- `go test -p 1 -count=1 ./internal/backup ./internal/control ./internal/gateway` on a fresh
  real PostgreSQL 16: pass.
- `sh -n` on the three scripts: pass.
- Local replay of drill steps 6 and 7 with the CLI and a locally built gateway: pass.

Not run: `test-pi-drill-local.sh`, `test-upgrade-retained.sh`, `measure-memory.sh`,
`scripts/test.sh` (all Docker), and `pi-drill.sh` on hardware.

## Memory, as measured on the Pi 4 (engineer's numbers, raw file committed)

| Container PSS | 1 project idle | 20 projects idle | 20 projects after one REST read each |
|---|---:|---:|---:|
| PostgreSQL | 29.9 MB | 115.7 MB | 118.7 MB |
| Gateway | 12.0 MB | 14.3 MB | 15.6 MB |

Reading these correctly: after the 20 reads only **one** project-database backend was still
open, so this is the cost of 20 projects used one after another, not 20 projects connected at
the same time. Postgres memory with many simultaneous connections is still unmeasured. The
PLAN.md M0 question ("do 20 active projects fit in 4 GB?") is answered for sequential use
(about 135 MB total) and remains open for concurrent load.

## Notes (no action required for this gate)

- `.pibase-pi-drill-state` holds the drill project's service key in plain text (mode 600,
  ignored by git). Acceptable for a disposable drill; delete the file after the post-reboot run.
- Restore leaves tables created after the backup in place (existing behaviour of
  `pg_restore --clean`), now without any realtime or webhook triggers that pointed into the
  dropped managed schemas. Document or decide in a later gate.

## Open release prerequisites

1. **Owner task:** run `scripts/pi-drill.sh` on a clean Raspberry Pi OS image with a USB SSD,
   reboot, run it again with `PIBASE_DRILL_POSTREBOOT=1`, and paste both output files back.
   This closes `M4-03`.
2. Memory under concurrent load (for example 20 projects with 2 to 3 connections each).
3. Default API-role privileges for new `public` tables (see `reviews/M6d-rereview.md`, "Note
   for a later gate").
