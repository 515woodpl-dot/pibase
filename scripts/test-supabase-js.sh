#!/bin/sh
set -eu

cd "$(dirname "$0")/.."
./scripts/init-env.sh

postgres_password="${POSTGRES_PASSWORD:-$(sed -n 's/^POSTGRES_PASSWORD=//p' .env)}"
authenticator_password="${PIBASE_AUTHENTICATOR_PASSWORD:-$(sed -n 's/^PIBASE_AUTHENTICATOR_PASSWORD=//p' .env)}"
master_key="${PIBASE_MASTER_KEY:-$(sed -n 's/^PIBASE_MASTER_KEY=//p' .env)}"
test_prefix="pibase-acceptance-$$"
test_network="$test_prefix"
test_postgres="$test_prefix-postgres"
test_gateway="$test_prefix-gateway"
admin_url="postgres://postgres:$postgres_password@$test_postgres:5432/postgres"
authenticator_url="postgres://authenticator:$authenticator_password@$test_postgres:5432/_control"
project_file="$(mktemp)"
migration_file="$(mktemp)"

cleanup() {
  docker rm --force "$test_gateway" "$test_postgres" >/dev/null 2>&1 || true
  docker network rm "$test_network" >/dev/null 2>&1 || true
  rm -f "$project_file" "$migration_file"
}
trap cleanup EXIT INT TERM

storage_image="${PIBASE_IMAGE:-pibase:gate}"
docker build --tag "$storage_image" .
test_postgres_image="${PIBASE_TEST_POSTGRES_IMAGE:-pibase-postgres:test}"
if ! docker image inspect "$test_postgres_image" >/dev/null 2>&1; then
  docker build --file Dockerfile.postgres --tag "$test_postgres_image" .
fi
docker run --rm -v "$PWD:/src" -w /src node:22-bookworm npm ci --ignore-scripts --no-audit --no-fund
docker network create "$test_network" >/dev/null
echo "acceptance: starting disposable PostgreSQL"
docker run --rm --detach --name "$test_postgres" --network "$test_network" \
  --env POSTGRES_PASSWORD="$postgres_password" "$test_postgres_image" >/dev/null
i=0
until docker exec "$test_postgres" pg_isready -U postgres >/dev/null 2>&1; do
  i=$((i+1))
  [ "$i" -lt 30 ] || { docker logs "$test_postgres"; exit 1; }
  sleep 1
done
echo "acceptance: bootstrapping control database"
docker run --rm --network "$test_network" \
  -e PIBASE_ADMIN_DATABASE_URL="$admin_url" \
  -e PIBASE_AUTHENTICATOR_PASSWORD="$authenticator_password" \
  "$storage_image" bootstrap
echo "acceptance: starting disposable gateway"
docker run --rm --detach --name "$test_gateway" --network "$test_network" \
  --publish 127.0.0.1::8080 \
  -e PIBASE_DATABASE_URL="$authenticator_url" \
  -e PIBASE_ADMIN_DATABASE_URL="$authenticator_url" \
  -e PIBASE_MASTER_KEY="$master_key" \
  -e PIBASE_LISTEN_ADDR=:8080 \
  -e PIBASE_ENV=test \
  -e PIBASE_AUTH_EXPOSE_TOKENS=true \
  "$storage_image" serve >/dev/null

echo "acceptance: creating isolated project"
docker run --rm --network "$test_network" \
  -e PIBASE_ADMIN_DATABASE_URL="$admin_url" \
  -e PIBASE_MASTER_KEY="$master_key" \
  "$storage_image" project-create "supabase-js-acceptance" >"$project_file"
chmod 600 "$project_file"

project_id="$(node -e 'const p=JSON.parse(require("node:fs").readFileSync(process.argv[1],"utf8"));process.stdout.write(p.id)' "$project_file")"
anon_key="$(node -e 'const p=JSON.parse(require("node:fs").readFileSync(process.argv[1],"utf8"));process.stdout.write(p.anon_key)' "$project_file")"
service_key="$(node -e 'const p=JSON.parse(require("node:fs").readFileSync(process.argv[1],"utf8"));process.stdout.write(p.service_key)' "$project_file")"

sed "s/__PROJECT_ID__/$project_id/g" tests/fixtures/m1.sql >"$migration_file"
echo "acceptance: applying fixture migration"
docker run --rm --interactive --network "$test_network" \
  -e PIBASE_DATABASE_URL="$authenticator_url" \
  "$storage_image" migration-apply "$project_id" 0001_acceptance <"$migration_file"

gateway_endpoint="$(docker port "$test_gateway" 8080/tcp)"
gateway_port="${gateway_endpoint##*:}"
i=0
until curl -fsS "http://127.0.0.1:$gateway_port/healthz" >/dev/null; do
  i=$((i+1))
  [ "$i" -lt 30 ] || exit 1
  sleep 1
done

echo "acceptance: running supabase-js"
docker run --rm --network host -v "$PWD:/src" -w /src \
  -e PIBASE_TEST_URL="http://127.0.0.1:$gateway_port" \
  -e PIBASE_TEST_ANON_KEY="$anon_key" \
  -e PIBASE_TEST_SERVICE_KEY="$service_key" \
  -e PIBASE_AUTH_EXPOSE_TOKENS=true \
  -e PIBASE_ENV=test \
  node:22-bookworm npm run test:supabase
docker run --rm --network host -v "$PWD:/src" -w /src \
  -e PIBASE_TEST_URL="http://127.0.0.1:$gateway_port" \
  -e PIBASE_TEST_ANON_KEY="$anon_key" \
  node:22-bookworm node tests/realtime-js.mjs
