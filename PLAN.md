# PLAN.md: a Supabase-shaped BaaS for Raspberry Pi 4

Working name: `pibase`. Roles: **Codex is the engineer. Claude is the planner and auditor.** Codex builds one milestone at a time and stops at each gate. The auditor reviews the gate report and either approves or lists fixes.

## 0. Principles

1. **Postgres is the product.** Roles, RLS, extensions and SQL are real Postgres, not a re-implementation.
2. **One Postgres cluster, one database per project, one Go gateway process.** No container per project.
3. **Speak the Supabase wire protocol, subset only.**
   - The gateway understands the PostgREST query syntax and the GoTrue and Storage endpoints that `supabase-js` uses.
   - An AI can then write normal Supabase code without a custom guide file, and existing Supabase apps can move over by changing the URL and key.
4. **Don't overengineer.** No Kubernetes, no message bus, no Node in production, no microservices. If a feature isn't in a milestone, it doesn't exist.
5. **Every RAM number is labeled measured or estimated.** Nothing is claimed ready until it has been run on a real Pi 4.
6. **Standing owner rules:**
   - No `sudo` beyond one setup command.
   - Never `git push` from the Pi.
   - Scripts install their own dependencies.
   - Customer-visible text never names internals beyond "Postgres".

## 1. Architecture

```
                       Internet
                          |
                 +--------v---------+
                 |  Caddy (TLS)     |   *.domain  ->  gateway :8080
                 +--------+---------+   (~30 MB est.)
                          |
+-------------------------v--------------------------------------------+
|  GATEWAY  (one Go binary, ~40-80 MB est.)                            |
|                                                                      |
|  router: Host -> project ; apikey header -> role (anon/service)      |
|  +-----------+ +---------+ +----------+ +---------+ +-------------+  |
|  | /rest/v1  | |/auth/v1 | |/storage  | |/realtime| | /admin API  |  |
|  | PostgREST | | GoTrue  | |  /v1     | | WS      | | + SQL edit  |  |
|  | subset    | | subset  | | buckets  | | (M5)    | | + static UI |  |
|  +-----+-----+ +----+----+ +----+-----+ +----+----+ +------+------+  |
|        |            |           |            |             |         |
|  limits + counting + JWT verify + request-scoped transaction         |
|  scheduler (cron/webhooks) + backup worker + metrics                 |
+--------+------------+-----------+------------+-------------+---------+
         |  pgx pools, few connections per project, closed when idle
+--------v-----------------------------------------------------------+
|  ONE PostgreSQL 16 cluster on SSD  (~300-500 MB est.)               |
|                                                                     |
|  db: _control   projects, owners, keys, usage, limits, snippets     |
|  db: p_ab12     auth.* storage.* public.* (customer tables, RLS)    |
|  db: p_cd34     ...one database per project...                      |
|                                                                     |
|  roles (cluster-wide names, per-db grants):                         |
|    authenticator (login, NOINHERIT, not superuser)                  |
|    anon | authenticated | service_role(BYPASSRLS) | p_<id>_admin     |
+---------------------------------------------------------------------+
  /data/files/<project>/<bucket>/...     bytes on disk, metadata in DB
  backups -> S3-compatible (encrypted pg_dump per project; WAL later)
```

**Request path (REST):** the gateway verifies the JWT, opens a transaction on the project's pool, and runs:

```
SET LOCAL ROLE <anon|authenticated|service_role>;
SELECT set_config('request.jwt.claims', <json>, true);
SET LOCAL statement_timeout = ...;
```

It then runs the generated SQL, so the database enforces RLS and no application code can bypass it. Table and column names come only from the catalog and are quoted with `pgx.Identifier`. Values are always parameters.

## 2. Supabase feature map

The milestone map is: M0 foundation and measurements; M1 REST, RLS, SQL and migrations; M2 auth; M3 storage; M4 operations, dashboard, backups and branching; M5 realtime, cron, webhooks and pgvector; M6 optional OAuth, MFA, PITR, transforms, S3 API and PostGIS. GraphQL, phone/SMS, SAML/SSO, Web3 and edge functions are not planned.

## 3. RAM and hardware budget (all estimates until M0 measures them)

| Piece | Estimate |
|---|---|
| OS + Docker or systemd | 300 MB |
| Postgres (`shared_buffers` 128-256 MB, `max_connections` 40, `work_mem` 4 MB) | 400 MB |
| Gateway | 60 MB |
| Caddy | 30 MB |
| **Total** | **~0.8-1 GB**, leaving room for the OS page cache |

- Requires a **USB3 SSD**, not an SD card. Postgres will wear out an SD card.
- Add zram. Assume a 4 GB Pi.
- Idle projects cost disk only (no connection). Active ones cost a few pooled connections at roughly 5-10 MB each. This is the main thing M0 must measure.

## 4. REST subset (M1 contract)

- Read: select, filters, ordering, pagination and exact count.
- Write: insert, update, delete, return representation and upsert.
- Functions, one-level embedding and PostgREST-shaped errors.
- Unsupported syntax returns a clear 400.
- Acceptance uses real `@supabase/supabase-js` and checks CRUD, RPC, auth and RLS isolation.

## 5. Milestones and audit gates

Each milestone ends with a written **GATE REPORT** (`reports/Mx.md`): what was built, tests run, what was **not** tested, measured numbers, known gaps. Codex stops there until the auditor replies.

- **M0, foundation and measurement (Mac, arm64 Docker, then the Pi).**
  - Repo, Go module, `docker compose` with Postgres 16, gateway skeleton, `_control` schema.
  - `create project` creates the database, roles, grants, JWT secret, `anon` and `service` keys.
  - Load test: RSS of Postgres and gateway with 1, 20 and 100 projects, idle and under light load.
  - **Gate:** the numbers exist. If 20 active projects don't fit comfortably in 4 GB, adjust before building further.
- **M1, data API and SQL.** REST subset, RLS presets, service key, migrations, owner SQL editor, limits and counting.
- **M2, auth:** email/password, refresh, confirm, reset, `auth.uid()`, admin user management.
- **M3, storage:** buckets, RLS on objects, signed URLs, quotas.
- **M4, operations:** dashboard, backups/restore, usage, monitoring, Pi setup and branching.
  - **M4-01 follow-up:** `project-branch` must recreate public-schema grants
    and RLS policies from PostgreSQL catalogs, mapping source project roles to
    fresh branch roles structurally. It must not rewrite dump text, and must
    continue excluding `auth`, `storage`, and object files.
- **M5:** realtime, cron, webhooks and pgvector toggle.
- **M6, only on request:** OAuth, MFA, PITR, image transforms, S3 API, PostGIS.

## 6. Security rules (auditor checks every gate)

1. Only the gateway connects as `authenticator`. It is **not** superuser and is `NOINHERIT`.
2. The REST path never executes raw user SQL. Only the owner SQL editor does, and it runs as that project's admin role on that project's DB.
3. `REVOKE CONNECT ON DATABASE ... FROM PUBLIC`. One project's role can't connect to another's DB. Test it.
4. `anon` and `authenticated` can't read the `auth` schema or other projects' data. `service_role` is never sent to browsers.
5. Every request has `statement_timeout`, body-size cap, row cap and per-project rate limit. Database size and file storage quotas are enforced.
6. Extensions installed from the allowlist only, by us.
7. `SECURITY DEFINER` functions written by customers are their responsibility, and the docs must say so.
8. Secrets (JWT keys, backup key) are encrypted at rest and never logged.

## 7. Rules for Codex (anti-overengineering)

- Go, `pgx`, standard library `net/http`. No web framework beyond a small router. No ORM.
- One binary, one Postgres. Add no new service without asking.
- Keep files small and tests real (Postgres in Docker, no mocks for the database).
- Prefer a clear 400 over supporting odd syntax.
- Cross-compile check on every milestone: `GOOS=linux GOARCH=arm64`.

## 8. Decisions already made (don't relitigate)

- **Anon key returns.** Supabase compatibility needs an anon key, and RLS is what makes it safe to expose.
- **Static dashboard, no Node in production.** A future Next.js dashboard would change M4 and cost about 200 MB RAM.
