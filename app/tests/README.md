# DATBase tests

## Unit tests (no services needed)
```bash
npm run test:unit        # retention rules + schema-import planner (tests/unit)
```

## End-to-end suites
Real PostgreSQL, real PocketBase and a production build, driven by a headless browser.

| Suite | Covers |
|---|---|
| `tables.mjs` | Tables, fields, records, files, filters, access rules, app users, cross-account isolation (18 checks) |
| `review-findings.mjs` | Regression tests for review findings F-01…F-09 (13) |
| `ai-guide.mjs` | "Only its owner" access, DATBASE.md generation, and running its `datbase.ts` client end to end, invalid/expired tokens → `401` |
| `usage-limits.mjs` | Usage measuring, exact request counting, every plan limit blocking and lifting (12) |
| `backups.mjs` | Encrypted backup, restore, undo, tampering, retention, control DB backup, delete (14) |
| `backup-fixes.mjs` | Review findings R-01, R-02, R-04, R-06: recovery incl. paused projects, deletion retry, protected restore source, cleanup failures (6) |
| `backup-scheduler.mjs` | R-03, R-05, R-07: daily due check, failed `pg_dump`, two instances at once, `backup-tool restore-control` (4) |
| `ai-fixes.mjs` | "Build with AI" review AI-02…05 + gap 7 + AI-V4-01: number limits, unique rules (incl. a 10-request race), server keys (create, `checkServerKey()`, use, revoke → `401` everywhere incl. owner-filtered lists, can't be revived by a restore), limit enforcement that admins can't bypass, strict type-check of the generated client, plain JS client |
| `email-monitor.mjs` | Dashboard email confirmation + password reset (no account enumeration), app-user email via hosted pages, phishing-proof project emails, per-project email cap, "confirmed email" access option, monitoring alerts (project down → email + webhook → all clear), `/api/monitor` token, admin-only status page, Home Assistant field check. Run with `run-email.sh` (needs `pip install aiosmtpd`) |
| `admin-ops.mjs` | 80%/100% plan-limit emails (once each), "Update all projects" (outdated detection, paused stays stopped, limits re-applied), suspend project/account (sign-in blocked, reasons shown, lifting restores paused/active correctly), data export zip (records, files byte-exact, no internals), account deletion (projects, containers, volumes, backups gone). Run with `WITH_MAIL=1 PLAN_LIMITS_OVERRIDE='{"requestsPerMonth":20}' run-backups.sh admin-ops.mjs` |
| `schema-import.mjs` | Tables → Import schema through the real UI: bad input rejected with nothing created, 8-table store in one paste, fields/links/rules verified in the database, rules exercised over the API (public read, server-only write, owner-only), DATBASE.md lists the new tables, re-paste is harmless, extending tables keeps data and access, Supabase-style JSON, a mid-way failure is fully undone, other accounts get 404 (11) |
| `pb-stress.mjs` | AI-01: 60 concurrent clients for 45 s (lists, images, token refresh, admin calls) → backend must stay up with 0 errors. Also runs against a deployed project: see the file header (1) |

### Requirements
Works on Linux and macOS (Intel or Apple silicon).
- PostgreSQL 16 reachable with the `PSQL` command (default `psql -h /tmp -p 5433 -U postgres`) and
  `DATABASE_URL_BASE` (default `postgres://postgres@localhost:5433`). `PSQL` may also be a
  `docker exec -i <container> psql -U postgres` command: test helpers always run it with your own
  `PATH` and `DOCKER_HOST`, never the fakes the backup suites give the app.
- PostgreSQL 16 client tools (`pg_dump`, `pg_restore`) matching the server, for the backup suites.
  If they aren't first in `PATH`, set `PG_BIN` (e.g. `PG_BIN=/opt/homebrew/opt/postgresql@16/bin`);
  that's enough for every suite, including the backup ones. `REAL_PG_DUMP` / `PG_RESTORE` override
  single tools.
- A production build: `npm run build`.
- PocketBase binary: set `PB_BIN`, or the harness downloads the right one for your OS and CPU
  (cached in `$TEST_WORK/pocketbase-<version>-<os>-<arch>/`).
- Chromium for Playwright: `npx playwright install chromium`, or set `CHROMIUM_PATH`.
- Backup suites: `pip install "moto[server]"` (a local S3 stand-in) and Python 3.
- Email suite: `pip install aiosmtpd` (a local SMTP + webhook stand-in, `support/mail-sink.py`).
- Free ports 3000–3002, 8090–8091, 5055–5056, 2375, 2525 and 2530. The harness stops only processes it started
  (tracked by PID) and refuses to run if another program holds port 3000 or 8090/8091.

### Run
```bash
npm run test:e2e                         # everything, with a summary
tests/e2e/harness.sh tables.mjs          # one suite
tests/e2e/run-backups.sh backup-fixes.mjs
```
Logs and screenshots go to `/tmp/datbase-e2e` (`TEST_WORK`).

No suite can hang: each has a time limit (`SUITE_TIMEOUT_SEC`, default 900) and every request a
default timeout (`FETCH_TIMEOUT_SEC`, 60). On timeout the suite fails and prints its last step;
`TEST_VERBOSE=1` prints every step as it runs. Processes a suite starts are stopped when it ends.

### Test doubles (tests/e2e/support)
- `fake-docker.py`: a minimal Docker Engine API. Real PocketBase processes stand in for containers;
  it records create/start/stop/remove calls and can "forget" all containers to simulate a lost server.
- `flaky-s3-proxy.py`: sits in front of the S3 stand-in and can be switched to fail everything, or only deletes.
- `pg_dump`: wraps the real `pg_dump` and can be switched to fail or to run slowly.
