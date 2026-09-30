#!/bin/sh
set -eu

if [ "$#" -ne 1 ]; then
  echo "usage: $0 <project-name>" >&2
  exit 2
fi
cd "$(dirname "$0")/.."
./scripts/init-env.sh
docker compose run --rm \
  -e PIBASE_ADMIN_DATABASE_URL="postgres://postgres:${POSTGRES_PASSWORD:-$(sed -n 's/^POSTGRES_PASSWORD=//p' .env)}@postgres:5432/postgres" \
  -e PIBASE_MASTER_KEY="${PIBASE_MASTER_KEY:-$(sed -n 's/^PIBASE_MASTER_KEY=//p' .env)}" \
  gateway project-create "$1"

