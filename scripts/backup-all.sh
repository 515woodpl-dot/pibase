#!/usr/bin/env bash
# Encrypted backup of every project into the backups folder, keeping
# PIBASE_BACKUP_KEEP_DAYS days (default 14). Run as the account that runs
# PIBASE, from its folder; the droplet setup schedules it daily.
#
# To also copy backups off the server, install and configure rclone (for
# example to a DigitalOcean Spaces bucket) and set PIBASE_BACKUP_REMOTE in
# .env, e.g. PIBASE_BACKUP_REMOTE=spaces:my-bucket/pibase
set -euo pipefail
cd "$(dirname "$0")/.."
env_value() { sed -n "s/^$1=//p" .env | tail -1; }
keep="$(env_value PIBASE_BACKUP_KEEP_DAYS)"; keep="${keep:-14}"
remote="$(env_value PIBASE_BACKUP_REMOTE)"
root="$(env_value PIBASE_DATA_ROOT)"; root="${root:-/srv/pibase}"
ops() { docker compose --profile tools run --rm --no-deps -T ops "$@"; }
# Keep only the JSON (anything Compose prints before it is ignored).
ids="$(ops project-list | sed -n '/^\[/,$p' | jq -r '.[].id')"
failed=0
for id in $ids; do
  if ops backup "$id" /data/backups >/dev/null; then
    echo "backed up $id"
  else
    echo "backup of $id failed" >&2
    failed=1
  fi
done
find "$root/backups" -maxdepth 1 -name 'pibase-*.pib' -mtime +"$keep" -delete
if [ -n "$remote" ]; then
  command -v rclone >/dev/null || { echo "PIBASE_BACKUP_REMOTE is set but rclone is not installed" >&2; exit 1; }
  rclone copy "$root/backups" "$remote" --include 'pibase-*.pib'
  echo "copied to $remote"
fi
exit "$failed"
