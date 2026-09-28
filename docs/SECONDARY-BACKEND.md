# Testing DATBase as a secondary backend for an existing app

A playbook for trying DATBase against a real, already-built app (Supabase or otherwise) without
touching that app's real database, auth, or users. Same pattern regardless of which app —
follow this once per app rather than improvising each time.

## The pattern: write-only, best-effort, non-blocking mirror

The existing app keeps its real database as the only source of truth. After every write that
succeeds there, also fire a write to DATBase — async, swallowing any error, on a short timeout.
If DATBase is slow, down, or wrong, the real app is completely unaffected. This is a test tap,
not a dependency.

```
real write succeeds → return to caller normally
                    → (don't await, or await-and-ignore) mirror to DATBase
```

Never make the real app's request wait on DATBase, and never let a DATBase failure surface as
an error to the real app's users.

## Three decisions to make once per app, before writing any code

1. **Which tables to mirror.** Doesn't need to be everything — pick the table(s) that best
   exercise real usage patterns (the one with the most write traffic is usually the right pick).
2. **Auth.** Default: **don't mirror it.** An existing app's users/sessions are the highest-risk,
   lowest-value thing to duplicate into a second system. Leave login exactly as it is; only
   mirror data tables. If you specifically want to test DATBase's own auth, do that separately
   (sign up a throwaway user directly against DATBase), not wired into the real app's login flow.
3. **IDs / any server-generated values (counters, computed totals, etc).** Default: **compute
   once in the real app, pass the same value to DATBase.** Keeps the two databases actually
   comparable. (The alternative — let DATBase generate its own — is simpler but means the two
   databases can diverge, which defeats the point of a parallel test.)

## Coming from Supabase specifically

Supabase's anon-key/service-role-key/RLS model doesn't map onto DATBase directly — there's no
anon key. Each DATBase project is a real PocketBase instance:

| Supabase | DATBase equivalent |
|---|---|
| Project URL | Project URL: `https://<slug>.<domain>` |
| Anon key + RLS | No key — access control is per-collection **rules**, set in the table's settings |
| Service role key | A **server key** (Dashboard → project → Server keys), scoped + expiring |
| `supabase.auth.*` | PocketBase's own auth endpoints, if/when you do choose to test auth (see above) |

For a *mirror* test (this doc's pattern), you only need a server key — no auth work at all,
since the mirror never touches login.

## Building the mirror client

1. In the DATBase dashboard, create one collection per mirrored table. Match field names/types
   to the source app's schema as closely as PocketBase's field types allow (text, number, date,
   select, relation, bool). Add a `sourceId` text field to every mirrored collection — this
   carries the source app's own id/number and is how the mirror finds "the same" record on a
   later update instead of creating duplicates.
2. Generate a server key for that project.
3. Set `DATBASE_URL` (the project's public URL) and `DATBASE_SERVER_KEY` in the app's environment.
4. Write one function per table: find-by-`sourceId` (via a PocketBase `filter` query), PATCH if
   found else POST. Wrap every function in try/catch that only logs (`console.warn`, not
   `console.error`) — it must never throw into the caller. Give every fetch a short timeout
   (`AbortSignal.timeout(5000)` is a reasonable default) so a hung DATBase project can't hang the
   real request even if someone forgets to fire-and-forget it.
5. Call `Authorization: <server key>` directly (no `Bearer` prefix) — that's the convention
   DATBase's own backend uses internally (`app/src/lib/pb.ts`).
6. Drop the calls in right after each real write succeeds, without awaiting them on the request's
   critical path.

## Worked example

`docs/examples/datbase-mirror-occ-inventory.ts` is a complete, working mirror client built this
way for a real app (an inventory/quoting system originally on its own Postgres/Drizzle schema).
Use it as a template — copy the shape (upsert-by-`sourceId` helper, one function per table, the
enabled/no-op-if-unconfigured guard at the top) and swap in the next app's actual field names.

## Tracking multiple apps

As more apps get a mirror set up, keep a short status note somewhere central (which apps, which
tables, what's mirrored vs not, any DATBase limitations hit) rather than re-discovering the same
decisions each time — this doc is that place; add a line under "Apps mirrored so far" below as
each one goes live.

### Apps mirrored so far
- `occ-inventory` — items, categories, quotes, quote_items, settings mirrored; auth not mirrored.
