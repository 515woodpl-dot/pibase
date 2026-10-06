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

## Follow-up: former known limits, now fixed (`580d56c` on `pibase/m7`)
| # | Was | Fix | Test |
|---|-----|-----|------|
| L1 | Behind a proxy on the Docker bridge, `X-Forwarded-For` was ignored, so every client shared one rate-limit bucket. | `PIBASE_TRUSTED_PROXIES` (IPs/CIDRs, in `compose.yaml` and `.env.example`). From a trusted peer the chain is walked right to left; the first untrusted, valid address is the client. Junk values fail startup. | `TestClientIPIgnoresSpoofedForwardedFor`, `TestParseTrustedProxiesRejectsJunk` |
| L2 | Cap checked without a lock. The earlier note said parallel creates "exceed it by one"; that was wrong: 4 parallel creates made 4 projects. | One server lock covers count + create + set owner, and account deletion. | H4: 4 parallel creates → exactly 2 (confirmed failing with the lock removed) |
| L3 | No per-email lockout. | 10 failed sign-ins for an email in 15 min → 429, even with the right password; success clears the count. Kept short because anyone who knows an email can trigger it. | H5, `TestLoginFailureCounter` |
| L4 | A partly failed account/project delete could not always be retried (e.g. database already dropped → retry errored; row deleted before roles → roles leaked). | `DeleteProject` uses `IF EXISTS` drops and removes the control row last; account delete keeps running if the client disconnects. | H4 (account delete removes all projects), H6 (retry after database already dropped) |

Remaining by design: the lock and the lockout counters are in memory, so they assume one admin process (as deployed) and reset on restart.

## Correction to earlier gate reports
`T3-gate.md` and `T5-gate.md` said their integration tests passed. When those were written, `PIBASE_TEST_ADMIN_DATABASE_URL` was unset, so every integration test **skipped**; only unit tests ran. They have now been run for real (local PostgreSQL 16) and pass: K1–K6b, U1–U3, L1–L4.

How to run them:
```
export PIBASE_TEST_ADMIN_DATABASE_URL='postgres://postgres@127.0.0.1:5433/postgres?sslmode=disable'
go test -p 1 -count=1 ./...
```
Use `-p 1`: packages share one cluster, and running them in parallel produced spurious failures in `internal/control` and `internal/backup` (both pass alone and serially).
