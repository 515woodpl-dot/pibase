# DATBase: start here (for a new Claude session)

Last updated 2026-09-29. Read this file first, then only the files it points to. It is written to
save you exploring the repo. If something here disagrees with the code, the code wins: fix this file.

## What this is
DATBase is the owner's own **backend-as-a-service** (a small Supabase alternative). Each customer
**project** gets its own isolated PocketBase (database, sign-in for the customer's app users, file
storage, REST API) at `https://<slug>.<DOMAIN>`. A Next.js dashboard + Postgres "control plane"
creates and manages them. Customers never see the word PocketBase (tests enforce this).

Owner/operator: `matosgeorgen@gmail.com` (`ADMIN_EMAIL`). Repo: `github.com/515woodpl-dot/datbase`, branch `main`.
The owner works mostly by pasting prompts into AI CLIs (Claude Code on the Pi, Codex for testing); write instructions they can paste as-is.

## Standing instructions from the owner (obey these)
1. **Be proactive.** Don't fix one symptom and wait for the next. Anticipate missing packages, plugins, updates, edge cases; build them into scripts and docs. Every reusable thing you do goes into the repo (docs, scripts, tests), not just into a chat reply.
2. **Avoid sudo.** Nothing should need repeated `sudo`. `scripts/setup-pi.sh` needs at most one root command once per machine (`usermod -aG docker`). Never write instructions that sprinkle `sudo`.
3. **Never `git push` from the Pi's checkout** (no credentials, nothing to push). Commit/push from the build session only when asked.
4. **Commits** end with the attribution lines given in the session's system reminder.
5. **Be honest about readiness.** Say what is untested. Correct earlier claims you find wrong (this has happened: see "Server keys" below).
6. Auth of existing apps is **never mirrored** into DATBase (see `docs/SECONDARY-BACKEND.md`).

## Where things are
| Area | Files |
|---|---|
| Dashboard pages | `app/src/app/dashboard/[id]/{tables,files,backups,usage,datbase-md,export}`, account/admin under `app/src/app/dashboard/{account,admin}` |
| **Tables + schema import** | `.../tables/actions.ts` (server actions), `ui.tsx`, `shared.ts` (field types, access presets, unique rules), **`import-schema.ts`** (pure planner + `runImport`, unit-tested) |
| Talking to a project's PocketBase | `app/src/lib/pb.ts`: `pb(project, path, init)` logs in as superuser, sends the control header and the token as a **raw `Authorization` header (no `Bearer`)**. Never forward a browser-supplied path. Internal tables (`_superusers`…) go through `getCollection`/`isInternal` and read as "not found". |
| **SQL editor** (read-only "DATBase SQL") | Checks in `pocketbase/pb_hooks/datbase_sql.js` (tokenizer, shadow CTEs, EXPLAIN access check; unit-tested in Node), endpoint `POST /api/datbase/sql` in `datbase.pb.js`, `lib/sql.ts` (ready/rate limit/10 s/history), route `dashboard/[id]/sql/run/route.ts` (**not** a server action: those run one at a time per tab, so Cancel couldn't overtake), `sql/{page,ui,actions,folders}`. Design + limits: `docs/SQL-EDITOR.md`. |
| Project lifecycle | `lib/provision.ts`, `docker.ts` (containers, Caddy labels, blocks `/_/`), `lifecycle.ts` (delete, suspend, update-all), `busy.ts` |
| Server keys | `lib/server-keys.ts` (superuser impersonation tokens: expire, revocable) |
| AI guide (`DATBASE.md`) | `lib/ai-guide.ts` (generator, incl. `schemaImportSection()` and the `datbase.ts` client), route `dashboard/[id]/datbase-md/route.ts`. Sample: `docs/DATBASE.example.md` (regenerate the changed sections when the generator changes). |
| Per-project hook (runs inside every project) | `pocketbase/pb_hooks/datbase.pb.js` (limits, request counting, fail-closed tokens, **schema lockdown**), `datbase_mail.js` (fixed-wording emails) |
| Usage/limits/plans | `lib/usage.ts`, `lib/plans.ts` (Free plan only; `PLAN_LIMITS_OVERRIDE`) |
| Backups | `lib/backups.ts`, `lib/retention.ts`, `scripts/backup-tool.mjs` (S3-compatible, AES-GCM, 7 daily + 4 weekly) |
| Email, monitoring | `lib/mail.ts`, `lib/project-mail.ts`, `lib/monitor.ts`, `app/api/monitor/route.ts`, `integrations/home-assistant/` |
| Auth (dashboard) | Better Auth: `lib/auth.ts` (`trustedOrigins` from `EXTRA_TRUSTED_ORIGINS`), pages in `app/src/app/(auth)` |
| Security headers | `app/src/proxy.ts` (nonce CSP) |
| Deploy | `docker-compose.yml`, `.env.example`, `scripts/setup-pi.sh`, `docs/RASPBERRY-PI.md`, `docs/DEPLOY-RASPBERRY-PI.md`, `scripts/nginx-datbase.conf.example` |
| Secondary-backend mirrors of the owner's other apps | `docs/SECONDARY-BACKEND.md`, `docs/examples/datbase-mirror-*.ts` |
| Feature parity vs Supabase (have / missing / why) | `docs/SUPABASE-COMPARISON.md` |

**Next.js here is version 16 with breaking changes**: `app/AGENTS.md` says read `node_modules/next/dist/docs/` before writing framework code. Server actions live next to their pages; `PageProps<"/route">` types are used.

## Rules that must not be broken
- **Schema is dashboard-only.** Tables, fields and access change only through the dashboard (forms or *Import schema*), which sends the control header. A server key or user token must get 403 on `POST/PATCH/PUT/DELETE /api/collections…`, `…/truncate`, `…/impersonate/…` (enforced in `datbase.pb.js`, tested in `schema-import.mjs`). *This was NOT enforced until 2026-09-28: it was only an instruction in DATBASE.md.* Existing projects need **Update all projects** (Server status) to get the hook.
- **Internal accounts and settings are dashboard-only too.** The same hook now 403s a server key on `POST/DELETE /api/collections/_superusers/records[/<id>]` (any internal `_`-collection record write) and on state-changing `/api/settings`. *Until 2026-09-29 both were open: a leaked server key could create a working superuser (then authenticate as it) and rename the project / repoint its URLs (SI-01, SI-02). Fixed in `datbase.pb.js`, regression-tested in `schema-import.mjs` item 9a.* DATBase's own key lifecycle and mail setup still work because they go through `pb()`, which always sends the control header. Existing projects need **Update all projects**.
- **The SQL editor is read-only and never shows internal data.** Every name spelling counts (SQLite accepts `'x'` as a table name; `sqlite_dbpage` is compiled in); hidden fields are removed by shadow CTEs, not by column-name filtering; the engine `EXPLAIN` check of opened tables must stay. Changing `datbase_sql.js` means extending `tests/unit/sql-guard.test.mjs` and `sql-editor.mjs`. *The first version (2026-09-29, `eed1978`) was never run and was bypassable; see FIXES.md.*
- Treat any new PocketBase admin endpoint as reachable by a server key until proven blocked and gated in the hook. The gate is allow-by-control-header: a call with the control header passes (that's DATBase itself); anything else mutating tables, internal-collection records or settings is 403.
- Schema import **never changes existing tables' access, retypes, renames or deletes**; it only adds tables/fields. It is all-or-nothing with rollback.
- Customer-facing text and generated files must never name the engine (`pocketbase`).
- `DATBASE.md` is the customer AI's only knowledge of a project. Any feature an AI must use has to be documented in `ai-guide.ts` and covered by `ai-guide.mjs`/`schema-import.mjs`.
- Secrets: `ENCRYPTION_KEY` encrypts project admin passwords and backups. Losing it loses the backups. Never print secrets or server keys in docs or logs.

## Run and test
```bash
cd app && npm install
npm run dev                # needs DATABASE_URL, BETTER_AUTH_URL (see README "Local development")
npm run test:unit          # 33 tests, no services (retention, schema-import planner, SQL guard, SQL folders)
npm run build && tests/e2e/harness.sh <suite>.mjs   # one suite
tests/e2e/run-all.sh       # 13 suites (≈18 min). 2026-09-29: sql-editor 21/21, schema-import 13/13, tables 18/18 re-run after the hook change; the full run-all was last all green on 2026-09-28
```
E2E needs PostgreSQL 16 (default socket `/tmp`, port 5433, user `postgres`; see `tests/README.md`),
Chromium (`CHROMIUM_PATH=/opt/pw-browsers/chromium` in the cloud sandbox), `pip install "moto[server]" aiosmtpd`. PocketBase is downloaded by the harness.
In the cloud sandbox a throwaway Postgres: `initdb` as user `postgres` in `/var/tmp/pgdata`, `pg_ctl -o '-p 5433 -k /tmp' start`.
Test setup that creates tables directly on PocketBase must send `controlHeaders()` (see `tests/e2e/lib.mjs`), because of the lockdown.
Acceptance docs for a second tester (Codex): `CODEX-HANDOFF.md`, `TESTING*.md`. Newest: `TESTING-SCHEMA-IMPORT.md`. SQL editor: `docs/SQL-EDITOR.md` + `FIXES.md` (SQL-01…10).
`sql-editor.mjs` needs `PB_PORTS="8090 8091"` and `SQL_RUNS_PER_MINUTE=150` (set in `run-all.sh`).

## Deployment state (as of 2026-09-28)
- **Raspberry Pi "gejg"** (test box): running via `scripts/setup-pi.sh`, `PUBLIC_SCHEME=http`, sslip.io hostname, plus a Tailscale hostname through `EXTRA_CADDY_DOMAINS`/`EXTRA_TRUSTED_ORIGINS` (auto-filled when Tailscale is present). Not yet real-world verified: SMTP (blank), backups (off), Stage-7 admin tools on this Pi, a public HTTPS URL (Tailscale Funnel prompt was given to the Pi's CLI; result not confirmed).
- **Second Pi "occnj"** (other tailnet) could not reach DATBase: tailnet mismatch. Fix is a public HTTPS URL (Funnel) or same tailnet.
- Update procedure on a box: `git pull && docker compose up -d --build`, then **Update all projects** if `pocketbase/` changed.
- Setup-script lessons (all fixed, keep them fixed): Docker compose/buildx plugins install to `~/.docker/cli-plugins` (no root); `id -nG` says the *account* is in the docker group, not that the *shell* is, so test with `docker version` and use `sg docker -c`; `sudo -v` fails with no TTY, so print a one-line fix instead of a raw error; Caddy only routes the configured hostnames, so a bare IP shows a blank page.

## The owner's other apps (all Supabase or custom) and the mirror pattern
DATBase is being tried as a **secondary, write-only, best-effort mirror** of existing apps (never their source of truth, never their auth): `occ-inventory` (custom Drizzle/Postgres), `ehsmanager` (`clients`), `gswstore` (`categories`, `inventory`; its `FULL_SCHEMA.sql` may predate 17 migrations, so field lists are unverified). Mirror client code is written and committed under `docs/examples/`; **not yet wired into any app**: collections not created, keys not generated, calls not added. Playbook: `docs/SECONDARY-BACKEND.md`. Schema import now makes creating those collections a single paste.

## What DATBase is and isn't, versus Supabase
Full table with reasons: `docs/SUPABASE-COMPARISON.md`. Short version:
- **Have:** read-only SQL tab (saved queries in folders, history), tables/fields (dashboard or JSON import), auto REST API with filters/sort/expand, unique and number rules, email+password sign-in with confirmation/reset (needs SMTP), preset access rules incl. "only its owner", server keys, file fields, daily encrypted off-server backups with restore, usage limits, export/delete account, monitoring/alerts, per-project isolation, Pi-friendly install, AI-ready `DATBASE.md` + zero-dependency client.
- **Missing:** SQL writes/views/transactions and SQL through the API (the SQL tab reads only, owner only), server functions/webhooks/cron (so a store's **checkout needs its own small server** using a server key), realtime (not exposed), OAuth/magic link/MFA, custom access-rule expressions, standalone storage buckets, logs UI, team members, paid plans/billing, custom domains, point-in-time recovery, HA/scaling, CLI/versioned migrations.
- **Why:** the engine is SQLite inside PocketBase (tiny, one folder per project, easy backups, runs on a Pi); running customer code safely needs sandboxing that doesn't exist yet.

## Working on the next thing
The SQL editor (2026-09-29) is not yet tried on the Pi: needs `git pull && docker compose up -d --build` and **Update all projects**.
Suggested order (owner decides): (1) wire a mirror into one real app end to end using schema import; (2) verify backups + SMTP + Funnel on the Pi; (3) a server-side story (webhooks/actions) so stores can check out; (4) custom access rules; (5) aggregates. When a feature ships: update `docs/SUPABASE-COMPARISON.md`, `README.md`, this file, and add/extend an e2e suite.

## Documentation map
`README.md` (features, deploy, backups, email, operator tools) · `docs/SUPABASE-COMPARISON.md` · `docs/SECONDARY-BACKEND.md` · `docs/RASPBERRY-PI.md` · `docs/DEPLOY-RASPBERRY-PI.md` · `docs/DATBASE.example.md` · `TESTING*.md`, `CODEX-HANDOFF.md`, `FIXES.md` (finding history) · `app/tests/README.md`.
