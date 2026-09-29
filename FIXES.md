# Fixes for the schema-import security review (2026-09-29): SI-01 … SI-04

From `SCHEMA-IMPORT-REPORT.md`. The two authorization findings were reproduced against PocketBase
0.39.11 with the real project hook and the control-secret derivation from `harness.sh`, then fixed and
re-verified: the exact probes that returned 200/204 before now return 403, while every DATBase call
(which carries the control header) still works. Regression coverage ships in `schema-import.mjs` (new
item 9a), taking that suite from 12 ✔ to 13 ✔.

| ID | Severity | Fix | Where | Verified by |
|---|---|---|---|---|
| SI-01 | Critical | The dashboard-only gate now also 403s any write to an internal (`_`-prefixed) system collection's records — `POST/DELETE /api/collections/_superusers/records[/<id>]`, `_otps`, etc. — unless the control header is present. Previously the `schemaWrite` regex matched `/api/collections/_superusers` (collection management) but not the `.../records` path, so a leaked server key could create a superuser and authenticate as it, or delete superusers. DATBase's own key lifecycle (`server-keys.ts`) still works because it goes through `pb()`, which always sends the control header. | `pocketbase/pb_hooks/datbase.pb.js` → `routerUse` | Server key: `POST _superusers/records` and `DELETE _superusers/records/<id>` → 403 (were 200/204); the attacker email never appears as a superuser and cannot authenticate; `_otps` record write → 403. Dashboard key create/revoke (control header) still 200. |
| SI-02 | High | The same gate 403s state-changing `/api/settings` (and `/api/settings/…`) for any non-control request. Previously only mail-relevant fields were neutralised (via `onMailerSend`) while the settings write itself was open, so a server key could rename the project or repoint its URLs. The one legitimate settings write (`project-mail.ts` → `configureProjectMail`) goes through `pb()` with the control header and is unaffected. | `pocketbase/pb_hooks/datbase.pb.js` → `routerUse` | Server key: `PATCH /api/settings` → 403 (was 200); `meta.appName` unchanged on read-back. Control-header PATCH still 200. |
| SI-03 | Low | A duplicate-name import failure now maps to a product-level message ("One of these tables already exists — another import may have just created it. Refresh the page and try again…") whether it surfaces as PocketBase's validation ("Collection name must be unique") or, under a concurrent commit, the raw SQLite line (`UNIQUE constraint failed: _collections.name (2067)`). The import was already rolled back cleanly; only the message was internal. | `tables/actions.ts` → `importErrorMessage`, used by `importSchema` | Unit-style check of the mapper over both error forms and the report's raw string → friendly message; unrelated errors pass through unchanged. |
| SI-04 | Low | Corrected the check counts. The committed `schema-import.mjs` emitted **12** ✔ (12 `ok()` calls), so the protocol's "12/12" was accurate as written and the report's "13 emitted" was a miscount; with the new SI-01/SI-02 regression the suite now emits **13**, and the protocol, `CLAUDE.md` and item 14's expectations are updated to match (and to record that `/api/settings` and `_superusers/records` are no longer open). | `TESTING-SCHEMA-IMPORT.md`, `CLAUDE.md` | Source now has 13 `ok()` calls; item 14 rewritten to expect 403 on the two newly-gated surfaces. |

**AI loop / DATBASE.md:** the report confirmed the additive bookstore import worked first-time in a
fresh empty project and found no wording defect in the generated guide. The "public/client-safe key"
the separated Builder invented was a test-harness artifact (it was handed the schema contract, not the
real downloaded guide, which has no such key), so no product-doc change was needed. Left as-is.

**Note on rollout:** like the 2026-09-28 schema lockdown, these gates live in the per-project hook, so
**projects created before 2026-09-29 need *Update all projects*** (Server status) to pick them up.


# Fixes for REPORT.md (Codex test, 2026-09-26)

Each fix was verified with an automated regression test against real PocketBase 0.40.4 + Postgres.
The earlier Stage 3 (18 checks) and Stage 4 (12 checks) suites were re-run and still pass.

| ID | Severity | Fix | Where | Verified by |
|---|---|---|---|---|
| F-01 | High | Project count + insert run in one transaction under a per-account Postgres advisory lock | `app/src/app/dashboard/actions.ts` → `createProject` | 5 parallel creates with 1 existing project and a limit of 2 → exactly 2 projects |
| F-02 | High | Built-in field names (`id`, `created`, `updated`, `collectionId`, `collectionName`, `expand`, `fields`, plus auth-table fields) are rejected, as are duplicate names (case-insensitive). After saving, the returned schema is checked before reporting success | `tables/actions.ts` → `addField` | `id`, `ID`, `created`, `collectionId`, `expand` rejected; `QTY` rejected when `qty` exists; records still create normally |
| F-03 | Medium | `getCollection()` treats PocketBase internal tables (`system` or `_` prefix) as not found. Every table action, page and file download goes through it; PocketBase is called by the checked table ID. Unknown or internal tables return a real 404 | `app/src/lib/pb.ts`, `tables/actions.ts`, `tables/notice.tsx`, `files/.../route.ts` | `_superusers` and `_otps` pages, settings, record and file routes → 404; a replayed `deleteRecord` with `table=_superusers` and a real superuser ID → blocked, superuser intact |
| F-04 | Medium | Content-Security-Policy with a per-request nonce (`strict-dynamic`, `frame-ancestors 'none'`, `object-src 'none'`), `X-Frame-Options: DENY`, `Referrer-Policy`, `nosniff`, `Permissions-Policy`, HSTS when served over HTTPS, `X-Powered-By` removed. Own 404 and error pages (Next's defaults used inline styles) | `app/src/proxy.ts`, `app/next.config.ts`, `app/src/app/{layout,not-found,error}.tsx` | Headers present on pages including `/login`; zero CSP violations across the whole suite |
| F-05 | Medium | Relation dropdown (capped at 200) replaced by a search-as-you-type picker that queries the full table server-side; linked records load by ID | `tables/ui.tsx` → `RelationPicker`, `tables/actions.ts` → `searchRecords`, record page | Found, saved and re-displayed record #250 out of 250 |
| F-06 | Medium | Usage collection runs in a transaction under a per-project advisory lock; the log cursor never moves backwards | `app/src/lib/usage.ts` → `collectUsage` | Two simultaneous Refreshes → exactly 25 of 25 requests counted |
| F-07 | Low | Select choices must be unique and ≤ 100 chars; file max size must be a finite 1–100 MB (checked on the server, not just the browser) | `tables/actions.ts` → `addField` | Duplicates rejected; 101 MB and 0 MB rejected with browser validation disabled |
| F-08 | Low | Numbers must be finite (`Number.isFinite`) | `tables/actions.ts` → `readRecord` | `1e309` → "not a valid number" |
| F-09 | Low | **Accepted tradeoff until email verification exists**: sign-up still says when an email is taken. Mitigated with a 5 sign-ups per IP per hour cap on top of Better Auth's default 3 attempts per 10 s | `app/src/lib/auth.ts` | 7 sign-ups from one IP → 200 ×5, then 429 |

Also from the report's improvements:
- A server action that references another account's project now returns a controlled 404 (`notFound()`) instead of a generic 500 (`app/src/lib/projects.ts`).
- Record IDs and file names are validated before any PocketBase call.

Not done yet (still open from "Improvements"): one aggregated count query for the tables list,
timeout-specific loading states, and a full keyboard, mobile and screen-reader pass.


# Fixes for the backup review (2026-09-27): R-01 … R-07

All verified by automated tests that now ship in `app/tests/` (`npm run test:unit`, `npm run test:e2e`).

| ID | Severity | Fix | Where | Verified by |
|---|---|---|---|---|
| R-01 | High | Disaster recovery rebuilds **active and paused** projects; paused ones are restored and then stopped again. **Resume** recreates a missing container around the data volume instead of failing | `lib/backups.ts` → `recoverAll`, `dashboard/actions.ts` → `resumeProject`, `lib/provision.ts` → `recreateInstance` | `backup-fixes.mjs`: data wiped + containers "lost" → both projects restored, paused one stopped and still paused; Resume with a missing container recreates it |
| R-02 | Medium | Deletion isn't finished until the backups are confirmed gone: on a storage error the project stays **deleting** ("Removing backups… this finishes automatically") and every scheduler run retries | `dashboard/actions.ts` → `deleteProject`, `lib/backups.ts` → `finishPendingDeletions` | Storage down during delete → project stays deleting, all 10 backups kept; storage back → row and every object removed |
| R-03 | Medium | Retention keeps the newest backup of each of the **7 most recent days**, then one per week for 4 weeks. Manual and "before restore" backups are kept **on top** (newest 10, for 7 days). Only an automatic backup counts as "today's backup done" | `lib/retention.ts` (pure, unit-tested), `lib/backups.ts` → `backupDue` | 7 unit tests incl. the report's burst reproduction; `backup-scheduler.mjs`: auto backup made despite a recent manual one |
| R-04 | Medium | Restore **downloads and verifies** the chosen backup first, then takes the safety backup. The chosen backup is protected from retention until the restore ends | `lib/backups.ts` → `requestRestore`, `prepare`, `inUse` | Unit test with the report's exact case; e2e: restoring a backup that retention would otherwise expire succeeds |
| R-05 | Medium | `pg_dump` output goes to a **temporary key** and is promoted to `control/` only after exit code 0 and a minimum size; temp objects are always removed. So the scheduler and `backup-tool restore-control` only ever see complete dumps | `lib/backups.ts` → `backupControlDb`, `lib/storage.ts` → `copyObject` | Failing `pg_dump` → nothing under `control/` or `control-tmp/`; later success → 1 valid dump that `restore-control` restores |
| R-06 | Low | Once uploaded **and** recorded, a backup is never downgraded: retention runs separately and only logs failures (rows stay for the next run). If recording fails after upload, the uploaded object is deleted | `lib/backups.ts` → `run`, `tidy`, `applyRetention` | Storage deletes failing → new backup stays "done", 3 expired backups wait; next run removes them |
| R-07 | Low | One sweep at a time: an in-process guard plus a Postgres advisory lock shared by all instances; the next sweep is scheduled only after the previous one finishes | `lib/backups.ts` → `runSweepExclusive`, `startBackupLoop` | Two app instances with slow `pg_dump` → exactly 1 control backup and 1 daily backup |

Also from the review's "test limitations": every suite is now in the repo (`app/tests/`, see `app/tests/README.md`).


# Fixes for the "Build with AI" review (2026-09-27): AI-01 … AI-05

Verified by `app/tests/e2e/ai-fixes.mjs` (18 checks) and `pb-stress.mjs`, plus every earlier suite, all run
together on PocketBase 0.39.11 (`npm run test:e2e`).

| ID | Severity | Fix | Verified by |
|---|---|---|---|
| AI-01 | High | Two causes addressed. (1) **The project image always downloaded the amd64 PocketBase binary**, so on arm64 (Codex's Mac, ARM servers) it ran under CPU emulation; it now downloads the binary for the build machine's architecture (`TARGETARCH`). (2) **Default PocketBase pinned to 0.39.11** (Go 1.26), the last release before 0.40's switch to Go 1.27's new JSON engine, where both crash traces point and which PocketBase's author says not to roll out blindly. `PB_VERSION` can move up once the stress test passes on the real server | `pb-stress.mjs`: 60 concurrent clients × 45 s (lists, 12-image pages, token refresh, admin calls), 0 errors, no fatal errors. 0.40.4 also passed ~90k requests here on amd64, so the arm64 image should be checked with the same test after deploying (see its header) |
| AI-02 | High | Dashboard: number **min / max / whole numbers only** (with a note that 0 = empty unless Required), **Unique** on a field, and **Unique rules** on combinations (e.g. `owner` + `product`) enforced by the database. Removing a field removes rules that use it. DATBASE.md documents all of it in words + error codes | 6, 2.5 and 0 rejected for a required 1–5 whole field; unique field and combination rejected with `validation_not_unique`; **10 simultaneous duplicate submissions → exactly 1 saved** |
| AI-03 | High | **Server keys** (Overview → Server keys): create (30 days / 90 days / 1 year, shown once), list, revoke instantly. Each key is its own internal admin account; revoking deletes it. After a restore, keys are reconciled so an older backup can't revive a revoked key. DATBASE.md has a *Server-side access* section (env var `DATBASE_SERVER_KEY`, usage with the client, expiry, never in browsers). The admin password is no longer shown anywhere | Key reads all users' records in an owner-only table; revoked key → 403 immediately; restore of a pre-revoke backup keeps it revoked |
| AI-04 | Medium | Examples use in-range values (the field's minimum, otherwise 1); the arbitrary `12.5` is gone | Guide shows `"stars":1` for a 1–5 field |
| AI-05 | Low | Linked records are typed: `expand?: { owner?: User; product?: Product }` (arrays for multi-links) | Generated client + interfaces pass `tsc --strict`, using `review.expand.product.name` without casts |
| Gap 7 | – | DATBASE.md also contains a **plain JavaScript** client (types stripped automatically) for pages without a build step | Imported and used as-is |

**Also fixed (found while adding server keys): plan limits could be bypassed by a project admin.** Anyone
holding admin credentials could call DATBase's hidden limit endpoints and lift their own limits, or turn off
PocketBase's request logs so nothing was counted. Now:
- Every project container gets a **per-project secret** (derived from `ENCRYPTION_KEY`, passed as an
  environment variable). DATBase's internal endpoints require it; admins and keys get 404.
- **Requests are counted by DATBase's hook**, not from logs, so no project setting can stop counting.
  Only DATBase's own calls are exempt; admin and server-key traffic counts and is blocked at the limit.
- The owner's dashboard edits still follow storage and user limits (only DATBase's internal accounts are exempt).

Verified: key → `/api/datbase/*` returns 404; with logs switched off, 15 key requests counted exactly;
monthly block returns 429 to key requests; 400 parallel requests counted exactly 400.

**After deploying this version:** rebuild (`docker compose up -d --build`), then delete and recreate test
projects so their containers get the new PocketBase version and the per-project secret.

---

# Fixes for AI-REPORT.md v4 (Codex, 2026-09-27)

All suites re-run together on PocketBase 0.39.11: unit **7/7**, e2e **110/110** (tables 18,
review findings 13, AI guide 22, usage 12, backups 14, backup fixes 6, backup scheduler 4,
AI fixes 20, stress 1). No processes left running afterwards.

| ID | Severity | Fix | Verified by |
|---|---|---|---|
| AI-V4-01 | High | **Tokens fail closed.** Any request that sends a token which is invalid, expired or revoked (user session *or* server key) now gets `401` on every endpoint, instead of silently running as "signed out" and returning empty lists. Sign-in endpoints still work with a stale token attached. New `GET /api/session` (`{type:"server"}` / `{type:"user"}` / 401) and client helper **`checkServerKey()`**. The client signs out by itself on a `401` for the stored session. DATBASE.md: the "not allowed" table, server-key section (validate first, never fall back to zeros, exit non-zero), session notes and troubleshooting updated. This corrects the earlier AI-03 claim ("revoked → 403"); a revoked key now gets `401` everywhere | Revoked key → `401` on create, **owner-filtered list**, public list and `checkServerKey()`; still revoked after restoring an older backup. `checkServerKey`: live key OK, user token 403, missing 401. Garbage token → 401 (even on public tables); password changed elsewhere → next call 401 and the client clears the session |
| AI-V4-02 | Medium | Postgres password is no longer put inside the URL: compose passes `PGPASSWORD` separately (`DATABASE_URL=postgres://postgres@db:5432/datbase`), so any characters work. `.env.example` and README now say `openssl rand -hex 32` for `POSTGRES_PASSWORD` / `BETTER_AUTH_SECRET` (`ENCRYPTION_KEY` stays base64) | `pg` resolves a password like `a/b=c+d@e` from `PGPASSWORD` with the new URL |
| AI-V4-03 | Medium | New **`scripts/local-api.sh <slug>`**: forwards `127.0.0.1:8090` to a Docker project (reuses the stack's Caddy image, bound to localhost only; `stop` removes it). TESTING-AI §1 explains both modes | Tested with Docker here: health 200, invalid token 401 through the forwarder, clear errors for unknown slugs, clean stop |
| AI-V4-04 | Medium | Test harness is portable: PocketBase download picks **OS + CPU** (`darwin`/`linux`, `arm64`/`amd64`); processes are tracked and stopped **by PID** (no name matching), including PocketBase restarts inside `backup-fixes`; the harness refuses to start if another program holds its ports; `psql` always runs with your own `PATH`/`DOCKER_HOST` (the fake Docker and fake `pg_dump` reach only the app); the fake `pg_dump` finds the real one in `PATH`; `PG_BIN`, `REAL_PG_DUMP`, `PG_RESTORE` select matching PostgreSQL 16 tools. `tests/README.md` updated | Full run on Linux. macOS paths couldn't be run here; please re-run `npm run test:e2e` on the Mac (set `PG_BIN` to your PostgreSQL 16 `bin` if it isn't first in `PATH`) |

Also: the plain JavaScript client in DATBASE.md no longer has wide gaps where types were removed.

**After deploying this version:** `docker compose up -d --build`, then recreate test projects (or restart
their containers from a rebuilt image) so they get the updated hook.

---

# Stage 6 (2026-09-27): email, monitoring, Home Assistant

New features, not fixes. See README → *Email* / *Monitoring & alerts*, `docs/RASPBERRY-PI.md`,
`integrations/home-assistant/`, and the Codex brief `TESTING-STAGE6.md`. Full run: unit **7/7**,
e2e **130/130** across 10 suites (new: `email-monitor` 20).

- **F-09 closed** (when SMTP is set): sign-up answers the same for registered emails, and the
  account owner gets a "you already have an account" email. Email confirmation required before
  the first sign-in; password reset signs out all other sessions. Accounts from before stay usable.
- **App-user email is phishing-proof:** each project's hook builds every email itself (DATBase
  sender, fixed wording, links only to DATBase's hosted pages), whatever a project admin puts in
  templates or settings; the "send test email" endpoint is blocked; hourly cap per project.

---

# Fixes for MAC-RETEST-REPORT.md (v5, 2026-09-27)

AI-V4-01 (revoked keys) confirmed fixed on the Mac and on a deployed container. Full run here with
only `PG_BIN` set: unit **7/7**, e2e **130/130** (10 suites incl. Stage 6), no processes left behind.

| ID | Severity | Fix | Verified by |
|---|---|---|---|
| MAC-V5-01 | Medium | `run-backups.sh` puts `PG_BIN` into the app's `PATH` (it was overwritten), and the fake `pg_dump` looks in `$PG_BIN` first. Setting `PG_BIN` alone is enough for every suite | Wrapper finds `$PG_BIN/pg_dump` with a PATH that lacks it; all backup suites pass with only `PG_BIN` |
| MAC-V5-02 | Medium | No suite can hang any more: per-suite time limit (`SUITE_TIMEOUT_SEC`, 900 s) that prints the **last step** and exits 1; default timeout on every request (`FETCH_TIMEOUT_SEC`, 60 s); step markers through the whole R-01 recovery path; the recovery instance's log is printed if it doesn't finish; every process a suite starts is stopped when it ends (and by the harness). The run-all summary therefore always completes | A deliberately stuck test fails after the limit with its step name and leaves no processes. The Linux run passes R-01; if the Mac still fails there, the output now says exactly which step (run with `TEST_VERBOSE=1`) |

Also: the monitor's disk percentage now uses the same formula as `df` (it counted reserved blocks as
used: 89% vs `df` 36% on the build machine). Verified: `/api/monitor` 35.1% vs `df` 36%.

---

# Stage 7 (2026-09-27): operator & owner tools

Full run: unit **7/7**, e2e **138/138** across 11 suites (new: `admin-ops` 8).
- Update all projects (outdated detection by image id), suspend project/account, data export,
  account deletion, 80%/100% limit emails. See README → *Operator tools*.
- **Race fixed:** background jobs (usage measuring, applying email settings, updates) now skip a
  project that is being restored or updated, re-reading it right before touching it. Changing a
  project's settings while the engine swaps in restored data could break a restore.
- **Test fix:** `ai-fixes` checked "restored?" before the restore had started (it was intermittently
  failing); it now waits for the restore to begin first.

---

# Branding + fixes after STAGE6-REPORT (2026-09-27)

STAGE6-REPORT: no product defects (Mac + Docker Desktop, real Caddy over HTTP, Mailpit). Open items
need hardware/accounts: Pi, real SMTP, real Home Assistant. Full run now: unit **7/7**, e2e **138/138**.

- **Brand:** blue `#2d7daf` (buttons, links, focus), green `#61b895` (healthy/success), bold
  **DAT**Base wordmark with a square "D" mark, new favicon, branded emails (dashboard and app-user).
  Link text uses a slightly darker blue in light mode for readability; white text never sits on green.
- **Recovery race fixed:** disaster recovery now marks each project busy, and the start-up job that
  applies email settings waits for recovery to finish. Before, a recovery instance could change a
  project's settings while restoring it. (Likely related to the earlier `backup-fixes` instability.)
- **Recovery falls back to older backups:** if the newest backup is missing or damaged, the next
  newest is used (up to 5), instead of giving up on that project. Tested with a damaged newest backup.
- **Time-of-day test bug:** `backup-fixes` created fake backups at "today 03:00 UTC", which is in the
  future during US evenings; they now use past times at any hour.
