# PIBASE: what it is, and a briefing for Claude Design

Written from `main` at commit `e983dda` (PR #12). Sources: `README.md`, `PLAN.md`,
`docs/COMPATIBILITY.md`, `docs/PI-OPERATIONS.md`.

## 1. What PIBASE is

PIBASE is our own small backend, shaped like Supabase, that runs on a Raspberry
Pi 4. One Go gateway and one PostgreSQL 16 cluster give a website everything it
needs from a backend, so a site doesn't depend on a hosted service.

| Part | What a site gets |
|---|---|
| Database + data API | Real Postgres tables, with row level security deciding who can read and write. Calls go to `/rest/v1/...` (filters, ordering, limit/offset, one-level relations, insert, update, delete, upsert). |
| Auth | Email + password sign-up, confirmation, sign-in, rotating refresh tokens, sign-out, password recovery, optional authenticator-app (TOTP) two-step sign-in. |
| Storage | Buckets and files: upload, download, public and signed links, list, delete. |
| Realtime | Live table changes, broadcast and presence over a WebSocket. |
| Functions | SQL functions in `public`, called at `/rest/v1/rpc/<name>`. There are no Edge Functions. |
| Dashboard | A web app on port 18081: sign up, then projects, API keys, tables, users, a SQL editor, migrations, backups and settings. |

Each customer project gets its own isolated database, its own roles and its own
keys. PIBASE speaks the Supabase wire protocol, so the official
`@supabase/supabase-js` client works against it by changing the URL and key.
Existing Supabase code usually runs after that change.

Standing rules: Postgres is the product, with no re-implementation of it; no
`sudo`; customer-visible text never names internals beyond "Postgres".

## 2. How a site connects

Two values identify a project, both shown on the dashboard's **API keys** page:

* **URL**: the gateway address (port 18080, or the public address if one is set).
* **Anon key**: a public key, safe to ship in browser code.

The **service key** bypasses row level security. It must never reach a browser,
a design file or a screenshot.

```js
import { createClient } from '@supabase/supabase-js'
const db = createClient(URL, ANON_KEY)
const { data } = await db.from('pages').select('*').eq('slug', 'home')
```

Sites that don't use supabase-js can load PIBASE's own dependency-free sign-in
client from the gateway. It shares one sign-in across browser tabs:

```html
<script src="URL/client/v1/pibase-auth.js"></script>
<script>
  const auth = PibaseAuth.createClient({ url: URL, anonKey: ANON_KEY })
  auth.onAuthStateChange((event, session) => {}) // SIGNED_IN, SIGNED_OUT, TOKEN_REFRESHED
  await auth.signIn(email, password)
  const res = await auth.fetch('/rest/v1/notes?select=*')
</script>
```

## 3. What a design can rely on

* **Who can see or change data is decided in the database**, by row level
  security policies. The UI can show or hide controls, but it is never the
  security boundary. Design "not allowed" states for every action.
* **Signed-out users, bans and expired sessions all end real access.** Signing
  out, or banning a user, stops every token of that sign-in immediately, closes
  their live connections, and signs out all open tabs of the site. Design for
  the signed-out state appearing suddenly while someone is mid-task.
* **Two people can edit the same record.** Writes replace what is stored, and
  the last writer wins unless the site adds a version column. A good editing UI
  needs a "someone else saved first, reload" state.
* **Uploads go through Storage**, not into table rows. A single file can be up
  to 1 GiB. Table requests are capped at 1 MiB, and a response returns at most
  1000 rows, so list views need paging.
* **Realtime** can push inserts and updates for tables an owner has enabled.
  Deletes are not pushed, so a live list must handle removal another way.
* **Email-confirmation flows exist** (sign-up, recovery), so designs need
  "check your email" and "link expired" screens.
* **MFA (TOTP) exists**: enroll, scan a code, verify. Phone, WebAuthn and
  recovery codes do not.

## 4. Not supported (don't design for these)

OAuth/social sign-in, magic links, phone/OTP sign-in, Edge Functions, GraphQL,
image transformations, schemas other than `public`, DELETE events and wildcard
subscriptions in realtime, private realtime channels, replay of missed realtime
messages.

## 5. Limits worth knowing

| Limit | Value |
|---|---|
| Access token lifetime | 1 hour (the client refreshes it automatically) |
| Request body, except uploads | 1 MiB (configurable 64 KiB to 16 MiB) |
| Rows per request | 1000 |
| Requests per project | 100 per second |
| File storage per project | 1 GiB (configurable) |
| Realtime connections per project | 32 by default |

## 6. The project dashboard (the operator-facing product)

* Port 18081. `/` is a landing page with log in and sign up. Sign-up asks for an
  organization name and the password twice.
* After login, `/app` shows only that account's projects, with an icon menu that
  expands on tap (mobile is a first-class target).
* Each project has an overview (database size, requests, success rate, warnings
  and errors by service, daily requests), API keys, tables, users, a SQL
  editor, migrations, backups and settings.
* Every page explains what it does, in plain language.
* Visitor-facing copy talks about "Postgres" and the product, never about
  internal implementation names.

## 7. Roles and SQL, for anyone writing policies

Each project has its own database roles: `p_<id>_anon`, `p_<id>_authenticated`,
`p_<id>_service_role` and `p_<id>_admin`. To keep SQL portable with Supabase,
leave out the `TO` clause and check the caller inside the policy:

```sql
create policy "owners edit" on notes for all
  using (auth.uid() = owner_id) with check (auth.uid() = owner_id);
```

Helpers: `auth.uid()`, `auth.jwt()`, `auth.role()`, `storage.foldername(name)`,
`storage.filename(name)`, `storage.extension(name)`. Tables created in `public`
get row level security switched on automatically.

## 8. How Claude Design learns about PIBASE

There is no built-in knowledge. Claude Design is not trained on PIBASE, and
nothing in the repository mentions it. It knows only what it is given in that
design or project. To make it work with PIBASE:

1. **Attach this file** (or paste sections 1 to 6) as project knowledge or
   notes at the start of the design session.
2. **Say what the site stores**, for example: "pages and their editable text,
   images in a bucket, editors who sign in". It can then design the matching
   states.
3. **Keep keys out.** Give it the URL and the anon key only if the design
   needs live data. Never give it the service key.
4. **Use Supabase wording when asking for code.** It already knows
   supabase-js well, and PIBASE is compatible with it, so it needs only the
   differences in sections 4 and 7.
5. **Ask for the states in section 3** (signed out, not allowed, conflict, empty,
   loading, link expired) so they are designed rather than discovered later.

Where it should read more: `docs/COMPATIBILITY.md` in the repository is the
detailed, test-backed reference.
