# Using pibase with Supabase clients

pibase speaks the Supabase wire protocol, so the official `@supabase/supabase-js`
client works against it from browsers and servers. This page lists what is
supported, where pibase deliberately differs, and the limits a site should
design for. It was checked with supabase-js 2.117.2 in Chromium from a
different origin, and by the Go integration tests named below.

```js
import { createClient } from '@supabase/supabase-js'
const db = createClient('https://api.example.com', ANON_KEY)
```

The URL is the gateway address (port 18080, or `PIBASE_PUBLIC_URL`). The
dashboard's **API keys** page shows it with the project's keys.

## Wire APIs

| Area | Supported | Not supported |
|---|---|---|
| Data (`/rest/v1`) | select with filters, order, limit/offset and embedded relations; count/sum/avg/min/max aggregates; insert, update, delete; `upsert()` with or without `onConflict` (defaults to the primary key, like PostgREST) and with `ignoreDuplicates`; `Prefer: return=representation` and `count=exact`; `columns=` | schemas other than `public` (requests for them get 406), GraphQL |
| Functions (`/rest/v1/rpc/<name>`) | calling SQL functions in `public`, including omitted named arguments with SQL defaults | overloaded functions |
| Server-side code (`/functions/v1/<name>`) | forwarding to your site's own server routes, with `supabase.functions.invoke` unchanged (see below) | running code on the PIBASE machine (no Edge Functions runtime) |
| Auth (`/auth/v1`) | email or username + password sign-in, email sign-up and confirmation, refresh, scoped sign-out, password recovery and verified password change, `getUser`/`updateUser`, TOTP MFA, admin user management and session termination with the service key, bcrypt hash import | OAuth/social providers, magic links, phone and OTP sign-in |
| Storage (`/storage/v1`) | buckets; upload (POST and PUT, `upsert`), download, public and signed URLs, list, remove | image transformations |
| Realtime (`/realtime/v1/websocket`) | table changes (after `realtime.enable_table`), broadcast, presence | |

Browsers: the gateway allows every request header these clients send
(`TestCORSPreflightAllowsSupabaseClientHeaders`).

Aggregate selects accept `count()`, `amount.sum()`, `amount.avg()`,
`amount.min()`, `amount.max()` and aliases such as `total:amount.sum()`.
Plain selected columns group the result, for example
`select=category,count(),total:amount.sum()`. Filters and RLS apply before
aggregation. Aggregate selects do not yet accept `order` or relation embeds.
(`TestDataAPIAggregatesRespectRLS`)

## SQL helpers

| Helper | Notes |
|---|---|
| `auth.uid()` | the signed-in user's ID, or NULL |
| `auth.jwt()` | the request's verified claims as `jsonb` (`{}` without a token) |
| `auth.role()` | `anon`, `authenticated` or `service_role` |
| `storage.foldername(name)` | folders of an object path: `'inline/a.png'` gives `{inline}`, and `'inline'` gives `{}`, so a file named like a folder never passes a folder check |
| `storage.filename(name)`, `storage.extension(name)` | same results as Supabase |

## Roles and policies

Each project has its own database roles, named after the project ID
(`p_<id>_anon`, `p_<id>_authenticated`, `p_<id>_service_role`, `p_<id>_admin`).
The request roles are members of stable, cluster-wide `anon`, `authenticated`
and `service_role` NOLOGIN roles. Migrations can use `GRANT ... TO
authenticated` and policies can use `TO authenticated`, as on Supabase. The
stable roles cannot sign in to PostgreSQL directly. Projects still use separate
databases and project-specific gateway roles. For policies based on identity:

```sql
create policy "owners edit" on notes for all
  using (auth.uid() = owner_id) with check (auth.uid() = owner_id);
```

Tables created in `public` get row level security switched on automatically.
Migrations run as the project admin role.

## Storage access

Signed-in users can write objects they own in any bucket. That is the default
`storage_object_owner` rule. The storage tables belong to the server operator,
so a project cannot add policies to them. To narrow uploads, define this
function in a migration. pibase then applies it to every signed-in upload,
update and delete:

```sql
create function public.storage_write_allowed(bucket_id text, name text)
returns boolean language sql stable security definer set search_path = public as $$
  select bucket_id <> 'media'
      or ((storage.foldername(name))[1] = 'inline'
          and exists (select 1 from public.editors where user_id = auth.uid()))
$$;
grant execute on function public.storage_write_allowed(text, text) to authenticated;
```

Returning false or NULL refuses the write (403). An error in the function also
refuses it; the text of a `raise exception` is returned to the caller. The service key is not affected. Migrations can
create and change buckets (`insert into storage.buckets ...`). Delete buckets
through the storage API so their files are removed too.
(`TestAuditStorageHelpersHookAndMigrationBuckets`)

## Sessions and sign-out

* Each refresh issues a new refresh token. An access token's `session_id`
  claim names the refresh-token row it was issued with, as in earlier releases,
  so SQL that checks that row in `auth.refresh_tokens` keeps working. All the
  rows of one sign-in belong to the same session.
* Signing out ends the whole sign-in. Every access token it ever issued is
  refused at once by the data API, functions, storage and auth, and its
  realtime connections are closed; they don't keep working until they expire.
  Deleting a user does the same. Project SQL no longer needs its own sign-out
  check. (`TestAuditLogoutEndsTheSessionsAccessTokens`)
* `POST /auth/v1/logout?scope=local|others|global` accepts an authenticated
  access token. The default is `local`; `others` keeps the caller's sign-in;
  `global` ends all of the user's sign-ins. A service-key caller can end all
  sessions of a target user with `POST /auth/v1/admin/users/{id}/logout`.
  Neither action bans the user, so a new sign-in remains possible.
* `POST /auth/v1/user/password` with `current_password` and `new_password`
  checks the old password, sets the new one, and ends the user's other
  sessions in one transaction. `PATCH /auth/v1/user` with `password` now also
  requires `current_password`; a recovery-token session may set a password
  without it. Admin password updates end all sessions. The admin create and
  update endpoints accept `password_hash` instead of `password` for importing
  an existing bcrypt hash (cost 4–16). Never expose that field to a browser.
  (`TestInventorySessionScopesAndPasswordChange`,
  `TestInventoryAdminPasswordImportEndsSessions`)
* The service-key admin create/update endpoints accept `app_metadata`. It is
  returned as part of the user object and included in newly issued access
  tokens. A signed-in user may edit `user_metadata` but cannot set
  `app_metadata`. (`TestServerOnlyAppMetadata`)
* Password sign-in success and failure are written to `auth.sign_in_events`.
  A service-key caller can read the latest 1–500 events at
  `GET /auth/v1/admin/signin-events?limit=100`. Events include email, optional
  user ID, result, reason, client IP and timestamp. Passwords and tokens are
  never logged. (`TestSignInAuditRecordsSuccessAndFailure`)
* Admins may create a user with `username` and no email. Such users are
  confirmed immediately and can sign in by posting `{ "username": "crew.lead",
  "password": "..." }` to `/auth/v1/token?grant_type=password`, or with
  `PibaseAuth.signInUsername(username, password)`. Usernames are case
  insensitive, 3–64 characters, and may contain letters, digits, dots,
  underscores and hyphens. Password recovery by email needs an email address.
  (`TestUsernameOnlySignIn`)

Project admin SQL can call `auth.admin_create_user(username,email,password,
app_metadata)`, `auth.admin_set_password(user_id,password)`, and
`auth.admin_end_sessions(user_id,keep_session DEFAULT NULL)`. These helpers
are private to the project admin role. An application RPC can wrap them in a
`SECURITY DEFINER` function after checking its own staff permissions. Password
setting and application-row writes can then commit together. SQL session
revocation is checked against PostgreSQL on the next HTTP request and closes
live realtime connections through a database notification.
(`TestSQLAuthAdminHelpers`)
* Banning a user (`ban_duration` in the admin API) does the same for all of
  their sign-ins, and refreshing is refused until the ban ends. A ban written
  straight into `auth.users.banned_until` takes effect on the next request.
  (`TestBanEndsSessionsAndBlocksRefresh`)
* Realtime connections follow the token they last presented. supabase-js sends
  each refreshed token on open channels (`access_token` messages), and the
  gateway uses it from then on. Every 30 seconds it closes connections whose
  token has expired or whose sign-in has ended, including a connection opened
  with the anon key that signed in through a channel join.
  (`TestRealtimeAcceptsRefreshedTokensAndDropsEndedOnes`)
* Two tabs that refresh the same sign-in within `PIBASE_REFRESH_REUSE_SECONDS`
  (default 10) both get working tokens. Reusing an old refresh token after that
  window is treated as theft and ends every session of the user, as before.
  Set the value to 0 for strict detection.
* Access tokens last one hour. A token your own server mints with the JWT secret
  must use `"iss": "pibase"`. Without a `session_id` claim it isn't tied to a
  sign-in, so it stays valid until its `exp`.

## SQL-selected HTTP status

A SQL function can raise a `PTxxx` SQLSTATE to select an HTTP error status
from 400 through 599. For example, `RAISE EXCEPTION 'order changed' USING
ERRCODE = 'PT409', HINT = 'reload the order'` returns HTTP 409 with the
message and hint in the JSON error body. (`TestSQLChosenHTTPStatus`)

## Browser sign-in client

A site that doesn't use supabase-js can load pibase's own sign-in client from
the gateway. It has no dependencies and keeps one sign-in shared by every tab
of the site:

```html
<script src="https://api.example.com/client/v1/pibase-auth.js"></script>
<script>
  const auth = PibaseAuth.createClient({ url: 'https://api.example.com', anonKey: ANON_KEY })
  auth.onAuthStateChange((event, session) => { /* SIGNED_IN, SIGNED_OUT, TOKEN_REFRESHED */ })
  await auth.signIn(email, password)
  const res = await auth.fetch('/rest/v1/notes?select=*')
</script>
```

| Call | Does |
|---|---|
| `signUp(email, password, data?)` | creates an account; the session is null until the email is confirmed |
| `signIn(email, password)` | signs in every open tab |
| `signOut()` | signs out every open tab at once, then ends the sign-in on the server |
| `session()`, `user()` | the stored session and user, or null |
| `accessToken()` | a valid access token, refreshed first when it is due; null when signed out |
| `fetch(path, init?)` | calls the gateway with the key and token; on a 401 refreshes once and retries |
| `refreshSession()` | refreshes now |
| `onAuthStateChange(cb)` | calls `cb(event, session)` for changes made in any tab; returns an unsubscribe function |
| `destroy()` | stops timers and cross-tab listening |

* When the token is due, one tab refreshes it and the others wait for and use
  its result (`navigator.locks`, or a lock in `localStorage` where that isn't
  available; pass `locks: false` to force the fallback).
* A refresh still running when the user signs out can't sign them back in.
* If the server ends the sign-in (sign-out elsewhere, a ban, user deleted), the
  next refresh is refused and every tab gets `SIGNED_OUT`.
* The session is kept in `localStorage` (key `pibase.auth:<url>`, or
  `storageKey`). Where `localStorage` is blocked it lives in memory and tabs
  don't share it.
* For an httpOnly refresh-token cookie, use
  `PibaseAuth.createCookieClient({ url, anonKey })`. Call
  `await auth.refreshSession()` on page load to restore the session. Its access
  token stays in memory, and its refresh token is never returned to JavaScript
  or written to `localStorage`. Sign-in, username sign-in, refresh, fetch,
  sign-out and auth events use the same method names. The gateway sets a
  host-only, httpOnly cookie scoped to `/auth/v1`; production cookies are
  `Secure`. For a different browser origin, list that exact HTTPS origin in
  `PIBASE_COOKIE_ORIGINS`. Same-site deployment is recommended because browsers
  may block third-party cookies. (`TestHttpOnlyCookieSession`,
  `TestCookieBrowserClientRestoresWithoutLocalStorage`)
* Checked in Chromium with two tabs, both lock kinds, and the page on a
  different origin from the gateway (`TestBrowserAuthClient`, run with
  `PIBASE_TEST_BROWSER=1` and Playwright installed). Not yet checked in Safari
  or on phones.

## Server-side code

PIBASE doesn't run customer code, so there is no Edge Functions runtime. Work
that needs a secret (an email or payment key, the service key) or must call an
outside service lives in your site's own server, for example Next.js route
handlers at `app/api/<name>/route.ts`. Plain reads and writes still go straight
from the browser to `/rest/v1` under row level security. The guide
[SERVER-SIDE-CODE.md](SERVER-SIDE-CODE.md) has the patterns: authenticated
routes, email, webhooks, scheduled jobs.

To keep `supabase.functions.invoke('send-quote', { body })` working unchanged,
set the project's **Server-side code** address in the dashboard's Settings, for
example `https://app.example.com/api`. The gateway then forwards
`/functions/v1/<name>` to `<address>/<name>`:

* It checks the project key and the caller's token first. A signed-out token
  gets 401 and the site is never called. The anon key alone is accepted, as with
  Supabase's default `verify_jwt`, so a route that needs a signed-in user must
  check for one.
* The site receives the method, query string, body and the `Authorization`
  header, so the route can act as the user. The project's `apikey` header and
  cookies are not forwarded.
* It adds `X-Pibase-Role` (`anon`, `authenticated` or `service_role`),
  `X-Pibase-Project-Id` and, for a signed-in user, `X-Pibase-User-Id`. Headers
  with those names (or `X-Forwarded-*`) sent by the caller are dropped. Trust
  them only if the site can be reached through the gateway alone; a site open
  to the internet should call `getUser(token)` instead (next point).
* Response status, body and headers come back, except cookies and CORS headers,
  which the gateway controls. Redirects are returned to the caller, not followed.
  Calls time out after 120 seconds. A failed or slow site gives 502 or 504.
* A name is one or more `/`-separated parts of letters, digits, `.`, `_`, `~` and
  `-` (each up to 128 characters, not starting with a dot); anything else is 404.
* Public addresses can be set by the project owner. An address on the Pi's own
  network, such as the Next.js app on the same machine, can only be set with
  the operator token, and the gateway refuses to connect to a private address
  that the operator didn't approve.
* Calls count towards the project's 100 requests per second and its request
  totals.

**Checking a token in your own server.** Ask PIBASE (`GET /auth/v1/user` with the
token, which is what `supabase.auth.getUser(token)` does). Don't check the
signature locally: PIBASE ends every token of a sign-in at sign-out or ban, but
a locally checked token stays valid until it expires, up to an hour.
(`TestFunctionsProxyForwardsWithVerifiedIdentity`, `TestFunctionsProxyRefusesUnsafeTargetsAndNames`,
`TestSupabaseFunctionsInvoke`)

## Concurrent editors

A write replaces what is stored; the last writer wins. To detect a conflict,
keep a version column and update only if it is unchanged. An empty result
means someone else saved first: reload and merge, rather than overwriting.

```js
const { data } = await db.from('pages')
  .update({ content, version: version + 1 })
  .eq('slug', slug).eq('version', version)
  .select()
if (data.length === 0) { /* conflict: reload */ }
```

(`TestAuditUpsertWorksLikeSupabaseJS`)

## Limits

| Limit | Default | Setting |
|---|---|---|
| Request body for data, functions, auth and bucket calls | 1 MiB; larger gets 413 with the limit | `PIBASE_MAX_BODY_BYTES` (64 KiB to 16 MiB) |
| One stored file | 1 GiB | |
| File storage per project | 1 GiB | `PIBASE_STORAGE_MAX_BYTES` |
| Rows returned per request | 1000 | |
| Requests per project | 100 per second | |

Upload files through storage instead of putting them inside table rows. That
keeps writes under the body limit.
