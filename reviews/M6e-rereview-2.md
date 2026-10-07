# M6e second re-review

Commit reviewed: `d24f4be` on `pibase/m6e`
Previous: `reviews/M6e-rereview.md` (blocked on M6e-05 at `31f3c19`)
Reviewer: Claude (planner/auditor), 2026-10-05
Environment: Linux x86_64 sandbox, Go 1.24.7, PostgreSQL 16.13, gateway built from this commit.
Docker scripts not run; nothing run on a Pi.

## Verdict: APPROVE WITH FIXES

No security findings remain. Two small defects must be fixed as the **first commit of M7**
(which branches from this commit); I will verify them in the M7 review.

## M6e-05: fixed (VERIFIED)

Same reproductions as last time, on a fresh project:

| Check | Result |
|---|---|
| Members-only older table, then `ADD COLUMN` | ACL and RLS flag unchanged; anon key still `404` |
| Older private table, then `ADD COLUMN` and `RENAME COLUMN` | unchanged; anon key `404` |
| `ALTER TABLE ... DISABLE ROW LEVEL SECURITY` on a new table | sticks (`relrowsecurity = f`); anon then reads it, as the owner asked |
| Table moved into `public` with `SET SCHEMA` | no grants, RLS untouched; anon and service both `404` |
| New plain table, `SELECT INTO` table, view | anon gets `[]` |
| New materialized view | anon `404`; service reads it |

The `pibase.public_relation_trigger` setting is gone and `ALTER TABLE` is no longer in the
trigger's tag list (READ).

## Defects to fix first in M7

### M6e-06 (Medium): the owner cannot read their own materialized view  — VERIFIED

The materialized-view branch now revokes from every non-bypass grantee in the ACL, which
includes the owner. Through `pibase sql` as the project admin:

```
SELECT note FROM public.plain_mat
ERROR: permission denied for materialized view plain_mat (SQLSTATE 42501)
```

`REFRESH MATERIALIZED VIEW` still works. Exclude the relation's owner from the revoke loop
(`r.oid <> c.relowner`). Add a test: after `CREATE MATERIALIZED VIEW`, the owner can select
from it and anon cannot.

### M6e-07 (Medium): `TestM6ePartATableDrivenRelations` fails  — VERIFIED

`go test -p 1 -count=1 ./internal/gateway` on a fresh real PostgreSQL 16:

```
m6e_part_a_integration_test.go:86: moved table should remain unreachable before owner grants
access: status=403 body={"code":"PIBASE",...,"message":"service role requires service apikey"}
```

The loop sends `apikey = anon key` together with a service-key bearer token, which the gateway
correctly rejects with `403`. It is a test bug, not a product bug: send the service key as the
`apikey` in the service iteration. `internal/backup` and `internal/control` pass.

`reports/M6e.md` says both full gates were rerun after this change. This test fails
deterministically, so that statement cannot be right as written. Correct the report, and from
now on paste the last lines of each gate's output into the report.

Also: only the auditor sets a gate's status. `reports/M6e.md` was changed to "audit complete"
before this review; set it to "approved with fixes (see reviews/M6e-rereview-2.md)".

## What I ran

- `go vet ./...`: pass. Backup and control integration suites: pass. Gateway suite: one
  failure (M6e-07). arm64 build: pass.
- Manual REST checks with the anon and service keys, and owner SQL, as tabled above.
