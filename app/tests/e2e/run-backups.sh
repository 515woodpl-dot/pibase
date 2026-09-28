#!/usr/bin/env bash
# Starts the S3 stand-in (moto), a failure-switchable proxy and a fake Docker, then runs a backup suite.
# Usage: tests/e2e/run-backups.sh backup-fixes.mjs | backup-scheduler.mjs
set -euo pipefail
HERE="$(cd "$(dirname "$0")" && pwd)"; WORK="${TEST_WORK:-/tmp/datbase-e2e}"; mkdir -p "$WORK"
PIDS="$WORK/support.pids"
cleanup() { [ -f "$PIDS" ] && while read -r p; do kill "$p" 2>/dev/null || true; done < "$PIDS"; rm -f "$PIDS"; }
cleanup; trap cleanup EXIT
moto_server -p 5055 > "$WORK/moto.log" 2>&1 & echo $! >> "$PIDS"
echo ok > "$WORK/s3-mode"; echo ok > "$WORK/pgdump-mode"
python3 "$HERE/support/flaky-s3-proxy.py" 5056 5055 "$WORK/s3-mode" > "$WORK/proxy.log" 2>&1 & echo $! >> "$PIDS"
python3 "$HERE/support/fake-docker.py" 2375 > "$WORK/docker.log" 2>&1 & echo $! >> "$PIDS"
for i in $(seq 1 20); do curl -s -o /dev/null http://127.0.0.1:5055/ && break; sleep 0.5; done
# WITH_MAIL=1: also an email/webhook stand-in (support/mail-sink.py) and email settings for the app
MAIL_ENV=()
if [ "${WITH_MAIL:-0}" = 1 ]; then
  python3 "$HERE/support/mail-sink.py" 2525 2530 > "$WORK/mail-sink.log" 2>&1 & echo $! >> "$PIDS"
  for _ in $(seq 1 20); do curl -s -o /dev/null http://127.0.0.1:2530/messages && break; sleep 0.3; done
  export MAIL_SINK=http://127.0.0.1:2530 ADMIN_EMAIL=admin@ops.test
  MAIL_ENV=(SMTP_HOST=127.0.0.1 SMTP_PORT=2525 SMTP_USER=test SMTP_PASS=test "SMTP_FROM=DATBase Test <no-reply@datbase.test>" ADMIN_EMAIL=admin@ops.test MONITOR_DISABLED=1)
fi
node -e '
const { S3Client, CreateBucketCommand } = require(process.argv[1] + "/../../node_modules/@aws-sdk/client-s3");
new S3Client({ region: "us-east-1", endpoint: "http://127.0.0.1:5055", forcePathStyle: true, credentials: { accessKeyId: "test", secretAccessKey: "test" } })
  .send(new CreateBucketCommand({ Bucket: "datbase-e2e" })).then(() => console.log("bucket ready"));' "$HERE"
export PB_PORTS="8090 8091" S3_MODE_FILE="$WORK/s3-mode" FAKE_PGDUMP_MODE="$WORK/pgdump-mode" S3_DIRECT=http://127.0.0.1:5055
export BACKUP_S3_BUCKET=datbase-e2e
"$HERE/harness.sh" "$1" \
  BACKUP_S3_ENDPOINT=http://127.0.0.1:5056 BACKUP_S3_REGION=us-east-1 BACKUP_S3_BUCKET=datbase-e2e \
  BACKUP_S3_KEY=test BACKUP_S3_SECRET=test BACKUP_S3_FORCE_PATH_STYLE=true \
  DOCKER_HOST=tcp://127.0.0.1:2375 "PATH=$HERE/support:${PG_BIN:+$PG_BIN:}$PATH" \
  BACKUP_CHECK_MIN="${BACKUP_CHECK_MIN:-60}" BACKUP_FIRST_DELAY_SEC="${BACKUP_FIRST_DELAY_SEC:-3600}" ${MAIL_ENV[@]+"${MAIL_ENV[@]}"}
