#!/bin/sh
set -eu

if [ "$#" -ne 1 ]; then
  echo "usage: $0 <project-name>" >&2
  exit 2
fi
cd "$(dirname "$0")/.."
./scripts/init-env.sh
# A Pi or droplet runs the server layout (compose.pi.yaml). Without it,
# Compose would swap the running database for the development volume.
if [ -z "${COMPOSE_FILE:-}" ] && ! grep -q '^COMPOSE_FILE=' .env; then
  root="$(sed -n 's/^PIBASE_DATA_ROOT=//p' .env | tail -1)"
  if [ -d "${root:-/srv/pibase}/postgres" ]; then
    export COMPOSE_FILE=compose.yaml:compose.pi.yaml
  fi
fi
# The ops tool already has the admin database URL and master key.
docker compose --profile tools run --rm -T ops project-create "$1"
