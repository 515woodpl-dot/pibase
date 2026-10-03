# M6a audit fixes — applied in commit 7a24837

**Branch:** `pibase/m6b`
**Audit:** `reports/M6a-audit.md`
**Files changed:** `internal/gateway/mfa.go`, `internal/control/schema.go`

---

## Fix 1 — M6a-01: session-scoped MFA revocation

**File:** `internal/gateway/mfa.go` lines 245–250

**Before:** After MFA verify issued a new aal2 session, the handler revoked
every refresh token for the user except the new one:

```go
newRefresh := session["refresh_token"].(string)
_, err = tx.Exec(r.Context(),
  `UPDATE auth.refresh_tokens SET revoked_at=COALESCE(revoked_at,now())
   WHERE user_id=$1 AND token_hash<>$2`, claims.Subject, tokenHash(newRefresh))
```

**After:** Only the caller's own aal1 session (the one being elevated) is
revoked. Other browser/device sessions are left intact:

```go
sessionID := cSessionID(claims)
if sessionID == "" {
    return httpError(http.StatusBadRequest, "invalid JWT session")
}
_, err = tx.Exec(r.Context(),
  `UPDATE auth.refresh_tokens SET revoked_at=COALESCE(revoked_at,now())
   WHERE id=$1 AND user_id=$2`, sessionID, claims.Subject)
```

`cSessionID` extracts `session_id` from the JWT claims — the same helper
already used by `authLogout`. The `id` column on `auth.refresh_tokens` is the
session identifier (set during `issueSession`).

**Why:** The old behavior silently logged out every other device when a user
verified MFA in one browser. GoTrue scopes revocation to the elevated session.

---

## Fix 2 — M6a-02: friendly_name uniqueness via expression index

**File:** `internal/control/schema.go` lines 60–62

**Before:** An inline table constraint `UNIQUE(user_id, friendly_name)`.
PostgreSQL treats NULLs as distinct in UNIQUE constraints, so multiple factors
with `friendly_name = ""` (stored as NULL via `NULLIF($3,'')`) all succeeded,
bypassing the uniqueness intent.

**After:** The inline constraint is removed. Two new statements handle both
fresh and existing databases:

```sql
ALTER TABLE auth.mfa_factors
  DROP CONSTRAINT IF EXISTS mfa_factors_user_id_friendly_name_key;
CREATE UNIQUE INDEX IF NOT EXISTS mfa_factors_user_friendly_idx
  ON auth.mfa_factors(user_id, COALESCE(friendly_name, ''));
```

- Fresh databases: `CREATE TABLE` has no UNIQUE clause; the expression index
  is created immediately after.
- Existing databases: the old inline constraint is dropped first (idempotent
  via `IF EXISTS`), then the expression index is created.

**Why:** `COALESCE(friendly_name, '')` maps NULL to the empty string inside
the index, so two unnamed factors now correctly collide. The 10-factor cap
was already a safety net, but the uniqueness semantics now match the intent.

---

## Fix 3 — audit report updated

**File:** `reports/M6a-audit.md`

Both findings have a **Status: FIXED** annotation and the fix list at the
bottom is struck through. No other audit content was changed.
