# M6d review

Commit reviewed: `ee42036` on `pibase/m6d`
Gate definition: `reviews/M6d-hardening-gate.md` on `pibase/review-m6c`
Reviewer: Claude (planner/auditor), 2026-10-04
Environment: Linux x86_64 sandbox, Go 1.24.7, PostgreSQL 16.13 with Ubuntu's PostGIS and
pgvector, Node 22. Docker scripts could not be run here; nothing was run on a Pi.

## Verdict: BLOCK (gate not met; no security regression)

Items 1 to 3 are accepted. Items 4, 5 and 6 do not yet meet the gate, and the engineer's own
drill uncovered a real High problem with backups (M6d-01). Good catch, honestly reported.

Tags: VERIFIED = I ran it. READ = code or script reading only. UNVERIFIED = not checked.

## What I ran

- `go vet ./...`, `go test ./...`: pass. VERIFIED
- `go test -p 1 -count=1 ./internal/backup ./internal/control ./internal/gateway` on a fresh
  real PostgreSQL 16: pass. VERIFIED
- `GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build ./cmd/pibase`: pass. VERIFIED
- `sh -n` on the three new scripts: pass. VERIFIED
- `scripts/test.sh`, `test-upgrade-retained.sh`, `measure-memory.sh`, `pi-drill.sh`: not run.
  UNVERIFIED (read only)

## Scope items

| # | Item | Status |
|---|---|---|
| 1 | Data-driven grant cleanup | **Accepted.** `revokeStrayCreateGrants` reads the ACL and revokes from every non-operator grantee, inside the upgrade transaction; test injects a grant to `authenticator`. READ + test VERIFIED |
| 2 | Narrow admin table grants | **Accepted.** `arwdDxt` to `arwd` on the four tables, version 9, assertion in two tests. READ + test VERIFIED |
| 3 | Restore visibility | **Accepted.** One stderr line, docs updated. READ |
| 4 | Upgrade from retained data | **Not met.** See M6d-01, M6d-02 |
| 5 | Memory as PSS | **Not met.** See M6d-03 |
| 6 | Pi drill kit | **Not met.** See M6d-04 |

## Findings

### M6d-01 (High): backups taken before a managed-schema change cannot be restored  — READ (engineer's finding)

Restoring an M5b backup into a current project fails: `pg_restore --clean` cannot drop
`auth.users` because the newer MFA tables reference it. This is not specific to MFA. Any
future managed table that references an older one will break restore of every older backup.
A backup that cannot be restored after an upgrade is not a backup.

Planner decision (answers question 1): **option (b), generalized, in this gate.**

1. Before `pg_restore`, `Restore` takes an automatic safety backup of the project's current
   state into the backups directory (`pre-restore-<project>-<timestamp>.pib`). If that fails,
   restore stops before changing anything.
2. Then, as the operator, drop the four managed schemas with `CASCADE`
   (`auth`, `storage`, `realtime`, `cron`), run `pg_restore --clean --if-exists` as today, then
   the canonical upgrade.
3. Do not tolerate `pg_restore` errors (option a is rejected: it leaves old and new rows mixed).
4. Tests: restore an M5b-era archive (the drill, item 4) and a current archive; a project whose
   `public` tables have foreign keys to `auth.users`; confirm the safety backup exists and can
   itself be restored.
5. Document in `docs/PI-OPERATIONS.md`, including that restore now needs free disk for the
   safety backup.

### M6d-02 (High, contract): the retained-data drill skips most of what the gate asked for  — READ

`scripts/test-upgrade-retained.sh`:

- creates no auth user, so "sign-in with the old user" is not tested (`auth.users` count is
  printed as OK whatever its value);
- writes a file straight to disk, with no `storage.objects` row and no download through the API;
- inserts table data with `psql`, not through REST;
- never restores the pre-upgrade backup. Step 9 is titled "Restore old backup" but restores the
  post-upgrade one; `pre_upgrade_backup` is unused.

Fix: start the old gateway, sign up a user and upload an object through the HTTP API, insert
rows through REST; after upgrade start the new gateway and verify sign-in with the same
password, REST read, and object download; restore the **pre-upgrade** backup once M6d-01 is
fixed and verify again. Keep the script's good parts (disposable volume, no live stack).

### M6d-03 (Medium): the memory numbers are not what the report says  — READ

- `measure-memory.sh` reads only the container's main process. For Postgres that is the
  postmaster alone, not the checkpointer, writers, autovacuum launcher or any backend. "Postgres
  26 MB" is therefore not Postgres's memory.
- PSS was not obtained. It does not need host root: read it from inside the container, for
  every process (`docker exec <container> sh -c 'cat /proc/[0-9]*/smaps_rollup'`), and sum.
- `reports/raw/m6d-memory-pss.txt` is cited but is not in the repo; `.gitignore` excludes
  `reports/raw/*` except listed files. Add the exception and commit the file.
- The 20-project case has no connections open, so it measures an idle catalog. Also record a
  case where each of the 20 projects has served one request.

### M6d-04 (Medium): the Pi drill kit would not produce a usable result  — READ

- Step 6 posts to a table that does not exist and accepts the failure, so there is no REST
  round trip. Create a table with `ops migration-apply`, insert, read back, compare.
- With `PIBASE_DRILL_POSTREBOOT=1` the script reruns setup, boot, project creation and backup
  before the post-reboot checks. Post-reboot mode must run only the checks, and must confirm the
  project and row created before the reboot are still there.
- Both runs write `pi-drill-<date>.txt` with `tee`, so the second run on the same day
  overwrites the first. Append, or use a second file name.
- `eval "$(grep ... .env)"` executes a line of `.env`. Read the value with `sed` as
  `scripts/test.sh` does. `PIBASE_BACKUP_KEY` is loaded and never used.
- The project id and key are parsed with `grep -o '"id":"..."'`, which breaks if the JSON has
  spaces. Use the same `json_field` helper as the upgrade drill.
- After the first `setup-pi.sh` the script continues even if the shell is not yet in the
  `docker` group. Detect that with `docker version` and stop with the one-line instruction.

### M6d-05 (Low): report wording

`reports/M6d.md` lists the restore check as "Post-upgrade backup and restore ... (measured)"
under item 4, which is accurate, but the gate item was the old backup. State it as not met in
the summary line, not only under "not tested".

## Answers to the planner questions

1. **Pre-MFA backup restore:** see M6d-01. Option (b) generalized, with a safety backup, in
   this gate.
2. **cgroup memory on the Pi:** do not change the boot config from `setup-pi.sh`. PSS from
   `/proc` is sufficient for measurement. Add a short optional section to
   `docs/PI-OPERATIONS.md` explaining what `cgroup_enable=memory` gives (container memory limits
   and `docker stats`) and the one line to add, for owners who want it.

## Owner's answer on the push rule (2026-10-04)

The engineering host is the owner's Raspberry Pi 4, and pushing from it is intended. The rule
is now: **push only `pibase/*` build branches from the engineering Pi; never push to `main`,
never force-push.** The old wording "never push from a Raspberry Pi" is withdrawn.

Two consequences:

- Numbers measured on this host are real Pi 4 numbers. Label them "measured on Pi 4
  (engineering host, Debian 13)", and keep them separate from the fresh-OS drill.
- The fresh-install drill (`M4-03`) still needs a clean Raspberry Pi OS image or a spare SD
  card or SSD. Do not run `scripts/pi-drill.sh` against this host's live data root; the script
  must refuse if the data root already contains a Postgres data directory, unless
  `PIBASE_DRILL_POSTREBOOT=1` is set.

## Fix list

1. M6d-01: safety backup, drop managed schemas, restore, upgrade; tests and docs.
2. M6d-02: real retained-data drill through the HTTP API, restoring the pre-upgrade backup.
3. M6d-03: whole-container PSS, raw file committed, served-request case.
4. M6d-04: Pi drill fixes.
5. M6d-05: report wording.

Update `reports/M6d.md` with an "Audit fixes" section. Re-review required.
