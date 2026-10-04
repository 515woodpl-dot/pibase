# M6c gate report — managed-schema ownership

**Status:** implementation and local verification complete; ready for independent audit

## What was built

- Project schema version 8 transfers `auth`, `storage`, `realtime`, and `cron`
  from the project admin to the existing database operator. Database ownership
  remains with the project admin.
- The admin retains `USAGE`, existing table/sequence privileges, and the same
  explicit function execution grants, but has no `CREATE` on managed schemas.
- Before changing ownership, bootstrap finds objects not owned by the operator
  inside any managed schema. If any exist, it records a bounded `schema_error`
  naming them, changes none of the managed schemas or objects, and continues
  upgrading other projects.
- Fresh and existing projects use the same idempotent canonical version 8 path.
  Schema-less measurement projects remain version 0.
- Restore reapplies the canonical project upgrade after `pg_restore`. A version
  7 archive therefore returns with operator-owned managed schemas and version 8
  recorded in `_control`.
- The operations guide contains the tested, per-project version 7 ownership
  rollback. No new role, service, feature, or dependency was added.

## Customer DDL boundary

The regression below runs every statement once through the `pibase sql` backend
and once through `migration-apply`; every attempt returns PostgreSQL
`permission denied` or `must be owner`:

```sh
PIBASE_TEST_ADMIN_DATABASE_URL="$DISPOSABLE_POSTGRES_URL" \
  go test -count=1 -run TestManagedSchemaOwnershipBlocksAdminDDL \
  -v ./internal/control
```

| Statement | `sql` | `migration-apply` |
|---|---|---|
| `DROP TABLE storage.objects` | denied | denied |
| `DROP TABLE storage.buckets` | denied | denied |
| `DROP TABLE auth.users` | denied | denied |
| `DROP TABLE cron.webhook_outbox` | denied | denied |
| `CREATE TABLE storage.x(id integer)` | denied | denied |
| `CREATE FUNCTION auth.x() ...` | denied | denied |
| `ALTER TABLE auth.users ADD COLUMN ...` | denied | denied |
| `DROP SCHEMA cron CASCADE` | denied | denied |
| `ALTER SCHEMA storage RENAME TO ...` | denied | denied |
| `DROP FUNCTION realtime.notify_change()` | denied | denied |

The test also proves all targets remain present, the admin has no `CREATE` on
any managed schema, and the project database remains admin-owned.

## `SECURITY DEFINER` inventory

This query and the DDL-boundary regression enumerate every security-definer
function in the four schemas and fail if an unexpected function, owner,
`search_path`, or `PUBLIC` execution grant appears:

```sql
SELECT n.nspname, p.oid::regprocedure, r.rolname AS owner,
       p.proconfig, p.proacl
FROM pg_proc p
JOIN pg_namespace n ON n.oid=p.pronamespace
JOIN pg_roles r ON r.oid=p.proowner
WHERE n.nspname=ANY(ARRAY['auth','storage','realtime','cron'])
  AND p.prosecdef
ORDER BY 1,2;
```

| Function | Owner | Fixed `search_path` | May execute | Why customer invocation is safe |
|---|---|---|---|---|
| `auth.uid()` | operator | `pg_catalog, auth` | anon, authenticated, service, admin, operator | It only parses the request-local JWT `sub` setting and returns a UUID. |
| `realtime.notify_change()` | operator | `pg_catalog` | operator only; invoked by triggers | It emits only the table, operation, and row id to the fixed notification channel. |
| `realtime.enable_table(regclass)` | operator | `pg_catalog` | admin, operator | It accepts only a catalog-resolved table in `public` and quotes both identifiers before managing its fixed trigger. |
| `cron.enforce_outbox_limit()` | operator | `pg_catalog, cron` | operator only; invoked by the managed trigger | It only serializes and enforces the fixed 256-row pending-outbox cap. |
| `cron.enqueue_webhook()` | operator | `pg_catalog, cron` | admin, operator; invoked by customer triggers | It validates the method and only inserts a bounded event into the managed outbox; delivery still applies the SSRF guard. |

`PUBLIC` has no execution grant on any of these functions.

## Version 7 upgrade, skip, and rollback

For a synthetic version 7 project, the following was run before and after
`pibase bootstrap`:

```sh
psql "$PROJECT_DATABASE_URL" \
  -c '\dn+ auth' -c '\dn+ storage' -c '\dn+ realtime' -c '\dn+ cron' \
  -c '\dp auth.*' -c '\dp storage.*' -c '\dp realtime.*' -c '\dp cron.*'
```

The four owners changed from the project admin to the operator; admin schema
rights changed from owner `UC` to explicit `U`; all table and sequence ACLs
were unchanged. Full command/output evidence is in
`reports/raw/m6c-v7-ownership.txt`.

`TestManagedSchemaUpgradeSkipsCustomerObjectsAndRollback` also creates
`storage.customer_keep` as the admin and `auth.customer_role_keep()` as another
customer role in a version 7 project. Bootstrap names both objects, leaves them
and that project's ownership untouched, and upgrades a second project to
version 8. It then executes the documented rollback, verifies all four schemas
return to the admin and catalog version 7, and successfully reapplies version 8.

The rollback commands are documented in `docs/PI-OPERATIONS.md`. They alter
only the four schema owners and the `_control` version/error fields; database
and managed-object ownership do not change.

## Compatibility verification

Fresh version 8 projects passed the Node 22 `supabase-js` suite for REST/RLS,
sign-up, sign-in, refresh, MFA, storage upload/download, plus the realtime JS
broadcast/presence suite. Upgraded projects passed the Go integration suites
for those same surfaces and for database-change realtime, a customer-enabled
table, cron SQL, a webhook trigger/outbox, and SSRF enforcement.

Branch integration passed with PostGIS and pgvector enabled and verified its
spatial/vector rows in the target. `TestRestorePreM6cBackupReappliesManagedOwnership`
took a version 7 ownership backup containing PostGIS, pgvector, public data and
an object file, upgraded the project, restored the archive, and verified data,
files, extensions, the extension guard, version 8, and all four operator owners.

## Tests run

- `PIBASE_IMAGE=pibase:m6c-final ./scripts/test.sh`: passed on the Linux arm64
  engineering host. This includes setup checks, `gofmt`, `go vet`, all unit
  tests, a production image build, fresh PostgreSQL 16 control/gateway
  integration, and the Linux arm64 binary build.
- `PIBASE_IMAGE=pibase:m6c-final ./scripts/test-supabase-js.sh`: passed both the
  M6a supabase-js suite and `tests/realtime-js.mjs` against its disposable
  PostgreSQL/gateway stack.
- `go test -p 1 -count=1 ./internal/backup ./internal/control ./internal/gateway`
  with `PIBASE_TEST_ADMIN_DATABASE_URL` pointing at disposable PostgreSQL 16:
  passed, including the version 7 restore, ownership, failure-isolation,
  rollback, extension branch, and upgraded-feature regressions.
- `git diff --check`: passed.

## Measurements

No M6c RAM, image-size, latency, or throughput numbers were measured or
estimated. M6c adds catalog checks and transactional DDL only during bootstrap
or restore; it adds no resident process or dependency. Prior M6b image and RSS
measurements remain labeled in `reports/M6b.md`.

## What was not tested

- Upgrade or rollback of a retained real customer data directory; version 7
  projects and archives were constructed in disposable PostgreSQL 16.
- A retained `.pib` file produced by the old M6b binary; the restore test uses
  the unchanged archive format with a synthetic version 7 ownership state.
- Skip detection regressions for every PostgreSQL object kind; the catalog
  query is generic, while live regressions cover a table and a function owned
  by two different customer roles.
- Successful delivery to an external public webhook endpoint; cron SQL,
  trigger enqueue, outbox processing, retry limits, and private-address SSRF
  rejection were tested.
- Fresh Raspberry Pi OS installation/reboot persistence, PSS, or new resource
  measurements.
- Independent M6c audit.

## Audit fixes

Four issues were found during the M6c audit and fixed in this commit:

**M6c-01 — database-local ownership scan.** `customerManagedObjects` queried
`pg_shdepend` without filtering on the current database, so objects owned by
non-operator roles in *other* project databases could incorrectly block the
upgrade. Fixed by adding `AND dependency.dbid=(SELECT oid FROM pg_database
WHERE datname=current_database())`. Regression:
`TestManagedSchemaUpgradeSkipsCustomerObjectsAndRollback` now creates the
healthy project first, verifying that a second project's objects do not cause a
false-positive skip.

**M6c-02 — restore completion and warning on schema failure.** `Restore`
previously returned early if `UpgradeProjectSchema` failed after `pg_restore`,
leaving the storage file swap incomplete: the project would have a new database
but old files. Fixed so that `Restore` always finishes the file swap and logs a
warning if the schema upgrade fails (the error is also recorded as
`schema_error` in `_control`). Regression:
`TestRestorePreM6cBackupWithCustomerObjectFinishesFileSwap` restores a
pre-M6c archive whose `storage.customer_keep` table blocks the upgrade, and
asserts the file swap completed, the customer table survived, and the error
was recorded.

**M6c-03 — CREATE grant cleanup.** The upgrade SQL revoked `CREATE` on managed
schemas only from the admin role. Stray `CREATE` grants on those schemas held
by `PUBLIC`, `anon`, `authenticated`, or `service` survived. Fixed: the
`REVOKE CREATE` now names `PUBLIC` and all four non-operator project roles, and
`GRANT USAGE` is scoped per schema (only `auth` and `storage` to anon,
authenticated, service, admin; `realtime` and `cron` to admin only).
Regression: the existing test injects `GRANT CREATE ON SCHEMA auth TO PUBLIC`,
plus per-role stray grants, before re-upgrading and calls
`assertOnlyOperatorCanCreateManagedSchemas` to verify no non-operator role
retains `CREATE`.

**M6c-04 — transactional preflight.** `ensureNoCustomerManagedObjects` ran
outside the upgrade transaction, creating a TOCTOU window: a customer object
could appear between the check and the DDL. Fixed by moving the check inside
the `BEGIN`/`COMMIT` block and accepting `pgx.Tx` instead of `*pgx.Conn`,
so the catalog snapshot used by the check is consistent with the DDL that
follows.
