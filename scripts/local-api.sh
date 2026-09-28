#!/bin/sh
# Local Docker testing only: make one project's API reachable at http://127.0.0.1:<port>.
# Project containers only listen on the private "datbase" network (in production Caddy serves
# https://<slug>.<DOMAIN>). This starts a tiny forwarder bound to 127.0.0.1, so nothing is exposed
# to your LAN. Note: unlike the Caddy route, the forwarder does not hide the engine's /_/ admin page.
#
#   scripts/local-api.sh <project-slug> [port]   # start (port defaults to 8090)
#   scripts/local-api.sh stop [port]             # stop
#
# The slug is the first part of the project's API URL in the dashboard (Overview → Connect your app).
set -eu
[ $# -ge 1 ] || { sed -n 2,10p "$0"; exit 1; }

if [ "$1" = stop ]; then
  if docker inspect "datbase-local-api-${2:-8090}" >/dev/null 2>&1; then
    docker rm -f "datbase-local-api-${2:-8090}" >/dev/null && echo "stopped"
  else
    echo "not running"
  fi
  exit 0
fi

SLUG=$1
PORT=${2:-8090}
NAME="datbase-local-api-$PORT"
docker inspect "dbp-$SLUG" >/dev/null 2>&1 || {
  echo "No project container dbp-$SLUG. Check the slug in the dashboard's API URL."
  exit 1
}
docker rm -f "$NAME" >/dev/null 2>&1 || true
# Reuses the stack's own Caddy image (already pulled by docker compose), so nothing new is downloaded.
docker run -d --name "$NAME" --network "${DOCKER_NETWORK:-datbase}" -p "127.0.0.1:$PORT:8090" \
  --entrypoint caddy "${CADDY_IMAGE:-lucaslorentz/caddy-docker-proxy:ci-alpine}" \
  reverse-proxy --from :8090 --to "dbp-$SLUG:8090" >/dev/null

i=0
until curl -fsS "http://127.0.0.1:$PORT/api/health" >/dev/null 2>&1; do
  i=$((i + 1))
  [ "$i" -lt 20 ] || { echo "Forwarder started but the API does not answer. Is the project active?"; exit 1; }
  sleep 0.5
done
echo "API of project '$SLUG' → http://127.0.0.1:$PORT   (stop: scripts/local-api.sh stop $PORT)"
