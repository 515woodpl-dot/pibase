# Schema import: acceptance test (instructions for Codex)

**What's new:** *Tables → Import schema*. An AI writes one JSON document, the owner pastes it, and
every table (fields, links, unique rules, access) is created at once. This replaces creating tables
and fields one by one. `DATBASE.md` now teaches any AI to write this JSON.

**Your job:** try to break it, and judge whether an AI can really use it. Automated checks already
pass on Linux (unit 16/16, e2e all 12 suites / 150 checks including `schema-import` 12/12), so **spend your effort on section 3 (attack it) and section 4 (the AI loop)**, not on
re-proving section 2.

## 0. Prompt to give Codex (copy everything in the box)

```
Read TESTING-SCHEMA-IMPORT.md in the repo root and follow it exactly. First `git pull`.
Test DATBase's new "Import schema" feature (Tables tab → Import schema). Sections 3 and 4 matter
most: try hard to break it and report anything unsafe, wrong or confusing. Keep the two roles in
section 4 separate. Use the fixtures in app/tests/fixtures/schema-import/ and
docs/examples/ecommerce-store.schema.json. Don't edit application code: log findings only, in
SCHEMA-IMPORT-REPORT.md (template in section 6) with evidence in schema-import-evidence/.
When done, say PASS/FAIL per section and list every finding with severity.
```

## 1. Setup

Same as `TESTING.md` §2 / `app/tests/README.md` (PostgreSQL 16, `npm run build`, Chromium, PocketBase is downloaded for you).
- Automated: `cd app && tests/e2e/harness.sh schema-import.mjs` (also part of `npm run test:e2e`) and `npm run test:unit`.
- Manual: the Docker stack (`docker compose up -d --build`), sign up, create a project, open **Tables**.

## 2. Confirm the automated suite (10 minutes, don't go deeper here)

Run `harness.sh schema-import.mjs`, expect 12 ✔ (box present, 4 kinds of bad input rejected with nothing
created, 8-table store in one paste, database matches the JSON, rules behave over the API, DATBASE.md
updated, re-paste harmless, growth paste keeps data/access, Supabase-style JSON, mid-way failure fully
undone, **server key cannot change schema**, other account 404). Run `npm run test:unit` (expect 16/16).

## 3. Attack it (manual, in the dashboard)

Fixtures live in `app/tests/fixtures/schema-import/`: `store.json` (8 tables), `supabase-style.json`,
`growth.json`, `invalid-*.json`. Also `docs/examples/ecommerce-store.schema.json` (same store, the one users will copy).

| # | Try | Expected |
|---|---|---|
| 1 | Paste `store.json`, then open each of the 8 tables' **Settings** | Fields, types, "required", unique rules and the access dropdowns match the JSON. `carts`/`cart_items`/`addresses` show "Only its owner"; `orders` list/view owner, create server-only. |
| 2 | In Settings, change `products` list to "Server only", then paste `growth.json` | `products` gets `brand`, `weightGrams`, `tags`; **list access stays "Server only"** (import never changes access of existing tables); a note says so if the JSON named access. |
| 3 | Add records to `products`, paste `growth.json` again | "Nothing to import" style message or only notes; records intact. |
| 4 | Paste `supabase-style.json` | `blog_posts` + `comments` created; notes list ignored keys (`default`, `primary`…); `id`/`created_at` not duplicated; enum → choice; `text[]` → JSON; `author_id` links to `users`. |
| 5 | Each `invalid-*.json` | Message names the exact problem; **no table is created** (check the Tables list). |
| 6 | Names: table `_x`, `a-b`, 51 characters, empty; field `Created`/`id` (silently skipped: DATBase adds them), `expand`, `collectionId`, duplicate field names differing only by case | Rejected with a readable message (except `id`/`created`/`updated`, skipped). A table named `Users`/`USERS` **extends the existing users table** (names match ignoring case): confirm that is safe and clear. |
| 7 | 51 tables; a table with 101 fields; a 300 KB+ document | Limit messages; nothing created. |
| 8 | Fields on `users`: `email`, `password`, `verified` (built in) plus `phone` | Built-ins skipped with a note; `phone` added; users' access unchanged. |
| 9 | Relations: to a table later in the same JSON; to an existing table; to itself (`parent` on `categories`); circular (`a`→`b`→`a`) | All work (or a clear message). No half-created tables. |
| 10 | `select` with 0 choices, duplicate choices, a 101-character choice, numbers as choices | Clear messages (numbers become text choices). |
| 11 | `unique: true` on `bool`, `json`, `file`, and on a `multiple` relation | Ignored with a note, or rejected clearly. Never a broken table. |
| 12 | **Force a failure half-way** (e.g. a table named `sqlite_bad` as the 3rd of 3, or stop PocketBase between pasting and applying) | "Nothing was imported…" and the project is exactly as before. Check Tables, Settings and the Usage tab for leftovers. |
| 13 | **Security:** signed-in as account B, POST the Tables page's server action for account A's project id (replay the request with A's `projectId`) | Refused (404/not found). Nothing created in A. |
| 14 | **Security (most important):** use a **server key** (Overview → Server keys) against the project URL: `POST /api/collections`, `PATCH`/`DELETE /api/collections/<id>`, `PUT /api/collections/import`, `DELETE /api/collections/<id>/truncate`, `POST /api/collections/_superusers/impersonate/<id>`. Then try to find **any other** way a server key or signed-in user can change tables, access rules, settings or create a new superuser (e.g. `/api/settings`, `/api/backups`, `/api/crons`, batch endpoints, `/api/collections/meta/*`). | The listed calls: 403 "Tables and access rules can only be changed in the DATBase dashboard." Until 2026-09-28 they **succeeded** (found by me, fixed in the project hook). **Projects created before need *Update all projects* to get the fix**, so test on a project created or updated with the current build. Report anything else that works as a finding. `/api/settings` PATCH is known-open (neutralised for sender/links only); tell me if you can do harm with it. |
| 15 | **Injection:** table/field/choice/relation values containing quotes, backticks, `;`, `<script>`, emoji, very long strings; a `unique` field on a table named with SQL keywords (`group`, `order`, `select`) | Rejected by the name rule, or handled safely; no XSS in the notes/success list; unique index still created correctly for allowed names. |
| 16 | Import with `"access": "public"` on a table that has a `password`-like text field | Allowed (owner's choice) but confirm `DATBASE.md` shows "Anyone" for it in plain words. |
| 17 | Mobile (375 px) and keyboard-only use of the import box (Tab to summary, Space to open, paste, Enter on Import) | Usable; the textarea doesn't overflow; focus visible. |
| 18 | Paste ` ```json … ``` ` (with the markdown fence) and JSON with a trailing comma / comments | Fence accepted; trailing comma/comments give a clear "Not valid JSON" message. |
| 19 | Two browser tabs, paste the same new table name at the same time | One wins, the other gets a clear error; no duplicate/half table. |
| 20 | Large realistic schema (40 tables × 30 fields) | Finishes in a reasonable time (note seconds); UI shows progress ("Importing…"); no timeout leaving partial tables. If it can time out (`pb()` allows 15 s per call), record it. |

## 4. The AI loop (the point of the feature)

Keep the roles separate (same rules as `TESTING-AI.md`):

| Role | May read | Does |
|---|---|---|
| **Operator** | This repo, TESTING.md | Runs DATBase, pastes JSON in the dashboard, downloads DATBASE.md |
| **Builder** (the customer's AI) | **Only** `DATBASE.md` + the task text below | Designs tables and writes code |

1. Operator: create a **new empty project**, download `DATBASE.md`. It must say the project has no tables and point to *Adding tables: schema import*.
2. Builder task (give only this): *"Build a small bookstore backend on DATBase: books with cover images and categories, customers' carts and orders. Customers can browse books without signing in, and manage only their own cart and see only their own orders. I will do anything you can't."*
3. **Expected:** Builder produces a JSON schema import in the documented format (not SQL, not API calls to create tables), with sensible access (catalog public read, cart owner-only, orders owner-read/server-write) and explains what to paste and where.
4. Operator pastes it exactly as given. Record: did it import first time? Which notes/errors appeared? Fix nothing by hand.
5. Operator downloads the new `DATBASE.md`, gives it to the Builder, who writes a `datbase.ts`-based script or small app: list books, sign up, add to cart, list own cart, prove another user can't see it, and (server side, with a server key) create an order.
6. Log every gap: anything the Builder had to guess, any wrong access it chose, any place `DATBASE.md`'s schema-import section was unclear or wrong. Also try a **second AI/prompt style** (e.g. "make me a Supabase schema for this" then paste that SQL-ish JSON) and record what the importer does.
7. Extend the app ("add product reviews, one per user per product"): the Builder should write a **small** import with only the new table + a unique combination need. Note: `unique` only supports single fields in import: does the Builder notice, and does `DATBASE.md` tell it what to do (settings → unique combinations)? Log as a finding if this is a trap.

## 5. Already known (don't report these as new)

- Import never changes access of tables that already exist (deliberate); to change access use table Settings.
- Import can't rename, retype or delete anything (deliberate).
- Unique *combinations* of several fields aren't part of the import format (single-field `unique` only).
- The store example doesn't let customers create orders from the browser (server-only by design; checkout needs a server).
- Custom access rule expressions can't be authored in the UI.
- Section 3 items 19 and 20 are untested by me; treat surprises there as valid findings.
- `pb-stress.mjs` against a deployed project now needs `STRESS_TABLE=<table>` (a server key can't create the table any more).

## 6. Report template (`SCHEMA-IMPORT-REPORT.md`)

```
# Schema import report
Environment: OS / arch / PocketBase version / commit
Automated: schema-import __/12, unit __/16, other suites re-run: ___
| Section | PASS/FAIL | Notes |
Findings (each: ID SI-01…, severity Critical/High/Medium/Low, what you did, expected, actual, evidence file)
Gaps in DATBASE.md (quote the line, say what was unclear, suggest wording)
AI loop: Builder's JSON (attach), first-time import result, what needed human help
Recommendation: ship / fix first (list)
```
