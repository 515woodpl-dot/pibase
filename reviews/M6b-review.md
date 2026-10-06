# M6b review

Commit reviewed: `48bf192` on `pibase/m6b` ("feat: complete M6b extensions and isolated upgrade gates")
Reviewer: Claude (planner/auditor), 2026-10-03
Environment: Linux x86_64 cloud sandbox, Go 1.24.7, PostgreSQL 16.13 (Ubuntu) with PostGIS 3.4.2 and
pgvector 0.6.0 from Ubuntu packages, Node 22. **Not** the project's Docker images and **not** a Pi.

## Verdict: BLOCK

Three High findings. None leaks data across projects, but one lets a single customer stop the
whole stack from starting, one silently corrupts branch copies, and one means a contract line
("reject customer extension DDL") is not actually enforced.

Tags: VERIFIED = I ran it. READ = code reading only. UNVERIFIED = could not check here.

## What I ran

| Check | Result | Tag |
|---|---|---|
| `go build ./...`, `go vet ./...`, `go test ./...` | pass | VERIFIED |
| `go test -p 1 -count=1 ./internal/control ./internal/gateway` against real Postgres 16 + PostGIS + pgvector (`PIBASE_TEST_ADMIN_DATABASE_URL`), after `npm ci` | pass | VERIFIED |
| `GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build ./cmd/pibase` | pass | VERIFIED |
| `bootstrap`, `project-create`, `postgis-enable`, `pgvector-enable`, `project-branch`, `sql`, `migration-apply` by hand | work as described on the happy path | VERIFIED |
| Branch keeps `geometry(Point,4326)` and `vector(3)` data and re-enables both extensions | pass | VERIFIED |
| `postgis` / `vector` cannot be created by the customer role | "permission denied" from Postgres (with Ubuntu's pgvector 0.6.0) | VERIFIED |
| Failed branch leaves no orphan database or catalog row | pass | VERIFIED |
| `scripts/test.sh`, `scripts/test-supabase-js.sh`, `Dockerfile.postgres` image build | not run: Docker image pulls are not available in my sandbox | UNVERIFIED |
| Upgrade from a real M5b/M6a data directory | only the engineer's integration test, no real old volume | UNVERIFIED |
| Anything on a Raspberry Pi | not run | UNVERIFIED |

Go modules were fetched through GitHub mirrors of `golang.org/x/*` with a temporary `-modfile`;
the repo's `go.mod`/`go.sum` were not changed.

## Findings

### M6b-01 (High): one customer can make `bootstrap` fail for every project  — VERIFIED

Bootstrap now runs `managedProjectUpgradeSQL` in every project and returns on the first error.
`compose.yaml` starts the gateway only when bootstrap `service_completed_successfully`. The
customer's admin role owns the `auth`, `storage`, `realtime` and `cron` schemas, and a schema
owner may drop tables in it even when the operator owns them.

Reproduction (project `b21d3923`, customer SQL through `pibase sql`):

```
DROP TABLE storage.objects;
DROP TABLE storage.buckets;
CREATE TABLE storage.buckets(id text primary key);
```

Then `pibase bootstrap`:

```
ERROR pibase stopped error="upgrade project p_b21d3923 schema: ERROR: column \"public\" does not exist (SQLSTATE 42703)"
exit 1
```

Before the three statements, bootstrap exited 0. Expected: one project's broken schema is
reported and skipped; other projects are upgraded and the gateway starts. Actual: bootstrap
aborts; projects later in id order are not upgraded; `docker compose up` (the update
procedure) will not start the gateway (READ for the compose part).

Fix: (a) per-project failures must not abort the loop: record the failure (for example a
`schema_error` column or version left unchanged), continue, print a summary, and let the gateway
start; (b) stop the cause: managed schemas (`auth`, `storage`, `realtime`, `cron`) should be
owned by the operator, with the admin role given only the specific rights it needs, so customer
SQL cannot drop or replace gateway tables. If (b) is too large for this gate, do (a) now and
log (b) as a tracked follow-up. Add a regression test using the three statements above.

### M6b-02 (High): branching silently drops customer content equal to `CREATE SCHEMA public;`  — VERIFIED

`copyPublicDump` removes every dump line whose trimmed text is `CREATE SCHEMA public;`. The
comment says customer DDL and COPY data "pass through byte-for-byte"; that is false, because the
filter does not know whether a line is pg_dump's own statement, a COPY data row, or part of a
function body.

Reproduction on the source project:

```
CREATE TABLE public.notes(body text);
INSERT INTO public.notes VALUES ('first'),('CREATE SCHEMA public;'),('last');
CREATE FUNCTION public.snippet() RETURNS text LANGUAGE sql AS 'SELECT 1::text ||
$x$
CREATE SCHEMA public;
$x$';
```

`pibase project-branch` exits 0. In the branch, `notes` has 2 rows (`first`, `last`) and
`snippet()` returns `1\n` instead of `1\nCREATE SCHEMA public;\n`. No warning.

Fix: do not text-filter the dump. Use `pg_dump -Fc` plus `pg_restore` with a TOC list (`-l` /
`-L`) that omits the `SCHEMA public` entry, or drop exactly the first matching statement before
any `COPY` block and assert it occurs once. Add the reproduction as a test.

### M6b-03 (High): "reject customer extension DDL" is not enforced  — VERIFIED

`validateNoExtensionDDL` is a text filter. The database does not back it up, because the admin
role owns the project database and can therefore create any *trusted* extension.

- Owner SQL: `DO $$ BEGIN EXECUTE 'CREATE EXTENSION hstore'; END $$` → `"command": "DO"`,
  and `pg_extension` then shows `hstore` owned by `p_b21d3923_admin`.
- Migration (`migration-apply`, multi-statement): `SELECT E'\''; CREATE EXTENSION citext; --'`
  is accepted (the tokenizer treats `\'` + `'` as a doubled quote and swallows the rest) and
  `citext` is installed.
- `DO $$ BEGIN EXECUTE 'DROP EXTENSION citext'; END $$` also works on extensions the customer
  created.

Consequences: PLAN.md M6b line "reject customer extension DDL" is not met, and a project that
uses such an extension can no longer be branched:

```
ERROR pibase stopped error="psql branch target: exit status 3: ERROR:  type \"public.hstore\" does not exist"
```

`postgis` and `vector` were refused by Postgres in my environment. UNVERIFIED for the shipped
image: PGDG's `postgresql-16-pgvector` may mark `vector` as trusted, in which case customers can
enable pgvector themselves. Check with
`grep trusted /usr/share/postgresql/16/extension/vector.control` inside `pibase-postgres:16`.

Fix: enforce in the database, keep the text filter only for a friendly message. Simplest: an
operator-owned event trigger on `ddl_command_start` for `CREATE EXTENSION`, `ALTER EXTENSION`
and `DROP EXTENSION` that raises unless the session user is the operator. Tests must include
the `DO` and `E'\''` forms. Decide and document what branching does with any extension outside
`postgis`/`vector`.

### M6b-04 (Low): the supabase-js realtime acceptance is silently skipped in both documented gates  — READ

`runSupabaseRealtimeAcceptance` logs and returns when `node` is missing. The `test` image
(`postgres:16-bookworm` + Go) has no Node, so `scripts/test.sh` never runs
`tests/realtime-js.mjs`; `scripts/test-supabase-js.sh` runs only `tests/supabase-js.mjs`, which
has no realtime calls. It ran and passed for me only because my sandbox has Node on PATH. This
predates M6b (the old `golang` image had no Node either), but the M6b report lists these gates
as passing. Fix: run `tests/realtime-js.mjs` in `test-supabase-js.sh`, and use `t.Skip` so the
skip is visible.

### M6b-05 (Low): no measurements for the new Postgres image  — READ

PLAN.md calls PostGIS "optional and heavy" and requires numbers to be labeled measured or
estimated. `reports/M6b.md` gives no image size, no Postgres RSS with PostGIS loaded, and no
arm64 build of `Dockerfile.postgres`. Add them, or state plainly that they are not measured.

## Claims in the engineer's report I could not confirm

- `PIBASE_IMAGE=pibase:m6b-final ./scripts/test.sh` and `./scripts/test-supabase-js.sh` passing
  on disposable containers (Docker not usable here). Reading the scripts, they no longer touch
  the live compose stack, which matches the claim (READ).
- "Upgrade regression ... idempotent recreation": the integration test passes, and bootstrap ran
  twice cleanly for me, but see M6b-01 for the case it does not survive.
- Production image build and Linux arm64 image build of the Postgres image.

## Scope notes

- PLAN.md was edited in this commit to add the M6b contract. That is fine if the owner asked
  for it; contract changes should be called out in the gate report, not only in the diff.
- No new services or frameworks. `Dockerfile.postgres` and the `test` image stage are within
  the M6b contract.
- Root cause shared by M6b-01 and M6b-03: the customer admin role owns the project database and
  the managed schemas. That design predates M6b; this milestone is where it starts to affect
  other projects.

## Fix list for Codex (in order)

1. M6b-01: bootstrap continues past a failing project; add the regression test; plan operator
   ownership of managed schemas.
2. M6b-02: replace the line filter in `copyPublicDump`; add the regression test.
3. M6b-03: database-level block on extension DDL; tests for the `DO` and escape-string forms;
   check whether `vector` is trusted in the shipped image.
4. M6b-04: make the realtime JS acceptance run in a documented gate.
5. M6b-05: add measured image size and RSS, labeled.

Re-review required after 1 to 3.
