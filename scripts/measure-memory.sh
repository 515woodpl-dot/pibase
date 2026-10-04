#!/bin/sh
set -eu

cd "$(dirname "$0")/.."

# Measure memory for Postgres and gateway processes at 1 and 20 projects, idle.
# Reports PSS from /proc/<pid>/smaps_rollup when accessible, otherwise falls
# back to RSS from docker stats. Writes raw output to reports/raw/.
#
# PSS requires read access to /proc/<pid>/smaps_rollup on the host, which
# typically needs root. On a Pi, the operator can re-run with sudo if PSS is
# needed; the script works without it.

output_dir="reports/raw"
mkdir -p "$output_dir"
output_file="$output_dir/m6d-memory-pss.txt"

gateway_image="${PIBASE_IMAGE:-pibase:gate}"
ops_image="${PIBASE_OPS_IMAGE:-${gateway_image}-ops}"
postgres_image="${PIBASE_TEST_POSTGRES_IMAGE:-pibase-postgres:test}"
postgres_password="$(od -An -N16 -tx1 /dev/urandom | tr -d ' \n')"
auth_password="memory-drill-auth"
master_key="$(head -c 32 /dev/urandom | base64 | tr -d '\n')"
postgres_container="pibase-memory-pg-$$"
gateway_container="pibase-memory-gw-$$"

cleanup() {
  docker rm --force "$gateway_container" "$postgres_container" >/dev/null 2>&1 || true
}
trap cleanup EXIT INT TERM

read_memory() {
  container="$1"
  label="$2"
  pid="$(docker inspect --format '{{.State.Pid}}' "$container" 2>/dev/null || echo 0)"
  if [ "$pid" != "0" ] && grep -i 'Pss:' "/proc/$pid/smaps_rollup" >/dev/null 2>&1; then
    echo "$label PSS (from smaps_rollup, measured):"
    grep -i 'Pss:' "/proc/$pid/smaps_rollup"
  elif [ "$pid" != "0" ] && grep -i 'Pss:' "/proc/$pid/smaps" >/dev/null 2>&1; then
    echo "$label PSS (from smaps, measured):"
    grep -i 'Pss:' "/proc/$pid/smaps" | awk '{sum += $2} END {printf "  Pss: %d kB\n", sum}'
  else
    echo "$label PSS: not accessible (smaps requires root)"
  fi
  if [ "$pid" != "0" ] && [ -r "/proc/$pid/status" ]; then
    echo "$label RSS (from /proc/$pid/status, measured):"
    grep -E 'VmRSS|RssAnon|RssFile|RssShmem' "/proc/$pid/status" | sed 's/^/  /'
  else
    echo "$label RSS: not accessible"
  fi
}

{
echo "=== Memory measurement ==="
echo "Date: $(date -Is)"
echo "Machine: $(uname -a)"
echo "Gateway image: $gateway_image"
echo "Postgres image: $postgres_image"
echo ""

docker run --rm --detach --name "$postgres_container" \
  --env POSTGRES_PASSWORD="$postgres_password" \
  --publish 127.0.0.1::5432 \
  "$postgres_image" \
  postgres -c shared_buffers=128MB -c max_connections=40 -c work_mem=4MB >/dev/null
i=0
until docker exec "$postgres_container" pg_isready -U postgres >/dev/null 2>&1; do
  i=$((i+1))
  [ "$i" -lt 30 ] || { docker logs "$postgres_container"; exit 1; }
  sleep 1
done
pg_port="$(docker port "$postgres_container" 5432 | sed 's/.*://')"
admin_url="postgres://postgres:${postgres_password}@127.0.0.1:${pg_port}/postgres?sslmode=disable"

docker run --rm --network host \
  --env PIBASE_ADMIN_DATABASE_URL="$admin_url" \
  --env PIBASE_AUTHENTICATOR_PASSWORD="$auth_password" \
  "$ops_image" bootstrap

docker run --rm --network host \
  --env PIBASE_ADMIN_DATABASE_URL="$admin_url" \
  --env PIBASE_MASTER_KEY="$master_key" \
  "$ops_image" project-create "pss-project-01" >/dev/null

auth_db_url="postgres://authenticator:${auth_password}@127.0.0.1:${pg_port}/_control?sslmode=disable"
docker run --detach --name "$gateway_container" \
  --network host \
  --env PIBASE_DATABASE_URL="$auth_db_url" \
  --env PIBASE_MASTER_KEY="$master_key" \
  --env PIBASE_LISTEN_ADDR=:28080 \
  --env PIBASE_ADMIN_DATABASE_URL="$auth_db_url" \
  --env PIBASE_STORAGE_ROOT=/data/files \
  "$ops_image" serve >/dev/null
sleep 5
if ! docker ps --filter "name=$gateway_container" --format '{{.Names}}' | grep -q .; then
  echo "WARNING: gateway container exited:" >&2
  docker logs "$gateway_container" 2>&1 | tail -5 >&2
fi

echo "--- After 1 project (idle) ---"
echo ""
read_memory "$postgres_container" "Postgres"
echo ""
read_memory "$gateway_container" "Gateway"
echo ""

docker rm --force "$gateway_container" >/dev/null 2>&1 || true

for n in $(seq 2 20); do
  proj_name="$(printf 'pss-project-%02d' "$n")"
  docker run --rm --network host \
    --env PIBASE_ADMIN_DATABASE_URL="$admin_url" \
    --env PIBASE_MASTER_KEY="$master_key" \
    "$ops_image" project-create "$proj_name" >/dev/null
done

docker run --detach --name "$gateway_container" \
  --network host \
  --env PIBASE_DATABASE_URL="$auth_db_url" \
  --env PIBASE_MASTER_KEY="$master_key" \
  --env PIBASE_LISTEN_ADDR=:28080 \
  --env PIBASE_ADMIN_DATABASE_URL="$auth_db_url" \
  --env PIBASE_STORAGE_ROOT=/data/files \
  "$ops_image" serve >/dev/null
sleep 5
if ! docker ps --filter "name=$gateway_container" --format '{{.Names}}' | grep -q .; then
  echo "WARNING: gateway container exited:" >&2
  docker logs "$gateway_container" 2>&1 | tail -5 >&2
fi

echo "--- After 20 projects (idle) ---"
echo ""
read_memory "$postgres_container" "Postgres"
echo ""
read_memory "$gateway_container" "Gateway"
echo ""

echo "=== Measurement complete ==="
} 2>&1 | tee "$output_file"

echo ""
echo "Raw output saved to $output_file"
