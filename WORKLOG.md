# Codex worklog

Last updated: 2026-09-30

## Current milestone

M1 — data API and SQL — implementation and verification complete on
2026-09-30; awaiting owner/auditor gate review. The M0 gate was approved by the
owner on 2026-09-30. The authoritative scope and security rules are in
`PLAN.md`.

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

## Repository state

The `pibase/m0` worktree intentionally contains the large replacement of the
old Node/PocketBase application with the Go/PostgreSQL design. Do not restore
the staged old-tree deletions or discard the untracked Go implementation.
