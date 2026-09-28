# DATBase — Full Test & Security Review (instructions for Codex)

You are testing **DATBase**, a Backend-as-a-Service dashboard. Your job is to **use every feature,
try to break it, try to abuse it, and write a findings report**. Do **not** fix code during the test;
only report. You may include a suggested patch in each finding.

Deliverable: **`REPORT.md`** in the repo root, following the template at the end of this file.
Save evidence (screenshots, request/response logs, scripts) under `test-evidence/`.

---

## 1. What you are testing

```
Browser ──> DATBase dashboard (Next.js 16, app/)  ──> Postgres (control plane: accounts, projects, usage)
                    │
                    └──> one PocketBase per customer project (auth, tables, files, REST API)
                            └─ pocketbase/pb_hooks/datbase.pb.js (usage report + limit enforcement)
Customer apps ─────────> PocketBase REST API directly (https://<slug>.<domain>/api/...)
```

- **Accounts** (Better Auth, email + password): sign up, sign in, sign out.
- **Projects**: create (max 2 on the Free plan), rename, pause, resume, retry, delete (type the name to confirm).
  In production each project is a Docker container; in local testing you link a local PocketBase instead (see §2).
- **Tables**: create/rename/delete tables, add/remove fields (text, number, bool, email, url, date,
  select, json, file, relation), per-table access rules (Anyone / Signed-in users / Server only).
- **Data browser**: create/edit/delete records, file upload/removal, filter, sort, pagination.
- **Users tab**: the project's app users (PocketBase `users` auth collection).
- **Usage tab**: API requests/month, database size, file storage, app users; limits enforced inside PocketBase.
- **File proxy**: `/dashboard/<projectId>/files/<collectionId>/<recordId>/<filename>`.
- **Overview**: API URL, code examples, reveal the server admin password.

Key files: `app/src/lib/{auth,pb,projects,usage,plans,docker,provision,crypto}.ts`,
`app/src/app/dashboard/**` (pages + server actions in `actions.ts`), `pocketbase/pb_hooks/datbase.pb.js`,
`docker-compose.yml`.

**Security model to verify:** a signed-in account must only ever reach its own projects. Every page and
server action loads projects via `findOwnedProject(id)` (id **and** owner). Customer apps reach only
their own PocketBase. Secrets (`ENCRYPTION_KEY`, `BETTER_AUTH_SECRET`, PocketBase admin passwords)
must never reach the browser except the admin password when the owner explicitly clicks "Reveal".

---

## 2. Setup (local, no Docker needed)

Only test against **localhost**. Never send traffic to external hosts.

### 2.1 Postgres
Use any local Postgres 14+. Create a database `datbase_test`.
```bash
# e.g. if Docker is available:
docker run -d --name dbt-pg -e POSTGRES_PASSWORD=pg -p 5432:5432 postgres:16-alpine
# otherwise: apt-get install -y postgresql && service postgresql start
createdb -h localhost -U postgres datbase_test
```

### 2.2 Two PocketBase instances (= two isolated customer projects)
```bash
mkdir -p /tmp/pb && cd /tmp/pb
curl -fsSL -o pb.zip https://github.com/pocketbase/pocketbase/releases/download/v0.39.11/pocketbase_0.39.11_linux_amd64.zip   # _linux_arm64.zip on ARM
unzip -o pb.zip
HOOKS="$REPO/pocketbase/pb_hooks"   # set REPO to the repo root
for port in 8090 8091; do
  ./pocketbase superuser upsert admin@local.test SuperSecret123 --dir=data$port
  # DATBase's per-project secret (same as app/src/lib/crypto.ts controlSecret(slug)); needs ENCRYPTION_KEY from §2.3
  export DATBASE_CONTROL_SECRET=$(node -e 'process.stdout.write(require("crypto").createHmac("sha256",Buffer.from(process.env.ENCRYPTION_KEY,"base64")).update("datbase-control:"+process.argv[1]).digest("base64url"))' $port)
  nohup ./pocketbase serve --http=127.0.0.1:$port --dir=data$port --hooksDir=$HOOKS --automigrate=false > pb$port.log 2>&1 &
done
```
Use the same flags as production (`--hooksDir`, `--automigrate=false`); see `pocketbase/Dockerfile`.

### 2.3 App
```bash
cd app && npm ci
cat > .env.local <<EOF
DATABASE_URL=postgres://postgres:pg@localhost:5432/datbase_test
BETTER_AUTH_SECRET=$(openssl rand -base64 32)
BETTER_AUTH_URL=http://localhost:3000
ENCRYPTION_KEY=$(openssl rand -base64 32)
DOMAIN=example.test
PB_INTERNAL_URL=http://127.0.0.1:{slug}
USAGE_INTERVAL_MIN=1
EOF
npm run build && cp -r drizzle .next/standalone/ && cp -r .next/static .next/standalone/.next/
set -a && . ./.env.local && set +a
node .next/standalone/server.js      # production build; runs DB migrations on start
```
`PB_INTERNAL_URL=http://127.0.0.1:{slug}` means a project whose slug is `8090` talks to the PocketBase on port 8090.

### 2.4 Link projects to accounts
Sign up two accounts in the UI (e.g. `alice@test.local`, `bob@test.local`), then:
```bash
cd app && set -a && . ./.env.local && set +a
node scripts/link-local-project.mjs alice@test.local "Alice Shop" 8090
node scripts/link-local-project.mjs bob@test.local   "Bob Blog"   8091
```
Each command prints the project's dashboard URL. Creating a project **from the UI** will end in
status `error` locally (no Docker socket). That is expected. Still test the UI flow, the limit,
retry, rename and delete on such projects.

### 2.5 Optional: full stack with Docker
If a Docker daemon is available, also run the real stack (`cp .env.example .env`, fill values, set
`DOMAIN=localhost`, `docker compose up -d --build`) and repeat §3.2, §3.9 and §3.12 against real
containers. Record which mode each result came from.

### 2.6 Tiny limits for the usage tests
Restart the app with `PLAN_LIMITS_OVERRIDE='{"requestsPerMonth":20,"users":2,"storageBytes":300000,"projects":2}'`.
Note: request logs are counted only once they are older than 60 seconds; wait ~65 s before pressing **Refresh**.

---

## 3. Test areas

For each item: do it, record **expected vs actual**, keep evidence. Items marked 🔓 are abuse or
security tests. Use browser automation (Playwright) for UI flows, and `curl` or scripts for API or abuse
tests. **Replaying server actions:** capture a real server-action request in the browser (POST with a
`Next-Action` header), then replay it with `curl`, changing the IDs or cookies.

### 3.1 Accounts
1. Sign up (valid). Then sign up again with the same email, an invalid email, a password of 7 characters,
   an empty name, a 5,000-character name, and a name with `<script>alert(1)</script>` (check where the name renders).
2. Sign in: wrong password, unknown email (🔓 do the error messages reveal whether the email exists?),
   different email casing, leading/trailing spaces.
3. Sign out, then press Back and reload protected pages; reuse the old session cookie with `curl`. 🔓
4. 🔓 Session cookie flags (HttpOnly, Secure, SameSite, lifetime). Two browsers signed in at once.
   Does signing out in one invalidate the other?
5. 🔓 Brute force: 50 rapid wrong logins for one account. Is there rate limiting or a lockout?
6. 🔓 Verify passwords are hashed in `account.password` (never plaintext).
7. Unauthenticated access to every `/dashboard/**` URL redirects to `/login`.

### 3.2 Projects
1. Create with names: 1 char, 2 chars, 50, 51, whitespace only, emoji, HTML/script, SQL-like `'; drop table project;--`.
2. Plan limit (2 projects): create a 3rd from the UI; 🔓 **send 5 create requests in parallel**
   (replay the server action concurrently). Can you exceed the limit?
3. Slug: two projects with the same name get distinct slugs? Is the slug ever taken from user input? 🔓
4. Rename (valid, invalid, XSS payload). Pause/resume (local: expect Docker errors; note how the UI handles them).
   Retry on an `error` project. Delete with wrong or partial confirmation text, then the exact name.
5. Double-click "Create project" and "Delete project". Do you get duplicates or errors?
6. Status polling: a project in `creating` auto-refreshes; does polling stop afterwards?

### 3.3 Tenant isolation 🔓 (highest priority)
As **bob**, try to read or modify **alice's** project in every way:
1. Open alice's `/dashboard/<id>`, `/tables`, `/tables/<t>`, `/tables/<t>/settings`, `/tables/<t>/<recordId>`,
   `/tables/users`, `/usage`. Expect 404.
2. Replay **every server action** with alice's project ID and bob's cookie: renameProject, pauseProject,
   resumeProject, retryProject, deleteProject, revealAdminPassword, refreshUsage, createTable, renameTable,
   deleteTable, addField, removeField, saveRules, saveRecord, deleteRecord.
3. File proxy: alice's file URL with bob's cookie, and with no cookie. Also try path tricks in
   `collection`/`record`/`name` (`..%2f`, `%00`, very long names, another project's collection ID).
4. Tamper with the `table`, `recordId` and `fieldId` form values (e.g. `_superusers`, `_otps`, `../`,
   `users/records`, URL-encoded slashes). Can any request reach a PocketBase path other than the intended one?
5. Can a project's app user token (from `/api/collections/users/auth-with-password`) do anything on the
   dashboard or on **another** project's PocketBase?

### 3.4 Tables & fields
1. Table names: valid, starting with a digit, with spaces, 51 chars, reserved (`users`, `_superusers`,
   `_otps`, `_mfas`), SQL keywords (`select`, `order`), a duplicate name (different case).
2. Add each field type. Choice with no values or duplicate values. File max size 0, 100, 101 MB.
   Relation to itself and to `users`. Required + multiple combinations. Duplicate field names.
   Reserved field names (`id`, `created`, `collectionId`, `expand`).
3. Remove fields (including one that has data). Try to remove built-in fields by tampering with `fieldId`. 🔓
4. Rename a table (including to an existing name), then check that relations, the data browser and URLs still work.
5. Delete a table that other tables link to.
6. Can the `users` table be deleted or renamed? What happens to sign-in afterwards?

### 3.5 Records (data browser)
1. Create/edit/delete a record for every field type, with empty, boundary and invalid values: huge numbers,
   `NaN`, 1 MB of text, emoji, invalid JSON, deeply nested JSON, dates at the extremes, bad URLs and emails.
2. Files: upload normal, empty (0 B), over max size, many files at once. Filenames with unicode, spaces,
   `../`, very long names. 🔓 Upload `.html` and `.svg` containing script, then open them via the
   dashboard file proxy **and** via the public PocketBase URL. Does script run on either origin?
   Check the `Content-Security-Policy: sandbox` and `nosniff` headers.
3. Remove an existing file while uploading a new one in the same save.
4. Pagination with 1,000+ records (seed through the API). Sort by every column, and `?sort=` with tampered values. 🔓
5. Filter: valid expressions, syntax errors, very long filters, 🔓 filters referencing other collections
   (`@collection._superusers.email != ""`). The owner is superuser of their own project, so reading
   their own data is fine. Check whether anything crosses projects or crashes the page.
6. Relation dropdown when the target table has more than 200 records. Is anything missing or broken?
7. Edit the same record in two tabs at once (last write wins?).
8. XSS 🔓: put `<img src=x onerror=alert(1)>` in text, JSON, choice values, table names and field names,
   then view the list, the edit form, settings and the usage page.

### 3.6 App users (Users tab + API)
1. Create a user in the dashboard, sign in via the API, change their password in the dashboard, and check
   the old token or password still works (PocketBase normally invalidates on password change).
2. Duplicate email, invalid email, short password, editing without changing the password.
3. Sign-up through the API when createRule = Anyone vs Server only.
4. Delete a user who owns records linked via a relation.

### 3.7 Access rules (API as a customer app)
For a table, set each operation to each preset and verify with `curl` as **anonymous**, as a
**signed-in app user**, and as **superuser**: list, view, create, update, delete. Also:
1. Set a custom rule directly in PocketBase (as superuser via the API), then check the dashboard shows
   "Custom rule (keep)" and saving other operations keeps it.
2. 🔓 Default rules for new tables are "Server only". Is anything readable anonymously by default?
3. 🔓 Can an app user read other app users' emails (`users` list/view rules, `emailVisibility`)?

### 3.8 Usage & limits (use §2.6 tiny limits)
1. Press Refresh on the Usage tab. Check the database, storage and user numbers are plausible.
2. Make 25 anonymous API calls, wait 65 s, press Refresh. The count should be 25 exactly (dashboard
   traffic and admin logins excluded). Then the customer API returns 429 while the dashboard still works.
3. Users limit: the 3rd user is blocked (dashboard and API sign-up). Storage limit: upload past 300 KB,
   then new and edited records are blocked while reading and deleting still work.
4. Clean up (delete the file and a user), press Refresh: writes and sign-ups unblock, the monthly request block stays.
5. 🔓 Try to bypass the limits: as an app user or anonymously, call `POST /api/datbase/limits` and
   `GET /api/datbase/usage`. Send requests that avoid being logged or counted (auth endpoints, 404s,
   `/api/health`, realtime `/api/realtime`, batch `/api/batch`, file downloads `/api/files/...`).
   Which requests are free but still cost server resources?
6. Month rollover: change `usage_month.month` to the previous month and press Refresh. The block should lift.
7. The background loop (`USAGE_INTERVAL_MIN=1`): stop one PocketBase. Does the loop keep working for the other
   project and log a clear error? Restart the stopped PocketBase: is state restored? (Limits persist in
   `datbase_limits.json`.)
8. Delete the project's `project_usage` row and press Refresh. Is anything double-counted?

### 3.9 Overview, secrets and leaks 🔓
1. Server keys (Overview): shown once, only to the owner (see §3.3); revoked keys stop working; a restore can't revive them. 🔓 A key or admin login must NOT reach `/api/datbase/*` (expect 404).
2. Search the client bundle and every HTML/RSC response for secrets:
   `grep -rE "ENCRYPTION_KEY|BETTER_AUTH_SECRET|admin_password_enc|adminPasswordEnc|SuperSecret123" .next/static`
   and inspect page responses (`curl` with the `RSC: 1` header) for `adminPasswordEnc` or other server-only fields.
3. Error handling: stop Postgres, then stop a PocketBase. Load pages and trigger actions. Do responses leak
   stack traces, internal URLs (`127.0.0.1:8090`, `dbp-…`), SQL or file paths?
4. Response headers on dashboard pages: CSP, `X-Frame-Options` / `frame-ancestors` (clickjacking),
   HSTS, Referrer-Policy. Try embedding the dashboard in an iframe from another local origin.
5. CSRF: send a sign-out, server action or auth request from a different Origin (e.g. a page served from
   `http://127.0.0.1:5555`). Is it rejected?
6. Open redirects: any parameter that controls where you are redirected?
7. The PocketBase token cache is per server process (`app/src/lib/pb.ts`): restart PocketBase (the old token
   becomes invalid). Does the dashboard recover on the next request?

### 3.10 Resilience & performance
1. Slow PocketBase: suspend it (`kill -STOP`), load pages, then resume it (`kill -CONT`). Are there timeouts and friendly messages?
2. Tables list with 50 tables (it does one count request per table). How long does it take?
3. Records list with large text/JSON values: page size and render time.
4. Run 20 concurrent users hitting the dashboard for a few minutes (autocannon, k6 or a script). Check
   memory growth of the Node process and the Postgres connection count (pool max = 10).

### 3.11 UX & accessibility (lighter pass)
Keyboard-only navigation, labels on inputs, focus states, mobile width (375 px), dark mode, clarity of
error messages, empty states, and whether destructive actions always confirm.

### 3.13 Backups & restore
Setup: any S3-compatible store (e.g. `pip install "moto[server]" && moto_server -p 5055`, create a bucket)
and set `BACKUP_S3_ENDPOINT=http://127.0.0.1:5055 BACKUP_S3_FORCE_PATH_STYLE=true BACKUP_S3_BUCKET=… BACKUP_S3_KEY=x BACKUP_S3_SECRET=x BACKUP_CHECK_MIN=1`.
1. Back up now, change data (records, files, users, tables), restore, and compare everything. Undo via the "Before restore" backup.
2. 🔓 Objects in the bucket must be encrypted (no plaintext; starts with `DBK1`). Flip one byte and restore → refused, data untouched.
3. 🔓 Try to restore another project's backup ID (replay `restoreAction` with a foreign `backupId`), start restores in
   parallel, restore while paused, delete during a restore, spam "Back up now" (cooldown), start two backups at once.
4. Kill the app mid-backup and mid-restore, then restart: rows are marked failed and the project is set to error with a message.
5. Retention (7 newest + 4 weekly), control DB backup (`control/`), `scripts/backup-tool.mjs list|get|restore-control`.
6. 🔓 Check that the S3 keys never reach a project container or the browser (`docker inspect`, bundle grep, PocketBase settings).

### 3.12 Infrastructure (only with Docker, §2.5)
Provisioning time. Container memory and CPU caps are applied. Project containers **cannot** reach the
Postgres container (`internal` network). `/_/` on a project URL returns 404. Pause really stops the
container. Delete removes the container **and** the volume. Resource use with 10 idle projects.
The app container mounts the Docker socket: note the blast radius.

---

## 4. Already known (don't report as new, unless you find a worse impact)
- No password reset or email verification (no email provider yet).
- No backups yet. Bandwidth is not measured.
- Per-subdomain TLS certificates (Let's Encrypt rate limits) and the Docker socket mounted in the app container.
- Dates entered in the data browser are treated as UTC.
- Creating projects from the UI fails locally without Docker (expected in local mode).
- Sign-up reveals whether an email is registered (accepted until email verification; rate-limited).
- "Build with AI" (DATBASE.md) has its own acceptance test: TESTING-AI.md.
- "Build with AI" review AI-01…AI-05 are fixed (FIXES.md). The admin password is no longer shown; use server keys.
- Backup review findings R-01…R-07 are fixed (FIXES.md). Automated suites: `app/tests/` (`npm run test:unit`, `npm run test:e2e`). Run them first.
- Findings F-01…F-09 from the first run are fixed. See FIXES.md, and re-test them.

---

## 5. REPORT.md template

```markdown
# DATBase Test Report
Date: … | Commit/zip: … | Mode: local / docker | Tester: Codex

## Summary
- Tests run: N | Passed: N | Failed: N | Findings: Critical N, High N, Medium N, Low N, Info N
- Top 3 risks (one line each)

## Coverage
| Area (§) | Tested? | Pass / Fail | Notes |
|---|---|---|---|
| 3.1 Accounts | ✅ | 6 / 1 | … |
… every area 3.1–3.12, including what was NOT tested and why

## What works
Short bullet list of features confirmed working.

## Findings
### F-01 <short title>
- **Severity:** Critical / High / Medium / Low / Info
- **Area:** §3.x
- **Type:** bug / security / leak / performance / UX
- **Steps to reproduce:** numbered, exact requests or clicks
- **Expected:** …
- **Actual:** …
- **Evidence:** test-evidence/F-01-*.png|txt
- **Suggested fix:** file + approach (patch optional)

(repeat, sorted by severity)

## Improvements (not bugs)
UX, performance and developer-experience suggestions, each with a one-line rationale.

## Test scripts
List of scripts in test-evidence/ and how to re-run them.
```

**Severity guide:** *Critical*: cross-tenant data access, secret leak, or account takeover.
*High*: limit or permission bypass, stored XSS, data loss. *Medium*: crash, wrong data, missing
security headers or rate limits. *Low*: validation gaps, confusing errors. *Info*: observations.
