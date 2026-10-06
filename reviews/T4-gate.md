# T4 Gate: SMTP email delivery

Date: 2026-10-06  
Branch: pibase/m7  
Status: **PASS**

## Scope

Wire transactional email sending into the gateway's auth flows:

- **Signup confirmation** — after a successful `POST /auth/v1/signup`, send a
  confirmation link to the new user's address.
- **Password recovery** — after a successful `POST /auth/v1/recover`, send a
  reset link to the requested address.

## Implementation

### New file: `internal/gateway/mail.go`

| Symbol | Description |
|--------|-------------|
| `smtpConfig` | Struct: `host`, `port`, `user`, `pass`, `from`, `siteURL` |
| `smtpFromEnv()` | Reads `PIBASE_SMTP_{HOST,PORT,USER,PASS,FROM}` and `PIBASE_SITE_URL`; defaults port to 587 |
| `configured()` | Returns true only when both `host` and `from` are set |
| `send(to, subject, body)` | `net/smtp.SendMail`; uses `PlainAuth` when `user != ""` |
| `sendConfirmationEmail(to, token)` | Async goroutine; no-ops when unconfigured; log-only on error |
| `sendRecoveryEmail(to, token)` | Async goroutine; no-ops when unconfigured; log-only on error |

Links generated:
- Confirmation: `{siteURL}/auth/v1/verify?token={token}&type=confirmation`
- Recovery: `{siteURL}/auth/v1/verify?token={token}&type=recovery`

### `internal/gateway/server.go`

- Added `smtp smtpConfig` field to `Server` struct.
- Initialised with `smtp: smtpFromEnv()` in `New()`.

### `internal/gateway/auth_handlers.go`

- `authSignup`: calls `s.sendConfirmationEmail(...)` after the DB insert succeeds,
  before `writeJSON`. The call is a no-op when SMTP is unconfigured.
- `authRecover`: calls `s.sendRecoveryEmail(...)` after the DB update succeeds,
  before `writeJSON`. The call is a no-op when SMTP is unconfigured.

## Design decisions

**Non-blocking sends.** Email delivery runs in a goroutine. A failed SMTP send is
logged at `WARN` but never propagates an error to the caller. Signup and recover
succeed regardless of mail server availability.

**No-op when unconfigured.** `configured()` checks `host != "" && from != ""`. If
SMTP is not set in env, every send call returns immediately. No change in behaviour
for deployments without a mail server.

**Auth tokens still available in test mode.** `exposeAuthTokens` (requires
`PIBASE_ENV=test`) is unchanged; test suites that need the raw token read it from
the response body. SMTP is not exercised by unit or integration tests — it is an
ops concern.

**PlainAuth only when credentials are set.** If `PIBASE_SMTP_USER` is empty, no
auth object is passed to `SendMail` (some local/relay servers require this).

## Env vars

| Variable | Default | Purpose |
|---|---|---|
| `PIBASE_SMTP_HOST` | (empty — SMTP disabled) | Mail server hostname |
| `PIBASE_SMTP_PORT` | `587` | Submission port |
| `PIBASE_SMTP_USER` | (empty — no auth) | SMTP username |
| `PIBASE_SMTP_PASS` | (empty) | SMTP password |
| `PIBASE_SMTP_FROM` | (empty — SMTP disabled) | Envelope From address |
| `PIBASE_SITE_URL` | (empty) | Base URL for email links (trailing `/` stripped) |

## Build and test

```
go build ./internal/gateway/   # exit 0
go test ./...                   # all packages PASS
```

No new test cases — SMTP delivery is an integration point that requires a live
mail server. The token-in-response path (`exposeAuthTokens`) already covers the
auth flow end-to-end in the existing integration tests.
