# T5 Gate: Admin usage history and project limits

Date: 2026-10-06  
Branch: pibase/m7  
Status: **PASS**

## Scope

Two new operator/account-owner endpoints on the admin server:

- `GET /_pibase/projects/{id}/usage` — daily request counts for a project (last
  30 days by default).
- `PATCH /_pibase/projects/{id}/limits` — update per-project resource caps
  (currently `max_database_bytes`).

## Implementation

### `internal/control/project.go` — new function `SetLimits`

```go
func SetLimits(ctx context.Context, adminURL, projectID string, maxDatabaseBytes int64) error
```

- Requires `maxDatabaseBytes > 0`; returns `fmt.Errorf("max_database_bytes must be positive")` otherwise.
- `UPDATE control.projects SET max_database_bytes=$1 WHERE id=$2`.
- Returns `ErrNotFound` when no rows are affected.
- Uses the existing `controlDBConn` helper.

The existing `Usage` function (introduced in an earlier milestone) is reused unchanged.

### `internal/adminserver/server.go` — two new routes and handlers

| Route | Handler | Action |
|---|---|---|
| `GET /_pibase/projects/{id}/usage` | `usage` | Calls `control.Usage(…, zero, zero)` — defaults to last 30 days |
| `PATCH /_pibase/projects/{id}/limits` | `setLimits` | Calls `control.SetLimits` |

Both handlers call `checkOwner` first: operators pass unconditionally; account
sessions are verified to own the project.

`usage` returns a JSON array (empty `[]` when no rows, never `null`).

`setLimits` accepts `{"max_database_bytes": <int64>}`. Returns 204 on success,
400 on invalid value, 404 on unknown project, 403 on ownership failure.

## Test matrix (appended to `TestProjectManagement`)

| ID | Description | Expected |
|---|---|---|
| U1 | Operator reads usage → JSON array | 200 |
| U2 | Non-owner reads usage → forbidden | 403 |
| U3 | Owner reads their own project's usage | 200 |
| L1 | Operator sets `max_database_bytes=2GiB` → limit appears in project list | 204 |
| L2 | Zero `max_database_bytes` → rejected | 400 |
| L3 | Non-owner sets limits → forbidden | 403 |
| L4 | Owner sets limits on own project | 204 |

All 7 sub-tests pass with `go test ./...`.

## Build and test

```
go build ./...   # exit 0
go test ./...    # all packages PASS
```
