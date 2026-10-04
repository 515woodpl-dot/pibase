# M6b gate report — optional extensions

**Status:** audit fixes applied and locally verified; independent re-review pending

## Delivered

- Operator-only `postgis-enable <project-id>` command.
- Operator-only `pgvector-enable` now validates extension availability in the
  target database and uses the `_control` authorization boundary.
- The Postgres image bundles `pgvector` and PostGIS binaries; customer SQL and
  migrations reject extension DDL.
- Project branching detects enabled `postgis`/`vector` extensions, enables the
  same extensions in the branch, and preserves public-schema data without
  copying extension-managed `spatial_ref_sys` data.
- Bootstrap recreates the gateway-owned project catalog view so upgrades from
  older column layouts remain idempotent.
- `_control` records a project schema version. Bootstrap upgrades older managed
  projects through the canonical storage, realtime, cron, and MFA schema path;
  intentionally schema-less M0 measurement projects stay at version zero and
  are skipped by the delivery worker.
- Fresh projects run through that same canonical upgrade path. This also fixes
  the M6a unnamed-factor uniqueness rule for newly created projects, not only
  upgraded projects.
- Go integration and supabase-js acceptance gates now use disposable
  PostgreSQL containers and random host ports. They no longer change the live
  stack's authenticator password, catalog, gateway image, or gateway process.

## Verification

- `go vet ./...` and unit tests: passed.
- Real PostgreSQL control and gateway integration tests: passed.
- Extension enablement and branch-preservation integration tests: passed.
- Production image build: passed.
- Linux ARM64 build: passed.
- `PIBASE_IMAGE=pibase:m6b-final ./scripts/test.sh`: passed against a
  disposable PostgreSQL 16/PostGIS cluster, including the production image and
  Linux ARM64 build.
- `PIBASE_IMAGE=pibase:m6b-final ./scripts/test-supabase-js.sh`: passed against
  a separate disposable stack under Node 22.
- Upgrade regression: removal and idempotent recreation of storage, realtime,
  cron, and MFA objects with operator ownership/RLS checks: passed.
- Scheduler regression proving version-zero measurement projects do not open a
  project pool: passed.
- Duplicate unnamed TOTP enrollment on a newly created project: rejected.
- Disposable-stack Node 22 `supabase-js` M6a acceptance: passed.

## Measured M6b image and memory (Raspberry Pi 4, Linux arm64)

- `pibase-postgres:test` image size: **647,530,620 bytes (617.5 MiB)**,
  measured with `docker image inspect`.
- Sum of `VmRSS` for PostgreSQL processes in a fresh container: **65,644 KiB**
  before PostGIS and **70,296 KiB** after `CREATE EXTENSION postgis` plus a
  buffered geography query, a measured increase of **4,652 KiB**. This is a
  process-RSS sum and can double-count shared pages; it is not container PSS.
- The PostgreSQL extension image was built natively as **linux/arm64** from
  `Dockerfile.postgres`.
- The shipped pgvector 0.8.6 control file has no `trusted = true` setting;
  regardless, the database event trigger rejects extension DDL from customer
  sessions.

## Known scope

Only core PostGIS is enabled by the operator command. Raster, topology,
geocoding, arbitrary extensions, and customer-managed extension installation
remain unsupported.

The fresh Raspberry Pi/USB SSD setup and reboot drill in `reports/M4-03.md`
remains a release prerequisite. This report is engineering evidence, not the
independent M6b audit required by `PLAN.md`.
