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
| Data (`/rest/v1`) | select with filters, order, limit/offset and embedded relations; insert, update, delete; `upsert()` with or without `onConflict` (defaults to the primary key, like PostgREST) and with `ignoreDuplicates`; `Prefer: return=representation` and `count=exact`; `columns=` | schemas other than `public` (requests for them get 406), GraphQL |
| Functions (`/rest/v1/rpc/<name>`) | calling SQL functions in `public` | Edge Functions |
| Auth (`/auth/v1`) | email + password sign-up, confirmation, sign-in, refresh, sign-out, password recovery, `getUser`/`updateUser`, TOTP MFA, admin user management with the service key | OAuth/social providers, magic links, phone and OTP sign-in |
| Storage (`/storage/v1`) | buckets; upload (POST and PUT, `upsert`), download, public and signed URLs, list, remove | image transformations |
| Realtime (`/realtime/v1/websocket`) | table changes (after `realtime.enable_table`), broadcast, presence | |

Browsers: the gateway allows every request header these clients send
(`TestCORSPreflightAllowsSupabaseClientHeaders`).

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
Supabase SQL that says `TO authenticated` must use the project's role name
instead. For SQL that works on both, leave the `TO` clause out and check the
caller in the policy:

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
grant execute on function public.storage_write_allowed(text, text) to p_<id>_authenticated;
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
* Two tabs that refresh the same sign-in within `PIBASE_REFRESH_REUSE_SECONDS`
  (default 10) both get working tokens. Reusing an old refresh token after that
  window is treated as theft and ends every session of the user, as before.
  Set the value to 0 for strict detection.
* Access tokens last one hour. A token your own server mints with the JWT secret
  must use `"iss": "pibase"`. Without a `session_id` claim it isn't tied to a
  sign-in, so it stays valid until its `exp`.

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
