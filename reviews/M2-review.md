# M2 review

**Commit reviewed:** `b7379e7e05f46aba33e182338a0252c29a0d88f2` on branch `pibase/m0`
**Environment:** Linux amd64, Go 1.24.7, PostgreSQL 16.13 native cluster (no Docker), Node 22.
**Verdict:** **BLOCK** (re-review required)

The auth data model is sound — per-project `auth` schema, argon2id hashing, `auth.uid()` as a locked-down `SECURITY DEFINER`, refresh-token rotation, hashed single-use action tokens, and cross-project sign-in isolation all check out. But three High issues make password auth unsafe on the 4 GB Pi target and one of them contradicts the M2 report: **no rate limiting on any auth endpoint, a ~73× login timing oracle, and unbounded 64 MB argon2 concurrency**. These are must-fix.

## What I ran

- `go test ./internal/gateway` → `TestM2AuthAndUID` **PASS** (covers `auth.uid()` RLS, reset-password replacement, refresh rotation/replay rejection, service-admin boundaries). VERIFIED.
- Live flow against a real project: signup → returns `confirmation_token` (because `PIBASE_AUTH_EXPOSE_TOKENS=true`) → `GET /auth/v1/verify?type=signup` (200) → `POST /auth/v1/token?grant_type=password` (200, access+refresh) → refresh. VERIFIED.
- Live security probes below.

## Contract checks (PLAN §5 M2, GoTrue subset)

- **Endpoints.** signup, `token?grant_type=password`, `token` refresh, `verify` (confirm + recovery), `GET/PATCH /auth/v1/user`, admin list/create/update/delete — present and working (`server.go` routes; live). **No `sign out` / `/auth/v1/logout`** route exists (finding M2-05).
- **`auth.uid()` and RLS.** VERIFIED: `SECURITY DEFINER`, `SET search_path=pg_catalog,auth`, `REVOKE … FROM PUBLIC`, granted to the four roles; RLS policies using it work from REST (`TestM2AuthAndUID`, and the storage owner policies in M3).
- **Passwords argon2id.** READ `internal/auth/crypto.go`: `m=64MiB, t=3, p=2`, length bounds 6–1024, constant-time verify. But see M2-03.
- **Refresh rotation.** READ + test: on refresh the old token gets `revoked_at` + `replaced_by`; a revoked/expired token is rejected. VERIFIED. (Reuse is *rejected* but does not trigger session-family revocation — minor, noted in M2-05.)
- **Confirm/reset tokens.** READ: stored as SHA-256 hashes, single-use (set NULL on use), expiring (24 h confirm, 1 h reset). VERIFIED.
- **auth schema not readable by anon/authenticated; A can't sign in to B.** VERIFIED: per-project `auth` tables, per-project secret; a project-A JWT is rejected by project B (M1 review, cross-project test).

## Findings

**M2-01 (High — no auth rate limiting).** `enforceRate` is called only from `restHandler`/`rpcHandler`; none of the auth handlers call it. Live: 30 rapid `POST /auth/v1/token` attempts → **0 returned 429** (all processed). Online password brute force is unthrottled. The playbook requires per-IP and per-email limits on sign-in/sign-up/reset; the report does not mention auth rate limits. Evidence: `internal/gateway/limits.go` call sites; `auth_handlers.go` has none.

**M2-02 (High — login timing oracle → user enumeration).** `authToken` does `if err != nil || !auth.VerifyPassword(...)`, so for an **unknown** email the expensive argon2 verify is skipped. Live median over 5:
- known user (wrong password): **94.7 ms**
- unknown user: **1.3 ms** (~**73×** faster)
An attacker enumerates valid emails by latency. On a Pi the gap widens. Contradicts the playbook's "unknown user vs wrong password doesn't differ wildly." Fix: always run a dummy argon2 verify against a fixed hash when the user is absent. Evidence: `auth_handlers.go:211-214`.

**M2-03 (High — unbounded argon2 concurrency; false report claim).** The M2 report states "argon2id … at most **2 concurrent hashes**." No such limiter exists: `HashPassword` calls `argon2.IDKey` directly with `p=2` **threads per hash** (not a request-concurrency cap), and the only semaphore in the codebase is the M0 measurement helper. At 64 MiB/hash and with auth unthrottled (M2-01), ~20 concurrent sign-ins/logins ≈ **1.28 GB** of transient argon2 allocation on a 4 GB Pi → OOM/DoS. This is both a false claim in the gate report and a real availability risk on the target device. Evidence: `internal/auth/crypto.go`; `grep` for any hashing semaphore → none.

**M2-04 (Medium — signup user enumeration).** `POST /auth/v1/signup` with an existing email returns `409 "user already registered"` (live confirmed). The playbook says signup/reset must not reveal account existence (or document it); it is not documented. (`/auth/v1/recover` is well-behaved — it returns 200 regardless of whether the email exists.)

**M2-05 (Medium — no sign-out / session revocation).** There is no `/auth/v1/logout` route, so `supabase-js` `auth.signOut()` cannot revoke a session server-side; only rotation/expiry retire refresh tokens. The playbook lists "sign out" as required and "sign-out revokes" as a check. Related: refresh-token *reuse* is rejected but does not revoke the descendant session family (Supabase does).

**M2-06 (Low — no `aud` check).** Access tokens carry `aud:"authenticated"` but `keys.VerifyJWT` never checks `aud`. The playbook lists "wrong audience" as an attack to reject. Low risk (role + per-project HS256 already bind the token), but a divergence.

**M2-07 (Low — email delivery unimplemented; token-exposure switch).** No mail provider; confirm/reset tokens are only returned when `PIBASE_AUTH_EXPOSE_TOKENS=true` (the report discloses this). No redirect allowlist exists yet (moot without email, but required once it does). Operational risk: `PIBASE_AUTH_EXPOSE_TOKENS` must never be true in production — it returns confirmation/reset tokens in API responses. Recommend a hard guard against enabling it outside tests.

**M2-08 (Low).** `authAdminCreate` lowercases email without `TrimSpace` (signup uses `TrimSpace`) — inconsistent normalization can create near-duplicate accounts.

## Claims in the engineer's report I could not confirm
- "at most 2 concurrent hashes" — **contradicted** (M2-03).
- "Measure RSS spike while 20 simultaneous sign-ins run" — no such measurement is in the repo; UNVERIFIED (and, given M2-03, likely unbounded).
- `bin/pibase-linux-arm64` SHA-256 — artifact not committed.

## Scope creep
None specific to M2 beyond X-01 (bundling).

## Fix list for Codex (must-fix are blocking)
1. **(blocking)** Add per-IP and per-email rate limiting to signup/token/recover. (M2-01)
2. **(blocking)** Constant-time login: always perform an argon2 verify for unknown users. (M2-02)
3. **(blocking)** Bound concurrent password hashes with a real semaphore (e.g. 2), and make the report match the code. (M2-03)
4. Make signup non-enumerable (or document the trade-off). (M2-04)
5. Add `/auth/v1/logout` with session revocation; add refresh-reuse family revocation. (M2-05)
6. Verify `aud`; guard `PIBASE_AUTH_EXPOSE_TOKENS` against production; add redirect allowlist when email lands; `TrimSpace` admin emails. (M2-06/07/08)

## Questions for the planner
- Are per-IP limits expected at the gateway or at Caddy? (Affects where M2-01 is fixed.)
- Is user-initiated sign-out in the M2 contract or deferred to M4?
