# M6d gate report — release hardening

**Status:** audit fixes implemented; re-review required

## What was built

### 1. Data-driven schema grant cleanup

`revokeStrayCreateGrants` queries the schema ACL for every non-operator grantee
with `CREATE` on any managed schema and revokes it, instead of only revoking
from named project roles. This closes the follow-up from the M6c re-review
where a grant to a role outside the project's four (e.g. `authenticator`)
would survive the upgrade.

Test: `TestManagedSchemaUpgradeSkipsCustomerObjectsAndRollback` injects
`GRANT CREATE ON SCHEMA auth TO authenticator` alongside the previous anon,
authenticated, service, and PUBLIC grants. After upgrade, `assertOnlyOperatorCanCreateManagedSchemas`
verifies no non-operator role retains `CREATE`.

### 2. Narrow admin table grants

Schema version bumped from 8 to 9. The admin role's table grants on `storage`
and `cron` tables were reduced from `ALL PRIVILEGES` to `SELECT, INSERT,
UPDATE, DELETE`. Explicit `REVOKE TRIGGER, REFERENCES, TRUNCATE` ensures
upgrades from v8 also lose the unwanted privileges.

Privilege justification:
- `storage.buckets`: SELECT (list/get), INSERT (create), UPDATE (update settings),
  DELETE (delete bucket) — used by the storage API endpoints.
- `storage.objects`: SELECT (list/download), INSERT (upload), UPDATE (re-upload
  metadata), DELETE (delete object) — used by the storage API endpoints.
- `cron.jobs`: SELECT (scheduler reads), INSERT (customer creates jobs),
  UPDATE (scheduler updates last_run/next_run/error, customer modifies),
  DELETE (customer deletes) — used by the scheduler worker and customer SQL.
- `cron.webhook_outbox`: SELECT (delivery worker reads), INSERT (customer
  trigger via `cron.enqueue_webhook()`, scheduler enqueues), UPDATE (delivery
  worker marks delivered/dead-lettered), DELETE (not currently used but
  consistent) — used by the webhook delivery worker.

TRIGGER is not needed: triggers on managed tables are created by the upgrade SQL
running as the operator. REFERENCES is not needed: no customer FK targets managed
tables. TRUNCATE is not needed: no feature uses it.

`\dp` before (v8):

```
storage.buckets: admin=arwdDxt/operator
storage.objects: admin=arwdDxt/operator
cron.jobs: admin=arwdDxt/operator
cron.webhook_outbox: admin=arwdDxt/operator
```

`\dp` after (v9):

```
storage.buckets: admin=arwd/operator
storage.objects: admin=arwd/operator
cron.jobs: admin=arwd/operator
cron.webhook_outbox: admin=arwd/operator
```

Test: `assertNarrowAdminTableGrants` verifies that the admin role has zero
TRIGGER, REFERENCES, or TRUNCATE grants on any relation in the storage or cron
schemas. Called from both `TestManagedSchemaOwnershipBlocksAdminDDL` and
`TestManagedSchemaUpgradeSkipsCustomerObjectsAndRollback`.

### 3. Restore visibility

When the post-restore upgrade fails, `Restore` now prints a clear line on
stderr:

```
restore: project <id> requires schema attention; run project-list to check schema_error
```

Exit code remains 0. Documented in `docs/PI-OPERATIONS.md`.

### 4. Upgrade from retained data

`scripts/test-upgrade-retained.sh` builds the M5b binary in a temporary
worktree, starts a disposable Postgres with a named Docker volume, creates two
projects with REST data, auth tables, a cron job, and storage files, takes a
pre-upgrade backup, then bootstraps with the current binary and verifies:

- Schema version 9 on both projects (measured)
- Operator ownership of all four managed schemas (measured)
- Data rows preserved in both projects (measured)
- Cron job preserved (measured)
- Auth table accessible (measured)
- No TRIGGER/REFERENCES/TRUNCATE grants on storage/cron tables (measured)
- Post-upgrade backup and restore: version 9, data rows, ownership, storage
  file all preserved (measured)

Re-run with `PIBASE_IMAGE=pibase:m6d-gate ./scripts/test-upgrade-retained.sh`
to reproduce.

The drill restores the pre-upgrade M5b archive after upgrade, including the
old user, REST rows, and uploaded object.

### 5. Memory measurement

`scripts/measure-memory.sh` sums PSS from `smaps_rollup` for every process
inside each container at one project, 20 projects idle, and 20 projects after
each project has served one request. Raw output is committed in
`reports/raw/m6d-memory-pss.txt`.

Machine: Raspberry Pi 4, Debian 13, Linux 6.18.34+rpt-rpi-v8, aarch64.

The raw run committed with this change is incomplete, so no memory number is
reported or estimated. PSS is read from inside each container, so host root is
not required. Docker cgroup memory statistics remain optional; see
`docs/PI-OPERATIONS.md`.

### 6. Pi drill kit

`scripts/pi-drill.sh` consolidates the `reports/M4-03.md` procedure into a
single script that: captures the baseline, runs setup, verifies host state,
re-runs setup for idempotence, initializes and boots the stack, creates a
project, attempts a REST round trip, takes a backup, and provides reboot
instructions with a `PIBASE_DRILL_POSTREBOOT=1` post-reboot check. All output
goes to `pi-drill-<date>.txt`.

The script needs no `sudo` beyond what `setup-pi.sh` already requires. It
refuses a data root that already contains PostgreSQL data, creates a migration
before its REST insert/read comparison, stores only the project id and anon key
for post-reboot checks, and post-reboot mode performs checks only. It was
tested with the existing `scripts/test-setup-pi.sh` regression approach
(mocked host tools). It cannot be run on real hardware from this build
environment.

## Tests run

- `sh -n scripts/test-upgrade-retained.sh scripts/measure-memory.sh
  scripts/pi-drill.sh`: passed.
- `gofmt`, `go vet ./internal/backup`, and `go test ./internal/backup`:
  passed (without `PIBASE_TEST_ADMIN_DATABASE_URL`, so only non-integration
  backup tests ran).
- `PIBASE_IMAGE=pibase:m6d-drill PIBASE_TEST_POSTGRES_IMAGE=pibase-postgres:m6d-drill
  ./scripts/test-upgrade-retained.sh`: passed. The M5b-to-current disposable
  drill verified the pre-upgrade archive, retained sign-in, REST rows, object
  download, and the post-restore equivalents.
- `git diff --check`: passed.

## Audit fixes

- **M6d-01:** Restore now takes a `pre-restore-*` encrypted safety archive in
  the archive directory before any mutation, drops `auth`, `storage`,
  `realtime`, and `cron` with `CASCADE`, restores strictly, then upgrades.
  Integration coverage simulates an M5b archive, includes a public foreign key
  to `auth.users`, verifies the safety archive exists, and restores it.
- **M6d-02:** The retained-data drill now signs up and signs in a user, writes
  rows via REST, uploads/downloads through Storage, and restores the actual
  pre-upgrade archive before repeating those checks.
- **M6d-03:** PSS is summed across all container processes, and the script
  includes the served-request case. The attempted raw run is committed as an
  incomplete measurement; no measured or estimated memory number is claimed.
- **M6d-04:** The Pi drill now performs a real migration/REST round trip,
  parses values without `eval` or whitespace-sensitive JSON matching, separates
  post-reboot execution and output, and stops when Docker group membership is
  inactive.
- **M6d-05:** The summary no longer represents a post-upgrade archive restore
  as satisfying the old-backup requirement.

## What was NOT tested

- `scripts/pi-drill.sh` on disposable Raspberry Pi hardware.
- The real-PostgreSQL backup integration tests and completed 20-project PSS
  run were not completed in this command session.
- `scripts/test-supabase-js.sh` was not re-run (no JS changes in this gate).
- Upgrade from a retained real customer data directory or real old `.pib` file.
- Gateway under load (measurements are idle only).
- Fresh Raspberry Pi OS image.
