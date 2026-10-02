# pibase

A small Supabase-shaped backend for Raspberry Pi 4. It runs one PostgreSQL 16
cluster and one Go gateway. M1 provides a PostgREST-compatible data subset,
database-enforced RLS, RPC, migrations, and owner SQL tooling. Auth user
management, storage, operations, and project branching. Realtime belongs to M5.

## Requirements

- Docker with Compose
- A USB3 SSD for persistent Pi deployments; do not use an SD card for PostgreSQL
- No `sudo` is used by these project scripts

## Start

```sh
./scripts/init-env.sh
docker compose up --build -d
curl --fail http://localhost:18080/healthz
```

`scripts/init-env.sh` creates local development secrets in the ignored `.env` file. Do not commit it.

Create a project (the keys are printed once; store them securely):

```sh
./scripts/create-project.sh "My project"
```

The command creates an isolated database and four project-specific roles. It revokes public database access, creates protected `auth` and `storage` schemas, encrypts the JWT signing secret with AES-256-GCM, stores only SHA-256 API-key hashes, and returns distinct anon and service JWTs. The service key must never be shipped to browsers.

Customer-authored `SECURITY DEFINER` functions can bypass intended policies if written incorrectly; their safety is the customer's responsibility.

## Data API (M1)

Use the returned project URL and anon key with `@supabase/supabase-js`, or call
the wire API directly:

```sh
curl 'http://localhost:18080/rest/v1/todos?select=id,title&done=eq.false&order=id.desc&limit=20' \
  -H "apikey: $ANON_KEY" \
  -H 'Prefer: count=exact'
```

M1 supports:

- select lists, `eq`, `neq`, `gt`, `gte`, `lt`, `lte`, `like`, `ilike`, `in`,
  and `is` filters;
- ordering, limit/offset, a 1,000-row cap, and exact counts;
- insert, update, delete, representation returns, and conflict-target upsert;
- named-argument functions at `/rest/v1/rpc/<name>`;
- one-level, single-column foreign-key embedding in both directions;
- anon, authenticated JWT, and service-role request contexts with RLS enforced
  by PostgreSQL.

Every request has a 1 MiB body cap, five-second statement timeout, bounded
database concurrency, a per-project 100 requests/second window, daily request
counting, and a configurable database-size quota. Unsupported PostgREST syntax
returns a clear JSON 400 response.

## Migrations and owner SQL

Migration and arbitrary SQL commands execute through the constrained
`authenticator` database URL, then assume only the selected project's admin
role inside that project's database. A superuser database URL is deliberately
rejected so submitted SQL cannot recover superuser privileges. SQL is read from
standard input so it does not appear in process arguments.

```sh
docker compose run --rm -T \
  gateway migration-apply PROJECT_ID 0001_create_todos < migration.sql

printf 'select current_user, now()' | docker compose run --rm -T \
  gateway sql PROJECT_ID
```

Migration names are idempotent and checksum-protected. Owner SQL has a
30-second statement timeout and is not exposed through the public HTTP API.

## Verify

```sh
./scripts/test.sh
./scripts/measure-m0.sh
./scripts/test-supabase-js.sh
```

The main test script runs static checks, unit tests, real-PostgreSQL security and
data tests, builds the production image, and cross-compiles for Linux ARM64.
The Supabase acceptance script creates a temporary isolated project, exercises
M1 through the real `@supabase/supabase-js` client, and deletes that project.
The M0 measurement harness remains available for reproducing Pi memory data.

## Operations (M4)

`/healthz` is the readiness check, `/metrics` exposes compact Prometheus text
metrics, and `/dashboard` is a static, no-Node operator UI. The dashboard lists
request, database, and file-storage usage and provides the owner SQL editor.
Dashboard API calls require `PIBASE_OPERATOR_TOKEN`; owner SQL executes through
the M1 authenticator boundary, with a 30-second timeout and 1,000-row cap.

The binary also provides encrypted per-project `backup` and guarded `restore`
commands plus `project-branch`. Branches receive fresh roles and keys and copy
only the source `public` schema/data—not users, sessions, or private files.
See [docs/PI-OPERATIONS.md](docs/PI-OPERATIONS.md) for Pi setup, backup-key
handling, restore drills, monitoring, and branching semantics.

## Realtime and delivery (M5a)

`/realtime/v1/websocket` supports the `postgres_changes` channel used by
supabase-js for unfiltered, explicit `INSERT` and `UPDATE` changes on `public`
tables. Wildcard and `DELETE` subscriptions are rejected clearly.
An owner enables a table explicitly through the SQL tool:

```sql
SELECT realtime.enable_table('public.todos'::regclass);
```

The gateway receives only a table, operation, and row id from PostgreSQL, then
fetches the current row in every subscriber's RLS-scoped transaction before it
sends anything. `DELETE` changes are explicitly unsupported: after commit the
row cannot be rechecked without retaining an unsafe stale copy. The connection
cap is 32 per project by default (set `PIBASE_REALTIME_MAX_CONNECTIONS` to an
integer from 1 through 512); idle clients time out after five minutes.

Each project has `cron.jobs` and `cron.webhook_outbox`. M5a accepts
`* * * * *` and `*/N * * * *` schedules (N is 1–1440) for either SQL or an
HTTP webhook. Webhooks are retried with bounded exponential backoff and cannot
target loopback, private, link-local, multicast, or unspecified addresses;
redirects are refused. An owner can enqueue durable event-driven webhooks with
an `AFTER` row trigger, for example

```sql
CREATE TRIGGER todo_webhook AFTER INSERT OR UPDATE OR DELETE ON public.todos
FOR EACH ROW EXECUTE FUNCTION cron.enqueue_webhook('https://hooks.example/todos', 'POST');
```

The trigger stores the event and row JSON in the outbox as part of the same
database transaction; the gateway performs the network delivery later. The
outbox permits at most 256 pending items per project (about 256 MiB at the
1 MiB body maximum), applying backpressure to scheduled, trigger, and direct
owner inserts alike. The
operator can enable pgvector only with:

```sh
docker compose run --rm ops pgvector-enable PROJECT_ID
```

The host PostgreSQL image must have the `vector` extension installed first.

M5b also supports ephemeral `broadcast` and `presence` events on public
Realtime channels. Routing is isolated by project and topic; `self`/`ack`,
presence state, track/untrack, and disconnect cleanup are supported. These
messages live only in one gateway process and disappear on restart. Private
channels, replay, cross-node fanout, HTTP broadcast, and arbitrary binary
payloads are not supported. Presence keys and payloads are client-supplied
metadata, not authenticated identity. Any browser origin may connect because
the socket uses the public project key plus JWT/RLS boundaries; applications
must apply their own trust rules to public broadcast and presence content.

## Stop

```sh
docker compose down
```

To also delete local PostgreSQL data, run the explicit destructive command:

```sh
docker compose down --volumes
```
