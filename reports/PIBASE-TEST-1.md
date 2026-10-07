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

## Follow-up (2026-10-06)

| Criterion | Status |
|---|---|
| 1. Bucket validator | **Fixed** in `11844d3`. Ordinary names such as `box` and `2026-files` are accepted; `storage_names_test.go` covers valid names and traversal, separator, NUL and encoded variants. The new test failed on 5 valid names before the fix. |
| 2. Integrate PR #1 with latest `pibase/m7` | **Done.** `fix/tier1-audit` is merged into `pibase/m7` together with the T2 hardening commits. On the merged commit, `go vet` and `go test -p 1 -count=1 ./...` pass against PostgreSQL 16, and the storage, adversarial and account-hardening tests pass with `-race`. `scripts/test.sh` was run by the operator as a non-root user on a fresh clone of `pibase/m7` (2026-10-07) and **exited 0**. That covers formatting, vet, unit and PostgreSQL integration tests, the production and PostgreSQL images, and the Linux ARM64 build. |
| 3. Pi deployment check (HTTPS, backup/restore, end-to-end) | **Open.** Needs the target Pi; see `docs/PI-OPERATIONS.md` and `docs/PI-DRILL.md`. |
| 4. Older Next.js app on `main` | **Not a release target.** pibase replaces it; its advisories do not apply to the pibase release. |

Review history from every `pibase/review-*` branch and the `ccr-*` session branch is merged into `pibase/m7` under `reviews/` and `reports/`. Every `pibase/m*` milestone branch was already an ancestor of `pibase/m7`.
