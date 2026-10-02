# M6a audit — TOTP multi-factor authentication

**Commit reviewed:** `f72bfc0` on branch `pibase/m6a`
**Environment:** code review (READ), not live attack
**Auditor:** Claude (planner/auditor role per PLAN.md)
**Verdict:** **APPROVE WITH FIXES**

M6a delivers a clean, well-scoped TOTP MFA implementation that tracks the
Supabase Auth contract closely. The TOTP core (RFC 6238 SHA-1 with ±1 window,
constant-time comparison, encrypted-at-rest secrets, replay-resistant counters,
AAL claims, and idempotent schema upgrades) is sound. The scheduler connection
leak fix is an important operational improvement. The branch remap hardening for
extensions and extension-owned objects is correct.

Two medium findings and several low findings are listed below. Nothing reaches
the blocking threshold.

---

## Contract checks

| Item | Status | Tag |
|---|---|---|
| Enroll TOTP factor (POST /auth/v1/factors) | implemented | READ |
| Challenge (POST /auth/v1/factors/{id}/challenge) | implemented | READ |
| Verify (POST /auth/v1/factors/{id}/verify) | implemented | READ |
| Unenroll (DELETE /auth/v1/factors/{id}) | implemented | READ |
| Factor listing via getUser (/auth/v1/user) | implemented | READ |
| 160-bit random secret, Base32, AES-GCM at rest | implemented | READ |
| otpauth URI + SVG QR code in enroll response | implemented | READ |
| 5-minute challenge expiry | implemented | READ |
| Rate limiter on challenge and verify | implemented | READ |
| Atomic last-counter replay check | implemented | READ |
| aal1/aal2 JWT claims with AMR | implemented | READ |
| Refresh preserves AAL while verified factor exists | implemented | READ |
| Refresh downgrades AAL after last factor removed | implemented | READ |
| Idempotent schema upgrade for existing projects | implemented, tested | READ |
| M0 measurement DBs skipped during upgrade | implemented | READ |
| Scheduler releases idle connections after scan | implemented | READ |
| Factor cap (10 per user) | implemented | READ |
| Phone/WebAuthn/recovery codes explicitly deferred | confirmed | READ |
| supabase-js acceptance test | passes per gate report | UNVERIFIED |
| All Go/integration gates pass | passes per gate report | UNVERIFIED |

---

## Findings

### M6a-01 (Medium — verify revokes ALL prior refresh tokens, not just the aal1 session)

`mfa.go:246-247`: after issuing the aal2 session, the verify handler revokes
every refresh token for the user except the newly created one:

```go
_, err = tx.Exec(r.Context(),
  `UPDATE auth.refresh_tokens SET revoked_at=COALESCE(revoked_at,now())
   WHERE user_id=$1 AND token_hash<>$2`, claims.Subject, tokenHash(newRefresh))
```

This is correct for security (it forces a single active session after MFA
verification), but it means that if a user has multiple browser sessions at
aal1, verifying MFA in one browser silently invalidates all other sessions.
Supabase Auth's GoTrue implementation revokes only the session that was
elevated, not all sessions.

**Impact:** Unexpected session loss for multi-device users. Not a security bug
— it's more restrictive than necessary.

**Fix for Codex:** Scope the revocation to the current session only. The
current session is identified by `claims.SessionID` (the refresh token `id`
that issued the current access token). Revoke `WHERE id=$1` (the old aal1
session) instead of all tokens for the user, or at minimum document the
behavior difference.

**Status:** FIXED — `mfa.go:245-250` now uses `cSessionID(claims)` to revoke
only the aal1 session that was elevated, matching GoTrue behavior.

---

### M6a-02 (Medium — friendly_name uniqueness constraint allows NULL collision enumeration)

`schema.go:61`: `UNIQUE(user_id, friendly_name)` combined with `NULLIF($3,'')`
means two factors with empty `friendly_name` both store SQL NULL, and
PostgreSQL's UNIQUE treats NULLs as distinct. A user can create 10 factors all
with `friendly_name = ""`, bypassing the intent of uniqueness.

Meanwhile, the error handler at `mfa.go:99` catches `unique` in the error
string to return a 422 for duplicate names. If the intent is to allow unnamed
factors, the cap of 10 provides the safety net. But if the intent is "each
named factor has a unique name," the NULL handling is correct.

**Impact:** Minor UX issue — not a security gap given the 10-factor cap.

**Fix for Codex:** Either document that unnamed factors are allowed (and
multiple unnamed factors are distinct), or use `COALESCE(friendly_name,'')`
in the unique constraint to prevent duplicate empty names.

**Status:** FIXED — `schema.go` replaces the inline `UNIQUE(user_id,friendly_name)`
with an expression index `UNIQUE(user_id,COALESCE(friendly_name,''))`. Existing
databases get the old constraint dropped and the new index created via
`mfaUpgradeSQL`.

---

### M6a-03 (Low — challenge cleanup deletes all challenges for a factor, not just expired ones)

`mfa.go:175`: `DELETE FROM auth.mfa_challenges WHERE factor_id=$1` removes ALL
challenges for the factor before inserting the new one. The comment explains
this is intentional (TOTP challenges carry no server nonce). This is correct
and safe — just noting it differs from Supabase Auth which retains recent
challenges. The TOTP protocol makes this a valid simplification.

**Status:** no fix needed, behavior is sound.

---

### M6a-04 (Low — verified_at / expired check ordering)

`mfa.go:224`: `if err != nil || verified != nil || !expires.After(time.Now())`
conflates three distinct failure modes into one error message. If the challenge
was already verified (replay), the user gets "invalid or expired MFA
challenge" rather than a more specific message. The test at line 106 confirms
replays are rejected. Not a security issue — defensive behavior is correct.

**Status:** cosmetic only, no fix required.

---

### M6a-05 (Low — branch remap: `--clean --if-exists` flags removed from pg_dump)

The diff shows `--clean --if-exists` were removed from the branch pg_dump
command, replaced by `copyPublicDump` which filters out `CREATE SCHEMA
public;`. This is the correct approach: without `--clean`, the dump produces
only creation statements, and the `copyPublicDump` filter handles the one
collision case (the schema already exists in the fresh target). The previous
`--clean --if-exists` approach risked dropping objects that the fresh project
setup had already created. Good change.

**Status:** improvement, no fix needed.

---

### M6a-06 (Low — extension-owned objects excluded from branch security remap)

The branch remap queries now filter `NOT EXISTS (SELECT 1 FROM pg_depend d
WHERE d.classid=... AND d.objid=... AND d.deptype='e')` for relations,
columns, and functions. This prevents the remap from trying to regrant on
extension-internal objects (e.g., PostGIS's spatial_ref_sys). Correct fix for
the M4-01 issue.

**Status:** improvement, verified by the updated integration test.

---

### M6a-07 (Low — TOTP window could accept a code from 30 seconds in the future)

`verifyTOTP` accepts counter offsets -1, 0, +1. This is standard practice per
RFC 6238 §5.2 to accommodate clock skew. Combined with the `lastCounter`
replay check, a code cannot be reused even within the window. Sound.

**Status:** no fix needed.

---

### M6a-08 (Low — connection leak fix is the scheduler's `resetProjectPool`)

`outbox.go:43`: after processing each project's cron/delivery, the scheduler
calls `s.resetProjectPool(p.id)`. This calls `pool.Reset()` which closes idle
connections while keeping the pool object alive for concurrent users. The gate
report confirms this brought 100-project idle connections from 37 to 2.
Excellent fix.

**Status:** confirmed improvement.

---

## Claims in the gate report I could not confirm

| Claim | Status |
|---|---|
| All Go tests pass | UNVERIFIED — not run in this review session |
| supabase-js acceptance passes | UNVERIFIED — not run |
| ARM64 static build | UNVERIFIED — not run |
| Production image `pibase:m6a-gate3` | UNVERIFIED — not built |
| Idle connection count = 2 after scanning 100 projects | UNVERIFIED — measured by engineer |

These are expected UNVERIFIED items for a code-review-only audit. The gate
report provides the evidence trail.

---

## Scope creep

None observed. M6a is cleanly scoped to TOTP MFA plus the scheduler
connection-leak fix and branch extension handling. No M6b/OAuth/PITR code is
present.

---

## Fix list for Codex (priority order)

1. ~~**(M6a-01, medium)** Scope the MFA verify refresh-token revocation to the
   current session only, not all sessions for the user.~~ **FIXED**
2. ~~**(M6a-02, medium)** Clarify or fix the NULL `friendly_name` uniqueness
   semantics.~~ **FIXED** — expression index with `COALESCE`

---

## Questions for the planner

- The M4-03 fresh-Pi setup drill remains deferred. Should it be gated before
  any further M6 slices, or is it acceptable to carry forward?
- M6a adds PostGIS as a branchable extension alongside pgvector. Should
  `EnablePostGIS` get its own PLAN.md entry or is it covered under M6 expansion?
