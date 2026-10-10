#!/usr/bin/env bash
# Export a project to one passphrase-protected file for another PIBASE server.
# Run as the account that runs PIBASE, from the repository folder:
#
#   ./scripts/export-project.sh <project-id>
#
# The file lands in the backups folder; copy it to the other server, e.g.
#   scp /srv/pibase/backups/pibase-<id>-<date>.pibx pibase@NEW_SERVER:
set -euo pipefail
[ $# -eq 1 ] || { echo "usage: $0 <project-id>   (the ID is on the project's overview page)" >&2; exit 2; }
id="$1"
printf '%s' "$id" | grep -Eq '^[0-9a-f]{8}$' || { echo "a project ID is 8 characters like 1a2b3c4d" >&2; exit 2; }
cd "$(dirname "$0")/.."
[ "$(id -u)" -ne 0 ] || { echo "run as the pibase operator account, not root" >&2; exit 1; }
[ -t 0 ] || { echo "run this in a terminal: it asks for a passphrase" >&2; exit 1; }
if [ -z "${COMPOSE_FILE:-}" ] && ! grep -q '^COMPOSE_FILE=' .env 2>/dev/null; then
  export COMPOSE_FILE=compose.yaml:compose.pi.yaml
fi
root="$(sed -n 's/^PIBASE_DATA_ROOT=//p' .env | tail -1)"; root="${root:-/srv/pibase}"
while :; do
  read -r -s -p "Passphrase for the file (at least 12 characters): " pass; echo
  read -r -s -p "Enter it again: " again; echo
  if [ "${#pass}" -lt 12 ]; then echo "Too short; try again."; continue; fi
  if [ "$pass" != "$again" ]; then echo "The two differ; try again."; continue; fi
  break
done
name="pibase-$id-$(date -u +%Y%m%d-%H%M).pibx"
# The ops image is only built on request (profile "tools"), so bring it up to
# date first; after an update it would otherwise still be the old version.
echo "Preparing the tools image (quick unless PIBASE was just updated)..."
docker compose --profile tools build -q ops >/dev/null
printf '%s' "$pass" | docker compose --profile tools run --rm --no-deps -T ops project-export "$id" "/data/backups/$name" >/dev/null
echo "Exported to $root/backups/$name"
echo "Copy it to the other server and run there: ./scripts/import-project.sh <file>"
