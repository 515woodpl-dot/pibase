# M5a Gate Report — realtime changes and delivery foundation

## Delivered

- A `postgres_changes` WebSocket endpoint at `/realtime/v1/websocket`, using
  the Phoenix frame shape expected by supabase-js.
- Opt-in `realtime.enable_table(regclass)` triggers that publish only table,
  operation, and primary-key id. For every candidate delivery the gateway
  fetches the current row through the subscriber's RLS role before sending it.
- A 32-connection-per-project default cap, five-minute idle deadline, and
  reconnecting PostgreSQL LISTEN loops after transient database failures.
- Per-project `cron.jobs`, durable `cron.webhook_outbox`, and an
  owner-installed `cron.enqueue_webhook()` row-trigger producer. Scheduled
  and event-driven webhooks use the same outbox worker.
- A database-enforced 256-item pending-outbox cap per project. It serializes
  admission and applies equally to scheduler, row-trigger, and direct owner
  inserts; at the existing 1 MiB item limit it bounds payload storage to about
  256 MiB before PostgreSQL overhead.
- Bounded retry delivery, redirect refusal, and DNS-time plus dial-time SSRF
  checks.
- An operator-only `pibase pgvector-enable PROJECT_ID` command. It is absent
  from the customer SQL/API paths and requires the host PostgreSQL image to
  provide the `vector` extension.

## Intentional subset limits

- Realtime is unfiltered `public`-table `INSERT` and `UPDATE` only. `DELETE`
  subscriptions fail clearly because a committed deleted row cannot be
  rechecked under generic RLS without retaining a potentially stale copy.
- Broadcast and presence remain M5b.
- Cron accepts only every-minute and `*/N`-minute schedules in M5a.
- The scheduler deliberately processes one cron job and one delivery per
  project per tick. This is a Pi-scale throughput trade-off; revisit batching
  or parallelism if project count or job density increases.

## Verification

In the supplied Docker Go build environment, from a throwaway copy with the
repository source left unchanged:

- `gofmt -l cmd internal` produced no files.
- `go vet ./...` and `go test ./...` passed. `go build -buildvcs=false
  ./...` passed; VCS stamping is disabled because the throwaway copy has no
  `.git` metadata.
- `GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -buildvcs=false -trimpath
  -o /tmp/pibase-linux-arm64 ./cmd/pibase` passed.
- `TestM5RealtimeRLSAndCronOutbox` passed against Docker PostgreSQL. It uses
  PostgreSQL and WebSocket clients to cover owner-only realtime delivery,
  scheduled SQL, scheduled webhook enqueue/delivery, loopback SSRF rejection,
  event-trigger outbox enqueue, and pending-outbox backpressure.

The full Pi hardware gate still requires the measurement below and the
auditor's live attack suite (including DNS rebinding and flapping endpoint
coverage).

## Remediation audit

- **VERIFIED:** `gofmt -l internal cmd` is clean after formatting
  `internal/gateway/realtime_test.go`.
- **VERIFIED:** the live PostgreSQL M5 integration test fills an outbox to 256
  pending items, verifies that a direct insert is rejected, and verifies that
  a scheduled webhook records the cap error and advances its next run instead
  of busy-retrying every tick.
- **READ:** `cron_webhook_outbox_limit` is a `BEFORE INSERT` trigger on the
  outbox itself, so cron jobs, `cron.enqueue_webhook()` triggers, and ordinary
  owner inserts share the same bound. An advisory transaction lock prevents a
  concurrent-insert race past the cap.
- **UNVERIFIED:** the Pi 4 RSS/socket measurement remains hardware-only.

## Pi measurement — pending hardware gate

The required Pi 4 WebSocket RAM measurement has not been taken in this x86
development environment. On the target Pi, record the gateway RSS before and
after opening the configured number of idle sockets, along with the connection
count, build revision, and `PIBASE_REALTIME_MAX_CONNECTIONS` setting. The
result belongs here before auditor sign-off; no x86/Docker number substitutes
for it.
