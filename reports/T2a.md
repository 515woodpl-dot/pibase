# T2a Report — Customer Accounts

**Status:** COMPLETE  
**Commit:** 09c7daa  
**Branch:** pibase/m7  
**Date:** 2026-10-06  

---

## Changes shipped

| File | Change |
|---|---|
| `internal/control/schema.go` | `control.accounts`, `control.sessions`, `owner_id` on `control.projects`, updated `project_catalog` view |
| `internal/control/accounts.go` | New — CreateAccount, AuthenticateAccount, IssueSession, ResolveSession, RevokeSession, AccountOwnsProject, SetProjectOwner |
| `internal/control/project.go` | ProjectStatsByOwner (owner-filtered list), projectStats internal helper |
| `internal/adminserver/server.go` | Identity middleware, 4 auth endpoints, rate limiter, checkOwner, ownership-aware project endpoints, updated dashboard |
| `internal/adminserver/server_test.go` | Updated "pibase operator" string check to "pibase" |
| `internal/adminserver/integration_test.go` | TestAccountAuth (U1-U10), TestAccountOwnership (I1-I12) |

## Test results

All tests pass on pibase/m7 @ 09c7daa against a live PostgreSQL cluster.

```
ok  internal/adminsql     1.426s
ok  internal/adminserver  7.781s   (includes U1-U10, I1-I12)
ok  internal/control      7.561s
```

### U1-U10 auth endpoint results

| Test | Status |
|---|---|
| U1 signup valid → 201 + cookie | PASS |
| U2 signup duplicate → 409 | PASS |
| U3 signup short password → 400 | PASS |
| U4 login correct → 200 + cookie | PASS |
| U5 login wrong password → 401 | PASS |
| U6 login absent email → 401 | PASS |
| U7 logout valid → 200, cookie cleared | PASS |
| U8 logout no cookie → 401 | PASS |
| U9 GET /me valid → 200 with fields | PASS |
| U10 GET /me invalid token → 401 | PASS |

### I1-I12 ownership integration results

| Test | Status |
|---|---|
| I1 account list returns only own projects | PASS |
| I2 other account → 403 on SQL | PASS |
| I3 operator token → 200 on any project | PASS |
| I4 account → 403 on extensions | PASS |
| I5 account project has owner_id in DB | PASS |
| I6 operator project has NULL owner_id | PASS |
| I7 no auth → 401 | PASS |
| I8 invalid session → 401 | PASS |
| I9 revoked session → 401 | PASS |
| I10 account list filtered by ownership | PASS |
| I11 delete own project then 404 | PASS |
| I12 cannot delete other account's project → 403 | PASS |

## Do-not-regress checks

All pre-existing tests pass:

- TestEveryAdminEndpointRejectsMissingToken
- TestDashboardPageLoadsWithoutTokenAndHoldsNoSecrets
- TestDashboardUIHardRules
- TestDashboardShowsKeysOnceAndWiresEveryEndpoint
- TestOperatorDashboardEndpoints
- TestBrowseTextCasting
- TestAnonReadable
- TestOwnerSQLRoleIsolation
