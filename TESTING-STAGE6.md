# Codex test brief: Stage 6 (email, monitoring, Home Assistant) on a Raspberry Pi 4

Goal: confirm email and monitoring work end to end on the real Docker stack running on a Pi 4
(arm64), and report what works, what doesn't, and what to improve. Report as `STAGE6-REPORT.md`
(same format as before: result, scorecard, findings with ID `S6-xx`, severity, steps, evidence).

**Read `CODEX-HANDOFF.md` first**: what changed, what's already verified (don't redo it), and a
ranked list of what's uncertain. Put most of your effort on its section 4.

## 0. Before you start
- Re-run everything on the Mac first: `cd app && PG_BIN=/opt/homebrew/opt/postgresql@16/bin TEST_VERBOSE=1 npm run test:e2e`
  (no `REAL_PG_DUMP`/`PG_RESTORE` needed now; the email suite needs `pip install aiosmtpd`).
  Expected: 10 suites, 130 checks. MAC-V5-02: if `backup-fixes` fails, include its output: it now
  names the step it stopped at and prints the recovery instance's log.

## 1. Set up the Pi (docs/RASPBERRY-PI.md)
- 64-bit Raspberry Pi OS, Docker, `.env` with `DOMAIN=<pi-ip-with-dashes>.sslip.io`, `PUBLIC_SCHEME=http`,
  `ADMIN_EMAIL=matosgeorgen@gmail.com`, `MONITOR_TOKEN` (hex), `ALERT_WEBHOOK_URL` empty for now.
- Email, do both runs:
  1. **Test inbox:** `docker compose --profile mailpit up -d --build`, `SMTP_HOST=mailpit`, `SMTP_PORT=1025`,
     `SMTP_FROM="DATBase <no-reply@<DOMAIN>>"`. Emails at `http://<pi-ip>:8025`.
  2. **Real provider** (only if the operator gives you SMTP credentials): confirm one email of each kind
     reaches a real inbox and isn't marked as spam.
- Record: build time, `docker stats` memory with 0, 3 and 10 projects, and the `pb-stress.mjs` result
  against one project (60 clients × 45 s) on the Pi.

## 2. Dashboard accounts
- Sign up with ADMIN_EMAIL → "check your email", no access before confirming; the link signs in.
- Sign in before confirming → a new link is sent.
- Sign up again with the same email → same screen; the owner gets "you already have an account".
- Forgot password: unknown email and real email get the same answer; link sets a new password, other
  sessions are signed out, link can't be reused, expired/garbage links show "Link expired".
- An account created **before** this version (existing Postgres data) still signs in.

## 3. App users (customers' apps)
- Create a project, a table with the "Signed-in users with a confirmed email" option, download DATBASE.md.
- Build a tiny page (plain JS client from DATBASE.md) with sign-up, "Forgot password?" and a list of that table.
- Sign-up email: sender = SMTP_FROM address, name = project name, link opens `/u/<slug>/users/verify`.
- Reset email → hosted page → new password works. Wrong/garbage/reused links → clear message.
- Unconfirmed user sees an empty list on the confirmed-only table; confirmed user sees records.
- Abuse: with a server key, change the users table's email templates and the project's sender in
  `/api/settings`, then request a reset → email must still use DATBase's sender, wording and link.
  `POST /api/settings/test/email` → 404. `GET /api/settings` never shows the SMTP password.
- Cap: more than `PROJECT_MAIL_PER_HOUR` (50) emails in an hour from one project → extra ones not sent.
- Rename the project → next email uses the new name.

## 4. Monitoring
- Sign in as ADMIN_EMAIL → **Server status**: disk, memory, projects, backups, email. Other accounts: no
  link, `/dashboard/admin` → 404.
- `docker stop dbp-<slug>` → within ~10 min: one alert email ("1 problem"), status page and
  `/api/monitor` show it; no repeated emails while it stays down. `docker start` → "all clear".
- "Send test alert" → email arrives.
- `curl -H "Authorization: Bearer <MONITOR_TOKEN>" http://<DOMAIN>/api/monitor` → JSON; no/wrong token → 401.
- Optional: set `MONITOR_DISK_PCT=1` and restart the app → disk alert; restore the value → all clear.

## 5. Home Assistant (if the operator has HA on the same network)
- Install `integrations/home-assistant/datbase.yaml` as a package + secrets, add `datbase-card.yaml`.
- All sensors have values (no "unknown"), the gauges render, the problem card appears when a project
  is stopped, the phone notification fires after 5 min and "all clear" after it's back.
- Webhook: set `ALERT_WEBHOOK_URL=http://<ha>:8123/api/webhook/datbase_alert`, enable the commented
  automation, stop a project → instant notification.
- Stop the whole DATBase app (`docker compose stop app`) → "DATBase unreachable" after 5 min.

## 5b. Operator & owner tools (Stage 7)
- After `docker compose up -d --build` with a change in `pocketbase/`: Server status shows projects
  outdated → **Update all projects** → all current. Measure downtime per project with a curl loop.
- Accounts & projects: suspend a project (owner sees the reason, can't resume; app gets errors),
  lift it; suspend an account (signed out, can't sign in), lift it (paused projects stay paused).
- Account → Download data for a project with files; open the zip (JSON readable, files intact).
- Limit emails: `PLAN_LIMITS_OVERRIDE={"requestsPerMonth":50}` on a test server, generate traffic,
  press Refresh on Usage: one 80% email, then one 100% email.
- Delete a test account: projects, containers (`docker ps -a`), volumes (`docker volume ls`) and
  backups in the bucket are gone; goodbye email arrives.

## 6. Also check
- Reboot the Pi: everything comes back by itself (projects, monitor, emails).
- Nothing in any email, page or JSON mentions the engine underneath.
- Mobile width for the new pages (sign-up states, forgot/reset, hosted `/u/...` pages, status page).
