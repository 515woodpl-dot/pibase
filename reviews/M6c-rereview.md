# M6c re-review (fix commit)

Commit reviewed: `855cb73` on `pibase/m6c` ("fix: address M6c audit findings")
Previous review: `reviews/M6c-review.md` (approve with fixes at `f83d61f`)
Reviewer: Claude (planner/auditor), 2026-10-04
Environment: Linux x86_64 sandbox, Go 1.24.7, PostgreSQL 16.13 with Ubuntu's PostGIS and
pgvector, Node 22. Not the project's Docker images, not a Pi.

## Verdict: APPROVE

All four findings are fixed. M6c is closed. One Low follow-up, non-blocking.

Same limit as the first M6c review: I read the fixes and ran the engineer's test suites; I did
not run independent hand-written attempts against the boundary.

## Findings status

| ID | Was | Now | Evidence |
|---|---|---|---|
| M6c-01 | Medium | Fixed | READ: `dbid` filter added to the `pg_shdepend` query. VERIFIED: `TestManagedSchemaUpgradeSkipsCustomerObjectsAndRollback` passes with the healthy project created first. |
| M6c-02 | Medium | Fixed | READ: `Restore` finishes the file swap, then logs the upgrade failure; `schema_error` is recorded. VERIFIED: `TestRestorePreM6cBackupWithCustomerObjectFinishesFileSwap` passes. |
| M6c-03 | Medium | Fixed | READ: `REVOKE CREATE` now names `PUBLIC` and the anon, authenticated, service and admin roles. VERIFIED: the test's ACL assertion covers every grantee, not only those roles, and passes. |
| M6c-04 | Low | Fixed | READ: the check runs inside the upgrade transaction. |

## What I ran

- `go vet ./...`, `go test ./...`: pass.
- `go test -p 1 -count=1 ./internal/backup ./internal/control ./internal/gateway` on a fresh
  real cluster: pass, including all 20 DDL-denial subtests and both restore tests.
- `GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build ./cmd/pibase`: pass.

Not run: the Docker gate scripts, anything on a Raspberry Pi, a real retained data directory
or old `.pib` file.

## Follow-up (Low, non-blocking)

**Stray `CREATE` grants to roles outside the project's four.** The revoke lists `PUBLIC` and
the project's own roles. A pre-M6c grant to any other role name (for example `authenticator`)
would survive. The test would catch it, but only for grants the test itself injects. Make the
revoke data-driven: loop over every non-operator grantee with `CREATE` in the schema ACL and
revoke it. Fold this into the next gate.

## Notes

- A restore whose upgrade fails now exits 0 with a warning. `project-list` shows the
  `schema_error`. Mention in `docs/PI-OPERATIONS.md` that operators should check `project-list`
  after a restore.
- The report says moving the check into the transaction gives a consistent snapshot. Under
  PostgreSQL's default isolation each statement takes its own snapshot, so a small window
  remains. The outcome is harmless (a customer object stays customer-owned inside an
  operator-owned schema), so no change is requested.

## Release prerequisites still open (from earlier gates)

Fresh Raspberry Pi drill with reboot (`M4-03`), upgrade from retained real data, PSS
measurement, and the narrower table grants noted in the first M6c review.
