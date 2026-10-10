#!/usr/bin/env bash
# Import a project exported from another PIBASE server. Run as the account
# that runs PIBASE, from the repository folder:
#
#   ./scripts/import-project.sh <file.pibx> [owner-email]
#
# Without an owner email the project belongs to no dashboard account and only
# the operator view shows it.
set -euo pipefail
[ $# -ge 1 ] && [ $# -le 2 ] || { echo "usage: $0 <file.pibx> [owner-email]" >&2; exit 2; }
file="$1"; owner="${2:-}"
[ -f "$file" ] || { echo "no such file: $file" >&2; exit 1; }
file="$(cd "$(dirname "$file")" && pwd)/$(basename "$file")"
cd "$(dirname "$0")/.."
[ "$(id -u)" -ne 0 ] || { echo "run as the pibase operator account, not root" >&2; exit 1; }
[ -t 0 ] || { echo "run this in a terminal: it asks for the passphrase" >&2; exit 1; }
if [ -z "${COMPOSE_FILE:-}" ] && ! grep -q '^COMPOSE_FILE=' .env 2>/dev/null; then
  export COMPOSE_FILE=compose.yaml:compose.pi.yaml
fi
root="$(sed -n 's/^PIBASE_DATA_ROOT=//p' .env | tail -1)"; root="${root:-/srv/pibase}"
# The import runs in a container that sees the backups folder as /data/backups.
name="$(basename "$file")"
copied=0
if [ "$file" != "$root/backups/$name" ]; then
  cp "$file" "$root/backups/.import-$name"
  name=".import-$name"
  copied=1
fi
cleanup() { if [ "$copied" -eq 1 ]; then rm -f "$root/backups/$name"; fi; }
trap cleanup EXIT
read -r -s -p "Passphrase of the file: " pass; echo
args=(project-import "/data/backups/$name")
[ -z "$owner" ] || args+=("$owner")
printf '%s' "$pass" | docker compose --profile tools run --rm --no-deps -T ops "${args[@]}"
echo "Point the app (or its pibase.client.com name) at this server; its keys stay the same."
