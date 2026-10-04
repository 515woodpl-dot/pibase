# Planner decision: M6d, release hardening (APPROVED to start)

Planner/auditor: Claude, 2026-10-04. Base: `pibase/m6c` at `855cb73`.
Engineer from this gate on: Claude Code CLI (previously Codex). Same rules as PLAN.md section 7.

## Why this gate

M6a (MFA), M6b (extensions) and M6c (schema ownership) are approved. Before any new feature,
close the open items the audits left behind. No new features in this gate.

## Read first (engineer onboarding)

`PLAN.md`, `README.md`, `docs/PI-OPERATIONS.md`, `reports/M6b.md`, `reports/M6c.md`,
`reports/M4-03.md`, and on the review branches: `reviews/M6c-review.md`,
`reviews/M6c-rereview.md`. Branches: build on `pibase/*`, reviews on `pibase/review-*`.
Never push to `main`, never force-push, never push from a Raspberry Pi.

## Scope

1. **Data-driven schema grant cleanup.** In the version upgrade, revoke `CREATE` on `auth`,
   `storage`, `realtime`, `cron` from every non-operator grantee found in the schema ACL, not
   only from named roles. Test: inject a grant to a role outside the project's four (for
   example `authenticator`) and assert it is gone after upgrade.
2. **Narrow the admin role's table grants.** Today the admin role has `ALL PRIVILEGES` on
   `storage` and `cron` tables, which includes `TRIGGER`, `REFERENCES` and `TRUNCATE`. Reduce to
   what the product needs: `SELECT, INSERT, UPDATE, DELETE` on `cron.jobs` and
   `cron.webhook_outbox`; for `storage.*` keep what the storage API and dashboard need and
   justify each privilege in the report. Bump the schema version. Show `\dp` before and after.
   If any existing feature needs one of the removed privileges, stop and list it under
   "Questions for the planner" instead of keeping `ALL`.
3. **Restore visibility.** When the post-restore upgrade fails, `restore` prints one clear
   line on stderr naming the project and telling the operator to run `project-list`; document
   it in `docs/PI-OPERATIONS.md`. Exit code stays 0.
4. **Upgrade from retained data.** Add a scripted drill (`scripts/test-upgrade-retained.sh`)
   that: checks out the `pibase/m5b` commit in a temporary worktree, builds it, starts a
   disposable Postgres with a named volume, creates two projects with data in REST tables, auth
   users, storage objects and a cron job, makes a backup; then starts the current build on the
   same volume, runs `bootstrap`, and verifies version, ownership, data, sign-in with the old
   user, file download, and restore of the old backup file. No mocks, no live stack touched.
5. **Memory as PSS.** Add `scripts/measure-memory.sh` that reports PSS (from
   `/proc/<pid>/smaps_rollup`) for the Postgres and gateway processes at 1 and 20 projects,
   idle. Save raw output under `reports/raw/`. State the machine it ran on.
6. **Pi drill kit (the drill itself is run by the owner on the Pi, not by you).** Make
   `reports/M4-03.md`'s procedure a single script, `scripts/pi-drill.sh`, that needs no `sudo`
   beyond what `setup-pi.sh` already needs, runs setup, bootstrap, project create, a
   supabase-style REST round trip with `curl`, backup, reboot instructions, and a post-reboot
   check, and writes everything to one file `pi-drill-<date>.txt` for the owner to paste back.
   You cannot run it on real hardware: say so, and test what you can with the existing
   `scripts/test-setup-pi.sh` approach.

Out of scope: OAuth, point-in-time recovery, image transforms, S3 API, dashboard changes, any
new dependency or service.

## Gate report: `reports/M6d.md`

What was built, commands run with results, `\dp` before/after for item 2, raw files for items
4 and 5, what was NOT tested, measured vs estimated labels, questions for the planner.
Branch `pibase/m6d` from `pibase/m6c`. Push only `pibase/m6d`. Stop there for audit.
