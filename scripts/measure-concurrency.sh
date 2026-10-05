#!/bin/sh
set -eu

# Disposable Pi capacity measurement. It never uses compose volumes or the
# installed stack; all containers and temporary files are removed on exit.
cd "$(dirname "$0")/.."
out="reports/raw/m6e-concurrency.txt"
image="${PIBASE_IMAGE:-pibase:m6e-capacity}"
ops="${PIBASE_OPS_IMAGE:-${image}-ops}"
pg_image="${PIBASE_TEST_POSTGRES_IMAGE:-pibase-postgres:m6e-capacity}"
pg="pibase-m6e-capacity-pg-$$"; gw="pibase-m6e-capacity-gw-$$"
tmp="$(mktemp -d)"
cleanup() { docker rm --force "$gw" "$pg" >/dev/null 2>&1 || true; rm -rf "$tmp"; }
trap cleanup EXIT INT TERM
password="$(od -An -N16 -tx1 /dev/urandom | tr -d ' \n')"
master="$(head -c 32 /dev/urandom | base64 | tr -d '\n')"
auth_password="m6e-capacity-auth"

pss() { docker exec --user "$2" "$1" sh -c 'sum=0; for f in /proc/[0-9]*/smaps_rollup; do [ -r "$f" ] || continue; v=$(awk "/^Pss:/{s+=\$2} END{print s+0}" "$f" 2>/dev/null) || continue; sum=$((sum+v)); done; echo "$sum"'; }
sample() { printf '%s %s %s %s %s\n' "$(date +%s)" "$(pss "$pg" postgres)" "$(pss "$gw" pibase)" "$(docker exec "$pg" psql -U postgres -tA -c "SELECT count(*) FROM pg_stat_activity WHERE backend_type='client backend'")" "$(free -m | awk '/Mem:/{print $7}')" >>"$tmp/samples-$1"; }
median() { sort -n "$1" | awk '{a[NR]=$1} END {if(NR) print a[int((NR+1)/2)]}'; }
p95() { sort -n "$1" | awk '{a[NR]=$1} END {if(NR) print a[int((NR*95+99)/100)]}'; }

{
echo "=== M6e concurrent capacity measurement ==="
echo "Date: $(date -Is)"; echo "Machine: $(uname -a)"; echo "All numbers below are measured."
echo "Host free memory before load (measured MiB): $(free -m | awk '/Mem:/{print $7}')"
docker run --rm -d --name "$pg" -e POSTGRES_PASSWORD="$password" -p 127.0.0.1::5432 "$pg_image" postgres -c shared_buffers=128MB -c max_connections=40 >/dev/null
i=0; until docker exec "$pg" pg_isready -U postgres >/dev/null 2>&1; do i=$((i+1)); [ "$i" -lt 30 ] || exit 1; sleep 1; done
port="$(docker port "$pg" 5432 | sed 's/.*://')"
admin="postgres://postgres:${password}@127.0.0.1:${port}/postgres?sslmode=disable"
auth="postgres://authenticator:${auth_password}@127.0.0.1:${port}/_control?sslmode=disable"
docker run --rm --network host -e PIBASE_ADMIN_DATABASE_URL="$admin" -e PIBASE_AUTHENTICATOR_PASSWORD="$auth_password" "$ops" bootstrap

run_case() {
  count="$1"; keys="$tmp/keys-$count"; : >"$keys"
  for n in $(seq 1 "$count"); do
    project="$(docker run --rm --network host -e PIBASE_ADMIN_DATABASE_URL="$admin" -e PIBASE_MASTER_KEY="$master" "$ops" project-create "capacity-$count-$n")"
    id="$(printf '%s\n' "$project" | sed -n 's/.*"id"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p')"
    key="$(printf '%s\n' "$project" | sed -n 's/.*"service_key"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p')"
    printf 'CREATE TABLE public.capacity_rows(id integer PRIMARY KEY, value text); INSERT INTO public.capacity_rows SELECT generate_series(1,1000), repeat('"'"'x'"'"',32);' | docker run --rm -i --network host -e PIBASE_DATABASE_URL="$auth" "$ops" migration-apply "$id" 0001_capacity
    printf '%s\n' "$key" >>"$keys"
  done
  docker rm --force "$gw" >/dev/null 2>&1 || true
  docker run -d --name "$gw" --network host -e PIBASE_DATABASE_URL="$auth" -e PIBASE_MASTER_KEY="$master" -e PIBASE_LISTEN_ADDR=:28084 -e PIBASE_STORAGE_ROOT=/data/files "$ops" serve >/dev/null
  sleep 3
  end=$(( $(date +%s) + 60 )); : >"$tmp/latency-$count"; : >"$tmp/status-$count"; : >"$tmp/samples-$count"
  while IFS= read -r key; do (
    while [ "$(date +%s)" -lt "$end" ]; do
      curl --silent --output /dev/null --write-out '%{http_code} %{time_total}\n' -H "apikey: $key" 'http://127.0.0.1:28084/rest/v1/capacity_rows?select=id&limit=1000' >>"$tmp/status-$count"
    done
  ) & done <"$keys"
  while [ "$(date +%s)" -lt "$end" ]; do sample "$count"; sleep 2; done
  wait
  awk '{print $2}' "$tmp/status-$count" >"$tmp/latency-$count"
  echo "--- $count projects, 60-second concurrent REST reads ---"
  pg_median="$(awk '{print $2}' "$tmp/samples-$count" | median /dev/stdin)"; pg_peak="$(awk 'BEGIN{m=0}{if($2>m)m=$2}END{print m}' "$tmp/samples-$count")"
  gw_median="$(awk '{print $3}' "$tmp/samples-$count" | median /dev/stdin)"; gw_peak="$(awk 'BEGIN{m=0}{if($3>m)m=$3}END{print m}' "$tmp/samples-$count")"
  echo "Postgres PSS kB median=$pg_median peak=$pg_peak"
  echo "Gateway PSS kB median=$gw_median peak=$gw_peak"
  echo "Client backends peak=$(awk 'BEGIN{m=0}{if($4>m)m=$4}END{print m}' "$tmp/samples-$count")"
  echo "Requests total=$(wc -l <"$tmp/status-$count") errors=$(awk '$1 !~ /^2/{n++}END{print n+0}' "$tmp/status-$count") p50_seconds=$(median "$tmp/latency-$count") p95_seconds=$(p95 "$tmp/latency-$count")"
  echo "Host free memory at sample peak (measured MiB): $(awk 'BEGIN{m=0}{if($5<m || NR==1)m=$5}END{print m}' "$tmp/samples-$count")"
  cat "$tmp/samples-$count"; cat "$tmp/status-$count"
}
run_case 20
run_case 40
} 2>&1 | tee "$out"
