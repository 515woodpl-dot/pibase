# Server-side code for a PIBASE site (Next.js)

Written by @Oasis, 9 October 2026. Updated for the `/functions/v1` forwarding that PIBASE now has. See [COMPATIBILITY.md](COMPATIBILITY.md#server-side-code).

## Why route handlers instead of Edge Functions

PIBASE has no Edge Functions, so server-side code for a PIBASE site lives in Next.js App Router route handlers (`app/api/<name>/route.ts`). That is the whole decision. No new runtime, no extra service, nothing to add to the Pi.

In Supabase, an [Edge Function](https://supabase.com/docs/guides/functions) is a TypeScript file run on Deno behind a gateway at `/functions/v1/<name>`. The gateway checks the caller's JWT, and the function reads secrets from environment variables. Supabase lists the typical jobs as webhook receivers, transactional email, calls to third-party APIs, small AI tasks and bots.

A [Next.js route handler](https://nextjs.org/docs/app/api-reference/file-conventions/route) does the same jobs. It takes a Web `Request` and returns a `Response`, runs on the server, and reads secrets from `process.env`. The one thing it does not do for you is the gateway's JWT check, so that becomes three lines of code (Pattern 1).

The database stays the security boundary. A route handler is for work that needs a secret (a Resend or Stripe key, the service key) or needs to talk to the outside world. Plain reads and writes still go straight from the browser to `/rest/v1` under row level security.

The browser talks to PIBASE directly for data; it goes through the Next.js app only when a secret or an outside service is involved.

## Supabase concept to Next.js equivalent

Every Edge Function feature has a plain Next.js equivalent; only JWT checking and scheduling need a few lines of your own.

| Supabase Edge Functions | Next.js route handler on PIBASE |
| --- | --- |
| File `supabase/functions/send-quote/index.ts` | File `app/api/send-quote/route.ts` |
| URL `/functions/v1/send-quote` on the project | URL `/api/send-quote` on the app's own domain, also reachable as `/functions/v1/send-quote` through the gateway once the address is set |
| `Deno.serve(async (req) => …)` | `export async function POST(req: Request) { … }`, one export per HTTP method |
| Deno runtime | Node.js runtime (the default; keep it, Resend and Stripe SDKs expect Node) |
| Secrets set with `supabase secrets set` | `.env.local` in development, the host's environment settings in production |
| Injected `SUPABASE_URL`, `SUPABASE_ANON_KEY`, `SUPABASE_SERVICE_ROLE_KEY` ([list](https://supabase.com/docs/guides/functions/secrets)) | You set `NEXT_PUBLIC_PIBASE_URL`, `NEXT_PUBLIC_PIBASE_ANON_KEY`, `PIBASE_SERVICE_KEY` yourself |
| `verify_jwt = true`: the gateway rejects calls without a valid user token ([auth](https://supabase.com/docs/guides/functions/auth)) | Nothing automatic. Call `requireUser(req)` at the top of the handler (Pattern 1) |
| Client scoped to the caller, so RLS applies | `createClient(URL, ANON_KEY, { global: { headers: { Authorization } } })` with the caller's token |
| Admin client that bypasses RLS | `createClient(URL, SERVICE_KEY)`, server-only file |
| `supabase.functions.invoke('send-quote', { body })` | Unchanged when the project's **Server-side code** address is set (the gateway forwards `/functions/v1/send-quote`), or `fetch('/api/send-quote', …)` with the token |
| CORS headers in every function | Not needed: the browser calls the same origin. Add only if another site calls the route |
| Scheduled with `pg_cron` + `pg_net` ([schedule](https://supabase.com/docs/guides/functions/schedule-functions)) | Host cron or a Pi `cron`/systemd timer calling the route with a shared secret (Pattern 4). PIBASE's own scheduled webhooks (`cron.jobs`) can call a route too, but only at a public address |
| Limits: 256 MB memory, 2 s CPU per request, 150 s idle timeout ([limits](https://supabase.com/docs/guides/functions/limits)) | Whatever the Next.js host gives; a self-hosted Node server has no per-request cap |

## Setup

Three environment variables and two small client files cover every pattern below.

```bash
# .env.local  (one set per company deployment)
NEXT_PUBLIC_PIBASE_URL=https://api.marlowstone.com   # gateway, port 18080 behind it
NEXT_PUBLIC_PIBASE_ANON_KEY=eyJ...                    # public, safe in the browser
PIBASE_SERVICE_KEY=eyJ...                             # bypasses RLS: server only, never NEXT_PUBLIC_
RESEND_API_KEY=re_...
RESEND_FROM_ADDRESS=jobs@send.yourdomain.com          # sender name comes from the settings row
CRON_SECRET=a-long-random-string
```

Only variables that start with `NEXT_PUBLIC_` reach the browser bundle. Everything else stays on the server, which is exactly where the service key and the Resend key belong.

```
app/
  api/
    send-quote/route.ts     # one folder per "function"
    webhooks/stripe/route.ts
    cron/daily/route.ts
lib/
  pibase/browser.ts         # anon client for components
  pibase/server.ts          # requireUser, userClient, adminClient
```

```ts
// lib/pibase/browser.ts
import { createClient } from '@supabase/supabase-js'

export const db = createClient(
  process.env.NEXT_PUBLIC_PIBASE_URL!,
  process.env.NEXT_PUBLIC_PIBASE_ANON_KEY!,
)
```

```ts
// lib/pibase/server.ts
import 'server-only' // build fails if a client component imports this file
import { createClient } from '@supabase/supabase-js'

const URL = process.env.NEXT_PUBLIC_PIBASE_URL!
const ANON = process.env.NEXT_PUBLIC_PIBASE_ANON_KEY!
const opts = { auth: { persistSession: false, autoRefreshToken: false } }

// Acts as the signed-in caller: row level security applies.
export const userClient = (token: string) =>
  createClient(URL, ANON, { ...opts, global: { headers: { Authorization: `Bearer ${token}` } } })

// Bypasses row level security. Use only after you have checked who is asking.
export const adminClient = () =>
  createClient(URL, process.env.PIBASE_SERVICE_KEY!, opts)
```

Install with `npm i @supabase/supabase-js resend server-only`. The session lives in the browser (supabase-js keeps it in local storage), and each API call carries the access token in the `Authorization` header. That keeps the server stateless and avoids cookie plumbing.

## Pattern 1: an authenticated function

Ask the PIBASE gateway who the token belongs to, then do the work as that user so row level security still decides what they can touch.

```ts
// lib/pibase/server.ts  (add below the clients)
export class HttpError extends Error {
  constructor(public status: number, message: string) { super(message) }
}

export async function requireUser(req: Request) {
  const token = req.headers.get('authorization')?.replace(/^Bearer /i, '')
  if (!token) throw new HttpError(401, 'Sign in first')
  const { data, error } = await userClient(token).auth.getUser(token)
  if (error || !data.user) throw new HttpError(401, 'Session ended, sign in again')
  return { user: data.user, token, db: userClient(token) }
}

export function fail(e: unknown) {
  if (e instanceof HttpError) return Response.json({ error: e.message }, { status: e.status })
  console.error(e)
  return Response.json({ error: 'Something went wrong' }, { status: 500 })
}
```

```ts
// app/api/jobs/[id]/advance/route.ts
import { requireUser, fail, HttpError } from '@/lib/pibase/server'

export async function POST(req: Request, ctx: RouteContext<'/api/jobs/[id]/advance'>) {
  try {
    const { db } = await requireUser(req)
    const { id } = await ctx.params
    const { stage } = await req.json()

    // Runs as the caller. If RLS says no, PIBASE returns no row and nothing changes.
    const { data, error } = await db.from('jobs').update({ stage }).eq('id', id).select().single()
    if (error || !data) throw new HttpError(403, 'Not allowed to move this job')
    return Response.json(data)
  } catch (e) { return fail(e) }
}
```

Why `getUser` and not a local JWT check: PIBASE ends every token of a sign-in the moment someone signs out or is banned. A local signature check (with `jose`, say) would keep accepting that token for up to an hour. `getUser` costs one request to the Pi and stays correct.

Use `adminClient()` only for the parts the user's own rights can't do, such as reading the company settings row or writing an audit log. Check the user first, and filter by their id yourself when you do.

## Pattern 2: sending email with Resend

One server-only helper sends every app email, with the company name from the database as the sender name and an idempotency key so a retry never sends twice.

```ts
// lib/email.ts
import 'server-only'
import { Resend } from 'resend'
import { adminClient } from '@/lib/pibase/server'

const resend = new Resend(process.env.RESEND_API_KEY)

export async function sendEmail(o: { to: string; subject: string; html: string; key: string }) {
  const { data: s } = await adminClient().from('settings').select('company_name, reply_to').single()
  const { data, error } = await resend.emails.send(
    {
      from: `${s?.company_name ?? 'NEXT'} <${process.env.RESEND_FROM_ADDRESS}>`,
      replyTo: s?.reply_to ?? undefined,
      to: o.to,
      subject: o.subject,
      html: o.html,
    },
    { idempotencyKey: o.key }, // same key within 24 h = sent once
  )
  if (error) throw new Error(error.message)
  return data!.id
}
```

```ts
// app/api/change-orders/[id]/request-signature/route.ts
import { requireUser, fail, HttpError } from '@/lib/pibase/server'
import { sendEmail } from '@/lib/email'

export async function POST(req: Request, ctx: RouteContext<'/api/change-orders/[id]/request-signature'>) {
  try {
    const { db } = await requireUser(req)
    const { id } = await ctx.params
    const { data: co } = await db
      .from('change_orders')
      .select('id, title, amount, jobs(portal_token, clients(name, email))')
      .eq('id', id).single()
    if (!co) throw new HttpError(404, 'Change order not found')

    const job = co.jobs as any
    const link = `${new URL(req.url).origin}/portal/${job.portal_token}`
    await sendEmail({
      key: `co-sign-${co.id}`,
      to: job.clients.email,
      subject: `Please sign: ${co.title}`,
      html: `<p>Hi ${job.clients.name},</p><p>${co.title} adds $${co.amount}.</p><p><a href="${link}">Review and sign</a></p>`,
    })
    return Response.json({ sent: true })
  } catch (e) { return fail(e) }
}
```

Notes from the [Resend docs](https://resend.com/docs/send-with-nextjs) and [API reference](https://resend.com/docs/api-reference/emails/send-email):

- `send` returns `{ data, error }` rather than throwing, so check `error`.
- Idempotency keys are up to 256 characters and expire after 24 hours.
- `to` takes at most 50 addresses per send.
- Verify one sending subdomain once (for example `send.yourdomain.com`) and send for every company from it, with the company name as the display name and their address as `replyTo`. No per-company DNS work.
- Escape client-entered text before putting it in `html`, or use the `react` option with a React Email template, which escapes for you.

**PIBASE's own auth emails** (sign-up confirmation, password recovery): if PIBASE sends those over SMTP, point it at Resend's SMTP relay (`smtp.resend.com`, username `resend`, password = an API key). Then every email from the product goes out through one verified domain.

## Pattern 3: webhooks

A webhook has no signed-in user, so the signature on the raw body is the only proof of who sent it. Verify it first, then write with the admin client.

```ts
// app/api/webhooks/resend/route.ts  (bounces and complaints)
import { Resend } from 'resend'
import { adminClient } from '@/lib/pibase/server'

const resend = new Resend(process.env.RESEND_API_KEY)

export async function POST(req: Request) {
  const payload = await req.text() // raw text: parsing then re-stringifying breaks the signature
  let event: any
  try {
    event = resend.webhooks.verify({
      payload,
      headers: {
        id: req.headers.get('svix-id')!,
        timestamp: req.headers.get('svix-timestamp')!,
        signature: req.headers.get('svix-signature')!,
      },
      webhookSecret: process.env.RESEND_WEBHOOK_SECRET!,
    })
  } catch {
    return new Response('Bad signature', { status: 400 })
  }

  // Unique on event_id: a resend from the provider inserts nothing the second time.
  const db = adminClient()
  const { error } = await db.from('webhook_events').insert({ event_id: req.headers.get('svix-id'), type: event.type })
  if (error?.code === '23505') return new Response('Already handled')

  if (event.type === 'email.bounced') {
    await db.from('clients').update({ email_bounced: true }).eq('email', event.data.to[0])
  }
  return new Response('ok')
}
```

The same shape works for Stripe later: `await req.text()`, then `stripe.webhooks.constructEvent(body, req.headers.get('stripe-signature'), secret)`, then the insert-once check. Route handlers read the raw body directly; no body-parser setting is needed ([Next.js webhooks](https://nextjs.org/docs/app/api-reference/file-conventions/route#webhooks)). The `resend.webhooks.verify` call and the three `svix-` headers come from [Resend's verification guide](https://resend.com/docs/dashboard/webhooks/verify-webhooks-requests).

The provider must reach the route over the internet. If the Next.js app runs on the Pi, it needs a public address (a tunnel or port forward) for webhooks to arrive.

## Pattern 4: scheduled jobs and database-triggered work

A cron entry calls one protected route every minute, and that route drains an `outbox` table that database triggers fill. This replaces both `pg_cron` + `pg_net` and Supabase database webhooks.

**The outbox** — a trigger records what happened, inside the same transaction as the change, so nothing is lost if the app is down:

```sql
create table outbox (
  id bigint generated always as identity primary key,
  kind text not null,              -- 'seams_approved', 'job_billed', ...
  payload jsonb not null,
  created_at timestamptz default now(),
  done_at timestamptz,
  error text
);
alter table outbox enable row level security; -- no policies: only the service key reads it

create function on_job_stage() returns trigger language plpgsql as $$
begin
  if new.stage = 'Billed' and old.stage is distinct from 'Billed' then
    insert into outbox(kind, payload) values ('job_billed', jsonb_build_object('job_id', new.id));
  end if;
  return new;
end $$;

create trigger job_stage_outbox after update of stage on jobs
  for each row execute function on_job_stage();
```

**The worker route:**

```ts
// app/api/cron/minute/route.ts
import { adminClient } from '@/lib/pibase/server'
import { sendEmail } from '@/lib/email'

export async function POST(req: Request) {
  if (req.headers.get('authorization') !== `Bearer ${process.env.CRON_SECRET}`)
    return new Response('Unauthorized', { status: 401 })

  const db = adminClient()
  const { data: rows } = await db.from('outbox').select('*').is('done_at', null).order('id').limit(50)
  for (const row of rows ?? []) {
    try {
      if (row.kind === 'job_billed') {
        // look up the client, then:
        // await sendEmail({ key: `outbox-${row.id}`, to, subject, html })
      }
      await db.from('outbox').update({ done_at: new Date().toISOString(), error: null }).eq('id', row.id)
    } catch (e) {
      await db.from('outbox').update({ error: String(e) }).eq('id', row.id) // retried next minute
    }
  }
  return Response.json({ processed: rows?.length ?? 0 })
}
```

**The schedule** — on the Pi or any Linux host, one crontab line:

```bash
* * * * * curl -fsS -X POST -H "Authorization: Bearer $CRON_SECRET" https://next.marlowstone.com/api/cron/minute >/dev/null
```

If a row is processed twice (two overlapping runs), the email's idempotency key `outbox-<id>` makes Resend send it once. For a daily job (overdue invoices, tomorrow's schedule to each crew), add a second route and a `0 7 * * *` line.

## Calling a route from the browser

If you call the routes directly instead of through the gateway (`/functions/v1`, below), a ten-line `invoke` helper sends the current access token and returns `{ data, error }`, the same shape as `supabase.functions.invoke`, so code ported from Supabase barely changes.

```ts
// lib/invoke.ts  (client side)
import { db } from '@/lib/pibase/browser'

export async function invoke<T = any>(path: string, body?: unknown, method = 'POST') {
  const { data: { session } } = await db.auth.getSession() // refreshed automatically if near expiry
  const res = await fetch(`/api/${path}`, {
    method,
    headers: { 'Content-Type': 'application/json', ...(session && { Authorization: `Bearer ${session.access_token}` }) },
    body: body === undefined ? undefined : JSON.stringify(body),
  })
  const json = await res.json().catch(() => null)
  return res.ok ? { data: json as T, error: null } : { data: null, error: { status: res.status, message: json?.error ?? res.statusText } }
}
```

```ts
// usage
const { error } = await invoke(`change-orders/${id}/request-signature`)
if (error?.status === 401) router.push('/login') // signed out or banned mid-task
```

Without the gateway address, porting a Supabase call is a find-and-replace: `supabase.functions.invoke('send-quote', { body })` becomes `invoke('send-quote', body)`. Handle 401 everywhere, because PIBASE can end a session at any moment (sign-out in another tab, a ban).

## Security checklist and gotchas

- [ ] `PIBASE_SERVICE_KEY`, `RESEND_API_KEY`, `CRON_SECRET` and webhook secrets never start with `NEXT_PUBLIC_`.
- [ ] Every file that uses `adminClient()` imports `server-only`.
- [ ] Every user-facing route calls `requireUser(req)` first, before reading the body.
- [ ] Writes on behalf of a user go through `userClient` so row level security decides; `adminClient` only after an explicit check.
- [ ] Webhooks verify the signature on `await req.text()` before anything else, and record the event id once.
- [ ] Cron routes check `CRON_SECRET` and are `POST` only.
- [ ] Emails escape any text a client or staff member typed.
- [ ] Each email has an idempotency key tied to the thing it is about (`co-sign-<id>`, `outbox-<id>`).
- [ ] The `outbox` and `webhook_events` tables have RLS on and no policies.

Gotchas:

- **Public functions are callable.** PIBASE exposes SQL functions in `public` at `/rest/v1/rpc/<name>`. A `security definer` helper there can be called by anyone with the anon key, so check `auth.uid()` or the arguments inside it. (Trigger functions can't be called this way.)
- **Route handlers are dynamic by default** since Next.js 15 ([version history](https://nextjs.org/docs/app/api-reference/file-conventions/route#version-history)), so a `GET` that reads data isn't cached unless you opt in.
- **`params` is a Promise** in Next.js 15 and later: `const { id } = await ctx.params`.
- **Keep the Node.js runtime.** Don't set `runtime = 'edge'` on routes that use the Resend or Stripe SDK.
- **1 MiB request cap.** PIBASE limits table requests to 1 MiB, so a route that bulk-inserts should batch. Files go to Storage, never through a route as base64.
- **Long jobs.** If the app is hosted on a serverless platform, its per-request time limit applies; keep cron batches small (50 rows above).

## Calling through the gateway (`/functions/v1`)

Set the project's **Server-side code** address in the dashboard's Settings (for
example `https://app.example.com/api`). Then the gateway forwards
`/functions/v1/<name>` to `<address>/<name>` after checking the key and token,
so code ported from Supabase keeps working:

```ts
await db.functions.invoke('send-quote', { body: { id } })
```

The route can trust the gateway's check in one of two ways:

* **Ask PIBASE** (`requireUser`, Pattern 1). Works whether or not the call came
  through the gateway, and is the right choice for a site open to the internet.
* **Read the forwarded headers** `X-Pibase-User-Id`, `X-Pibase-Role` and
  `X-Pibase-Project-Id`. Safe only when the site can be reached through the
  gateway alone (for example, bound to the Pi's private network), because any
  other caller can set the same headers.

Details and limits are in [COMPATIBILITY.md](COMPATIBILITY.md#server-side-code).
The Next.js app on the same Pi is a private address, so the operator sets it.

## What PIBASE provides today

| Suggestion | Status |
| --- | --- |
| Functions proxy, `verify_jwt`-style check, `X-Pibase-User-Id` | Built: `/functions/v1/<name>`, set per project in Settings |
| Auth email over SMTP | Instance-wide today: `PIBASE_SMTP_HOST`, `_PORT`, `_USER`, `_PASS`, `_FROM`, and `PIBASE_SITE_URL`. Pointing it at `smtp.resend.com` works. Per-project settings and editable templates are not built |
| Database webhooks | Built: an `AFTER` row trigger calling `cron.enqueue_webhook(url, method)` writes to a durable outbox with retries. Requests are not HMAC-signed; put a shared secret in the headers instead. Targets must be public addresses |
| Scheduled calls | Built for `* * * * *` and `*/N * * * *` schedules with a URL and headers in `cron.jobs`. Public addresses only, so a route on the Pi's own network still needs host cron (Pattern 4) |
| `/auth/v1/user` as the revocation-safe check | Documented here and in COMPATIBILITY.md |
