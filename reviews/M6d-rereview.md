# M6d re-review (fix commits)

Commits reviewed: `de81229`, `b1f7341`, `7717d73` on `pibase/m6d`
Previous review: `reviews/M6d-review.md` (BLOCK at `ee42036`)
Reviewer: Claude (planner/auditor), 2026-10-04
Environment: Linux x86_64 sandbox, Go 1.24.7, PostgreSQL 16.13 with Ubuntu's PostGIS and
pgvector, Node 22. Docker scripts could not be run here; nothing was run on a Pi.

## Verdict: BLOCK on item 6 only

Items 1 to 5 are accepted. The Pi drill script (item 6) still cannot complete: I replayed its
project and REST step against a local gateway and it fails twice. Since the owner runs this
script by hand on real hardware, it has to work first time.

Tags: VERIFIED = I ran it. READ = code or script reading only. UNVERIFIED = not checked.

## What I ran

- `go vet ./...`, `go test ./...`: pass. VERIFIED
- `go test -p 1 -count=1 ./internal/backup ./internal/control ./internal/gateway` on a fresh
  real PostgreSQL 16, including the new old-archive restore and safety-backup test: pass. VERIFIED
- `GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build ./cmd/pibase`: pass. VERIFIED
- `sh -n` on the three scripts: pass. VERIFIED
- Replay of `pi-drill.sh` step 6 against a locally built gateway: fails (M6d-04a, M6d-04b). VERIFIED
- `test-upgrade-retained.sh`, `measure-memory.sh`, `scripts/test.sh`: not run (Docker). UNVERIFIED

## Findings status

| ID | Was | Now | Evidence |
|---|---|---|---|
| M6d-01 old backups cannot be restored | High | **Fixed** | READ: safety archive first, then `DROP SCHEMA ... CASCADE` on the four managed schemas, strict `pg_restore`, upgrade. VERIFIED: integration test with a simulated M5b archive, a public foreign key to `auth.users`, and a restore of the safety archive passes. Docs updated, including the disk-space note. |
| M6d-02 retained-data drill | High | **Fixed (READ)** | The script now signs up and signs in through the old gateway, writes rows through REST, uploads and downloads through Storage, upgrades, repeats the checks, restores the real pre-upgrade archive and repeats them again. Not run by me. |
| M6d-03 memory numbers | Medium | **Fixed, one weakness** | Raw file committed. PSS summed over every process in each container, on the Pi 4: Postgres 30.2 MB (1 project), 90.1 MB (20 idle), 98.7 MB (20 served); gateway 11.9 / 14.6 / 15.5 MB. See M6d-06. |
| M6d-04 Pi drill kit | Medium | **Not fixed** | See below. |
| M6d-05 report wording | Low | Fixed | READ |

Planner questions: both answers were applied as decided (restore approach; optional cgroup
section in the docs, no boot-config change).

## Open findings

### M6d-04a (Medium): the drill calls an endpoint that does not exist  — VERIFIED

`pi-drill.sh` creates the project with `POST /admin/projects`. The gateway has no such route:

```
$ curl -s -X POST http://127.0.0.1:28090/admin/projects -H "Authorization: Bearer $TOKEN" ...
404 page not found
```

`project_id` and `anon_key` come back empty and the script stops. Create the project with the
ops container (`ops project-create drill-project`) as the other scripts do.

### M6d-04b (Medium): the drill's table is not readable or writable through REST  — VERIFIED

With the project created through the CLI instead, the drill's migration
(`CREATE TABLE public.drill_probe(note text NOT NULL);`) applies, and then:

```
POST /rest/v1/drill_probe  (anon key)     -> 404 relation not found or not accessible
POST /rest/v1/drill_probe  (service key)  -> 404 relation not found or not accessible
GET  /rest/v1/drill_probe  (service key)  -> 404 relation not found or not accessible
```

A new table has no grants for the API roles. `test-upgrade-retained.sh` handles this with an
explicit `GRANT ... TO p_<id>_service_role` and the service key; the Pi drill must do the same
(grant in the migration, use the service key for insert and read, including the post-reboot
check).

### M6d-04c (Low): drill hygiene  — READ

- Failures inside the `{ ... } | tee` block end the block but the script still prints "Drill
  output saved" and exits 0. Capture the block's status and exit non-zero on failure.
- `.pibase-pi-drill-state` and `pi-drill-*.txt` are not in `.gitignore`.
- Add a local self-test for the drill's steps 5 to 7 (boot, project, REST round trip, backup)
  against a disposable stack, so this class of error is caught without hardware.

### M6d-06 (Low): the "served one request" case does not touch the project databases  — READ

The served case calls `/auth/v1/user` with only the anon key. Postgres shows one extra backend
(about 5.8 MB), not twenty, so the measurement does not show what 20 active projects cost. Use
a REST read on a real table in each project, and record how many backends were open
(`SELECT count(*) FROM pg_stat_activity`). Until then, treat 98.7 MB as "20 projects, mostly
idle", not "20 active projects".

## Note for a later gate (not part of M6d)

M6d-04b shows that a table created by the owner is invisible to the API until grants are added
by hand. Supabase grants the API roles access to new `public` tables by default and relies on
RLS. PLAN.md principle 3 ("an AI can write normal Supabase code") will keep tripping on this.
I will propose default privileges for new `public` tables as a separate, small gate.

## Fix list

1. M6d-04a and M6d-04b: project via `ops project-create`; grant in the migration; service key.
2. M6d-04c: exit status, `.gitignore`, local self-test of steps 5 to 7.
3. M6d-06: served case reads a real table in every project; record backend count; update the
   table and raw file.

Append to the "Audit fixes" section of `reports/M6d.md`. I will re-check only these items.
