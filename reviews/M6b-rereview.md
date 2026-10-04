# M6b re-review (fix commit)

Commit reviewed: `7368868` on `pibase/m6b` ("fix: address M6b audit findings")
Previous review: `reviews/M6b-review.md` (BLOCK at `48bf192`)
Reviewer: Claude (planner/auditor), 2026-10-03
Environment: same as the first review (Linux x86_64 sandbox, Go 1.24.7, PostgreSQL 16.13 with
Ubuntu's PostGIS 3.4.2 and pgvector 0.6.0, Node 22). Not the project's Docker images, not a Pi.

## Verdict: APPROVE WITH FIXES

All three High findings are fixed and I could not reproduce them. No new Critical or High
findings. The follow-ups below do not block M6b.

## Findings status

| ID | Was | Now | Evidence (all VERIFIED by running) |
|---|---|---|---|
| M6b-01 | High | Fixed | Same three statements as before, then `bootstrap`: exit 0, warning logged, attacker project keeps its old version with `schema_error = upgrade schema: ERROR: column "public" does not exist`, the three other projects (including one later in id order) reach version 7. After the customer removes the bad table, the next bootstrap upgrades it and clears the error. |
| M6b-02 | High | Fixed | Branch of the reproduction project: `notes` has 3 rows, `snippet()` returns `1\nCREATE SCHEMA public;\n`, geometry and vector rows intact, a table named `odd SCHEMA - public name` is copied, no temp files left in `/tmp`. |
| M6b-03 | High | Fixed | `DO ... EXECUTE 'CREATE EXTENSION hstore'`, the `E'\''` migration, and a `SECURITY DEFINER` function that creates an extension are all refused by the database: `extensions can only be managed by the operator`. `pg_extension` shows only `plpgsql`. The customer cannot drop or disable the event trigger or replace its function. The guard is present in branch targets. Branching a project that has a legacy `hstore` is refused with a clear message and leaves no orphan database, roles or catalog row. |
| M6b-04 | Low | Fixed | READ: the Go helper now uses `t.Skip`, and `scripts/test-supabase-js.sh` runs `tests/realtime-js.mjs`. The realtime JS test passed for me inside the Go integration run. |
| M6b-05 | Low | Addressed | READ: report gives arm64 image size 617.5 MiB and RSS 65,644 → 70,296 KiB, labeled as RSS, not PSS. I could not reproduce these numbers. |

## What I ran

- `go build`, `go vet`, `go test ./...`: pass.
- `go test -p 1 -count=1 ./internal/control ./internal/gateway` against a fresh real cluster: pass.
- `GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build ./cmd/pibase`: pass.
- Manual reruns of all three original reproductions (table above).
- Extra check not in the engineer's report: `backup` then `restore` of a project with PostGIS,
  pgvector and the new event trigger. Restore exit 0; rows, geometry and vector data are back;
  the guard still blocks customer extension DDL afterwards.

## Not verified

- `scripts/test.sh` and `scripts/test-supabase-js.sh` on Docker (image pulls unavailable here).
- The shipped image's pgvector 0.8.6 not being "trusted" (engineer's claim). With the event
  trigger in place this no longer matters for the boundary.
- Image size and RSS numbers.
- Anything on a Raspberry Pi; upgrade from a real older data directory.

## Follow-ups (non-blocking)

1. **Managed-schema ownership (tracked in PLAN.md).** The customer admin role still owns `auth`,
   `storage`, `realtime` and `cron` and can still drop gateway tables in its own project. The
   damage is now contained to that project. Do the operator-ownership migration as its own
   gated change before any real customer data goes on this.
2. **Make skipped projects visible.** Bootstrap exits 0 when projects are skipped, and
   `project-list` does not show `schema_error`. Add the column to `project-list` so the operator
   sees it without reading logs.
3. **Guard function lives in `pg_catalog`**, which `pg_dump` does not export. Restore works today
   because it targets an existing project database that already has the function. Note this in
   the restore docs, or recreate the guard at the end of `restore`.
4. **Branch temp archive** is written to the container's temp directory at full dump size. Fine
   at Pi scale; put it next to the backups volume if projects grow toward the 1 GB limit.
5. Release prerequisites the report already lists: fresh Pi drill (`M4-03`), upgrade from
   retained data, PSS measurement.
