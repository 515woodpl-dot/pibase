# M5b review — broadcast, presence, and realtime ergonomics

**Commit reviewed:** `44e2ced` on branch `pibase/m5b`
**Environment:** Raspberry Pi 4 Model B Rev 1.5, Linux arm64, Go 1.23.12 in
Docker, PostgreSQL 16.15 in Docker, Node 20.19.2 with native WebSocket flag
**Verdict:** APPROVE

M5b adds bounded, in-memory public broadcast and presence without weakening
the M5a database-change boundary. Project/topic isolation, lifecycle cleanup,
current supabase-js compatibility, and the carried two-user RLS attack all
hold under live tests. No blocking finding remains.

## What I ran

| Check | Result | Tag |
|---|---|---|
| `gofmt -l internal cmd` | clean | VERIFIED |
| `go vet ./...` | clean | VERIFIED |
| `go test ./...` | pass | VERIFIED |
| Full suite with `PIBASE_TEST_ADMIN_DATABASE_URL` | pass against PostgreSQL 16.15 | VERIFIED |
| `go build -buildvcs=false ./...` | pass | VERIFIED |
| static Linux arm64 and amd64 builds | pass | VERIFIED |
| `TestM5RealtimeRLSAndCronOutbox` | pass, including A→B→A ownership changes | VERIFIED |
| `TestM5bBroadcastPresenceIsolation` | pass | VERIFIED |
| `tests/realtime-js.mjs` using bundled supabase-js | pass | VERIFIED |
| 32-socket Pi PSS measurement | +2,144 KiB, 68,608 bytes/socket | VERIFIED |
| `go test -race ./internal/gateway` | unsupported VMA range on this Pi kernel | UNVERIFIED |

The container sees the mounted repository without usable VCS ownership, so
build checks used `-buildvcs=false`; no product source or module file was
modified for the build.

## Contract checks

- **VERIFIED:** public broadcasts route only to clients with the same project
  id and exact topic. A same-named topic in another project and a different
  topic in the same project receive nothing.
- **VERIFIED:** broadcast `self: false`, `self: true`, and `ack: true` behavior
  matches the Phoenix frames consumed by supabase-js.
- **VERIFIED:** both JSON frames and the current supabase-js compact JSON
  broadcast binary envelope work. Arbitrary binary payloads are rejected.
- **VERIFIED:** presence sends initial state and join/leave diffs; track,
  untrack, channel leave, re-track, and socket-disconnect cleanup are bounded
  in memory.
- **VERIFIED:** private joins fail explicitly. No HTTP broadcast route, replay
  store, persistence, or cross-node fanout exists.
- **VERIFIED:** sockets are capped at 20 topics, broadcast/presence payloads at
  16 KiB, names at 128 bytes, and client events at 60 per second. Live tests
  cover topic and payload rejection; unit tests cover the event limiter and
  malformed binary envelopes.
- **READ:** Gorilla's per-connection read limit remains 1 MiB. An oversized
  frame was not sent during this audit.
- **READ:** public channel metadata is intentionally untrusted. Presence keys
  are routing labels, not authenticated identity; README documents this and
  the any-origin WebSocket decision.
- **VERIFIED:** no new cron HTTP/API surface was added. Owner SQL remains the
  management surface and scheduled SQL still runs over the authenticator pool
  with `SET LOCAL ROLE`.
- **VERIFIED:** after the M5b changes, a user receives a full-row UPDATE only
  while RLS makes the row visible; A→B and B→A transfers fail closed for the
  former owner.

## Adversarial findings and fixes

### M5b-01 (Medium — fixed before gate): aborted scheduler transaction

The outbox limit trigger raises a PostgreSQL error. The M5a scheduler initially
tried to record that error in the already-aborted transaction, so its status
update rolled back. The enqueue now runs in a savepoint; on failure the
savepoint rolls back and the outer transaction records `last_error` plus the
next scheduled run. The live backpressure test now passes. **VERIFIED.**

### M5b-02 (Low — environment limitation): race detector unavailable

Go's race runtime exits with `ThreadSanitizer: unsupported VMA range` on this
Pi kernel. Lock ordering and all shared realtime state were reviewed manually,
but race instrumentation remains **UNVERIFIED** and should be rerun on a
supported amd64 or arm64 host.

## Claims not fully verified

- Node 22 was not installed. The bundled current supabase-js passed under Node
  20.19.2 with `--experimental-websocket`, but the package emits its upstream
  Node-20 deprecation warning. **UNVERIFIED on Node 22.**
- Gateway restart clearing presence is inherent to the in-memory design and
  confirmed by code inspection, not a live restart test. **READ.**
- Cross-node fanout is deliberately unsupported; only one gateway is in the
  product architecture. **READ.**

## Scope creep

None. M5b contains only realtime ergonomics, their resource guards/tests, the
M5a scheduler savepoint fix found during regression audit, and the carried Pi
socket-memory measurement.

## Open carryovers

- M4-03: run `scripts/setup-pi.sh` on a disposable fresh Pi. This host is an
  active Pi and is not safe evidence for a fresh-install test.
- M4-01: replace textual branch role remapping with structural remapping before
  branching is heavily used.

## Gate

APPROVE. M5b is complete. M6 remains request-only under `PLAN.md` and has not
been started.
