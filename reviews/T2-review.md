# T2 review: customer accounts, sessions, ownership

Date: 2026-10-06
Reviewed: `09c7daa` (T2a), `6786829` (T2b) on `pibase/m7`
Fix commit: `9249b05` on `pibase/m7`
Status: **fixed, tests green against real PostgreSQL 16**

## What was checked and held up
- Passwords: argon2id; equal-cost dummy verify on unknown email (no timing oracle).
- Sessions: 32-byte random token, only its SHA-256 stored; HttpOnly, SameSite=Strict, Secure outside test; 24 h TTL matches cookie Max-Age.
- Ownership: every project route calls `checkOwner` (404 unknown / 403 not yours); extensions operator-only; account project list filtered by `owner_id`.
- Account deletion requires typing the email and deletes owned projects first.

## Findings and fixes

| # | Severity | Finding | Fix |
|---|---|---|---|
| R1 | High | Changing the password left every other session valid; a stolen cookie survived the change. | `ChangePassword` deletes all sessions of the account except the caller's, in the same transaction as the hash update. Test H1. |
| R2 | High | `clientIP` used the first `X-Forwarded-For` entry from any peer, so signup/login/password rate limits were bypassed by sending a random header. | XFF honoured only from a loopback peer, and only its last (proxy-written) entry. Unit test `TestClientIPIgnoresSpoofedForwardedFor`. |
| R3 | High | No cap on projects per account; anyone who can reach the dashboard can sign up and create unlimited Postgres databases. | `PIBASE_MAX_PROJECTS_PER_ACCOUNT` (default 2, in `compose.yaml`); 403 above it. Operators unaffected. Test H2. |
| R4 | High (T5) | `PATCH /projects/{id}/limits` was owner-allowed, so a customer could raise their own database quota. | Operator-only. Test L4 now expects 403. |
| R5 | Medium | Password change shared the login IP bucket with a different limit. | Own limiter keyed by account ID. |
| R6 | Medium | Rate limiter map never dropped keys (unbounded memory, worse with R2). | Stale keys swept once per window. |
| R7 | Medium | If setting the owner failed after `CreateProject`, an ownerless project was left behind. | Project is deleted on that failure. |
| R8 | Low | Expired sessions were never removed. | Purged on each `IssueSession`. |
| R9 | Low | Signup accepted any non-empty string as email. | Requires `local@domain`, no whitespace, ≤ 254 chars. Test H3. |

## Known limits, not changed
- Behind a reverse proxy that reaches the admin container from the Docker bridge (not loopback), `X-Forwarded-For` is ignored and all clients share one rate-limit bucket. This is the strict side; a trusted-proxy setting would be needed to refine it.
- The project cap is checked before creation, not under a lock; two simultaneous creates can exceed it by one.
- No per-email login lockout (would let anyone lock a user out); limits stay per IP.
- `DeleteAccount` is not atomic across projects; a failure midway leaves the account with fewer projects, and retrying finishes the job.

## Correction to earlier gate reports
`T3-gate.md` and `T5-gate.md` said their integration tests passed. When those were written, `PIBASE_TEST_ADMIN_DATABASE_URL` was unset, so every integration test **skipped**; only unit tests ran. They have now been run for real (local PostgreSQL 16) and pass: K1–K6b, U1–U3, L1–L4.

How to run them:
```
export PIBASE_TEST_ADMIN_DATABASE_URL='postgres://postgres@127.0.0.1:5433/postgres?sslmode=disable'
go test -p 1 -count=1 ./...
```
Use `-p 1`: packages share one cluster, and running them in parallel produced spurious failures in `internal/control` and `internal/backup` (both pass alone and serially).
