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

The separate final M5a review records the auditor's live attack suite,
including two-user ownership changes and webhook SSRF behavior. M5b's audit
also reran the ownership-change regression after adding broadcast/presence.

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
- **VERIFIED:** the Pi 4 PSS/socket measurement is recorded below.

## Pi measurement

On a Raspberry Pi 4 Model B Rev 1.5 with 4 GB RAM, 32 external supabase-js
clients increased the gateway test process's PSS from 12,333 KiB to 14,477
KiB: a 2,144 KiB delta, or 68,608 bytes per connected socket. Client memory
was excluded by running Node in a separate process. The test process includes
the gateway plus the Go test harness, so the delta—not the absolute baseline—
is the useful figure. **VERIFIED.** Raw command, environment, and output:
[`reports/raw/m5b-realtime-pi4.txt`](raw/m5b-realtime-pi4.txt).
