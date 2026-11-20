# Billing with Stripe

pibase can charge dashboard accounts a monthly plan through Stripe. People pay
on Stripe's own pages (Checkout) and manage their card, invoices, plan and
cancellation in Stripe's customer portal. Card details never reach pibase.

Billing is **off** until you add a Stripe key. Without it, every account works
as before, limited only by `PIBASE_MAX_PROJECTS_PER_ACCOUNT`.

## How it works for your customers

| Stage | What they can do |
|---|---|
| **Trial** (14 days from sign-up, no card) | 1 project, up to 512 MB of data |
| **Paid plan** | the plan's projects and database size |
| **Card failing** (Stripe retrying) | everything keeps working; the dashboard asks them to update their card |
| **Grace** (7 days after the trial or subscription ends) | projects keep working; no new projects; a banner asks them to choose a plan |
| **Paused** (after the grace period) | their projects' APIs answer `402` with a plain reason, so their websites can't reach their data. **Nothing is deleted.** Paying resumes them within a minute |

Accounts that existed before billing, and accounts you create with
`create-account.sh` / `pibase account-create`, are **exempt**: never charged,
never paused. In the operator view you can exempt any account (for a client you
bill yourself) or start billing it.

Deleting an account cancels its Stripe subscription first, so nobody is charged
for an account that no longer exists.

## Set it up

Do this in Stripe's **test mode** first, then repeat it in live mode.

1. **Create your plans.** In Stripe, **Product catalog**, **Add product**: for
   example "pibase Starter" with a recurring monthly price of $12, and
   "pibase Pro" at $25. Copy each price's ID (`price_...`).
2. **Turn on the customer portal.** **Settings**, **Billing**, **Customer
   portal**: allow updating payment methods, viewing invoices and cancelling.
   To let people switch plans themselves, turn on plan switching and add both
   products.
3. **Add the webhook.** **Developers**, **Webhooks**, **Add endpoint**:
   * URL: `https://app.yourdomain.com/_pibase/billing/webhook` (your public
     dashboard address; see "Public dashboard" in [VPS.md](VPS.md))
   * Events: `checkout.session.completed`, `customer.subscription.created`,
     `customer.subscription.updated`, `customer.subscription.deleted`,
     `invoice.paid`, `invoice.payment_failed`
   * Copy the endpoint's **signing secret** (`whsec_...`).
4. **Get an API key.** **Developers**, **API keys**: the secret key
   (`sk_test_...`, later `sk_live_...`). A restricted key works too, with write
   access to Customers, Checkout Sessions, Customer portal and Subscriptions.
5. **Tell pibase.** Add to `/home/pibase/pibase/.env`:

   ```sh
   PIBASE_STRIPE_SECRET_KEY=sk_test_...
   PIBASE_STRIPE_WEBHOOK_SECRET=whsec_...
   PIBASE_PLANS=Starter:price_AAA:1:1024:$12 a month;Pro:price_BBB:3:5120:$25 a month
   ```

   then, as `pibase`: `cd /home/pibase/pibase && docker compose up -d`. The
   dashboard log says `billing on plans=2`.

`PIBASE_PLANS` lists plans separated by `;`. Each is
`Name:price_id:projects:database_MB:label`, where the label is what customers
read on the plan card. The name must match nothing else; the price must be the
Stripe price ID.

Optional settings (defaults shown):

```sh
PIBASE_TRIAL_DAYS=14         # 0 means no trial: pay before the first project
PIBASE_TRIAL_PROJECTS=1
PIBASE_TRIAL_DB_MB=512
PIBASE_BILLING_GRACE_DAYS=7
```

## Try it

In test mode, sign up a new account on the dashboard, confirm its email, open
**Billing**, choose a plan and pay with Stripe's test card `4242 4242 4242 4242`
(any future date, any CVC). You return to the dashboard with the plan active.
Under **Manage billing**, cancel it: the dashboard shows the grace period.

In **Developers**, **Webhooks**, each delivery shows pibase's answer. `200` means
it was applied; `400` usually means the signing secret in `.env` does not match.

## Going live

Repeat steps 1 to 4 in live mode (live prices have different IDs), put the
live key, webhook secret and price IDs in `.env`, and run
`docker compose up -d`. Existing test-mode subscriptions do not carry over.

## Notes

* pibase checks every 10 minutes for trials and grace periods that have run
  out; payments and cancellations apply as soon as Stripe's webhook arrives.
* Promotion codes you create in Stripe can be entered at Checkout.
* Taxes and invoices follow your Stripe settings. pibase adds no tax itself;
  ask an accountant what you need to collect.
* A paused project's open realtime connections end when their access token
  next refreshes (within an hour); new requests are refused at once.
