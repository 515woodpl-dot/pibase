#!/bin/sh
set -eu

cd "$(dirname "$0")/.."
./scripts/init-env.sh

postgres_password="${POSTGRES_PASSWORD:-$(sed -n 's/^POSTGRES_PASSWORD=//p' .env)}"
master_key="${PIBASE_MASTER_KEY:-$(sed -n 's/^PIBASE_MASTER_KEY=//p' .env)}"
mkdir -p reports/raw

# This is a dedicated Compose project (pibase-m0), so this removes only its M0 test data.
docker compose down --volumes
docker compose build
docker compose up -d postgres bootstrap

printf 'timestamp,projects,mode,sample,gateway_rss_kib,postgres_summed_rss_kib\n' > reports/raw/m0-memory.csv
{
  uname -a
  free -m
  docker version --format 'docker_client={{.Client.Version}} docker_server={{.Server.Version}}'
  docker compose version
} > reports/raw/m0-environment.txt

created=0
for target in 1 20 100; do
  while [ "$created" -lt "$target" ]; do
    created=$((created + 1))
    docker run --rm --network pibase-m0_default \
      -e PIBASE_ADMIN_DATABASE_URL="postgres://postgres:$postgres_password@postgres:5432/postgres" \
      -e PIBASE_MASTER_KEY="$master_key" \
      pibase-m0-gateway project-create "measurement-$created" >/dev/null
  done

  for mode in idle light; do
    light=false
    if [ "$mode" = light ]; then light=true; fi
    PIBASE_M0_ACTIVE_PROJECTS="$target" PIBASE_M0_LIGHT_LOAD="$light" docker compose up -d --force-recreate gateway
    docker run --rm --network host busybox:1.36 sh -c \
      'i=0; until wget -qO- http://127.0.0.1:18080/healthz >/dev/null; do i=$((i+1)); [ "$i" -lt 30 ] || exit 1; sleep 1; done'
    sleep 5
    sample=1
    while [ "$sample" -le 3 ]; do
      gateway_rss="$(docker top pibase-m0-gateway-1 -eo pid,rss,comm | awk '$3=="pibase" {sum+=$2} END {print sum+0}')"
      postgres_rss="$(docker top pibase-m0-postgres-1 -eo pid,rss,comm | awk '$3=="postgres" {sum+=$2} END {print sum+0}')"
      printf '%s,%s,%s,%s,%s,%s\n' "$(date -Iseconds)" "$target" "$mode" "$sample" "$gateway_rss" "$postgres_rss" >> reports/raw/m0-memory.csv
      sample=$((sample + 1))
      sleep 1
    done
  done
done

docker compose logs --no-color gateway > reports/raw/m0-gateway.log
