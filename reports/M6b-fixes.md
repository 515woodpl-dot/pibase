# M6b audit fixes

**Audit:** `reviews/M6b-review.md` on `pibase/review-m6b`  
**Branch:** `pibase/m6b`

## M6b-01 — isolate broken project upgrades

- Added `control.projects.schema_error` and exposed it through the gateway-owned
  catalog view.
- Bootstrap records a bounded error for a project whose managed schema cannot
  be upgraded, logs the skip and continues with later projects. Successful
  upgrades clear the error; delivery workers ignore errored projects.
- Added the exact damaged-`storage.buckets` reproduction and proved bootstrap
  returns success, records the error, installs the independent extension guard,
  and upgrades a later healthy project.
- Tracked operator ownership of managed schemas as a separately gated
  compatibility follow-up in `PLAN.md`, as permitted by the audit fix list.

## M6b-02 — archive/TOC branch restore

- Replaced plain-text dump filtering with `pg_dump -Fc`, `pg_restore -l`, and a
  `pg_restore -L` TOC that omits exactly one `SCHEMA public` archive entry.
- Restore uses the fresh branch admin role and fails closed if the archive has
  zero or multiple matching schema entries.
- Added the audit reproduction: both a COPY row and a SQL-function body
  containing `CREATE SCHEMA public;` survive branching byte-for-byte.

## M6b-03 — database-enforced extension boundary

- Every managed project gets an operator-owned PostgreSQL event trigger on
  `CREATE EXTENSION`, `ALTER EXTENSION`, and `DROP EXTENSION`. It permits only a
  superuser session; the SQL tokenizer remains a friendly early check.
- The guard is committed separately from the rest of a schema upgrade so it is
  retained even when customer damage makes that upgrade fail.
- Added live regressions for dynamic `DO ... EXECUTE 'CREATE EXTENSION hstore'`
  and the escape-string migration form from the audit. Neither extension is
  installed.
- Branching now explicitly rejects legacy extensions other than `postgis` and
  `vector`; a live `hstore` regression covers that decision.
- The shipped pgvector 0.8.6 control file is not marked trusted.

## M6b-04 — visible realtime JavaScript gate

- The Go helper now uses `t.Skip` when Node is unavailable, making the omitted
  sub-gate visible.
- `scripts/test-supabase-js.sh` runs both the main Node 22 acceptance and
  `tests/realtime-js.mjs` against its disposable gateway.

## M6b-05 — measured extension-image cost

- ARM64 image size: 647,530,620 bytes (617.5 MiB).
- PostgreSQL process RSS sum: 65,644 KiB baseline; 70,296 KiB after loading and
  exercising PostGIS; measured delta 4,652 KiB. RSS sums can double-count shared
  pages and are not PSS.
- `Dockerfile.postgres` was built natively for linux/arm64.

## Tests run

- `PIBASE_IMAGE=pibase:m6b-fixes ./scripts/test.sh`: passed, including setup
  script checks, formatting, `go vet`, all unit tests, fresh PostgreSQL 16
  control/gateway integration, production image build, and Linux ARM64 build.
- `PIBASE_IMAGE=pibase:m6b-fixes ./scripts/test-supabase-js.sh`: passed both the
  Node 22 M6a acceptance and `tests/realtime-js.mjs` against its disposable
  PostgreSQL/gateway stack.
- Individual verbose control integration run: all M6b-01 through M6b-03
  regressions passed, including schema-error continuation, schema-like branch
  content, dynamic extension DDL, and unsupported-extension branch rejection.
- `git diff --check` and shell syntax checks: passed.

## Not tested

- Fresh Raspberry Pi OS installation and reboot persistence (`M4-03`).
- Upgrade from a retained real M5b/M6a data directory; upgrade coverage uses
  deliberately downgraded projects in a fresh PostgreSQL 16 cluster.
- Migration of managed-schema ownership to an operator role; this is the
  explicitly tracked compatibility follow-up.
- PostgreSQL PSS after loading PostGIS; only process RSS sum was measured.
- Independent re-review of these fixes.
