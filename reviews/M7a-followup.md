# M7a follow-up review

Commits reviewed: `25646ec`, `0c7cb3e` on `pibase/m7` (on top of `2c6b2ef`)
Reviewer: Claude (planner/auditor), 2026-10-05
Environment: Linux x86_64 sandbox, Go 1.24.7, PostgreSQL 16.13. Nothing run on a Pi or in Docker.

## Verdict: ACCEPTED, with three items to fold into M7b

## What I checked

- `go vet ./...` clean; backup, control, gateway, adminserver, adminsql and cmd suites pass on a
  fresh real PostgreSQL 16; `scripts/test-setup-pi.sh` passes. VERIFIED
- `25646ec`: root cause of the dashboard backup `400` on the Pi was host ownership of the
  bind-mounted `files` and `backups` directories. The engineer reports HTTP 201 and a `.pib`
  file on the engineering Pi after the repair, and a working `ops backup`. READ (their evidence)
- `0c7cb3e`: `adminsql.RunParams` passes bound parameters to the same owner-SQL path. READ

## Items for M7b

### M7a-08 (Medium): fixed numeric owner `65532:998` for the data directories  — READ

- GID `998` is whatever the image's `useradd --system` happened to assign. A rebuild on a
  different base image can change it, and on the host `998` is some unrelated group whose
  members then get read access to files and backups (mode `0750`).
- The host operator can no longer list or copy the backups without `sudo`, which conflicts with
  the no-sudo rule and with the documented "copy the `.pib` files to a second device".
- Existing installs need another `sudo` run.

Decision: run the `gateway`, `admin` and `ops` services as the host operator's own uid and gid
(`user: "${PIBASE_UID}:${PIBASE_GID}"` in Compose, written to `.env` by `scripts/init-env.sh`
from `id -u` / `id -g`), and keep `files` and `backups` owned by the operator as before.
Remove the `65532:998` chown from `setup-pi.sh`. Pin the image's own user to `65532:65532` so
the default is deterministic when Compose is not used. Verify on the Pi: dashboard backup
returns 201, a storage upload works, and the operator can `ls` and `cp` the backup without
`sudo`.

### M7a-09 (Medium): the owner-SQL text filter also runs on generated statements  — VERIFIED (code)

`run()` applies `validateOwnerSQL` to `RunParams` statements too. That filter rejects any
statement containing the substrings `program`, `copy `, `grant `, `revoke `, `set role` and so
on. A row editor that generates `SELECT ... FROM public."programs"` or touches a column named
`copy ` will be refused. Statements built by the dashboard from catalog names must not pass
through the text filter (they are not user SQL); values stay as bound parameters.

The same filter already rejects ordinary owner SQL such as `SELECT * FROM programs`. Make it
token-based (reuse `sqlTokens`) so it matches keywords, not substrings inside identifiers and
strings, and add tests for `programs`, `copyright`, `grant_date`.

### M7a-10 (Low): gates not rerun after the auditor's changes

`reports/M7a.md` still shows the gate output from before `87c3d45`. Run `scripts/test.sh` and
`scripts/test-supabase-js.sh` at the current head and paste the last lines.
