#!/usr/bin/env bash
# Reset the local test stack and run one e2e suite:  tests/e2e/harness.sh <suite.mjs> [KEY=VALUE for the app...]
# Works on Linux and macOS. Requires: PostgreSQL 16 (PSQL, default "psql -h /tmp -p 5433 -U postgres";
# PG_BIN puts matching psql/pg_dump/pg_restore first in PATH), a production build (npm run build),
# and for backup suites moto_server (pip install "moto[server]"). PocketBase is downloaded for your
# OS/CPU unless PB_BIN is set. See tests/README.md.
set -euo pipefail
HERE="$(cd "$(dirname "$0")" && pwd)"; APP_DIR="$(cd "$HERE/../.." && pwd)"
SUITE="$1"; shift || true
WORK="${TEST_WORK:-/tmp/datbase-e2e}"; mkdir -p "$WORK"
[ -n "${PG_BIN:-}" ] && export PATH="$PG_BIN:$PATH"
PSQL="${PSQL:-psql -h /tmp -p 5433 -U postgres}"
PB_VERSION="${PB_VERSION:-0.39.11}"
OS="$(uname -s | tr '[:upper:]' '[:lower:]')"                      # linux | darwin
ARCH="$(uname -m | sed 's/x86_64/amd64/; s/aarch64/arm64/')"        # amd64 | arm64
PB_BIN="${PB_BIN:-$WORK/pocketbase-$PB_VERSION-$OS-$ARCH/pocketbase}"
PB_PORTS="${PB_PORTS:-8090}"
export TEST_DB="${TEST_DB:-datbase_e2e}" WORK PB_BIN APP_DIR PSQL
# Remember the real PATH/DOCKER_HOST so test helpers (psql) aren't affected by app-only fakes.
export HARNESS_ORIG_PATH="$PATH" HARNESS_ORIG_DOCKER_HOST="${DOCKER_HOST:-}"

# Only processes this harness started are stopped (tracked by PID, never by name).
PIDS="$WORK/harness.pids"
stop() {
  [ -f "$PIDS" ] || return 0
  while read -r p; do
    kill "$p" 2>/dev/null || continue
    for _ in $(seq 1 50); do kill -0 "$p" 2>/dev/null || break; sleep .2; done
    kill -9 "$p" 2>/dev/null || true
  done < "$PIDS"
  rm -f "$PIDS"
}
stop
trap 'stop; exit 130' INT TERM
for port in 3000 $PB_PORTS; do
  if curl -s -o /dev/null --max-time 2 "http://127.0.0.1:$port/"; then
    echo "Port $port is already in use by another process. Stop it first (e.g. lsof -i :$port)." >&2
    exit 1
  fi
done

if [ ! -x "$PB_BIN" ]; then
  echo "Downloading PocketBase $PB_VERSION ($OS/$ARCH) → $PB_BIN"
  mkdir -p "$(dirname "$PB_BIN")"
  curl -fsSL -o "$WORK/pb.zip" "https://github.com/pocketbase/pocketbase/releases/download/v${PB_VERSION}/pocketbase_${PB_VERSION}_${OS}_${ARCH}.zip"
  unzip -oq "$WORK/pb.zip" pocketbase -d "$(dirname "$PB_BIN")"
fi

# Fresh control database
$PSQL -qc "drop database if exists $TEST_DB" -c "create database $TEST_DB" postgres >/dev/null

export ENCRYPTION_KEY="${ENCRYPTION_KEY:-$(openssl rand -base64 32)}"
export BETTER_AUTH_SECRET="${BETTER_AUTH_SECRET:-$(openssl rand -base64 32)}"
# Same derivation as app/src/lib/crypto.ts → controlSecret(slug)
control_secret() { node -e 'const c=require("crypto");process.stdout.write(c.createHmac("sha256",Buffer.from(process.env.ENCRYPTION_KEY,"base64")).update("datbase-control:"+process.argv[1]).digest("base64url"))' "$1"; }

# Fresh PocketBase instances (port = project slug), same flags as production
for port in $PB_PORTS; do
  rm -rf "$WORK/pb$port"
  "$PB_BIN" superuser upsert admin@proj.test SuperSecret123 --dir="$WORK/pb$port" >/dev/null
  DATBASE_CONTROL_SECRET="$(control_secret "$port")" nohup "$PB_BIN" serve --http=127.0.0.1:$port --dir="$WORK/pb$port" \
    --automigrate=false --hooksDir="$APP_DIR/../pocketbase/pb_hooks" > "$WORK/pb$port.log" 2>&1 &
  echo $! >> "$PIDS"; echo $! > "$WORK/pb$port.pid"
done

# App (production build). Extra KEY=VALUE arguments configure the app (and the suite, which may
# start more app instances with the same settings).
cd "$APP_DIR/.next/standalone" && rm -rf drizzle .next/static && cp -r "$APP_DIR/drizzle" . && cp -r "$APP_DIR/.next/static" .next/
export BETTER_AUTH_URL=http://localhost:3000 DOMAIN=example.test PB_INTERNAL_URL='http://127.0.0.1:{slug}' PORT=3000
export DATABASE_URL="${DATABASE_URL_BASE:-postgres://postgres@localhost:5433}/$TEST_DB?host=/tmp"
for kv in "$@"; do export "$kv"; done
nohup node server.js > "$WORK/app.log" 2>&1 &
echo $! >> "$PIDS"
for _ in $(seq 1 30); do curl -sf -o /dev/null http://localhost:3000/login && break; sleep 1; done
for port in $PB_PORTS; do
  for _ in $(seq 1 30); do curl -sf -o /dev/null "http://127.0.0.1:$port/api/health" && break; sleep .5; done
done

cd "$HERE" && set +e; node "$SUITE"; code=$?
[ "${KEEP_RUNNING:-0}" = 1 ] || stop
exit $code
