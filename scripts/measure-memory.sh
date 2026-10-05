#!/bin/sh
set -eu

cd "$(dirname "$0")/.."

# Measure whole-container PSS for Postgres and gateway at one project, then at
# 20 projects both idle and after every project has served one API request.

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
  process_user="$3"
  echo "$label whole-container PSS (all /proc processes, measured):"
  docker exec --user "$process_user" "$container" sh -c '
    found=0; total=0
    for f in /proc/[0-9]*/smaps_rollup; do
      [ -r "$f" ] || continue
      pss=$(awk "/^Pss:/{sum += \$2} END{print sum+0}" "$f" 2>/dev/null) || continue
      printf "  %s: %s kB\\n" "${f%/smaps_rollup}" "$pss"
      total=$((total + pss)); found=1
    done
    [ "$found" = 1 ] || exit 1
    printf "  total: %s kB\\n" "$total"
  ' || echo "  PSS unavailable inside container"
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

project_json="$(docker run --rm --network host \
  --env PIBASE_ADMIN_DATABASE_URL="$admin_url" \
  --env PIBASE_MASTER_KEY="$master_key" \
  "$ops_image" project-create "pss-project-01")"
auth_db_url="postgres://authenticator:${auth_password}@127.0.0.1:${pg_port}/_control?sslmode=disable"
project_keys="$(mktemp)"
project_id="$(printf '%s\n' "$project_json" | sed -n 's/.*"id"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p')"
service_key="$(printf '%s\n' "$project_json" | sed -n 's/.*"service_key"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p')"
printf 'CREATE TABLE public.pss_probe(id integer PRIMARY KEY);' | docker run --rm -i --network host \
  --env PIBASE_DATABASE_URL="$auth_db_url" "$ops_image" migration-apply "$project_id" 0001_pss_probe
printf '%s %s\n' "$project_id" "$service_key" >>"$project_keys"

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
read_memory "$postgres_container" "Postgres" postgres
echo ""
read_memory "$gateway_container" "Gateway" pibase
echo ""

docker rm --force "$gateway_container" >/dev/null 2>&1 || true

for n in $(seq 2 20); do
  proj_name="$(printf 'pss-project-%02d' "$n")"
  project_json="$(docker run --rm --network host \
    --env PIBASE_ADMIN_DATABASE_URL="$admin_url" \
    --env PIBASE_MASTER_KEY="$master_key" \
    "$ops_image" project-create "$proj_name")"
  project_id="$(printf '%s\n' "$project_json" | sed -n 's/.*"id"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p')"
  service_key="$(printf '%s\n' "$project_json" | sed -n 's/.*"service_key"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p')"
  printf 'CREATE TABLE public.pss_probe(id integer PRIMARY KEY);' | docker run --rm -i --network host \
    --env PIBASE_DATABASE_URL="$auth_db_url" "$ops_image" migration-apply "$project_id" 0001_pss_probe
  printf '%s %s\n' "$project_id" "$service_key" >>"$project_keys"
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
read_memory "$postgres_container" "Postgres" postgres
echo ""
read_memory "$gateway_container" "Gateway" pibase
echo ""

while IFS=' ' read -r project_id service_key; do
  curl --fail --silent --output /dev/null --header "apikey: $service_key" "http://127.0.0.1:28080/rest/v1/pss_probe?select=id"
done <"$project_keys"

backend_count="$(docker exec "$postgres_container" psql -U postgres -tA -c "SELECT count(*) FROM pg_stat_activity WHERE datname LIKE 'p\\_%' ESCAPE '\\' AND backend_type='client backend'")"
echo "Project database client backends after served REST reads (measured): $backend_count"

echo "--- After 20 projects (each served one request, measured) ---"
echo ""
read_memory "$postgres_container" "Postgres" postgres
echo ""
read_memory "$gateway_container" "Gateway" pibase
echo ""
rm -f "$project_keys"

echo "=== Measurement complete ==="
} 2>&1 | tee "$output_file"

echo ""
echo "Raw output saved to $output_file"
