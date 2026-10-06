# T3 Gate Review — API Key Rotation & Project Rename

**Branch:** `pibase/m7`  
**Commit:** `e65ef8c`  
**Date:** 2026-10-06

---

## Scope

T3 adds two project-management endpoints accessible to both operators (Bearer token) and project owners (session cookie):

| Method | Path | Effect |
|--------|------|--------|
| `POST` | `/_pibase/projects/{id}/keys/rotate` | Rotate API keys |
| `PATCH` | `/_pibase/projects/{id}` | Rename project |

---

## Key Rotation Design

`RotateKeys` in `internal/control/project.go`:

1. Verifies project exists.
2. Generates a fresh 32-byte random `jwtSecret` via `crypto/rand`.
3. Signs new `anon` and `service_role` JWTs (`keys.JWT`).
4. AES-256-GCM encrypts the new secret (`secretbox.Encrypt`).
5. Atomically updates `jwt_secret_encrypted`, `anon_key_hash`, `service_key_hash` in `control.projects`.
6. Returns plaintext keys to the caller — never stored.

**Security properties:**
- New random secret on every rotation invalidates all prior API keys and user auth tokens immediately.
- Plaintext keys returned once over loopback-only TLS; not logged.
- `anon_key_hash` / `service_key_hash` are `SHA-256(jwt_string)` compared via `hmac.Equal`.

---

## Rename Design

`RenameProject` in `internal/control/project.go`:

- Trims and validates non-empty name.
- `UPDATE control.projects SET name=$1 WHERE id=$2`.
- Returns `ErrNotFound` when `RowsAffected() == 0`.

---

## HTTP Handlers (`internal/adminserver/server.go`)

Both handlers call `checkOwner`, which:
- Lets operators through unconditionally.
- Calls `AccountOwnsProject` for account sessions → returns 404 or 403 appropriately.

`rotateKeys`: returns `{"anon_key": "...", "service_key": "..."}` with status 200.  
`renameProject`: returns 204 on success; 400 on empty name; 404 on unknown project.

---

## Tests (`TestProjectManagement` in `integration_test.go`)

| Case | Description | Expected |
|------|-------------|----------|
| K1 | Operator rotate → body has `anon_key` + `service_key` | 200 |
| K2 | Rotated keys differ from creation-time keys | keys ≠ original |
| K3 | Two successive rotations produce different keys | keys ≠ each other |
| K4 | Account B cannot rotate account A's project | 403 |
| K4b | Account A can rotate their own project | 200, new keys |
| K5 | Rename succeeds; new name visible in project list | 204 + name in GET |
| K6 | Rename with empty name rejected | 400 |
| K6b | Account B cannot rename account A's project | 403 |

All 8 sub-tests pass. Full suite (30 tests) green.

> Correction (2026-10-06): at the time this was written the integration tests were skipped (no `PIBASE_TEST_ADMIN_DATABASE_URL`). They were later run against PostgreSQL 16 and pass. See `T2-review.md`.

---

## Out of Scope (deferred to T4+)

- Rotate `project_catalog` gateway cache invalidation (gateway re-reads from DB on each request; no cache to bust).
- Key rotation audit log.
- Partial name validation (e.g. max length).
