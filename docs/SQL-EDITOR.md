# DATBase SQL (the SQL editor)

Each project has an **SQL** tab: read-only queries against the project's own data, saved queries in
folders, and the last 20 runs. Owner-only, like every other project tab. Customer-facing text calls it
"DATBase SQL"; "SQLite-style" appears only in the editor's help popover.

## Decision (Step 0): where queries run

**Chosen: a control-only endpoint inside each project's engine** (`POST /api/datbase/sql` in
`pocketbase/pb_hooks/datbase.pb.js`, logic in `pocketbase/pb_hooks/datbase_sql.js`).
Rejected: opening the project's database file from the dashboard, which needs the container's
file path and a second SQLite connection from outside it. That adds moving parts without adding
safety, and the engine's own `EXPLAIN` and `query_only` are only available inside.

> **Correction (2026-09-29).** The first version (commit `eed1978`) was shipped without ever being
> executed, and several claims made about it were wrong: every successful query crashed the engine
> handler (`new DynamicModel({})` panics in PocketBase 0.39), the internal-table filter could be
> bypassed (`FROM "main"."_superusers"`, and also `FROM '_superusers'`, since SQLite accepts a
> single-quoted string as a table name), `password` could be read under another name, `query_only` was
> set on one pooled connection while the query might run on another, Cancel didn't stop anything, and
> the CSP claim was untested. Codex's review found the bypass and five other defects; the rest turned up
> while fixing them. Everything below describes the rewritten version, which is covered by the e2e and
> unit suites listed at the end.

## How a query is kept read-only and away from internal data

Each layer below would stop its attack on its own; a leak needs every one of them to fail.

1. **Who can call it.** The route `/dashboard/<id>/sql/run` loads the project only for its signed-in
   owner (404 otherwise), refuses cross-site and non-JSON posts (403), and needs the project to be
   active and not mid-restore or mid-update (409 with a plain message). The engine endpoint sits
   under `/api/datbase/*`, so without the per-project control header it is **404 for server keys,
   superuser tokens, app users and anonymous callers**.
2. **A real tokenizer** (`check()` in `datbase_sql.js`), not regexes. It accepts one statement starting
   with `SELECT`, `WITH`, `VALUES` or `EXPLAIN [QUERY PLAN]`. Every identifier is checked in each
   spelling SQLite accepts: bare, `"x"`, `` `x` ``, `[x]`, and `'x'` where a name fits. Names starting
   with `_`, `sqlite_` or `pragma_` are rejected, as are `dbstat`, `load_extension` and
   `fts3_tokenizer`. Also rejected: schema prefixes (`main.`, `temp.`), a `'string'` used as a
   function name, parameters, unbalanced parentheses, and `[[ ]]`/`{{ }}`/`{:…}`, which the engine's
   query builder would rewrite even inside quotes. `sqlite_dbpage` (raw database pages) is compiled
   into this engine, which is why this layer checks every spelling.
3. **Subquery wrapper.** What runs is `SELECT … FROM (<your query>) LIMIT 1001`. A subquery can only be
   a `SELECT`, so data-changing statements are a syntax error there, and the 1,000-row cap holds even
   against the query's own `LIMIT`.
4. **Shadow tables for hidden fields.** Every table with hidden fields (all auth tables: `password`,
   `tokenKey`; data tables' hidden fields too) is replaced by a CTE of the same name that lists only
   its visible columns. SQLite resolves `users`, `"USERS"` and `'users'` to the CTE, so
   `SELECT password FROM users` is "no such column", and `SELECT * FROM users` shows email etc. without
   hashes.
5. **Engine-level access check.** Before running, the handler `EXPLAIN`s the exact statement and looks
   up the root page of every table/index it opens in `sqlite_schema`. Anything internal, anything in
   another schema, an unknown root page, or any write cursor → refused. This check doesn't depend on
   how the query was spelled.
6. **Engine-level read-only.** One pooled read connection is pinned by a transaction:
   `PRAGMA query_only = ON` (blocks even `TEMP` writes), the query, `query_only = OFF`, then an
   unconditional `ROLLBACK`. The e2e checks that normal writes still work afterwards.
7. **Time and cancel.** The dashboard aborts the engine call at 10 s, or when the browser aborts
   (Cancel, closing the tab). The engine query runs with the request's context, so an abort really
   interrupts it; the engine logs `[datbase-sql] query stopped after N ms` (no query text).
   Measured: the timeout at 10.02 s, Cancel stops the engine within ~150 ms.

Runs go through a **route handler, not a server action**: Next.js runs server actions one at a time
per tab (`node_modules/next/dist/docs/…/backend-for-frontend.md`, "Sequential dispatch"), so a
cancel action would wait behind the query it should stop.

Limits: 1,000 rows (long text cut at 10,000 characters, about 5 MB per response), 10 s,
**30 runs per minute per project** (`SQL_RUNS_PER_MINUTE` overrides it; tests use it), 100 KB of SQL.
SQL runs are not counted as customer API requests. Binary values show as `(binary, N bytes)`.

## Saved queries (snippets) and folders

Stored in the control Postgres (`app/drizzle/0006_sql_editor.sql`, `0007_sql_editor_fixes.sql`):

- `sql_folder`: nested up to 5 levels; names unique per parent, ignoring case, top level included.
  `parent_id` cascades, so deleting a folder removes its subfolders and their queries (the delete
  confirmation says how many).
- `sql_snippet`: name, SQL text (≤ 100 KB), favorite.
- `sql_run`: the last 20 runs per project (text, time, row count or error; never result rows).
- `sql_editor_state`: one row per project, written in the same transaction as the five starter
  queries ("Getting started"), so they appear exactly once, even with two tabs, and don't come back
  after you delete them. The starters use the `users` table, which every project has, so they run as
  they are.

UI: **+** (New query / New folder), **right-click** on a query (Open, Rename, Duplicate, Add to
favorites, Move to, Delete), on a folder (New query here, New subfolder, Rename, Move to, Delete) or on
empty space (New query / New folder). The **⋯** button opens the same menu (touch screens have no
right-click), double-click renames, Escape closes. The Move list never offers a folder's own
subfolders, and the server refuses a cycle anyway. The toolbar has Run (Ctrl/⌘+Enter; with text
selected, runs only the selection), Cancel, Save (for the open query; a dot marks unsaved changes)
and Save as. Results can be downloaded as CSV.

Every action loads the project through `actionProject` and touches only folders and queries with that
`project_id`, so another project's ids behave like ids that don't exist. The e2e rewrites real
requests from a second account to prove this, and proves each rewrite actually took effect.

**Data export** (Download data) adds `sql-snippets/<folder>/<subfolder>/<name>.sql`. Paths are built
from the parent links (not creation order), segments are sanitized (no `..`), and duplicate names
become `name (2).sql`.

## Files

| File | Role |
|---|---|
| `pocketbase/pb_hooks/datbase_sql.js` | Tokenizer/checker, wrappers, shadow CTEs, access check (pure JS, unit-tested in Node) |
| `pocketbase/pb_hooks/datbase.pb.js` | `POST /api/datbase/sql`: pinned read-only transaction, EXPLAIN check, interruption |
| `app/src/lib/sql.ts` | Ready check, rate limit, 10 s deadline, history |
| `app/src/app/dashboard/[id]/sql/run/route.ts` | Owner-only, same-site JSON POST → `runProjectSql` |
| `app/src/app/dashboard/[id]/sql/{page,ui,actions,folders}.tsx/ts` | Page (status notice, CSP nonce for CodeMirror), UI, snippet/folder actions, pure tree rules |
| `app/src/lib/export.ts` | Snippets in the data export |
| `app/tests/unit/sql-guard.test.mjs`, `sql-folders.test.mjs` | 17 unit tests |
| `app/tests/e2e/sql-editor.mjs` | 21 checks; needs `PB_PORTS="8090 8091"` (set in `run-all.sh`) |

**Rollout:** existing projects need **Update all projects** (Server status) to get the new hook
files. Until then (an engine with no SQL endpoint, or the first broken version), the editor says the project needs an update.

## Not done / known limits

- No writes, DDL or multi-statement scripts; this is v1 read-only by design.
- `EXPLAIN` (bytecode) shows `addr, opcode, p1, p2, p3, p5` (no `p4`/comment: the engine API can't
  read those nullable columns).
- Rate limit and cancel live in the one dashboard process (like `busy.ts`); several dashboard
  instances would each count separately.
- The editor's autocomplete uses the table list loaded with the page; reload after adding tables.
- Not yet tried on the Raspberry Pi or on a real deployment behind Caddy/nginx. The cross-site
  check relies on browsers sending `Sec-Fetch-Site` plus the JSON-only rule.
