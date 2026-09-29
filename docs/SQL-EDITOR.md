# DATBase SQL Editor

## Step 0: Architecture decision

Two approaches were considered for executing SQL against a project's database:

**Option A — Hook endpoint** (`POST /api/datbase/sql` in `datbase.pb.js`)  
**Option B — Open the SQLite file read-only** from the Next.js server, connecting directly to the container's data path.

**Chosen: Option A (hook endpoint).**

Reasons:
- `$app.db().newQuery()` is already used in `datbase.pb.js` for PRAGMA queries (proven API).
- `PRAGMA query_only = ON` is engine-level write prevention — PocketBase's own SQLite engine enforces it for the duration of the request, so DML/DDL is impossible even if the allowlist has a gap.
- Belt-and-suspenders: `PRAGMA query_only` + always-ROLLBACK + statement allowlist. Three independent layers must all fail for a write to land.
- No need to expose or discover the SQLite file path, no direct container filesystem access from the control plane.
- The control header gate (`X-Datbase-Control`) already ensures only DATBase itself can reach `/api/datbase/*`. Server keys and app-user tokens never carry that header → they get `NotFoundError` → HTTP 404.

Option B was rejected because it requires knowing the container's file path, mounting it or SSHing in, and setting up a second SQLite connection — more moving parts with no security benefit.

---

## Security model

The SQL endpoint is **dashboard-only**:

| Caller | Result |
|---|---|
| Dashboard owner (correct project) | ✅ Runs query |
| Dashboard owner (wrong project) | 404 (`pageProject` / `actionProject`) |
| Another dashboard account | 404 (same ownership check) |
| Server key (`Authorization: <token>`) | 404 (no control header → `NotFoundError` in hook) |
| Signed-in app user token | 404 (same) |
| Unauthenticated | 401 (Better Auth middleware) |

Inside the hook, additional guards:
1. **Statement allowlist**: only `SELECT`, `WITH`, `EXPLAIN` pass. Single statement only (`;` in the middle → rejected).
2. **Internal table block**: any reference to a name starting with `_`, or to `sqlite_master`, `sqlite_schema`, `pragma_*` table-valued functions → rejected.
3. **Secret column strip**: `password` and `tokenKey` columns are removed from result rows even if they somehow appear.
4. **`PRAGMA query_only = ON`**: engine-level; active for the duration of the query.
5. **Always-ROLLBACK**: even if a write slipped through, the transaction is rolled back unconditionally.
6. **1000-row cap**: rows beyond 1000 are dropped; `truncated: true` is set in the response.
7. **10-second timeout**: enforced via `AbortSignal.timeout` on the fetch from the action.
8. **Rate limit**: 30 runs/minute/project (in-memory, Next.js server process).

SQL runs do **not** count as customer API requests (the control header fetch bypasses the per-project request counter in `datbase.pb.js`). They are stored in `sql_run` (last 20 per project, metadata only — no result rows).

---

## Data model (control Postgres)

Three new tables, all with `project_id FK → project(id) ON DELETE CASCADE`:

- **`sql_folder`** — folder tree for organising snippets (nested up to 5 levels; unique name per parent).
- **`sql_snippet`** — saved queries. `folder_id` FK → `sql_folder(id) ON DELETE CASCADE`. Max 100 KB SQL text enforced in application code.
- **`sql_run`** — run history, last 20 per project. Stores SQL text + metadata (elapsed ms, row count, error). No result data.

Migration: `app/drizzle/0006_sql_editor.sql`.

---

## UI overview

Route: `/dashboard/:id/sql` (Next.js page at `app/src/app/dashboard/[id]/sql/page.tsx`).

Layout:
- Left sidebar (3 tabs): **Schema browser** (tables → columns), **Snippets** (folder tree + favorites + search), **History** (last 20 runs, click to reload).
- Main area: **CodeMirror 6 editor** (top 40%) → **Results table** (bottom 60%).
- Toolbar: Run (`Ctrl/Cmd+Enter`), Cancel, Save as snippet.

CodeMirror is bundled via npm (`@codemirror/lang-sql` with SQLite dialect) — no CDN, no CSP violations.

Starter snippets ("Getting started" folder with 5 example queries) are created idempotently on first visit via `ensureStarterSnippets`.

---

## Customer-facing language

- Call it **DATBase SQL**, not "PocketBase SQL" or "SQLite editor".
- If dialect differences come up in help text, "SQLite-style" is acceptable. Nowhere else.
- The SQL tab appears in the project dashboard tab bar alongside Tables, Users, Usage, Backups.

---

## Files changed

| File | What changed |
|---|---|
| `pocketbase/pb_hooks/datbase.pb.js` | Added `POST /api/datbase/sql` hook endpoint |
| `app/src/db/schema.ts` | Added `sqlFolder`, `sqlSnippet`, `sqlRun` Drizzle tables |
| `app/drizzle/0006_sql_editor.sql` | Generated migration |
| `app/src/lib/export.ts` | Exports snippets as `.sql` files inside zip |
| `app/src/app/dashboard/[id]/sql/actions.ts` | All server actions (run, schema, history, CRUD for folders/snippets) |
| `app/src/app/dashboard/[id]/sql/ui.tsx` | Client UI: CodeMirror editor, results table, sidebar, schema browser |
| `app/src/app/dashboard/[id]/sql/page.tsx` | Next.js page: ownership check, starter snippets, initial data load |
| `app/src/app/dashboard/[id]/tabs.tsx` | Added "SQL" tab |
| `tests/e2e/sql-editor.mjs` | E2E test suite |
| `tests/e2e/run-all.sh` | Registered `sql-editor.mjs` |
