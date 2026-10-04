#!/bin/sh
set -eu

# Fresh Raspberry Pi setup drill. Run this script on a Pi 4 with at least 4 GB
# RAM, a fresh 64-bit Raspberry Pi OS image, and a USB3 SSD formatted as ext4
# mounted at $PIBASE_DATA_ROOT. It needs no sudo beyond what setup-pi.sh
# already requires.
#
# Output goes to pi-drill-<date>.txt for the owner to paste back.

cd "$(dirname "$0")/.."
repo_root="$PWD"
data_root="${PIBASE_DATA_ROOT:-/mnt/pibase-ssd/pibase}"
output_file="pi-drill-$(date -I).txt"

run() {
  printf '\n$ %s\n' "$*"
  "$@" 2>&1 || printf '(exit %d)\n' "$?"
}

{
echo "=== pibase Pi drill ==="
echo "Date: $(date -Is)"
echo "Commit: $(git rev-parse HEAD)"
echo ""

echo "--- 1. Baseline ---"
run cat /etc/os-release
run uname -a
run findmnt -n -o SOURCE,FSTYPE,TARGET --target "$data_root"
echo ""

echo "--- 2. Setup (first run) ---"
run env PIBASE_DATA_ROOT="$data_root" "$repo_root/scripts/setup-pi.sh"
echo ""
echo ">>> Log out and back in for Docker group membership, then continue."
echo ">>> If running non-interactively, the script continues assuming group"
echo ">>> membership is already active."
echo ""

echo "--- 3. Verify host state ---"
run docker version
run docker compose version
run systemctl is-enabled docker
run systemctl is-active docker
run id -nG
run stat -c '%U:%G %a %n' "$data_root" "$data_root/postgres" "$data_root/files" "$data_root/backups"
echo ""

echo "--- 4. Idempotent re-run ---"
run env PIBASE_DATA_ROOT="$data_root" "$repo_root/scripts/setup-pi.sh"
echo ""

echo "--- 5. Initialize and boot ---"
run env PIBASE_DATA_ROOT="$data_root" "$repo_root/scripts/init-env.sh"
run docker compose -f compose.yaml -f compose.pi.yaml up --build -d
sleep 10
run curl --fail --silent http://127.0.0.1:18080/healthz
run curl --fail --silent http://127.0.0.1:18080/metrics
run docker compose -f compose.yaml -f compose.pi.yaml ps
echo ""

echo "--- 6. Create a project and round-trip REST ---"
# Source .env for operator token (without printing it)
eval "$(grep '^PIBASE_OPERATOR_TOKEN=' .env)"
project_json="$(curl --fail --silent -X POST http://127.0.0.1:18080/admin/projects \
  -H "Authorization: Bearer $PIBASE_OPERATOR_TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"name":"drill-project"}')"
project_id="$(echo "$project_json" | grep -o '"id":"[^"]*"' | head -1 | cut -d'"' -f4)"
anon_key="$(echo "$project_json" | grep -o '"anon_key":"[^"]*"' | head -1 | cut -d'"' -f4)"
echo "Created project: id=$project_id"

# Insert a row
curl --fail --silent -X POST "http://127.0.0.1:18080/rest/v1/drill_probe" \
  -H "apikey: $anon_key" \
  -H "Authorization: Bearer $anon_key" \
  -H 'Content-Type: application/json' \
  -H 'Prefer: return=representation' \
  -d '{"note":"hello from drill"}' \
  --output /dev/null --write-out 'REST INSERT: HTTP %{http_code}\n' || echo "(table may not exist yet; expected if no public tables)"

echo ""

echo "--- 7. Backup ---"
eval "$(grep '^PIBASE_BACKUP_KEY=' .env)"
run docker compose -f compose.yaml -f compose.pi.yaml --profile tools run --rm \
  ops backup "$project_id" /data/backups
echo ""

echo "--- 8. Reboot instructions ---"
echo ">>> Reboot the Pi now:  sudo reboot"
echo ">>> After reboot, run this script again with PIBASE_DRILL_POSTREBOOT=1"
echo ">>> to execute the post-reboot checks."
echo ""

if [ "${PIBASE_DRILL_POSTREBOOT:-}" = "1" ]; then
  echo "--- 9. Post-reboot checks ---"
  run docker compose -f compose.yaml -f compose.pi.yaml ps
  run curl --fail --silent http://127.0.0.1:18080/healthz
  run curl --fail --silent http://127.0.0.1:18080/metrics
  run findmnt -n -o SOURCE,FSTYPE,TARGET --target "$data_root"
  run stat -c '%U:%G %a %n' "$data_root/postgres" "$data_root/files" "$data_root/backups"
  echo ""
  echo "--- Post-reboot checks complete ---"
fi

echo ""
echo "=== Pi drill complete ==="
} 2>&1 | tee "$output_file"

echo ""
echo "Drill output saved to $output_file"
echo "Paste the contents back for review."
