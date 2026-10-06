# M0 review

**Commit reviewed:** `b7379e7e05f46aba33e182338a0252c29a0d88f2` on branch `pibase/m0`
**Environment:** Linux **amd64** (x86_64), Go 1.24.7, PostgreSQL **16.13** run as a **native cluster** (Docker is unavailable in this review sandbox), Node 22. This is **not** Apple Silicon/arm64 and **not** a Raspberry Pi, so no memory number below is a Pi number.
**Verdict:** **APPROVE WITH FIXES**

> Note on how this milestone was delivered: the branch is named `pibase/m0` but its unique commits are labelled M1, M2 and M3 ("replace legacy stack with pibase M1", "add M2 authentication", "add M3 storage"). There is no commit labelled M0 and there are no `pibase/m1..m3` branches. All four milestones landed on one branch, so the "stop at each gate, auditor approves before the next" discipline in PLAN.md §3/§5 did not happen. See finding **X-01** in SUMMARY.md. I reviewed M0's contract items against the tree at this commit.

## What I ran

| # | Command (abridged) | Result | Tag |
|---|---|---|---|
| 1 | `gofmt -l cmd internal` | no output (clean) | VERIFIED |
| 2 | `go vet ./...` (deps via GitHub mirrors) | clean | VERIFIED |
| 3 | `go build ./...` | ok | VERIFIED |
| 4 | `GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build ./...` and `... -ldflags="-s -w" -o pibase ./cmd/pibase` | ok; `file` → `ELF 64-bit LSB executable, ARM aarch64, statically linked, stripped` | VERIFIED |
| 5 | `go test ./internal/keys ./internal/secretbox` | `ok` (JWT + AES-GCM unit tests) | VERIFIED |
| 6 | `PIBASE_TEST_ADMIN_DATABASE_URL=… go test -p1 ./internal/control` | `ok` (isolation, authenticator attrs, anon auth-schema denial, distinct keys, encrypted secret) | VERIFIED |
| 7 | `pibase bootstrap` + `pibase project-create ×20` against native PG | roles/DBs/keys created | VERIFIED |
| 8 | `psql` inspection of `pg_roles`, `pg_database.datacl`, `pg_auth_members` | see below | VERIFIED |
| 9 | native 20-project light-load run, sampled `/proc/*/smaps` RSS + PSS | see measurement section | VERIFIED (amd64, not Pi) |

I could **not** run `scripts/test.sh`, `scripts/measure-m0.sh`, or `scripts/test-supabase-js.sh` as written — they require Docker, which is unavailable here. I reproduced their substance natively instead.

## Contract checks (PLAN.md §5 M0, §3)

- **One command brings up the stack; no `sudo`.** READ: `README.md` start is `./scripts/init-env.sh && docker compose up --build -d && curl /healthz`. `bootstrap` runs as a compose service; Postgres 16, `shared_buffers=128MB`, `max_connections=40`, `work_mem=4MB` match §3. No `sudo` appears in any script (`grep` clean). VERIFIED (READ) — I could not execute the compose path (no Docker).
- **`create project` creates DB, grants, JWT secret, anon+service keys.** VERIFIED: `project-create` produced isolated DB `p_<id>`, four roles, encrypted secret, and distinct anon/service JWTs; `control` integration test asserts the same.
- **Roles.** VERIFIED in `psql`:
  - `authenticator` → `rolsuper=f, rolinherit=f (NOINHERIT), rolcanlogin=t, rolbypassrls=f`.
  - `p_*_anon`, `p_*_authenticated`, `p_*_admin` → `NOLOGIN, NOSUPERUSER, rolbypassrls=f`.
  - `p_*_service_role` → `NOLOGIN, NOSUPERUSER, rolbypassrls=t` (BYPASSRLS ✓).
  - each project DB is owned by its `p_<id>_admin` (a non-superuser).
- **`REVOKE CONNECT … FROM PUBLIC`.** VERIFIED: `datacl` for each project DB shows PUBLIC has only `T` (TEMP), with CONNECT granted to `authenticator` and that project's roles only. A fresh `LOGIN` role with no grant was refused: `FATAL: permission denied for database … User does not have CONNECT privilege`. Cross-project: as `authenticator` connected to B, `SET ROLE p_A_admin; SELECT … FROM todos` → `permission denied for table todos`; a cross-database qualified reference is rejected by Postgres outright.
- **Keys.** VERIFIED (read + live): per-project HS256 JWTs signed with a per-project random secret; `anon` key cannot act as `service_role` (role-claim tampering needs the project secret to re-sign → rejected, see M1 review JWT tests); a key/JWT from project A is rejected by project B (secret mismatch). Secrets are AES-256-GCM encrypted at rest (`secretbox`), only SHA-256 key **hashes** are stored.
- **Secrets not logged.** VERIFIED: `grep` of the running gateway log for `eyJhbGci|secret|password|BEGIN|MASTER` → 0 matches.
- **Pool behaviour.** READ: per-project `pgxpool` `MaxConns=2, MinConns=0, MaxConnIdleTime=30s`; a global 32-slot semaphore gates concurrent transactions. See M1-03 for a cross-pool accumulation concern.
- **Cross-compile arm64.** VERIFIED (run #4).

## Measurement quality (the heart of the M0 gate)

**M0-01 (High — evidence missing from repo).** `reports/M0.md` cites `reports/raw/m0-memory.csv` and `reports/raw/m0-environment.txt` as the raw evidence, but `.gitignore` contains `reports/raw/`, so **those files are not in the repository** (confirmed: `git log --all -- 'reports/raw/*'` is empty). Per playbook ground rule 3, a RAM number is only "measured" if the command and raw output are shown. As committed, the M0 numbers have no reproducible backing.

**M0-02 (Medium — metric double-counts shared memory).** The report's headline column is "PostgreSQL **summed** MiB", produced by `docker top … -eo pid,rss | awk '{sum+=$2}'`. Summing per-backend RSS counts `shared_buffers` (128 MiB, shared) once per backend. I measured this directly on my 20-project native run:
- Postgres **summed RSS = 162 MiB** vs **PSS = 88 MiB** (shared pages counted once) — the summed metric overstates true unique memory by ~1.8×.
- Gateway `VmRSS` ≈ **18 MiB**; 8 Postgres backends.
The report does add a caveat about double-counting, but still presents summed RSS as the metric. Fix: report `smem`/PSS (or `docker stats`) as the primary figure.

**M0-03 (Medium — not reproducible as specified).** `scripts/measure-m0.sh` is Docker- and arm64-specific and writes only to the gitignored `reports/raw/`. It cannot be reproduced in a Docker-less/amd64 environment, and numbers labelled "measured on Raspberry Pi 4" cannot be independently reproduced here. My native amd64 scaled reproduction (above) is offered as corroboration of **shape only**, explicitly **not** Pi numbers.

**M0-04 (Low).** The report honestly discloses that the 100-project light loop later logged two 2-second query timeouts and that no sustained-load claim is supported. No action beyond keeping that caveat.

### Gate question — do 20 active projects fit comfortably in 4 GB?

**My answer: YES, comfortably**, and the conclusion is robust to architecture. Using my amd64 PSS: ~88 MiB (Postgres, unique) + ~18 MiB (gateway) + ~30 MiB (Caddy, est.) + ~300 MiB (OS) ≈ **~440 MiB**, leaving multiple GB of headroom. Even by the report's inflated summed method (~330 MiB combined at 20/light) the answer is still a comfortable yes. No change required before M1 on capacity grounds.

## Scope creep

- **X-01** (process): the M0 branch also contains all of M1/M2/M3 (see SUMMARY.md).
- Minor: `PIBASE_M0_ACTIVE_PROJECTS` / `PIBASE_M0_LIGHT_LOAD` measurement knobs live in the production `serve` path (`internal/gateway/server.go`). Harmless, but measurement scaffolding in the production binary.

## Claims in the engineer's report I could not confirm
- The exact Pi RSS table and the `bin/pibase-linux-arm64` SHA-256 values: **UNVERIFIED** — raw data not in repo; my arm64 build is byte-different (different toolchain/deps), which is expected and not a defect.

## Fix list for Codex (priority order)
1. Commit the raw measurement evidence (or a reproducible harness that emits it) instead of gitignoring `reports/raw/`. (M0-01)
2. Report PSS/`smem` (or `docker stats`) as the headline memory metric, not summed per-backend RSS. (M0-02)
3. State the measurement host/arch next to every number and keep "estimated vs measured" labels. (M0-03)
4. Re-establish per-milestone gating/branches for future work. (X-01)
5. Optional: move M0 measurement knobs out of the production `serve` path.

## Questions for the planner
- Is a per-milestone branch/gate still required, or is a single integration branch acceptable going forward? (Affects how M4+ is audited.)
