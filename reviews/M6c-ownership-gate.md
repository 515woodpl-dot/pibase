# Planner decision: M6c, managed-schema ownership (APPROVED to start)

Answers PLAN.md section 9 ("Managed-schema ownership migration gate") at `840266e`.
Planner/auditor: Claude, 2026-10-04.

## Follow-up commit `840266e`: accepted

- `project-list` shows `schema_error`; restore docs note the extension guard. READ.
- `go vet` and the control + gateway integration tests pass against real PostgreSQL 16. VERIFIED.
- One correction needed in `reports/M6b.md`: the heading "Measured M6b image and memory
  (Raspberry Pi 4, Linux arm64)" has no raw output behind it. Add the commands and output under
  `reports/raw/` (as was done for `m5b-realtime-pi4.txt`), or change the heading to name the
  machine the numbers really came from. Do this in the first M6c commit.

## M6c scope

Goal: customer SQL can no longer drop, replace or add objects in `auth`, `storage`, `realtime`
or `cron`. Nothing else changes for customers.

Decisions (do not relitigate):

1. **Owner:** the operator role that already owns the tables in these schemas. No new role.
2. **Admin role rights:** `USAGE` on the four schemas, no `CREATE`. Keep exactly the table,
   sequence and function grants it has today (for example full access to `cron.jobs`, execute on
   `realtime.enable_table` and `cron.enqueue_webhook`).
3. **Customer-made objects already in a managed schema:** never drop or move them. Skip that
   project, set `schema_error` to a message that lists them, continue with the rest.
4. **Database ownership stays with the admin role.** Out of scope.
5. **Schema version 8**, applied through the same canonical path for fresh and existing projects,
   idempotent, one project's failure never stops bootstrap.
6. No new services, features or dependencies.

## Gate requirements

Report `reports/M6c.md` must show, with commands:

- [ ] As the admin role through `pibase sql` and `migration-apply`, each of these fails with a
      permission error: `DROP TABLE storage.objects`, `DROP TABLE storage.buckets`,
      `DROP TABLE auth.users`, `DROP TABLE cron.webhook_outbox`, `CREATE TABLE storage.x(...)`,
      `CREATE FUNCTION auth.x()`, `ALTER TABLE auth.users ...`, `DROP SCHEMA cron CASCADE`,
      `ALTER SCHEMA storage RENAME ...`, `DROP FUNCTION realtime.notify_change()`.
- [ ] Every `SECURITY DEFINER` function in the four schemas is listed with its owner, its
      `search_path`, who may execute it, and one sentence on why it is safe for a customer to call.
- [ ] Still working after migration, on a fresh project and on an upgraded one: REST with RLS,
      sign up / sign in / refresh / MFA, storage upload and download, realtime changes on a table
      the customer enabled, a cron SQL job, a webhook trigger, backup then restore, branching
      (including PostGIS and pgvector). The supabase-js and realtime JS gates pass.
- [ ] Upgrade from version 7: ownership moves, grants are unchanged (show `\dn+` and `\dp`
      before and after for one project).
- [ ] A project that already has a customer-made object in a managed schema is skipped with a
      `schema_error` naming the object; nothing is dropped; other projects upgrade.
- [ ] Rollback: a documented SQL snippet that returns one project to version 7 ownership, tested.
- [ ] Restore of a backup taken **before** M6c into an M6c project: state what ownership results
      and fix it if it comes back customer-owned (for example re-run the upgrade at the end of
      `restore`).
- [ ] Not tested section, measured vs estimated labels, as always.

Stop at `reports/M6c.md` for audit. Branch: `pibase/m6c` from `pibase/m6b`.
