#!/bin/sh
set -eu

cd "$(dirname "$0")/.."
./scripts/init-env.sh

postgres_password="${POSTGRES_PASSWORD:-$(sed -n 's/^POSTGRES_PASSWORD=//p' .env)}"
master_key="${PIBASE_MASTER_KEY:-$(sed -n 's/^PIBASE_MASTER_KEY=//p' .env)}"
admin_url="postgres://postgres:$postgres_password@postgres:5432/postgres"
project_file="$(mktemp)"
migration_file="$(mktemp)"
project_id=""

cleanup() {
  if [ -n "$project_id" ]; then
    docker compose run --rm -e PIBASE_ADMIN_DATABASE_URL="$admin_url" gateway project-delete "$project_id" >/dev/null 2>&1 || true
  fi
  rm -f "$project_file" "$migration_file"
}
trap cleanup EXIT INT TERM

storage_image="${PIBASE_IMAGE:-pibase:m3}"
if ! docker image inspect "$storage_image" >/dev/null 2>&1; then
  docker build --tag "$storage_image" .
fi
docker tag "$storage_image" pibase-m0-bootstrap:latest
docker tag "$storage_image" pibase-m0-gateway:latest
docker compose up -d postgres
docker compose run --rm bootstrap
PIBASE_M0_ACTIVE_PROJECTS=0 PIBASE_M0_LIGHT_LOAD=false PIBASE_AUTH_EXPOSE_TOKENS=true docker compose up -d --force-recreate gateway

docker compose run --rm \
  -e PIBASE_ADMIN_DATABASE_URL="$admin_url" \
  -e PIBASE_MASTER_KEY="$master_key" \
  gateway project-create "supabase-js-acceptance" >"$project_file"
chmod 600 "$project_file"

project_id="$(node -e 'const p=JSON.parse(require("node:fs").readFileSync(process.argv[1],"utf8"));process.stdout.write(p.id)' "$project_file")"
anon_key="$(node -e 'const p=JSON.parse(require("node:fs").readFileSync(process.argv[1],"utf8"));process.stdout.write(p.anon_key)' "$project_file")"
service_key="$(node -e 'const p=JSON.parse(require("node:fs").readFileSync(process.argv[1],"utf8"));process.stdout.write(p.service_key)' "$project_file")"

sed "s/__PROJECT_ID__/$project_id/g" tests/fixtures/m1.sql >"$migration_file"
docker compose run --rm -T \
  -e PIBASE_ADMIN_DATABASE_URL="$admin_url" \
  gateway migration-apply "$project_id" 0001_acceptance <"$migration_file"

i=0
until curl -fsS http://127.0.0.1:18080/healthz >/dev/null; do
  i=$((i+1))
  [ "$i" -lt 30 ] || exit 1
  sleep 1
done

docker run --rm --network host -v "$PWD:/src" -w /src \
  -e PIBASE_TEST_URL=http://127.0.0.1:18080 \
  -e PIBASE_TEST_ANON_KEY="$anon_key" \
  -e PIBASE_TEST_SERVICE_KEY="$service_key" \
  -e PIBASE_AUTH_EXPOSE_TOKENS=true \
  node:22-bookworm npm run test:supabase
