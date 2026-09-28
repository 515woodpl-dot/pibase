# Codex handoff: read this first (saves you exploring the repo)

Date: 2026-09-27. Covers everything since your v5 Mac re-test: MAC-V5-01/02 fixes, **Stage 6**
(email, monitoring, Home Assistant, Raspberry Pi support) and **Stage 7** (operator & owner tools:
update all projects, suspend, data export, account deletion, limit emails). Then follow `TESTING-STAGE6.md`.

## 1. State in one paragraph
Everything below passes on Linux amd64 (this build machine): unit **7/7**, e2e **138/138** in
11 suites, PocketBase 0.39.11, Postgres 16. **Not yet run anywhere real:** a Raspberry Pi, a real
SMTP provider, real Home Assistant, Caddy in `PUBLIC_SCHEME=http` mode, Mailpit. Section 4 lists
what I'm unsure about, most important first: **spend your effort there**, not on re-proving section 3.

## 2. Where things are (don't grep the whole repo)
| Area | Files |
|---|---|
| Dashboard email (Better Auth) | `app/src/lib/mail.ts` (SMTP, templates), `app/src/lib/auth.ts` (verification, reset, `isAdmin`), pages `app/src/app/(auth)/{login,signup,forgot-password,reset-password}` |
| App-user email (per project) | Hook `pocketbase/pb_hooks/datbase.pb.js` (bottom: *Email* section) + `pb_hooks/datbase_mail.js` (builds each email). Control plane `app/src/lib/project-mail.ts` (`configureProjectMail`). Hosted pages `app/src/app/u/[slug]/[table]/[action]/` |
| Monitoring | `app/src/lib/monitor.ts` (checks, alerts, loop), `app/src/app/api/monitor/route.ts` (JSON for HA), `app/src/app/dashboard/admin/` (status page) |
| Home Assistant | `integrations/home-assistant/datbase.yaml` (package), `datbase-card.yaml` |
| Pi / LAN | `docs/RASPBERRY-PI.md`, `PUBLIC_SCHEME` in `app/src/lib/config.ts`, `docker.ts` (Caddy label), `docker-compose.yml` (+ optional `mailpit` profile) |
| New settings | `.env.example`, bottom half (SMTP_*, ADMIN_EMAIL, ALERT_WEBHOOK_URL, MONITOR_*) |
| DB | `app/drizzle/0004_monitoring.sql`: `alert_state` table + marks existing users email-verified |
| Operator/owner tools (Stage 7) | `app/src/lib/lifecycle.ts` (delete, suspend, update all), `lib/export.ts`, `lib/busy.ts` (don't touch a project mid-restore), limit emails at the end of `lib/usage.ts`; pages `dashboard/admin/accounts`, `dashboard/account`, route `dashboard/[id]/export`; migration `0005_admin_ops.sql` |
| Tests | `email-monitor.mjs` via `run-email.sh`; `admin-ops.mjs` via `WITH_MAIL=1 run-backups.sh` (needs `pip install aiosmtpd`); watchdog/`step()`/`track()` in `lib.mjs` |

## 3. Already verified here: don't redo, just re-run the suite
`npm run test:e2e` covers all of it (`email-monitor` = 20 checks):
- Sign-up needs email confirmation; unconfirmed sign-in re-sends link; confirm link signs in.
- Same-email sign-up → identical screen + "you already have an account" email (F-09 closed).
- Forgot password: same answer for unknown emails; new password works, old fails, link single-use,
  **all other sessions signed out**.
- `/dashboard/admin` only for ADMIN_EMAIL (others 404, no link); `/api/monitor` 401 without/wrong token.
- App users: sign-up email from SMTP_FROM with the project name; hosted verify/reset pages work;
  bad/garbage/reused links → clear message.
- **Phishing-proofing:** edited PocketBase templates + sender are ignored; `POST /api/settings/test/email`
  → 404; SMTP password never readable via `GET /api/settings`.
- Per-project cap (`PROJECT_MAIL_PER_HOUR`) exact.
- "Signed-in users with a confirmed email" access option.
- Monitor: project down → after 2 checks one webhook + one email; no repeats; "all clear" when back;
  test alert button.
- HA YAML reads only fields that exist in `/api/monitor`.
- Stage 7 (`admin-ops`, fake Docker): 80%/100% emails once each; update-all detects outdated projects,
  keeps paused stopped, re-applies limits; suspend project/account (reason shown, sign-in blocked,
  lifting restores active vs paused correctly, no self-suspend); export zip (records, files
  byte-exact, no internal tables/engine names, other accounts 404); account deletion removes
  projects, containers, volumes and backups.
- MAC-V5-01: `PG_BIN` alone works (fake `pg_dump` + app PATH).
- MAC-V5-02: a stuck suite fails after `SUITE_TIMEOUT_SEC` naming its last step, and child processes
  are stopped.

## 3b. Results of your Stage 6 run (STAGE6-REPORT, Mac + Docker Desktop): don't repeat these
Passed: Mac 130/130; MAC-V5-01/02; plain HTTP with real Caddy + cookies; sslip.io; Mailpit (dashboard
and app-user mail); old-project recreation; monitor numbers vs container `df`/MemAvailable;
project-down alert, dedupe and app-restart persistence; `/api/monitor` token; mobile 375 px;
no engine names; deployed stress. **Still open:** section 4 items 5 (real SMTP), 8 (real Home
Assistant), 9 (Pi), and **all Stage 7 items 12–16 plus the `admin-ops` suite (never run by you yet:
the full run is now 11 suites / 138 checks).**

On S6-04 (disk path): the app container's `/` is Docker's overlay, which lives in Docker's data
directory: the same disk as every project's data volume. So it already measures the disk that
matters. If Docker's data directory is on the USB SSD, that's what you'll see; the SD card (host
`/`) isn't monitored. Compare with `df -h $(docker info -f '{{.DockerRootDir}}')` on the Pi host, not
`df -h /`.

## 3c. Since then
Branding (blue #2d7daf / green #61b895, bold wordmark), a recovery race fix (recovery marks
projects busy; start-up email job waits), recovery falls back to older backups if the newest is
damaged, and a time-of-day test bug in `backup-fixes` fixed. Please glance at the new look on mobile
and dark mode (§6) and run the full suite once (expected 11 suites / 138 checks).

## 4. Where I'm NOT sure (test these, ranked)
1. **MAC-V5-02 root cause is unknown.** I couldn't reproduce the `backup-fixes` hang; R-01 passes on
   Linux. I only made it fail fast with a step name. Run
   `PG_BIN=… TEST_VERBOSE=1 tests/e2e/run-backups.sh backup-fixes.mjs` on the Mac and report the last
   step + the recovery log it prints. My guesses: PocketBase's self-restart during restore on macOS,
   or the recovery instance on :3001 never becoming healthy.
2. **`PUBLIC_SCHEME=http` on the Pi is untested with real Caddy.** Labels become `http://<host>`
   (should stop Caddy trying HTTPS). Check: dashboard loads over http, sign-in cookie sticks (Better
   Auth over http), project URLs `http://<slug>.<ip>.sslip.io` work, `/_/` still 404 on projects.
3. **sslip.io on a home router:** many routers block DNS answers with private IPs ("rebinding
   protection"). If names don't resolve, report it; the doc suggests Pi-hole/local DNS as fallback.
4. **Mailpit profile is untested** (Docker Hub was blocked here). Check that project containers can reach
   `mailpit:1025` (they're on the `datbase` network, like the app) and emails show at `:8025`.
5. **Real SMTP providers:** only tested against a local SMTP sink (no TLS). Unknown: STARTTLS on 587
   and `SMTP_SECURE=true` on 465 with a real provider, spam placement (we don't set up SPF/DKIM; that's
   the operator's DNS). PocketBase sends app-user mail with its own SMTP client (settings `tls` =
   our `SMTP_SECURE`). Please test both dashboard mail and app-user mail with one real provider.
6. **Existing project containers need recreating** to get the new hook (it's baked into the image).
   Their email settings are pushed at app start, but **without the new hook, emails would use
   PocketBase's own templates (links to the blocked `/_/`)**. Please confirm that old projects are
   recreated and new ones send correct emails.
7. **Monitor numbers inside Docker:** disk = `statfs("/")` in the app container (`df` formula, fixed
   today; it had been 89% vs `df` 36%), memory = host `/proc/meminfo` MemAvailable. Compare with
   `df -h /` and `free -m` **on the Pi host**; if the Docker data lives on another disk (USB SSD), set
   `MONITOR_DISK_PATH` or tell me.
8. **Home Assistant is untested in a real HA** (YAML parses and fields match). Unsure about: `!secret` in
   `resource`, the legacy `trigger:/action:/service:` syntax on your HA version, the markdown card
   template, and the `timestamp` sensor when `last_control_backup` is null (should show unknown).
9. **Pi 4 resources:** first `docker compose build` time, memory with 3/10 projects, `pb-stress` result
   on a Pi (arm64 build of 0.39.11 already passed on your Mac; the Pi is much slower).
10. **Rate limits behind a second proxy:** Better Auth limits per IP from `X-Forwarded-For`, which Caddy
   sets to the real client. Fine as shipped; if Cloudflare ever sits in front, all users would share
   Cloudflare's IPs. Just note it; no test needed.
11. **Monitor restarts:** "2 checks in a row" counters live in memory, so an app restart delays alerts by
   one extra interval. Alert state itself is in Postgres (no duplicate alerts after restart; please
   spot-check by restarting the app while a project is stopped).

12. **"Update all projects" with real Docker + Caddy** (tests use a fake Docker). Expect a few
   seconds of 502 per project while Caddy notices the new container. Measure it: run a loop of
   `curl` against a project during the update and report the gap. Also run it right after a
   `docker compose up -d --build` that changed `pocketbase/` and confirm "outdated" → "current".
13. **Suspended project URL** returns Caddy's error (the container is stopped), not a friendly page.
   Tell me if that's confusing for app users.
14. **Export size/time:** only tested with a few records. Try a project with ~50 MB of files: time,
   app memory (`docker stats`), and whether the download completes through Caddy.
15. **Account deletion when backup storage is down:** the account row stays (sign-in blocked) until
   the backup scheduler finishes the project deletions; untested end to end with real Spaces/S3.
16. Limit emails for database/storage/users are also "once per month" per level, even though those
   limits aren't monthly. Deliberate (no spam); tell me if you'd expect otherwise.

## 5. Tips to save tokens
- Don't read `ai-guide.ts`, `backups.ts` or the e2e suites to understand behavior: section 2/3 is the
  summary, and the suites print one line per check.
- Use `/api/monitor` JSON and the Mailpit UI/API (`GET :8025/api/v1/messages`) as evidence instead of
  screenshots where possible.
- Report only: the summary table, failures with the printed step/log tail, and answers to the section 4
  items (numbered the same way).
