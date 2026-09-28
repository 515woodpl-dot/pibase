# DATBase

Backend-as-a-service for developers. Each project gets its own isolated PocketBase
(database, app users, file storage, REST API) at `https://<project>.yourdomain.com`.

PocketBase runs underneath each project; customers only ever see DATBase.

**Built so far: Stages 1–5**
- Accounts: sign up, sign in, sign out
- Projects: create, rename, pause, resume, retry failed setup, delete (typed confirmation)
- Automatic provisioning: container + data volume + health check + admin account, per project
- Internal admin password stored encrypted (AES-256-GCM) and never shown; customers use **server keys**
- Per-user project limit, per-project RAM/CPU caps
- **Tables:** create, rename, delete; add/remove fields (text, number, yes/no, email, URL,
  date, choice, JSON, file, link to another table)
- **Data browser:** add, edit, delete records; file uploads; filter, sort, pagination
- **Users:** your app's users: create, edit, delete (they sign in through the API)
- **Access:** per table, choose who can list/view/create/edit/delete: anyone, any signed-in
  user, only the record's owner (needs a link field to `users`), or server only
- **Data rules:** number min/max/whole numbers, unique fields and unique combinations (e.g. one review
  per user per product), enforced by the database
- **Server keys:** for the customer's own servers (Overview → Server keys): create, shown once, expire,
  revoke instantly; can't be revived by restoring an older backup
- **Build with AI:** each project's Overview has *Download DATBASE.md* + a copy-paste prompt. The
  guide is generated from the live schema (tables, fields, access in plain words, error meanings)
  and includes a dependency-free `datbase.ts` client. It never names the engine underneath.
  Example: `docs/DATBASE.example.md`. Acceptance test for Codex: `TESTING-AI.md`
- **Connect:** API URL and copy-paste code examples on the project overview
- PocketBase's own admin panel is blocked on project URLs
- **Backups:** every project daily + DATBase's own database daily, encrypted, stored off-server in
  DigitalOcean Spaces. Keeps one backup for each of the last 7 days + one per week for 4 weeks
  (manual and "before restore" backups are kept on top for 7 days); 14 for DATBase's database. Backups tab:
  list, "Back up now" (once per 10 min), and Restore (typed confirmation; a safety backup is taken
  first, so any restore can be undone).
- **Security headers:** nonce-based CSP, clickjacking protection, HSTS over HTTPS (`app/src/proxy.ts`)
- **Usage & limits:** API requests per month, database size, file storage and app users are
  measured every 10 minutes (plus a Refresh button). Warning at 80%. At 100%:
  - requests used up → the project's API returns 429 until next month (dashboard keeps working)
  - database or storage full → new/edited records are blocked; reading and deleting still work
  - user limit → new sign-ups are blocked
  Limits lift automatically once usage drops (or the month rolls over). Free plan limits are in
  `app/src/lib/plans.ts`; enforcement and request counting run inside each project via
  `pocketbase/pb_hooks/datbase.pb.js`, unlocked only by a per-project secret, so project admins can't bypass them.

```
app/              Next.js dashboard + control plane (Drizzle, Better Auth, dockerode)
pocketbase/       Image every project runs
docker-compose.yml
.env.example
```

## Deploy on DigitalOcean

1. **Droplet:** Ubuntu 24.04, at least 2 GB RAM. Allow ports 22, 80 and 443 in the firewall.
2. **DNS:** point both at the Droplet IP:
   - `A  yourdomain.com`
   - `A  *.yourdomain.com`
3. **Install and start:**
   ```bash
   curl -fsSL https://get.docker.com | sh
   git clone <your repo> /opt/datbase    # or scp this folder
   cd /opt/datbase
   cp .env.example .env
   # fill in DOMAIN; POSTGRES_PASSWORD and BETTER_AUTH_SECRET: `openssl rand -hex 32`
   # ENCRYPTION_KEY: `openssl rand -base64 32` (must be base64). Keep a copy of .env off the server.
   docker compose up -d --build
   ```
4. Open `https://yourdomain.com`, sign up, and create a project.

Update: `git pull && docker compose up -d --build`. Database migrations run automatically on start.

## Email

Fill `SMTP_*` in `.env` (any provider: Resend, Brevo, Postmark, Amazon SES, a Gmail app password) and
restart. That turns on:
- **Dashboard accounts:** email confirmation before the first sign-in, and "Forgot your password?".
  Sign-up no longer reveals whether an email is registered (the owner gets a "you already have an
  account" email instead).
- **Your customers' app users:** password reset and email confirmation, documented in DATBASE.md
  (`requestPasswordReset()`, `requestVerification()`). Emails come from your `SMTP_FROM` address with the
  app's name, and the links open pages hosted by DATBase (`/u/<project>/…`), so customers don't build any.
  The project's hook writes every email itself (a project admin can't change sender, wording or links)
  and caps each project at `PROJECT_MAIL_PER_HOUR`.
- Access option **"Signed-in users with a confirmed email"** for tables.

Without SMTP everything works, minus those emails. Accounts created before email existed stay usable.

## Monitoring & alerts

Every 5 minutes DATBase checks: projects not responding or in error, backups older than 36 h or
failed, its own database backup, disk and memory. A problem must show up twice in a row before it
alerts; you also get a message when it clears.
- **Alerts** go to `ADMIN_EMAIL` and, optionally, `ALERT_WEBHOOK_URL` (Home Assistant, Discord, Slack).
- **Server status page:** sign in with `ADMIN_EMAIL` → *Server status* (run checks, send a test alert,
  re-apply email settings to all projects).
- **Home Assistant:** set `MONITOR_TOKEN`, then use `integrations/home-assistant/` (sensors, a
  problem binary sensor, phone notifications and a dashboard card). The JSON is at `GET /api/monitor`
  with `Authorization: Bearer <MONITOR_TOKEN>`.

Testing on a Raspberry Pi first? See `docs/RASPBERRY-PI.md`.

## Operator tools (ADMIN_EMAIL account)

- **Update all projects** (Server status): after `docker compose up -d --build`, projects keep running
  the old project image until updated. The page shows how many are outdated; the button gives each
  one a fresh container with its data kept (a few seconds offline each; paused/suspended stay stopped;
  limits and email settings are re-applied). Do this after every update that touches `pocketbase/`.
- **Accounts & projects:** every account with its projects and this month's requests. **Suspend** a
  project (with a reason the owner sees) or a whole account (signed out, can't sign in, projects
  stopped). Lifting restores exactly what was running before.

Owners have **Account** (click your email): download each project's data (JSON + files zip) and
delete the account (all projects, data and backups). Owners also get an email at **80% and 100%** of
each plan limit (once per month each).

## Backups & disaster recovery

### Set up (once)
1. DigitalOcean → Spaces → create a bucket (e.g. `datbase-backups`, private). Pick a different
   region from your Droplet if you can.
2. Spaces → Access Keys → create a key with access to that bucket.
3. Fill `BACKUP_S3_*` in `.env`, then `docker compose up -d`. The log shows
   `[backup] checking every 15 min`. If it says `backups are OFF`, a value is missing.
4. **Save your whole `.env` in a password manager, off the server.** Backups are encrypted with
   `ENCRYPTION_KEY`. If the server dies and you don't have that key, the backups can't be read.

### If the server is lost
1. New Droplet, same DNS records, install Docker, copy this repo, and put back the **same `.env`**.
2. Start the database only: `docker compose up -d db`
3. Restore DATBase's own database from the newest backup:
   `docker compose run --rm app node scripts/backup-tool.mjs restore-control`
4. Add `RECOVER_ON_START=1` to `.env`, then `docker compose up -d --build`. Every active or paused
   project whose container is missing is recreated and loaded from its newest backup (paused ones stay paused)
   (`docker compose logs -f app` shows `[recover] … restored backup …`).
5. Remove `RECOVER_ON_START` from `.env` and run `docker compose up -d`.

Other tool commands: `list [prefix]` shows stored backups, and `get <key> <file>` decrypts one
(a project backup is a PocketBase zip).

## Test checklist (Stages 1–5)
- [ ] Sign up, sign out, sign in
- [ ] Create a project → goes from `creating` to `active` in under ~30s
- [ ] Create a table in project A; it does not exist in project B
- [ ] A second account cannot see or open the first account's projects
- [ ] Pause → the project's URL stops responding; Resume → data still there
- [ ] Delete → container and data are gone (`docker ps -a`, `docker volume ls`)
- [ ] `docker stats`: note RAM per idle project
- [ ] Tables: create `products`, add fields, add a record with an image
- [ ] Set `products` list/view to "Anyone", then open
      `https://<project>.yourdomain.com/api/collections/products/records` in a browser
- [ ] `https://<project>.yourdomain.com/_/` returns 404
- [ ] Usage tab shows numbers after ~10 minutes (or press Refresh)
- [ ] Optional limit test: add `PLAN_LIMITS_OVERRIDE={"requestsPerMonth":20}` to `.env`,
      `docker compose up -d`, call the API 25 times, wait a minute, Refresh → API returns 429.
      Remove the line afterwards. (Blocks lift at the next month; to reset early, delete the
      project's rows in `usage_month`.)
- [ ] Backups tab → Back up now → a row appears; the bucket has `projects/<id>/….dbk`
- [ ] Change some data → Restore that backup → data is back; a "Before restore" backup exists
- [ ] `docker compose run --rm app node scripts/backup-tool.mjs list` shows project + `control/` backups
- [ ] Next day: a "Daily" backup appeared on its own

## Automated tests
`cd app && npm run test:unit` (no services needed) and `npm run test:e2e` (full stack). See `app/tests/README.md`.

## Local development
```bash
cd app
cp ../.env.example .env.local   # add DATABASE_URL and BETTER_AUTH_URL=http://localhost:3000
# optional, to test the Tables screens against a local PocketBase on port 8090:
# PB_INTERNAL_URL=http://127.0.0.1:8090
npm install
npm run dev
```
Provisioning needs Docker locally. Without it, projects show `error` with the reason.
Running the full stack with `docker compose` on a laptop? Project APIs live on the private Docker
network; `scripts/local-api.sh <slug>` makes one reachable at `http://127.0.0.1:8090` for local apps.
After changing `src/db/schema.ts`, run `npx drizzle-kit generate`.

## Known limits (for later stages)
- **HTTPS certificates:** each project subdomain gets its own Let's Encrypt certificate
  (limit: 50 new per week per domain). Before scaling, switch to one wildcard
  certificate using DigitalOcean DNS validation.
- **Docker socket:** the app controls Docker directly, so it effectively has root on the
  Droplet. Fine for a single-server MVP. Isolate it before adding more servers.
- **Without SMTP settings** there are no reset/confirmation emails and sign-up reveals whether an
  email is registered (capped at 5 sign-ups per IP per hour). Set `SMTP_*` in production.
- **Monitoring** runs inside the app: if the whole server or the app is down, it can't alert. Home
  Assistant's "unreachable" automation (integrations/home-assistant) covers that case.
- **Bandwidth** isn't measured yet (needs proxy logs).
- **PocketBase version:** pinned to 0.39.11. Before moving to 0.40+, run `app/tests/e2e/pb-stress.mjs`
  against a test project on your real server (see FIXES.md, AI-01).
- **Licence:** PocketBase is MIT-licensed; its LICENSE.md ships inside the project image. Keep it.
