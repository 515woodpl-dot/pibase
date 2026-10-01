# Codex worklog

Last updated: 2026-10-01

## Current milestone

M4 — operations — implementation complete on `pibase/m4`, pending independent
audit and a fresh-Pi setup drill. The authoritative scope and security rules
remain in `PLAN.md`; current evidence and known limits are in `reports/M4.md`.

## M4 implementation

- Added authenticated static dashboard, owner SQL UI, health/Prometheus
  metrics, and per-project request/database/storage usage.
- Added streaming authenticated encrypted backup/restore with database and file
  state, guarded project matching, and an ephemeral PostgreSQL-client ops image.
- Added safe public-schema/data branching with fresh roles and keys, explicitly
  excluding auth and storage state.
- Added Pi setup/operations runbook and verified unit, real-Postgres,
  production-image, ARM64, live restore/branch, and Supabase JS regression gates.

## Completed M0

- M0 implementation, real-PostgreSQL isolation tests, production image, and
  static ARM64 build pass.
- Pi measurements for 1, 20, and 100 projects are complete.
- Gate report: `reports/M0.md`.
- Raw measurements (ignored by Git): `reports/raw/m0-memory.csv` and
  `reports/raw/m0-environment.txt`.
- Standalone ignored artifact: `bin/pibase-linux-arm64`; SHA-256
  `b7562a8a6eceec26ebf46caa0793ccaf62fc783b8fd365d848c79c107e2eb056`.
- The dedicated measurement stack was restored after integration testing and
  was healthy with 100 idle project pools at handoff.

## M2 contract

- `/auth/v1/signup`, password login, refresh-token rotation, email confirmation,
  password recovery/reset, and authenticated `/auth/v1/user` access.
- Argon2id password hashes; opaque refresh/action tokens are stored only as
  hashes and expire/revoke server-side.
- Access JWTs carry `sub`, `role=authenticated`, `aud`, `email`, and session
  claims. `auth.uid()` in each project database reads the request claims under
  a locked-down `SECURITY DEFINER` function.
- Service-key admin user listing, creation, update, ban/delete, with no admin
  endpoint available to anon/authenticated keys.
- Real PostgreSQL isolation/RLS tests, real `@supabase/supabase-js` auth tests,
  ARM64 verification, and `reports/M2.md`.

## M2 implementation order

1. Per-project `auth.users`, refresh-token, and action-token tables plus
   `auth.uid()` function and grants.
2. Password/opaque-token primitives and access-token claims.
3. Signup, password login, refresh rotation, verify/confirmation, recovery,
   reset, and `/auth/v1/user` endpoints.
4. Service-key admin user management and abuse-safe boundaries.
5. Supabase client and real-Postgres acceptance, docs, gate report, ARM64
   build, and push.

## Completed M2 evidence

- Gate report: `reports/M2.md`.
- Containerized static checks and all Go tests pass.
- Real PostgreSQL isolation/gateway tests pass, and a live gateway auth flow
  was exercised through signup, confirmation, login, and `/auth/v1/user`.
- Supabase JS auth acceptance covers signup, confirmation, login, and refresh.
- The consolidated M2 gate also covers recovery/reset, refresh replay
  rejection, `auth.uid()` RLS, and service-admin user lifecycle operations.
- Production image `pibase:m2` built successfully and the static ARM64
  artifact checksum is recorded in `reports/M2.md`.

## M3 contract

- Per-project storage buckets and objects with PostgreSQL RLS metadata.
- Filesystem-backed upload/download/list/delete, public objects, signed URLs,
  MIME/file limits, and project storage quotas.
- Real PostgreSQL RLS integration, real Supabase JS storage acceptance,
  production image, ARM64 artifact, and `reports/M3.md`.

## Completed M3 evidence

- Gate report: `reports/M3.md`.
- Consolidated static/unit/real-Postgres/image/ARM64 gate passes.
- Supabase JS acceptance passes Blob upload, public download, list, object
  removal, and bucket deletion.

## M1 contract

- PostgREST-shaped REST subset: CRUD, filters, ordering, pagination, exact
  count, upsert, RPC, one-level embedding, and clear 400 responses for
  unsupported syntax.
- Project resolution plus anon/service JWT authentication.
- Every data request runs in a transaction with `SET LOCAL ROLE`,
  `request.jwt.claims`, and a statement timeout so PostgreSQL enforces RLS.
- Owner migrations and owner SQL editor, isolated to the project's admin role
  and database.
- Request body, row, statement-time, rate, and usage/counting limits.
- Acceptance through real PostgreSQL and `@supabase/supabase-js`.
- Full static/unit/integration/image/ARM64 verification and `reports/M1.md`.

## Completed M1 evidence

- Gate report: `reports/M1.md`.
- `./scripts/test.sh` passes, including real-PostgreSQL tests, production image,
  and Linux ARM64 build.
- `./scripts/test-supabase-js.sh` passes with the real Supabase JavaScript
  client and cleans up its temporary project.
- Final ignored artifact: `bin/pibase-linux-arm64`, SHA-256
  `65e8f857504ccf4ccd3cc44ff4e6e03e35e6f968161b12addb4f243e35e9e021`.

## Implementation order

1. Project lookup, key verification, JWT verification, and bounded per-project
   pool cache.
2. Request-scoped transaction/role/JWT context and catalog-safe identifiers.
3. REST reads, then writes/upsert, RPC, and one-level embedding.
4. Owner migration and SQL boundaries.
5. Limits/counting, Supabase JS acceptance, docs, and gate report.

## Important test-state note

`internal/control/integration_test.go` calls `Bootstrap` with an integration
password. When it targets the Compose database, this changes the shared
`authenticator` password. After running that test against an already-running
gateway, rerun the Compose bootstrap and recreate the gateway. M1 should give
integration tests a disposable database/cluster path or restore the original
password automatically.

## Audit fix pass

- Added per-IP/email auth rate limits, constant-cost unknown-user login,
  two-slot Argon2 concurrency, audience validation, non-enumerating duplicate
  signup responses, logout refresh-session revocation, and refresh-family
  revocation on replay.
- Added `or`/`not` REST filters, owner-SQL privilege keyword guards and a
  1,000-row result cap.
- Added streamed storage writes up to 1 GiB, no-key public/signed downloads,
  safe download headers, storage rate/usage accounting, project storage cleanup,
  and committed M0 raw measurement evidence with a PSS-capable harness.
- Re-ran static/unit/real-Postgres/image/ARM64 gates and Supabase JS acceptance;
  the updated ARM64 artifact checksum is recorded in the milestone reports.
- Follow-up hardening caps project pools at one connection with one-second idle
  eviction, accounts auth requests, binds owner SQL to authenticator session
  authorization, normalizes admin emails, guards token exposure to test mode,
  and restricts private bucket metadata under RLS.

## Repository state

The `pibase/m0` worktree intentionally contains the large replacement of the
old Node/PocketBase application with the Go/PostgreSQL design. Do not restore
the staged old-tree deletions or discard the untracked Go implementation.
