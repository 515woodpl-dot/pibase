# M6a gate report — TOTP multi-factor authentication

**Branch:** `pibase/m6a`
**Environment:** Raspberry Pi 4 Model B, Linux arm64, Go 1.23 in Docker,
PostgreSQL 16 in Docker
**Status:** audit complete; approved findings fixed in `7a24837`

## Built

- Supabase Auth-compatible TOTP enrollment, challenge, verification, factor
  listing through `getUser`, and unenrollment endpoints.
- Random 160-bit Base32 secrets encrypted at rest with the existing AES-GCM
  master key. Enrollment returns an `otpauth` URI and the raw SVG QR payload
  that `supabase-js` converts to a data URL.
- Five-minute challenges, the existing dual IP/user auth rate limiter, and an
  atomic last-counter check that prevents reuse of a TOTP time step.
- `aal1` and `aal2` JWT claims with authentication-method references. Refresh
  rotation preserves AAL while a verified factor exists and downgrades after
  the last verified factor is removed.
- Bootstrap-owned, idempotent schema upgrades for existing auth-enabled
  project databases. Intentionally schema-less M0 measurement projects are
  detected and left unchanged.
- Scheduler scans release idle physical project connections after each
  project. The M6a Compose gate exposed the prior M5 behavior retaining one
  connection per scanned project and exhausting PostgreSQL at 100 projects.

Only TOTP is supported. Phone MFA, WebAuthn, and recovery codes remain outside
M6a. OAuth, PITR, image transforms, S3 API, and PostGIS remain later M6 slices.

## Evidence

- The endpoint and response contract was checked against the current Supabase
  Auth OpenAPI document and current `supabase-js` MFA implementation on
  2026-10-02.
- `TestTOTPMatchesRFC6238SHA1Vector` covers the RFC 6238 SHA-1 vector and an
  incorrect code.
- `TestBootstrapUpgradesExistingProjectMFA` removes the M6a objects from a live
  project, reruns bootstrap, and verifies recreation plus ownership.
- `TestM6TOTPFlowAndAAL` exercises live signup/login, enrollment, factor
  listing, QR/URI output, challenge, verification, challenge replay rejection,
  refresh rotation, AAL preservation, protected unenrollment, and AAL
  downgrade against PostgreSQL.
- `tests/supabase-js.mjs` exercises the same public flow through the current
  `@supabase/supabase-js` client.

## Gate results

| Check | Result |
|---|---|
| `git diff --check` | pass |
| shell syntax and setup regression via `scripts/test.sh` | pass |
| `gofmt`, `go vet ./...`, and `go test ./...` in Go 1.23 container | pass |
| live `internal/control` and `internal/gateway` suites against PostgreSQL 16 | pass |
| fresh production image `pibase:m6a-gate3` on Linux arm64 | pass |
| static `GOOS=linux GOARCH=arm64` build | pass |
| Node 22 + current bundled `@supabase/supabase-js` acceptance | pass |
| repeated bootstrap with the fixed gateway live | pass |
| live idle connection count after scanning 100 measurement projects | 2 authenticator connections (previously 37) |

The complete commands were:

```text
PIBASE_IMAGE=pibase:m6a-gate3 ./scripts/test.sh
PIBASE_IMAGE=pibase:m6a-gate3 ./scripts/test-supabase-js.sh
```

## Carryovers

- M4-03 remains pending a fresh disposable-Pi execution of `setup-pi.sh`.
- The independent audit requested for the M4 preflight commit remains deferred
  at the operator's direction.
