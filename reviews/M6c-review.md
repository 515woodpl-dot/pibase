# M6c review

Commits reviewed: `858db53`, `f83d61f` on `pibase/m6c`
Gate definition: `reviews/M6c-ownership-gate.md` on `pibase/review-m6b`
Reviewer: Claude (planner/auditor), 2026-10-04
Environment: Linux x86_64 sandbox, Go 1.24.7, PostgreSQL 16.13 with Ubuntu's PostGIS 3.4.2 and
pgvector 0.6.0, Node 22. Not the project's Docker images, not a Pi.

## Verdict: APPROVE WITH FIXES

No Critical or High findings. Three Medium findings from code reading, to be fixed before real
customer data is hosted.

**Limit of this review:** unlike the M6b reviews, I did not run my own hand-written attempts to
break the boundary. I ran the engineer's test suites and read the code. The DDL-denial evidence
is therefore the engineer's regression test passing on my machine, not an independent attack.

Tags: VERIFIED = I ran it. READ = code reading only. UNVERIFIED = not checked.

## What I ran

| Check | Result | Tag |
|---|---|---|
| `go vet ./...`, `go test ./...` | pass | VERIFIED |
| `go test -p 1 -count=1 ./internal/backup ./internal/control ./internal/gateway` on a fresh real cluster (includes `TestManagedSchemaOwnershipBlocksAdminDDL`, `TestManagedSchemaUpgradeSkipsCustomerObjectsAndRollback`, `TestRestorePreM6cBackupReappliesManagedOwnership`, realtime JS) | pass | VERIFIED |
| `GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build ./cmd/pibase` | pass | VERIFIED |
| `scripts/test.sh`, `scripts/test-supabase-js.sh` on Docker | not run | UNVERIFIED |
| Raspberry Pi, real retained data directory, real old `.pib` file | not run | UNVERIFIED |

## Gate checklist

| Requirement | Status |
|---|---|
| Ten DDL statements denied through `sql` and `migration-apply` | Met (engineer's test passes for me) |
| `SECURITY DEFINER` inventory with owner, `search_path`, grants, rationale | Met (READ) |
| Features still work on fresh and upgraded projects | Met by the Go suites I ran; Docker JS gates UNVERIFIED |
| Version 7 upgrade keeps grants, raw before/after output | Met (`reports/raw/m6c-v7-ownership.txt`, READ) |
| Project with customer-made objects is skipped, nothing dropped | Met, with M6c-01 below |
| Rollback documented and tested | Met |
| Pre-M6c backup restore | Met, with M6c-02 below |
| M6b heading correction | Done (`858db53`) |
| Decisions 1 to 6 respected, no new role/service/dependency | Yes (READ) |

## Findings

### M6c-01 (Medium): the customer-object check reads other projects' rows  — READ

`customerManagedObjects` queries `pg_shdepend` without filtering on `dbid`. `pg_shdepend` is a
cluster-wide catalog: it holds ownership rows for every database. The query passes each row's
object id to `pg_identify_object`, which resolves it in the *current* database. An object id
from another project that happens to match a managed object here would be reported as
"customer-owned", and that project would be skipped with a false `schema_error`. Collisions are
unlikely today, but the result should not depend on other tenants, and the scan grows with
every project.

Fix: add `AND dependency.dbid = (SELECT oid FROM pg_database WHERE datname = current_database())`.
Add a test with two projects where the second has customer-made objects and the first must
still upgrade cleanly.

### M6c-02 (Medium): a failed post-restore upgrade leaves the project half restored  — READ

`Restore` runs `pg_restore`, then `UpgradeProjectSchema`, then swaps the storage files. If the
archive contains customer-made objects in a managed schema (possible for any pre-M6c backup),
the upgrade returns an error and `Restore` exits before the files are swapped: new database,
old files, non-zero exit.

Fix: finish the file swap regardless, then report the upgrade failure. `schema_error` is
already recorded, so the restore can succeed with a clear warning that the project needs
attention. Add a test for a pre-M6c archive that contains such an object.

### M6c-03 (Medium): schema grants made by the old owner are not removed  — READ

Before M6c the admin role owned these schemas and could grant `CREATE` on them to other roles
(the code comment on `customerManagedObjects` says so). The upgrade revokes `CREATE` from the
admin role only, and revokes from `PUBLIC` only for `storage`, `realtime` and `cron`. A grant of
`CREATE` on a managed schema to the project's `anon`, `authenticated` or `service` role, or to
`PUBLIC` on `auth`, survives the migration.

Fix: in the version 8 step, `REVOKE CREATE ON SCHEMA auth,storage,realtime,cron FROM PUBLIC` and
from the project's anon, authenticated and service roles, then re-grant the intended `USAGE`.
Extend the ownership test to assert that no role other than the operator has `CREATE` on any
managed schema after upgrade.

### M6c-04 (Low): check and upgrade are not in one transaction  — READ

`ensureNoCustomerManagedObjects` runs before `Begin`. Move it inside the transaction so the
decision and the ownership change see the same state.

## Notes

- The admin role keeps `ALL PRIVILEGES` on `storage` and `cron` tables, which includes `TRIGGER`.
  That follows gate decision 2 ("keep exactly the grants it has today") and is not a finding.
  It is worth narrowing in a later gate.
- Not-tested items in `reports/M6c.md` are stated honestly and match what I could not verify.

## Fix list for Codex

1. M6c-01: `dbid` filter and two-project test.
2. M6c-02: restore completes the file swap, then reports the upgrade failure; test.
3. M6c-03: revoke stray `CREATE` grants; test.
4. M6c-04: move the check inside the transaction.

Small enough that a full re-review is not required: send the fix commit and I will check the
four items.
