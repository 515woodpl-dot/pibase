#!/usr/bin/env bash
# Starts the email/webhook stand-in (support/mail-sink.py), then runs a suite with email + monitoring on.
# Usage: tests/e2e/run-email.sh email-monitor.mjs     (needs: pip install aiosmtpd)
set -euo pipefail
HERE="$(cd "$(dirname "$0")" && pwd)"; WORK="${TEST_WORK:-/tmp/datbase-e2e}"; mkdir -p "$WORK"
PIDS="$WORK/mail.pids"
cleanup() { [ -f "$PIDS" ] && while read -r p; do kill "$p" 2>/dev/null || true; done < "$PIDS"; rm -f "$PIDS"; }
cleanup; trap cleanup EXIT
python3 "$HERE/support/mail-sink.py" 2525 2530 > "$WORK/mail-sink.log" 2>&1 & echo $! >> "$PIDS"
for _ in $(seq 1 20); do curl -s -o /dev/null http://127.0.0.1:2530/messages && break; sleep 0.3; done
export PB_PORTS="8090 8091" MAIL_SINK=http://127.0.0.1:2530 ADMIN_EMAIL=admin@ops.test MONITOR_TOKEN=test-monitor-token
"$HERE/harness.sh" "$1" \
  SMTP_HOST=127.0.0.1 SMTP_PORT=2525 SMTP_USER=test SMTP_PASS=test "SMTP_FROM=DATBase Test <no-reply@datbase.test>" \
  ADMIN_EMAIL=admin@ops.test MONITOR_TOKEN=test-monitor-token ALERT_WEBHOOK_URL=http://127.0.0.1:2530/webhook \
  MONITOR_INTERVAL_MIN=0.05 MONITOR_FIRST_DELAY_SEC=2 MONITOR_CONFIRM_RUNS=2 PROJECT_MAIL_PER_HOUR=8 \
  MONITOR_DISK_PCT=99.9 MONITOR_MEM_PCT=99.9   # thresholds out of reach: this test machine's disk may be full-ish
