# Inventory compatibility work

Inventory's pibase port identified these platform gaps on 10 October 2026.
This page tracks the implementations added for each gap. See
[COMPATIBILITY.md](COMPATIBILITY.md) for the current wire behavior.

| # | Need | Status |
|---|---|---|
| 1 | End one, other, or all sessions without banning a user | Implemented: scoped logout and service-key admin logout |
| 2 | Check the current password when changing it and end other sessions | Implemented: verified change endpoint and `PATCH /auth/v1/user` guard; recovery tokens remain usable |
| 3 | Stable role names for migration `GRANT` statements | Implemented: `anon`, `authenticated` and `service_role` NOLOGIN roles |
| 4 | Import bcrypt password hashes | Implemented through service-key admin create/update |
| 5 | Sign-in audit log or auth hooks | Implemented: success/failure events and service-key read endpoint |
| 6 | SQL-selected HTTP status with `PTxxx` | Implemented for 400–599 |
| 7 | Admin user creation and password set callable from SQL | Implemented: private `auth.admin_*` helpers for project-admin SQL |
| 8 | httpOnly-cookie session mode | Implemented: refresh token cookie and memory-only browser client |
| 9 | Sign-in by username | Implemented: admin-created username-only users and username password grant |
| 10 | Optional `DEFAULT` RPC arguments | Implemented: omitted named arguments use PostgreSQL defaults |
| 11 | Aggregates in the data API | Implemented: grouped count, sum, average, min and max |
| 12 | Server-only `app_metadata` on users | Implemented through the service-key admin API; returned on users and included in access tokens |

The Omnia access-control review also identified application-level follow-ups:
duplicate completion outbox entries, checking a change-order state update
before sending email, and using a configured public origin in outbound links.
Those changes belong in Omnia's SQL migration and application routes. pibase's
shared setup now aligns storage and backup volume ownership with the container
UID/GID before accepting uploads.
