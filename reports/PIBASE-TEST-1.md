# Pibase test 1 — branch and app readiness review

Date: 2026-10-06  
Verdict: **Not ready for release**

## Scope and branch state

- `main` (`0807126`) contains the older Next.js/PocketBase DATBase app.
- `pibase/m7` (`580d56c`) contains the current Go/PostgreSQL pibase app. Earlier `pibase/m*` milestone branches are ancestors of this branch.
- `fix/tier1-audit` (`19314bd` at review time) contains the Tier 1 auth and storage fixes. PR #1 targets `pibase/m7` and is still draft. It branched from `89936a4`, before the two later T2 hardening commits on `pibase/m7`.
- `ccr-c30bb396-0l8gsj` contains review reports for T2 through T5. The other `pibase/review-*` branches carry gate and review history.

The current Go app and Tier 1 fixes have no single pushed release ref. A local `git merge-tree --write-tree origin/pibase/m7 HEAD` produced tree `2e19d8193b0f604f3edc138c8510b67c37d5ea3a` without conflicts. The tests below used an exported snapshot of that combined tree; it was not deployed.

## Test evidence

| Target | Check | Result |
|---|---|---|
| Combined `pibase/m7` + Tier 1 PR | `go test -p 1 -count=1 -timeout=180s ./...` with disposable PostgreSQL 16 | Passed in all Go packages |
| Combined tree | `./scripts/test.sh` | Passed: Go checks and integration tests, production image, PostgreSQL image, Linux ARM64 build |
| `main` Next.js app | `npm test` | 33 unit tests passed |
| `main` Next.js app | `npm run build` with documented build placeholders | Passed |
| Running local DATBase container | Read-only HTTP probes | `/login` returned 200; `/dashboard` redirected unauthenticated requests |

The running Next.js app container was created on 2026-09-28, before the reviewed `main` commit. Its logs reported `BACKUP_S3_* not set: backups are OFF`. A local HTTPS probe failed; the container's Caddy label specifies an `http://` host. These observations describe this local deployment, not every possible deployment configuration.

## Findings

### P2 — valid bucket names containing `x` or `0` are rejected

`internal/gateway/storage.go` passes a raw string literal to `strings.ContainsAny` where the intended NUL escape is interpreted as the characters `x` and `0`. A temporary test against the combined tree confirmed that `storageBucketName("box")` and `storageBucketName("2026-files")` both return `invalid bucket name`. The temporary test was removed after reproduction.

Replace the raw literal with an explicit NUL check and separator checks. Add cases for ordinary names containing `x` and digits alongside the traversal tests.

### Release integration and deployment checks remain open

PR #1 is draft and its fixes are not part of `pibase/m7`. The combined source passed local tests, but the exact combination has not been deployed to the Pi or exercised through a fresh browser/end-to-end deployment. The older Next.js app's full end-to-end suite was not rerun in this review. Its local deployment has backups disabled and did not answer the HTTPS probe.

### Dependency advisories on the older Next.js app

`npm audit` against `main` reported six advisories: two high and four moderate. The high findings were `sharp` 0.35.4 (`GHSA-wq5f-xc86-pv6w`) and `source-map-js` 1.2.1 (`GHSA-68fv-2mgg-jv7q`). This is dependency inventory evidence, not proof that either advisory is exploitable through this app. Update or assess the dependency paths before releasing that app.

## Readiness criteria

1. Fix the bucket validator and keep the new regression test.
2. Integrate PR #1 with the latest `pibase/m7`, then run the release gate on that exact commit.
3. Verify the intended production deployment on the target Pi, including HTTPS, backups and restore, and representative end-to-end flows.
4. If the older Next.js app is the intended release, resolve or document the dependency advisories and run its full end-to-end suite against an isolated stack.
