# M5b Gate Report — broadcast, presence, and realtime ergonomics

**Branch:** `pibase/m5b`
**Status:** PRE-BUILD

## Acceptance items

- [ ] Public-channel broadcast works with supabase-js-compatible frames,
  including `self` and `ack` behavior.
- [ ] Presence sends initial state, join/leave diffs, supports track/untrack,
  and removes socket state on disconnect.
- [ ] Project id plus topic is the routing boundary; same-named topics in two
  projects cannot exchange broadcast or presence data.
- [ ] Private channels, replay, HTTP broadcast, and arbitrary binary payloads
  fail clearly or remain unreachable as documented.
- [ ] Topic count, frame/payload/name size, and per-socket event-rate limits are
  enforced without affecting heartbeats or server pings.
- [ ] M5a postgres_changes RLS isolation and delivery behavior do not regress.
- [ ] Formatting, vet, full tests, live PostgreSQL integration, amd64 build,
  and static arm64 cross-build pass.

## Pre-build decisions

- Broadcast and presence are ephemeral and single-gateway. Restarting the
  gateway clears presence; horizontal fanout is outside the single-Pi design.
- Only public channels are accepted. The anon key is public, so broadcast and
  presence payloads are untrusted application data. Clients must not treat a
  presence key as authenticated identity.
- No new cron HTTP API is added. Owner SQL remains the sole management surface,
  preserving the already-audited authenticator/admin-role boundary.

## Verification

Pending implementation and adversarial audit.

## Known hardware carryovers

- M5a Pi 4 realtime socket RSS measurement: UNVERIFIED pending hardware.
- M4 fresh-Pi `scripts/setup-pi.sh` run: UNVERIFIED pending hardware.
