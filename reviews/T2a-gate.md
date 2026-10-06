# T2a Gate — Customer Accounts

**Status:** DRAFT — not implemented  
**Scope:** Tier 2, part A  
**Code branch:** `pibase/m7` (continuing from `c0efb64`)  
**Review branch:** `pibase/review-m7`  
**Written:** 2026-10-06  

---

## What T2a adds

T1 (M7a + M7b) gave the operator a working dashboard secured by a single shared token.
T2a replaces the shared-token model with per-customer accounts: email + password login,
a session cookie, and project ownership — every dashboard action is authorised against the
account that owns the project. The operator token becomes a super-admin fallback, hidden
from customers.

Out of scope for T2a (deferred):
- Password reset via email (needs SMTP / outbox wiring; later milestone)
- Account deletion
- Multiple owners per project (teams, invites)
- OAuth / SSO

---

## Schema changes

### New: `control.accounts`

```sql
CREATE TABLE control.accounts (
  id            uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
  email         text        NOT NULL UNIQUE CHECK (email = lower(trim(email))),
  password_hash text        NOT NULL,
  created_at    timestamptz NOT NULL DEFAULT now()
);
```

Email normalised to lowercase + trimmed at write time (same as `auth.users` in the gateway).
Password hashed with `auth.HashPassword` (argon2id, same as M7b user create).

### New: `control.sessions`

```sql
CREATE TABLE control.sessions (
  token_hash  bytea       PRIMARY KEY,           -- SHA-256 of the opaque token
  account_id  uuid        NOT NULL REFERENCES control.accounts(id) ON DELETE CASCADE,
  expires_at  timestamptz NOT NULL,
  created_at  timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX ON control.sessions (account_id);
CREATE INDEX ON control.sessions (expires_at);
```

Token: 32 random bytes, base64url-encoded, stored only as SHA-256 hash (same pattern as
`auth.RandomToken` in `internal/auth/crypto.go`). TTL: 24 hours from creation.

### Modified: `control.projects`

```sql
ALTER TABLE control.projects
  ADD COLUMN owner_id uuid REFERENCES control.accounts(id);
```

Nullable to allow a migration path: existing projects created before T2a have `owner_id = NULL`
and are visible only to the operator token, not to any account.

### View: `control.project_catalog`

The view must include `owner_id`. Add to the `SELECT` list and recreate it (the schema upgrade
pattern already drops and recreates the view each bootstrap).

---

## New endpoints

All new endpoints are on the admin server. They use `Content-Type: application/json`.

| Method | Path | Auth required | Description |
|--------|------|--------------|-------------|
| POST | `/_pibase/auth/signup` | none | Create account + issue session |
| POST | `/_pibase/auth/login` | none | Verify credentials + issue session |
| POST | `/_pibase/auth/logout` | session cookie | Revoke session + clear cookie |
| GET | `/_pibase/auth/me` | session cookie | Return `{id, email, created_at}` |

### `POST /_pibase/auth/signup`

Request: `{"email": "...", "password": "..."}` (password 8–1024 chars)  
Success: 201, sets `pibase_session` cookie, body `{id, email, created_at}`.  
Errors: 400 (invalid input), 409 (email already exists, constant-time to prevent enumeration).  
Rate limit: 5 sign-ups per IP per minute.

### `POST /_pibase/auth/login`

Request: `{"email": "...", "password": "..."}`  
Success: 200, sets `pibase_session` cookie, body `{id, email, created_at}`.  
Errors: 401 (wrong credentials — same response whether email absent or password wrong, to prevent
enumeration; always runs argon2id with dummy hash for equal-cost path when email not found).  
Rate limit: 10 attempts per IP per 10 minutes (sliding window, in-memory, not persisted).

### `POST /_pibase/auth/logout`

Session cookie required; if absent, 401.  
Deletes the session from `control.sessions`. Sets `pibase_session` to `Max-Age=0`.  
Returns 200 with empty body.

### `GET /_pibase/auth/me`

Session cookie required; if absent or expired, 401.  
Returns `{id, email, created_at}` for the authenticated account.

---

## Session cookie

Name: `pibase_session`  
Flags: `HttpOnly; Path=/; SameSite=Strict; Max-Age=86400`  
Set `Secure` when `PIBASE_ENV != "test"` (avoids breaking local HTTP dev runs).  
Value: opaque 32-byte random token, base64url-encoded, **never logged**.

Expiry cleanup: a goroutine in the admin server purges `expires_at < now()` rows from
`control.sessions` every hour.

---

## Authorization model

Two auth identities, checked in order:

1. **Operator token** (`Authorization: Bearer <PIBASE_OPERATOR_TOKEN>`) — super-admin, no
   ownership constraint, sees/manages all projects. Available only over the loopback listener
   (`127.0.0.1`). The dashboard page never shows the token input to customers; the operator
   accesses it via the admin CLI or a separate loopback URL.
   
2. **Account session** (`pibase_session` cookie) — sees and manages only projects where
   `owner_id = account.id`.

If neither is present: 401.

The `authorized` function in `server.go` becomes `authorizeRequest(r) (identity, error)` where
`identity` carries either `{kind: "operator"}` or `{kind: "account", id: uuid, email: string}`.

### Per-endpoint enforcement

| Endpoint | Account allowed? | Operator only? |
|----------|-----------------|---------------|
| `GET /_pibase/projects` | yes — filters to `owner_id = account.id` | no |
| `POST /_pibase/projects` | yes — sets `owner_id = account.id` | no |
| `DELETE /_pibase/projects/{id}` | yes — must own project | no |
| `POST /_pibase/projects/{id}/sql` | yes — must own project | no |
| `POST /_pibase/projects/{id}/migrations` | yes — must own project | no |
| `POST /_pibase/projects/{id}/extensions/{e}` | operator only | yes |
| `POST /_pibase/projects/{id}/backup` | yes — must own project | no |
| `GET /_pibase/projects/{id}/backups` | yes — must own project | no |
| `GET /_pibase/projects/{id}/tables` | yes — must own project | no |
| `GET /_pibase/projects/{id}/tables/{t}/rows` | yes — must own project | no |
| `GET /_pibase/projects/{id}/users` | yes — must own project | no |
| `POST /_pibase/projects/{id}/users` | yes — must own project | no |
| `DELETE /_pibase/projects/{id}/users/{uid}` | yes — must own project | no |

"Must own project": check `SELECT 1 FROM control.projects WHERE id=$1 AND owner_id=$2`. If the
project doesn't exist: 404. If it exists but is owned by someone else: 403 (not 404 — the
project list already reveals which projects the account owns).

Extensions remain operator-only because they modify the shared database cluster, not just
the project.

---

## Dashboard page changes

The page needs a login/signup flow before the operator token prompt. Proposed layout:

1. If no `pibase_session` cookie: show "Log in" form (email + password) and a "Sign up" link.
   No token input visible to end users.
2. After login: show the project list for the authenticated account.
3. An "Admin" or operator mode: reachable only if the page is served with a query param
   (`/dashboard?operator=1`) or a separate path (`/admin`). Only the operator token input
   appears there. Alternatively, keep the existing token input but move it behind a hidden
   path to avoid confusion.
4. Logout button in the side menu.

The exact UI layout is an implementation decision; the gate requirement is that:
- No customer sees the operator token input on the default `/dashboard` path.
- Session is stored in the cookie only, never in `localStorage` or `sessionStorage`.
- The login/signup forms use the new `/_pibase/auth/*` endpoints.

---

## Typed test matrix

All tests use real Postgres (the existing `PIBASE_TEST_ADMIN_DATABASE_URL` pattern).

### Unit tests (`internal/adminserver/server_test.go` or new `auth_test.go`)

| # | What | Expected |
|---|------|---------|
| U1 | Signup with valid email/password | 201, `Set-Cookie: pibase_session=...`, body has id and email |
| U2 | Signup duplicate email | 409 |
| U3 | Signup password < 8 chars | 400 |
| U4 | Login correct credentials | 200, cookie set |
| U5 | Login wrong password | 401, same JSON shape as absent email |
| U6 | Login absent email | 401, same JSON shape as wrong password |
| U7 | Logout with valid session | 200, `Set-Cookie: pibase_session=; Max-Age=0` |
| U8 | Logout with no cookie | 401 |
| U9 | `GET /_pibase/auth/me` with valid session | 200, `{id, email, created_at}` |
| U10 | `GET /_pibase/auth/me` with expired session | 401 |

### Integration tests (`internal/adminserver/integration_test.go`)

| # | What | Expected |
|---|------|---------|
| I1 | Account creates project; project list returns only that project | 200, 1 project |
| I2 | Second account cannot access first account's project SQL endpoint | 403 |
| I3 | Operator token can access any project | 200 |
| I4 | Account cannot access extensions endpoint | 401/403 |
| I5 | Project created with account has `owner_id = account.id` in DB | verified via direct query |
| I6 | Project created with operator token has `owner_id = NULL` | verified via direct query |
| I7 | Session cookie not present → all `/_pibase/projects/...` endpoints → 401 | all 401 |
| I8 | Expired session (`expires_at` set to past in DB) → 401 | 401 |
| I9 | Login then logout then reuse cookie → 401 | 401 |
| I10 | `GET /_pibase/projects` with account session returns only owned projects | correct count |
| I11 | Account deletes own project; then GET returns 404 | 404 |
| I12 | Account cannot delete another account's project | 403 |

---

## "Do not regress" list

The following T1 controls must still pass after T2a is shipped:

| Control | Test location |
|---------|--------------|
| `validateOwnerSQL` blocks role/privilege changes | `TestSQLFilterComprehensive`, `TestSQLFilterReviewBypasses`, `TestSQLFilterSecondReview` |
| Owner connection cannot reach other projects after `RESET ROLE` | `TestOwnerConnectionReach` |
| `ListTables` uses one project connection | `TestListTablesUsesOneProjectConnection` |
| Browse rows paging stable (no duplicates, correct page boundaries) | `TestBrowseRowsPagingIsStable` |
| Keys shown once in create response; never in project list | `TestDashboardShowsKeysOnceAndWiresEveryEndpoint` |
| Extension DDL blocked in SQL tab | `TestExtensionDDLIsOperatorOnly` |
| Extension DDL blocked in migrations (DB guard) | `TestExtensionGuardRejectsDynamicCustomerDDL` |
| RLS + anon_readable accurate (5-case table set) | `TestAnonReadable` |
| Owner SQL bypass spellings blocked | `TestSQLFilterReviewBypasses`, `TestSQLFilterSecondReview` |
| Backup create + list | `internal/adminserver/integration_test.go` |
| User create + list + delete | `internal/adminserver/integration_test.go` |
| Migration apply + list | `internal/adminserver/integration_test.go` |
| Browser localStorage / sessionStorage not written | `TestDashboardShowsKeysOnceAndWiresEveryEndpoint` |
| Gateway operates independently | `scripts/test-supabase-js.sh` |
| supabase-js acceptance (M6a) | `scripts/test-supabase-js.sh` |
| Realtime acceptance (M5b) | `tests/realtime-js.mjs` |

Run the full test suite before and after the T2a implementation:

```
go test -p 1 -count=1 ./internal/adminsql ./internal/adminserver ./internal/control ./internal/gateway ./internal/backup
```

Both runs must be green. Record the before/after summary in `reports/T2a.md`.

---

## Implementation order

1. **Schema**: Add `control.accounts`, `control.sessions`, `owner_id` on `control.projects`.
   Extend bootstrap to apply new columns idempotently. Unit test: bootstrap is idempotent when
   tables already exist.

2. **Auth package helpers**: `auth.RandomToken` already exists. Add `adminserver.issueSession`
   (writes to `control.sessions`, sets cookie), `adminserver.resolveSession` (reads cookie,
   looks up in DB, checks `expires_at`). These are internal helpers, not exported.

3. **`authorized` → `authorizeRequest`**: Returns `(identity, bool)`. Account path reads the
   cookie, calls `resolveSession`. Operator path checks the bearer token as today. Keep
   constant-time comparison for the token.

4. **New auth endpoints**: Signup, login, logout, me. Add to the handler map.

5. **Ownership enforcement helper**: `checkOwner(ctx, conn, projectID, identity) error`.
   Returns `nil` for operator, queries `control.projects` for account identity.

6. **Wire enforcement** into every project-scoped endpoint. Remove the old single `authorized`
   call from the `/_pibase/` prefix guard; each endpoint calls `authorizeRequest` and
   `checkOwner` directly (the extensions endpoint remains operator-only).

7. **Dashboard page**: Add login/signup UI, hide operator token input from the default path,
   add logout button.

8. **Tests**: Write all tests in the matrix above before marking any task done.

9. **Gates**: Run `scripts/test.sh` and `scripts/test-supabase-js.sh`. Record output under
   `reports/raw/`.

---

## Known gaps / open questions

- **Rate limiting**: The spec above calls for in-memory rate limiting on login and signup.
  A persistent rate limit table (in `control`) would survive restarts but adds schema complexity.
  For T2a, in-memory is sufficient because the admin server is single-process.
  
- **Operator dashboard path**: The exact path to separate operator vs. customer views is an
  implementation decision. Option A: `/admin` (operator) vs. `/dashboard` (customer).
  Option B: single `/dashboard` with a query param that shows the token input. Choose before
  implementation; document in `reports/T2a.md`.

- **Session expiry on login**: Should a new login from the same account revoke all previous
  sessions? For T2a, no — multiple active sessions (e.g. phone + laptop) are allowed.
  Single-session enforcement is a later hardening step.

- **Email normalisation**: The gate spec requires `lower(trim(email))`. Verify the gateway
  does the same for `auth.users` (it does: `strings.ToLower(strings.TrimSpace(email))` in
  `auth_handlers.go`). Use the same function.

---

## Acceptance checklist (for the owner before closing T2a)

- [ ] Sign up with a new email → project list is empty
- [ ] Create a project → it appears in the list; keys shown once
- [ ] Log out → redirected to login; cookie gone
- [ ] Log in again → same project visible
- [ ] Sign up with a second account → second account cannot see first account's project
- [ ] Operator token (via `/admin` or `Authorization: Bearer`) → can see all projects
- [ ] Run `go test -p 1 -count=1 ./internal/...` → all green
- [ ] Run `scripts/test-supabase-js.sh` → all green

---

## Report (`reports/T2a.md`)

Same structure as `reports/M7b.md`:
- Built: commit + `go build ./...` clean  
- Test before (all green or list failures)  
- Tests added (list by name)  
- Test after (all green)  
- Gates (raw output under `reports/raw/`)  
- Known gaps  
- Acceptance checklist  
