# T2b Gate — Project Connection Info + Account Self-Service

**Status:** DRAFT — not implemented  
**Scope:** Tier 2, part B  
**Code branch:** `pibase/m7` (continuing from `4b494dc`)  
**Review branch:** `pibase/review-m7`  
**Written:** 2026-10-06  

---

## What T2b adds

T2a gave customers email/password accounts and scoped project ownership.  
T2b completes the loop: a customer who just created a project needs to know
where to point their frontend, needs to be able to rotate their password
without an email flow, and needs to be able to delete their account.

None of these require external services (SMTP, OAuth).

---

## Changes

### 1. Project API URL in the dashboard

The admin server learns `PIBASE_PUBLIC_URL` (operator-configured; e.g.
`https://api.example.com`). Each project's response now includes:

```json
{
  "api_url": "https://api.example.com/<project-id>"
}
```

The dashboard displays the API URL on each project card so the customer can
copy-paste it into their frontend.

**Config:**
```
PIBASE_PUBLIC_URL=https://api.example.com
```

When unset, `api_url` is omitted or empty. No error if missing — it is an
optional operator hint.

**Admin server Config struct:**
```go
PublicURL string  // e.g. "https://api.example.com", no trailing slash
```

**`ProjectStat` response field:**
```go
APIURL string `json:"api_url,omitempty"`
```

The `projects` handler sets `APIURL` on each stat before writing JSON.

---

### 2. Password change — `POST /_pibase/auth/password`

Requires a valid session cookie (account session only).

Request body:
```json
{
  "current_password": "...",
  "new_password": "..."
}
```

Rules:
- `new_password` must satisfy the same 8–1024 character constraints as signup
- Verifies `current_password` against the stored argon2id hash (uses `auth.VerifyPassword`)
- On success: updates `password_hash` in `control.accounts`, returns 204

Endpoint is under `/_pibase/auth/` so it is exempt from the operator-token check.

New control function:
```go
func ChangePassword(ctx context.Context, adminURL, accountID, currentPassword, newPassword string) error
```

Rate limit: 5 attempts per IP per 10 minutes (same store as login limiter; exempt in `Env=="test"`).

---

### 3. Account self-deletion — `DELETE /_pibase/auth/account`

Requires a valid session cookie.

Request body:
```json
{
  "confirm": "<account-email>"
}
```

The caller must type their own email as confirmation (prevents accidental deletion).

On success:
- Deletes all projects owned by this account (`control.DeleteProject` for each)
- Deletes the account row from `control.accounts` (sessions cascade-delete via FK)
- Clears the session cookie
- Returns 204

New control function:
```go
func DeleteAccount(ctx context.Context, adminURL, accountID, email string) error
// lists projects owned by accountID, deletes each, then deletes the account row
```

The admin master key is needed to delete projects (they hold encrypted secrets).
The `DeleteAccount` signature accepts `adminURL` only; project deletion reuses
`control.DeleteProject`.

The `Server` must hold the master key already (it does: `Config.MasterKey`) —
no new config needed.

---

## New auth endpoints summary

| Method | Path | Auth | Description |
|---|---|---|---|
| POST | `/_pibase/auth/password` | session cookie | Change password |
| DELETE | `/_pibase/auth/account` | session cookie | Delete account + projects |

Both are under `/_pibase/auth/` and bypass the operator-token check.

---

## New admin server config field

```
PIBASE_PUBLIC_URL  (optional)
```

Passed to `adminserver.Config.PublicURL`. When set, each project in
`GET /_pibase/projects` includes `"api_url": "<PublicURL>/<id>"`.

`cmd/pibase/main.go` `adminServe()` reads `os.Getenv("PIBASE_PUBLIC_URL")` and
passes it to Config.

---

## Test matrix

### Unit (no DB — in server_test.go)
None needed beyond what T2a already covers for auth middleware.

### Integration (DB required — in integration_test.go)

| ID | Test | Expected |
|---|---|---|
| U11 | POST /auth/password valid → 204 | password updated, new password works for login |
| U12 | POST /auth/password wrong current → 401 | no change |
| U13 | POST /auth/password short new → 400 | no change |
| U14 | POST /auth/password no session → 401 | rejected |
| I13 | DELETE /auth/account valid confirm → 204 | account + owned projects gone |
| I14 | DELETE /auth/account wrong confirm → 400 | no change |
| I15 | DELETE /auth/account no session → 401 | rejected |
| I16 | GET /projects with PIBASE_PUBLIC_URL set → api_url present | api_url = PublicURL+"/"+id |
| I17 | GET /projects without PIBASE_PUBLIC_URL → api_url absent/empty | no api_url field |

---

## Implementation order

1. Add `PublicURL string` to `Config`, wire `PIBASE_PUBLIC_URL` in main.go
2. Add `APIURL string json:"api_url,omitempty"` to `ProjectStat`, populate in `projects` handler
3. Add `ChangePassword` to `control/accounts.go`
4. Add `DeleteAccount` to `control/accounts.go`
5. Wire `authPassword` and `authDeleteAccount` handlers in `server.go`
6. Write tests U11–I17
7. Run full suite, commit, push

---

## Do-not-regress list

- All T2a tests (U1–U10, I1–I12) must still pass
- All M7a/M7b tests must still pass
- Dashboard `TestDashboardUIHardRules` must still pass
- `TestEveryAdminEndpointRejectsMissingToken` — the two new auth endpoints do
  NOT require a token (same as signup/login); the test's fixed path set does
  not include them

---

## Out of scope for T2b

- Password reset via email (needs SMTP)
- Multiple owners / team invites
- OAuth / SSO
- Transferring project ownership to another account
