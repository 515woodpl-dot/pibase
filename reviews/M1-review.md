# M1 review

**Commit reviewed:** `b7379e7e05f46aba33e182338a0252c29a0d88f2` on branch `pibase/m0`
**Environment:** Linux amd64, Go 1.24.7, PostgreSQL 16.13 native cluster (no Docker), Node 22.
**Verdict:** **APPROVE WITH FIXES**

The data-API security posture is genuinely strong: identifiers come only from the catalog and are quoted with `pgx.Identifier.Sanitize()`, values are always bound parameters, and every request runs in a transaction under `SET LOCAL ROLE` so PostgreSQL — not app code — enforces RLS. I could not break the REST/RPC injection surface. The main gaps are two missing contract operators, a non-conformant error `code`, a cross-pool connection-accumulation risk, and the owner-SQL escalation mechanism.

## What I ran (live, against a running gateway with real projects A=`26ed9c49`, B=`ab2ff88a`)

All tagged VERIFIED unless noted.

**Injection (all rejected; table intact afterward):**
| Attempt | Result |
|---|---|
| `select=title,nonexistent_col` | 400 `unknown column "nonexistent_col"` |
| `select=title,"secret"` (quoted ident) | 400 `unknown column "\"secret\""` |
| `order=title;drop table todos` | 400 `unknown column "title;drop table todos"` |
| `order=(select 1)` | 400 `unknown column "(select 1)"` |
| `evilcol=eq.1` (fake filter column) | 400 `unknown column "evilcol"` |
| `title=eq.x'||(SELECT secret…)||'` (value) | 200 `[]` — value bound as a **parameter**, no execution |
| `title=in.(x,y');DROP TABLE todos;--)` | 200 `[]` — items bound as parameters |
| `POST ?on_conflict=title)--` | 400 `unknown column "title)--"` |
| relation `todos;drop table todos` | 404 relation not found |
| `GET /rest/v1/auth.users` / `/rest/v1/users` (anon) | 400 (dot blocked / not accessible) |

`todos` still returned its two rows after all of the above. **SQL injection: not exploitable.** READ confirms `internal/gateway/rest.go`, `catalog.go`, `rpc.go`: identifiers validated via `pg_catalog` gated by `has_table_privilege`/`has_function_privilege`, cast types drawn from the catalog, embedding restricted to `public`.

**JWT / auth context:**
| Attempt | Result |
|---|---|
| `alg:none` token, role=service_role | 401 invalid JWT |
| HS256 signed with wrong secret | 401 |
| expired token | 401 |
| A's real service JWT presented to project B | 401 (B's secret rejects it) |
| Host-header tricks (port, UPPER, trailing dot, unknown) with A's key | 200, **A's data only** — project is bound to the API key, not Host |
| service key present in an error body? | 0 occurrences |

`internal/gateway/database.go`: `SET LOCAL ROLE <who.role>` where `who.role` is only ever `anon/authenticated/service` (from the catalog; any other JWT role → 403), and `set_config('request.jwt.claims',$1,true)` + `statement_timeout=5s` are parameterised. **User-controlled text cannot reach a raw `SET ROLE` on the REST path.** VERIFIED (read + live).

**RLS, limits, migrations:**
- Anon sees only `done=true` rows; service bypasses RLS and reads `secret`. VERIFIED live + `TestRESTWithRLS` passes.
- `limit=100000` → 400 (1000-row cap). Oversized body (>1 MiB) rejected. VERIFIED.
- Migrations: re-apply same name+content → no-op; same name/different content → checksum error; a migration whose 2nd statement fails → **fully rolled back** (`partial_a` absent, not recorded in `pibase_migrations`). Advisory-lock serialised. VERIFIED live.
- `@supabase/supabase-js` acceptance (`tests/supabase-js.mjs`) is a **real** client test (no DB mocks): insert/select/filter/count/order/embed/update/upsert/rpc/delete with assertions. READ + the `gateway` integration suite passes.

## Findings

**M1-01 (High — missing contract operators).** `or` and `not` are **not** implemented. The playbook's M1 operator list requires `… is not or`. The report declares them out-of-scope and the README omits them, so this is honest, but it is still two missing contract items and a real Supabase-compatibility gap (any app using `.or(...)` / `.not(...)` breaks). Evidence: `internal/gateway/rest.go` `filterExpression` switch has no `or`/`not`; a query param named `or` is treated as a column and 400s.

**M1-02 (Medium — errors are shape-only PostgREST).** `writeAPIError` always emits `{"code":"PIBASE","details":null,"hint":null,"message":…}`. Real PostgREST returns the PG `SQLSTATE` in `code` and populates `details`/`hint`. The keys match but `code` is a constant and `details`/`hint` are always null, even when the underlying `pgconn.PgError` carries them — clients that branch on `code` will misbehave. Separately, raw `pgErr.Message` is returned to clients (leaks table/constraint internals; low-value info disclosure). `internal/gateway/rest.go:557-582`.

**M1-03 (Medium — cross-pool connection accumulation).** The global semaphore (`databaseSlots`, size 32, `server.go:77`) bounds *concurrent* transactions, but each per-project `pgxpool` independently keeps up to `MaxConns=2` connections for `MaxConnIdleTime=30s` (`database.go:24-27`). A workload touching many *distinct* projects within a 30 s window can drive total cluster connections past `max_connections=40`, yielding `FATAL: too many connections` that starves other projects. The report notes the limiter is per-process but not this accumulation. (Reasoned from code; not reproduced at full scale here.) Fix: cap total open connections globally, or shorten idle time / evict pools under pressure.

**M1-04 (Medium — owner-SQL escalation mechanism; operator-only today).** `adminsql.Run`/`ApplyMigration` connect as the **superuser** admin URL and then `SET LOCAL ROLE <admin>`. Because the session login role is a superuser, a submitted `RESET ROLE` or `SET ROLE postgres` re-escalates to superuser — so the stated property "runs as the project admin role on that project's DB only" (PLAN §6.2) is **not enforced by the mechanism**. In M1 this path is operator-only (CLI, `PIBASE_ADMIN_DATABASE_URL`, not on the HTTP surface), so it is not customer-exploitable now, but it is a latent **Critical** for the M4 browser SQL editor. Also `Run` applies no row cap (playbook asks for one). Fix before M4: connect *as* the admin role (a non-superuser) rather than superuser-then-`SET ROLE`.

**M1-05 (Low).** Rate limiting and usage counting are wired only into REST/RPC (`enforceRate`/`recordRequest` call sites), not auth/storage — so M1 usage counters miss non-REST traffic (see M2/M3).

**Coverage note.** The acceptance test exercises anon-vs-service and anon RLS via a `visible` flag, but not two *authenticated* end-users checking cross-user row isolation. I verified owner-scoped isolation through the storage RLS path and by code; recommend adding an explicit two-user data-API isolation test.

## Claims in the engineer's report I could not confirm
- The `bin/pibase-linux-arm64` SHA-256 (artifact not committed; my build differs, as expected).
- "1,000-row cap / 1 MiB body cap / 5 s timeout" for the *timeout* specifically: cap and row-limit VERIFIED live; the 5 s statement timeout is READ (set via `set_config`) — I did not force a slow query (anon lacks `EXECUTE` on `pg_sleep`).

## Scope creep
None beyond X-01 (milestone bundling). The declared out-of-scope list (full-text, json-path, nested embedding, multi-column FK embed, aliases, computed columns) is returned as clean 400s, which is acceptable per PLAN "prefer a clear 400".

## Fix list for Codex (priority order)
1. Implement `or` and `not` (contract). (M1-01)
2. Put the real PG `SQLSTATE` in `code` and populate `details`/`hint`; stop returning raw PG messages verbatim. (M1-02)
3. Bound total cluster connections across per-project pools. (M1-03)
4. Make owner SQL connect as the admin role (non-superuser) and add a row cap — required before any M4 customer SQL editor. (M1-04)
5. Extend rate-limit/usage-count to all surfaces; add a two-authenticated-user RLS test. (M1-05)

## Questions for the planner
- Are `or`/`not` in the M1 contract or deferred? The playbook lists them; PLAN §4 is less specific.
