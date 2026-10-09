#!/usr/bin/env bash
# Back up, fetch the latest PIBASE, rebuild and restart. Run as the account
# that runs PIBASE:  sudo -u pibase ~pibase/pibase/scripts/update.sh
set -euo pipefail
cd "$(dirname "$0")/.."
# Both the Pi and the droplet run the server layout: data under
# PIBASE_DATA_ROOT and automatic restarts (compose.pi.yaml). The droplet setup
# records that in .env; a Pi set up earlier passes -f flags instead, so use it
# here unless .env or the environment says otherwise.
if [ -z "${COMPOSE_FILE:-}" ] && ! grep -q '^COMPOSE_FILE=' .env 2>/dev/null; then
  export COMPOSE_FILE=compose.yaml:compose.pi.yaml
fi
echo "==> Backing up every project first"
./scripts/backup-all.sh
echo "==> Fetching the latest version"
if ! git pull --ff-only origin main; then
  cat >&2 <<'MSG'
Could not fetch the latest version. If the repository is private, give this
account a read-only deploy key (see "Updates from a private repository" in
docs/VPS.md). Nothing was changed; PIBASE is still running the old version.
MSG
  exit 1
fi
echo "==> Rebuilding and restarting (bootstrap upgrades the schema first)"
docker compose up --build -d
docker compose --profile tools build ops
docker image prune -f >/dev/null
for _ in $(seq 1 60); do
  if curl -fsS http://127.0.0.1:18080/healthz >/dev/null 2>&1; then echo "PIBASE is up."; exit 0; fi
  sleep 2
done
docker compose logs --tail 40 gateway >&2
echo "the gateway did not come back; see the log above" >&2
exit 1
