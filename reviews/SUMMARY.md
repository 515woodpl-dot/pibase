# Review summary — pibase M0–M3

**Commit reviewed:** `b7379e7e05f46aba33e182338a0252c29a0d88f2` on branch `pibase/m0`
**Reviewer environment:** Linux **amd64**, Go 1.24.7, PostgreSQL **16.13** run as a **native cluster** (Docker was unavailable in the review sandbox), Node 22. **Not** arm64 and **not** a Raspberry Pi — no memory figure here is a Pi number.
**How verification was done:** Go unit tests and the four real-Postgres integration tests were run natively; the gateway was run natively with two real projects (and scaled to 20 for M0) and attacked over HTTP. The repo's Docker-based scripts (`test.sh`, `measure-m0.sh`, `test-supabase-js.sh`) could not be run as written (no Docker); their substance was reproduced natively. Go dependencies were pulled from GitHub mirrors because `proxy.golang.org` is blocked by egress; **product source was not modified**.

## Verdicts

| Milestone | Verdict | Open Critical | Open High |
|---|---|---|---|
| M0 foundation & measurement | **APPROVE WITH FIXES** | 0 | M0-01 (raw evidence not in repo) |
| M1 data API & SQL | **APPROVE WITH FIXES** | 0 | M1-01 (`or`/`not` missing) |
| M2 auth | **BLOCK** | 0 | M2-01, M2-02, M2-03 |
| M3 storage | **BLOCK** | 0 | M3-01, M3-02, M3-03 |

No single finding meets the playbook's "Critical" bar (data leak / auth bypass / cross-project access) — cross-project isolation, JWT verification, SQL-injection defence, and path-traversal defence all held under live attack. The BLOCKs are driven by clusters of High findings that make **auth** and **storage** unsafe/incomplete on the 4 GB Pi target, plus one gate-report claim that the code contradicts.

## Open High findings

- **M0-01** — `reports/raw/` is gitignored, so the raw memory CSV/environment the M0 report cites are **not in the repo**; the Pi numbers are unverifiable as committed.
- **M1-01** — REST operators `or` and `not` are not implemented (required by the playbook's M1 operator list); honestly declared, but a real Supabase-compat gap.
- **M2-01** — **no rate limiting on any auth endpoint** (30/30 rapid logins processed, 0×429): unthrottled online password brute force.
- **M2-02** — **login timing oracle**: known user 94.7 ms (argon2) vs unknown user 1.3 ms (~73×) → user enumeration.
- **M2-03** — **unbounded 64 MiB argon2 concurrency**; the M2 report's "at most 2 concurrent hashes" is false (no such limiter exists). ~20 concurrent sign-ins ≈ 1.28 GB → OOM/DoS on a 4 GB Pi.
- **M3-01** — downloads served from the API origin with the stored `Content-Type` and **no `nosniff` / no `Content-Disposition`**: an uploaded `<script>`/SVG executes as active content (stored XSS).
- **M3-02** — a global 1 MiB request cap makes **every object ≤1 MiB**; the milestone cannot meet its own 1 GB streaming/RAM requirement; oversized upload → 500.

## Notable Medium findings
- **M1-03** per-project pools can accumulate past `max_connections=40` across many projects (cross-project starvation).
- **M1-04** owner SQL runs on a **superuser** session then `SET LOCAL ROLE` → `RESET ROLE` re-escalates; operator-only today, latent **Critical** for the M4 SQL editor.
- **M2-04** signup enumerates accounts (409 on duplicate). **M2-05** no sign-out/session revocation.
- **M3-03** deleting a project leaves its files on disk. **M3-04** "public"/signed URLs still require the apikey header. **M3-05** over-permissive bucket-read RLS.

## Cross-cutting / process
- **X-01 (process, High).** Milestones were **not gated or branched** as PLAN.md requires: branch `pibase/m0` contains the M1/M2/M3 commits, there is no M0 commit and no `pibase/m1..m3` branches. The "stop at each gate; auditor approves before the next" discipline did not happen; all four landed together.
- **X-02.** Raw M0 evidence is gitignored (= M0-01).
- **X-03 (Low).** `scripts/test.sh` mutates the tree (`go mod tidy && gofmt -w`) and needs network mid-test.

## What was independently verified as sound
- `authenticator` is `LOGIN NOINHERIT NOSUPERUSER`; `service_role` is `BYPASSRLS`; project DBs owned by non-superuser admins; `REVOKE CONNECT … FROM PUBLIC` in effect; cross-project connect/read denied.
- SQL injection not exploitable (identifiers from catalog + `pgx.Identifier.Sanitize`, values parameterised); RLS enforced by Postgres.
- JWT `alg:none`/forged/expired/cross-project all rejected; project bound to API key (Host-header tricks inert); service key never leaked to responses/logs.
- Migrations: idempotent, checksum-protected, advisory-locked, atomic (failing migration rolls back).
- Storage path traversal blocked; signed URLs HMAC'd, object-bound, expiring, cross-project-safe; cross-project file reads denied.
- Real `@supabase/supabase-js` acceptance and 4 real-Postgres integration tests exist and pass.
- M0 capacity gate holds: 20 active projects fit comfortably in 4 GB (my amd64 PSS ≈ 88 MiB Postgres + 18 MiB gateway; the report's summed-RSS metric overstates true memory ~1.8×).

## Safe to proceed to M4?

**Not yet.** M0 and M1 can proceed once their fixes are scheduled (they are APPROVE-WITH-FIXES, not blocking). **M2 and M3 must be fixed and re-reviewed first** — shipping auth without rate limiting/constant-time login/bounded hashing, or storage that executes uploaded HTML in the API origin and cannot store files >1 MiB, is not safe on the target device. Additionally, re-establish per-milestone gating (X-01) so M4 can be audited against a clean gate, and land the M1-04 owner-SQL fix **before** the M4 browser SQL editor (latent Critical).

### Top of the fix queue
1. M2-01/02/03 (auth rate limit, constant-time login, bounded argon2) — blocking.
2. M3-01/02/03 (safe download headers, real object sizes/streaming, delete project files) — blocking.
3. M1-04 owner-SQL non-superuser connection — before M4.
4. M0-01/02/03 measurement evidence + PSS metric; M1-01 `or`/`not`.
