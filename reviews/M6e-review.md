# M6e review

Commit reviewed: `e0661ee` on `pibase/m6e`
Gate definition: `reviews/M6e-gate.md` on `pibase/review-m6d`
Reviewer: Claude (planner/auditor), 2026-10-05
Environment: Linux x86_64 sandbox, Go 1.24.7, PostgreSQL 16.13 with Ubuntu's PostGIS and
pgvector, Node 22, gateway built from this commit. Docker scripts not run; nothing run on a Pi.

## Verdict: BLOCK

Part B is accepted. Part A has two Critical findings: with the new defaults, the **anon key
can read data the owner never exposed**. The report was honest that most Part A tests were not
written; those missing tests are exactly what would have caught this.

Tags: VERIFIED = I ran it. READ = code or script reading only. UNVERIFIED = not checked.

## What I ran

- `go vet ./...`, `go test ./...`: pass. VERIFIED
- `go test -p 1 -count=1 ./internal/backup ./internal/control ./internal/gateway` on a fresh
  real PostgreSQL 16: **`internal/backup` FAILS** (M6e-03). VERIFIED
- `GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build ./cmd/pibase`: pass. VERIFIED
- Manual functional checks of Part A through the gateway's REST API with the service and anon
  keys of fresh projects (results below). VERIFIED
- Local reproduction of the connection-limit error body for Part B. VERIFIED
- `measure-concurrency.sh`, `test-pi-drill-local.sh`: not run (Docker). UNVERIFIED

## What works (VERIFIED)

A plain `CREATE TABLE public.plain(...)` applied with `migration-apply`:

- gets API grants and `relrowsecurity = true`;
- service key reads the row; anon key gets `[]`;
- a table in another schema (`app.other`) gets no grants and no RLS;
- an existing private table (no API grants, RLS off) is unchanged by backup then restore.

## Findings

### M6e-01 (Critical): tables made from a query, views and materialized views are readable with the anon key  — VERIFIED

The event trigger fires only for the command tag `CREATE TABLE`. Default privileges, however,
apply to every table-like object the admin role creates. Four migrations on a fresh project:

```
CREATE TABLE public.ctas AS SELECT 1 AS id, 'secret-ctas'::text AS note;
SELECT 1 AS id, 'secret-into'::text AS note INTO public.sel_into;
CREATE VIEW public.plain_view AS SELECT * FROM public.plain;
CREATE MATERIALIZED VIEW public.plain_mat AS SELECT * FROM public.plain;
```

Catalog afterwards: all four have `anon=arwd` and `relrowsecurity = f`. With the **anon key**:

```
GET /rest/v1/plain       -> []                          (correct)
GET /rest/v1/ctas        -> [{"note": "secret-ctas"}]
GET /rest/v1/sel_into    -> [{"note": "secret-into"}]
GET /rest/v1/plain_view  -> [{"note": "secret-plain"}]  (rows of the RLS-protected table)
GET /rest/v1/plain_mat   -> [{"note": "secret-plain"}]
```

The view case is the worst: an ordinary view over a protected table hands all its rows to
anyone holding the public key, and anon also has insert, update and delete on these objects.

Planner decision:

1. The trigger must also handle `CREATE TABLE AS` and `SELECT INTO`: enable RLS, same as
   `CREATE TABLE`.
2. New views in `public`: set `security_invoker = true`, so the caller's RLS applies.
3. New materialized views in `public` (RLS is not possible on them): revoke the default grants
   from every non-owner role that lacks `BYPASSRLS`. The service key keeps access.
4. The owner may undo any of these on their own object, as with `DISABLE ROW LEVEL SECURITY`.
   Document each as "this exposes the object to the anon key".
5. Test as a table-driven check: for every way of creating a relation in `public` (plain,
   `AS`, `INTO`, `LIKE`, partitioned parent and partition, view, materialized view, and a table
   moved in with `ALTER TABLE ... SET SCHEMA public`), the anon key and a signed-in user with no
   policy get no rows and cannot write; the service key can read.

### M6e-02 (Critical): branching exposes private tables to the branch's anon key  — VERIFIED

Source project: `public.old_private` owned by the admin role, no API grants, RLS off (the state
of every table created before this version). Anon key on the source: `404`.

After `project-branch`:

```
old_private | relrowsecurity=f | {p_<branch>_anon=arwd/..., p_<branch>_authenticated=arwd/..., p_<branch>_service_role=arwd/..., ...}
GET /rest/v1/old_private (branch anon key) -> [{"note": "legacy-secret"}]
```

Cause: the branch restore creates tables as the branch's admin role, so the new default
privileges add API grants, and the source's RLS-off state is copied faithfully. Every existing
project's private tables become public in a branch.

Planner decision: a branch must end with exactly the source's access. After the restore and
before the ACL remap, revoke all privileges on every relation and sequence in `public` from the
branch's anon, authenticated and service roles; then let the remap apply the source's ACLs.
Test: branch ACLs equal the source ACLs with role names mapped, for tables, views, materialized
views and sequences; the private-table case above returns `404` in the branch.

### M6e-03 (High): the test suite is red and the required Part A tests were not written  — VERIFIED

- `TestRestorePreM6cBackupReappliesManagedOwnership` fails: `backup_integration_test.go:143`
  still asserts `version != 9`. The Go integration suites were not run before pushing
  (`reports/M6e.md` lists only `sh -n` and two scripts).
- No Go or JS test was added for Part A. Missing from the gate's list: `pibase sql` path,
  two-user policy isolation, RLS-disabled state across bootstrap/restore/branch, upgrade from
  version 9 with `\dp` and `relrowsecurity` before and after, trigger tamper checks, the
  `supabase-js` case.
- "Evidence for existing tables" in the report is an assertion with no output attached.

Fix: write the full list, plus the tests named in M6e-01 and M6e-02; stop hard-coding the
schema version in tests; run `scripts/test.sh` and `scripts/test-supabase-js.sh` and say so.

### M6e-04 (Medium): `docs/PI-DRILL.md` would fail on a fresh Pi  — READ

- The SSD is mounted by hand with no `/etc/fstab` entry, so after the reboot the data root is
  an empty directory on the SD card. The drill exists to prove reboot persistence. Add an fstab
  line by `UUID` with `nofail`, and have the post-reboot check confirm the mount source.
- `sudo mkfs.ext4 /dev/sda1` is hard-coded. On a Pi booted from USB, `sda` is the system disk.
  Make the owner set `DISK=/dev/...` from the `lsblk` output, print the device's size and model,
  and ask for a typed confirmation before formatting. Handle a disk with no partition table.
- The repository is private: `git clone https://github.com/...` on a clean Pi will ask for
  credentials. Give one working route (for example `git bundle` or a tarball copied from the
  engineering Pi with `scp`), and say how to install `git` if the image lacks it.

## Part B: accepted

Measured on the engineering Pi 4 (raw file committed, 2,705 lines):

| Case | Postgres PSS median / peak | Gateway PSS median / peak | Peak backends | Requests / errors | p50 / p95 | Free memory at peak |
|---|---:|---:|---:|---:|---:|---:|
| 20 projects | 199.8 / 213.0 MB | 17.7 / 18.1 MB | 21 | 1,300 / 0 | 0.33 / 0.86 s | 1,299 MiB |
| 40 projects | 232.2 / 254.5 MB | 18.2 / 18.6 MB | 38 | 1,375 / 101 | 0.41 / 1.68 s | 1,202 MiB |

This answers the PLAN.md M0 question for this workload: 20 continuously active projects cost
about 230 MB for Postgres plus gateway and ran with no errors. The host had other services
running (DATBase, Home Assistant), so "free memory" is for this machine, not a clean Pi.

Minor: the script omits `-c work_mem=4MB`, which the other scripts and the compose file set.

### Answer to the planner question (error at the connection limit)

Reproduced locally with a small `max_connections`. VERIFIED. The client receives:

```
HTTP 400
{"code":"53300","details":null,"hint":null,"message":"database request failed"}
```

`53300` is PostgreSQL's "too many connections". A `400` tells the customer their request was
wrong, which it was not, and `supabase-js` will not retry it.

Planner decision, for the **next** gate (not this one): the gateway limits how many project
connections it opens at once to a configured number below `max_connections`, queues requests
for a short bounded time when the limit is reached, and then answers `503` with `Retry-After`.
Do not implement it in M6e.

## Fix list

1. M6e-01: trigger coverage for all relation kinds, with the table-driven test.
2. M6e-02: branch access equals source access, with the test.
3. M6e-03: complete Part A tests, fix the failing test, run the full gates.
4. M6e-04: drill checklist corrections.
5. Add `-c work_mem=4MB` to `measure-concurrency.sh` (no need to re-measure).

Add an "Audit fixes" section to `reports/M6e.md`. Re-review required.
