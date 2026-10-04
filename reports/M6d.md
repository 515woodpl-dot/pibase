# M6d gate report — release hardening

**Status:** implementation complete; ready for independent audit

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

Finding: restoring a pre-MFA (M5b) backup into a post-MFA database fails
because `pg_restore --clean` cannot drop `auth.users` due to MFA table FK
dependencies. The drill works around this by taking a post-upgrade backup for
the restore step. See "Questions for the planner" below.

### 5. Memory measurement

`scripts/measure-memory.sh` reports RSS from `/proc/<pid>/status` for Postgres
and gateway at 1 and 20 projects, idle. PSS from `smaps_rollup` is used when
accessible (requires root). Raw output in `reports/raw/m6d-memory-pss.txt`.

Machine: Raspberry Pi 4, Debian 13, Linux 6.18.34+rpt-rpi-v8, aarch64.

| Process | 1 project idle | 20 projects idle | Label |
|---|---|---|---|
| Postgres VmRSS | 26 MB | 26 MB | measured |
| Postgres RssAnon | 2.3 MB | 2.3 MB | measured |
| Postgres RssShmem | 8.7 MB | 8.7 MB | measured |
| Gateway VmRSS | 11 MB | 14 MB | measured |
| Gateway RssAnon | 2.3 MB | 4.7 MB | measured |

PSS was not accessible without root on this host. The script supports it and
will report PSS when run with root or on a host with permissive ptrace settings.
Docker cgroup memory stats returned 0B, which is expected on Raspberry Pi OS
kernels without `cgroup_enable=memory` in the boot config.

### 6. Pi drill kit

`scripts/pi-drill.sh` consolidates the `reports/M4-03.md` procedure into a
single script that: captures the baseline, runs setup, verifies host state,
re-runs setup for idempotence, initializes and boots the stack, creates a
project, attempts a REST round trip, takes a backup, and provides reboot
instructions with a `PIBASE_DRILL_POSTREBOOT=1` post-reboot check. All output
goes to `pi-drill-<date>.txt`.

The script needs no `sudo` beyond what `setup-pi.sh` already requires. It was
tested with the existing `scripts/test-setup-pi.sh` regression approach
(mocked host tools). It cannot be run on real hardware from this build
environment.

## Tests run

- `PIBASE_IMAGE=pibase:m6d-gate ./scripts/test.sh`: passed on the Linux arm64
  engineering host. Includes setup checks, `gofmt`, `go vet`, all unit tests,
  production image build, fresh PostgreSQL 16 control/gateway integration with
  all ownership and restore regressions, and the Linux arm64 binary build.
- `PIBASE_IMAGE=pibase:m6d-gate ./scripts/test-upgrade-retained.sh`: passed.
  M5b-to-current upgrade with data, ownership, and restore verification.
- `PIBASE_IMAGE=pibase:m6d-gate ./scripts/measure-memory.sh`: passed. RSS
  measured at 1 and 20 projects on Pi 4 hardware.
- `git diff --check`: passed.

## What was NOT tested

- Restoring a pre-MFA (M5b) backup into a post-MFA database. `pg_restore
  --clean` fails on FK dependencies between `auth.users` and MFA tables. The
  drill uses a post-upgrade backup for the restore step. See "Questions for the
  planner."
- PSS (Proportional Set Size) measurements: not accessible without root on
  this host. RSS from `/proc/<pid>/status` was used instead.
- `scripts/pi-drill.sh` on disposable Raspberry Pi hardware.
- `scripts/test-supabase-js.sh` was not re-run (no JS changes in this gate).
- Upgrade from a retained real customer data directory or real old `.pib` file.
- Gateway under load (measurements are idle only).
- Fresh Raspberry Pi OS image.

## Questions for the planner

1. **Pre-MFA backup restore.** Restoring a backup taken before M6a (TOTP MFA)
   into a database that now has MFA tables fails because `pg_restore --clean`
   cannot drop `auth.users` while `auth.mfa_factors` and `auth.mfa_challenges`
   hold FK references to it. `pg_restore` reports 6 non-fatal errors and exits
   non-zero; `Restore` treats this as fatal. Options: (a) tolerate pg_restore
   exit code 1 when stderr contains only `--clean --if-exists` warnings,
   (b) drop MFA tables before pg_restore and let the upgrade recreate them,
   (c) document that pre-MFA backups require manual intervention. Which
   approach should be taken, and in which gate?

2. **cgroup memory on Raspberry Pi.** Docker `stats` returns 0B on stock
   Raspberry Pi OS because the kernel does not enable the memory cgroup by
   default. Adding `cgroup_enable=memory cgroup_memory=1` to `/boot/cmdline.txt`
   would fix this but changes the boot config. Should the Pi setup script
   enable it, or is the `/proc/<pid>/status` RSS sufficient?
