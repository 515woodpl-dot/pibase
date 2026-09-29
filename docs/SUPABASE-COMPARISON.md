# DATBase vs Supabase: what we have, what's missing, and why

Written 2026-09-28 from the code as it stands (schema import just shipped). Honest, not marketing:
if a row says "Missing", nothing in the repo does it. Verify against code before promising anything
to a customer. When a feature ships, update the row and the date.

## The one-paragraph difference

**Supabase** = a shared Postgres platform. You get real SQL, row-level security written as SQL,
serverless functions, realtime, and dozens of extensions. Its power comes with complexity.

**DATBase** = one small, isolated backend per project (its own container, database file and
storage folder). You get tables, a ready REST API, sign-in for your app's users, file storage,
preset access rules and daily backups, all driven from a simple dashboard or one pasted JSON.
No SQL, no functions. Its power is that it is small, cheap (runs on a Raspberry Pi), easy for a
non-expert to operate, and easy for an AI to use correctly from a single generated file (`DATBASE.md`).

Good fit: catalogs, stores' product/cart/order data, CRMs, inventory, bookings, blogs, dashboards
of records, internal tools, mobile-app backends.
Poor fit today: reporting/analytics, anything needing real SQL, realtime collaboration, apps whose
business logic must run "inside the database".

## Feature by feature

Status: **Have** = works and is tested. **Partial** = works with a real limitation. **Missing** = not built.

### Data

| Feature | Supabase | DATBase | Status | Why / notes |
|---|---|---|---|---|
| Create tables and fields | SQL or table editor | Table editor, or **paste one JSON** (Tables → Import schema) | Have | Import understands SQL/Supabase type names, extends existing tables, all-or-nothing. |
| Field types | Every Postgres type | text, number, yes/no, email, URL, date, choice, JSON, file, link to table | Have (smaller set) | Deliberately small: each one has a form, validation and docs. Arrays become JSON; money is a number. |
| Relations | Foreign keys, joins | Link fields; `expand` returns the linked record in one call | Partial | One level of "give me the related record". No arbitrary joins. |
| Unique fields / combinations | Constraints | Yes, enforced by the database (even under 10 concurrent requests) | Have | |
| Number limits, required fields | CHECK / NOT NULL | Min/max, whole numbers, required | Have | |
| Auto REST API | PostgREST | Yes: list/get/create/edit/delete, filter, sort, paginate | Have | Filter syntax is PocketBase's, documented in `DATBASE.md`. |
| Generated client | supabase-js | Zero-dependency `datbase.ts` inside `DATBASE.md` | Have | No npm package or React hooks (Partial vs Supabase's ecosystem). |
| **SQL access** | SQL editor, connection string | None | **Missing** | Engine is SQLite inside PocketBase. This is the core trade-off (small, one folder to back up, one container per project). |
| Aggregates (sum, count, group by) | Yes | None (only "total items" on a list) | **Missing** | Same cause. Workaround: fetch with `listAll()` and compute in your server. Possible future fix: PocketBase view collections. |
| Views, functions, triggers, stored procedures | Yes | None | **Missing** | No SQL. Business logic lives in your app's server. |
| Transactions across several writes | Yes | Not offered | **Missing** | An order + its items are separate writes today; write them from your server and clean up on failure. |
| Full-text search, vectors (pgvector) | Yes | Only "contains" filter | **Missing** | Needs engine features SQLite/PocketBase don't offer in this setup. |
| Realtime subscriptions | Yes | Not exposed or tested | **Missing** | The engine can do it; DATBase doesn't document, meter or test it through the proxy. Needs work on limits and proxy behaviour first. |
| Versioned migrations, CLI, branching | Yes | Import is additive only (no rename/retype/delete); no CLI | **Missing** | Deliberate for safety: import never destroys data. Changing/removing fields is done in table settings. |

### Access control

| Feature | Supabase | DATBase | Status | Why / notes |
|---|---|---|---|---|
| Row-level security | Any SQL policy | Per table, per action (list/view/create/edit/delete): Anyone, Signed-in, Confirmed email, Only its owner, Server only | Partial | Presets cover most apps and are what an AI can get right. **No custom rule expressions in the UI** (the engine supports them; existing hand-written rules are kept but can't be authored). |
| Column-level privacy | Views/grants | None: a table's fields are all visible to whoever can read it | **Missing** | Put private fields in a separate, owner-only table. |
| Anon key | Public key + RLS | **No key at all**: public access = the table says "Anyone" | Have (different) | Nothing to leak. |
| Service-role key | Bypasses RLS | **Server key**: created in the dashboard, shown once, expires, revocable instantly | Have | Never in browser code. |

### Users and sign-in (for your app's customers)

| Feature | Supabase | DATBase | Status | Why / notes |
|---|---|---|---|---|
| Email + password | Yes | Yes: sign up, sign in, refresh, sign out | Have | |
| Email confirmation, password reset | Yes | Yes, when SMTP is set (hosted pages, no UI to build) | Have | Needs `SMTP_*`. Sender/wording fixed by DATBase (anti-phishing). |
| OAuth (Google, GitHub…), magic link/OTP, phone, MFA | Yes | **No** | **Missing** | Each needs per-project provider setup and safe redirect handling. Not started. |
| User metadata/profiles | `auth.users` + your table | `users` table you can add fields to | Have | |
| Roles | Custom claims | Add a field/table and use server-only writes | Partial | No built-in role system. |

### Files

| Feature | Supabase | DATBase | Status | Why / notes |
|---|---|---|---|---|
| File uploads | Storage buckets | File fields on records (single/multiple, size limits) | Partial | Files belong to a record and follow that table's access rules. **No standalone buckets, signed URLs API or image transformations.** |

### Server-side logic

| Feature | Supabase | DATBase | Status | Why / notes |
|---|---|---|---|---|
| Edge/serverless functions | Yes | **None** | **Missing** | Running customer code safely needs sandboxing and resource limits per project. Big feature, not started. |
| Webhooks, database events, cron | Yes | **None** | **Missing** | Same. |
| Sending your own emails | Via functions | Only sign-in emails | **Missing** | |
| Payments (e.g. Stripe checkout) | Function + webhook | Your own small server with a server key | **Missing (by design, for now)** | So a store's *checkout* needs a server you run; browsing, carts and order history work without one. |

### Operating it

| Feature | Supabase | DATBase | Status | Why / notes |
|---|---|---|---|---|
| Isolation between projects | Separate project infra | One container + data volume per project | Have | |
| Backups | Daily (paid: point-in-time) | Daily encrypted off-server + restore with automatic safety backup; 7 daily + 4 weekly | Have | Up to a day of loss; **no point-in-time recovery**. |
| Data export / delete account | Manual | Owner can download JSON + files; delete account removes everything | Have | |
| Usage limits & metering | Plans | Requests/month, database size, storage, users; warnings at 80%, blocks at 100% | Have | Only a Free plan exists. |
| Monitoring & alerts | Dashboard | Checks every 5 min, email/webhook alerts, Home Assistant | Have | Runs inside the app (can't alert if the whole server is down; HA covers that). |
| Logs / analytics UI | Yes | Usage tab only | **Missing** | |
| Team members, roles on a project | Yes | One owner per project | **Missing** | Not started. |
| Billing / paid plans | Yes | None (Free only) | **Missing** | Stage for later. |
| Custom domain per project | Yes | Projects live at `<slug>.<your domain>` | **Missing** | Not started. |
| High availability, scaling, regions | Yes | One server; Docker socket = root on the host | **Missing** | Fine for an MVP/home server; isolate before multi-server. |
| Self-hosting on a Raspberry Pi | Heavy | `scripts/setup-pi.sh`; each project is capped at 128 MB RAM by default | Have | A real advantage. |
| Compliance (SOC 2, SSO for the dashboard) | Yes | None | **Missing** | |

## The "AI knows Supabase" gap, and how DATBase closes it

An AI writes good Supabase code from memory because Supabase is all over its training data. It
knows nothing about DATBase. So every project generates **`DATBASE.md`** (Overview → *Download DATBASE.md*):
exact tables, fields, access in plain words, error meanings, the API, and a copy-paste client. The
AI reads that one file and is then as accurate as it would be with Supabase. When the app needs new
tables, `DATBASE.md` tells the AI to **write a schema import** (JSON) for the owner to paste, then
download a fresh `DATBASE.md`. That loop (write import → paste → re-download → build) replaces
"paste SQL into the SQL editor".

Rule that makes it work: **the AI never gets schema-changing power over the API.** A leaked server
key can read and write data but can never create tables or loosen access.

## Moving an app from Supabase to DATBase

| Supabase | DATBase |
|---|---|
| Project URL | `https://<slug>.<your domain>` |
| Anon key + RLS policies | No key; set each table's access |
| Service-role key | Server key |
| `supabase.from('t').select()` | `db.list('t', …)` from `datbase.ts` |
| `auth.users` | The `users` table |
| Storage bucket | A file field |
| SQL migration | Schema import JSON |
| Edge function / RPC | Your own server (no equivalent yet) |

For trying DATBase next to an existing Supabase app without risk, use the write-only mirror in
`docs/SECONDARY-BACKEND.md`.

## If we build next (my ranking, not a commitment)

1. **A server-side story** (webhooks or small sandboxed actions). Unblocks checkout, emails on events and most "but I need one function" apps. Biggest gap, biggest effort.
2. **Custom access rules** (an advanced box, validated and explained in `DATBASE.md`). Small effort, removes the main RLS limitation.
3. **Aggregates/views** (counts, totals, reports) via engine view collections.
4. **OAuth and magic-link sign-in.**
5. **Realtime**, once proxying and limits are proven.
6. Team members, paid plans, custom domains.
