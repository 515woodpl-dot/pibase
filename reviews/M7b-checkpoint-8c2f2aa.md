# M7b work-in-progress checkpoint review

Commit reviewed: 8c2f2aa on pibase/m7 (diff from 0c7cb3e, 5 files)
Environment: Linux x86_64 cloud sandbox, Go toolchain with offline module file, Python YAML parser. No Docker, no Pi.
Verdict: **BLOCK** (the commit does not compile and the Compose file does not parse). This is a checkpoint, not a gate, so nothing was claimed complete; fix before building M7b on top.

## What I ran
- VERIFIED `go build ./...` fails: `internal/adminsql/sql.go:154:111: syntax error: unexpected {, expected )`.
- VERIFIED `yaml.safe_load(compose.yaml)` fails: tab character at line 41 column 1 (also lines 64, 92).
- VERIFIED `.env.example` has no `PIBASE_UID=` / `PIBASE_GID=` lines.
- VERIFIED filter behaviour with a local, discarded one-character parenthesis fix (table in M7b-C04).
- READ Dockerfile, compose.pi.yaml, scripts/init-env.sh, scripts/setup-pi.sh.
- UNVERIFIED anything in Docker or on the Pi.

## Findings

**M7b-C01, High: Go does not compile.** `validateOwnerSQL` has an extra `(` before `(token == "reset" ...`. Every package that imports adminsql fails, so `scripts/test.sh` cannot pass. The checkpoint was pushed without `go build`.

**M7b-C02, High: compose.yaml is invalid YAML.** The three new `user:` lines are indented with a tab. `docker compose` rejects the file, so the stack will not start after `git pull` on the Pi. Use four spaces.

**M7b-C03, High: fresh installs get no PIBASE_UID/GID.** `init-env.sh` fills them with `sed` replacements on `.env.example`, but the template has no such lines, so nothing is written. Compose then stops with "set PIBASE_UID". Only the existing-`.env` path appends them. Add both lines to `.env.example` and add an assertion that a fresh `.env` contains both.

**M7b-C04, Medium: the token filter is narrower than it reads.** With the parenthesis fixed locally:

| Statement | Blocked |
|---|---|
| `select * from programs`, `select grant_date from t`, `'please grant access'` in a string | no (correct, the old false positives are gone) |
| `SET ROLE x`, `GRANT ...`, `select copy from t` | yes |
| `SET LOCAL ROLE x`, `SET SESSION ROLE x` | **no** |
| `set session_authorization = 'x'`, `SET "role" = 'x'`, `select set_config('role','x',false)`, `RESET ALL` | **no** |
| `DO $$ ... EXECUTE 'gr'\|\|'ant ...' $$` | **no** |

The connection is `authenticator`, not a superuser, so the role model is the real boundary and this filter is a guard rail. Required: (a) skip `local`/`session` after `set`; treat `role` and `session_authorization` as forbidden targets of `SET`/`RESET`, quoted or not; block `set_config` and `RESET ALL`; (b) add unit tests for every row above; (c) add one real-Postgres test that proves what an owner statement can reach after changing role inside the transaction (it must not read another project's data, the control database, or `auth` secrets beyond what the project admin already can), and state the result in the report; (d) say in docs that the filter is not a security boundary. This must be settled before Tier 2, when customers, not the operator, type the SQL.

**M7b-C05, Medium: named volumes in plain compose.yaml are owned by 65532.** Without compose.pi.yaml, `/data/files` and `/data/backups` are named volumes initialised from the image, owned 65532. The services now run as the host uid, so writes should fail with permission denied in `scripts/test-supabase-js.sh` and the local drill. READ only; run it and paste the result. Likely fix: keep 65532 as the default (`${PIBASE_UID:-65532}`) and set the host uid only for the Pi bind mounts.

**M7b-C06, Medium: existing Pi data keeps the old owner.** `setup-pi.sh` now re-owns the two directories but not the files already inside them (earlier backups and stored objects are owned by 65532). After the switch the services cannot read or delete them. Add a one-time recursive ownership repair to the setup script, and a line in docs/PI-OPERATIONS.md for boxes set up before this commit.

**M7b-C07, Low:** `init-env.sh` run through sudo records uid 0, which makes the services run as root. Refuse uid 0. `sql.go` and `init-env.sh` have tab/one-line formatting that gofmt will rewrite; run gofmt before committing. The stale comment in setup-pi.sh still says containers run as a fixed pibase uid.

## Fix order
C01, C02, C03 (nothing runs without them), then C05/C06 with pasted Docker output, then C04 with tests, then M7a-10 and M7b.
