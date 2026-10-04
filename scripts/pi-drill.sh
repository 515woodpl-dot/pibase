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
state_file=".pibase-pi-drill-state"

run() {
  printf '\n$ %s\n' "$*"
  "$@" 2>&1 || printf '(exit %d)\n' "$?"
}

json_field() {
  sed -n 's/.*"'"$1"'"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' | head -1
}

state_value() {
  sed -n "s/^$1=//p" "$state_file" | head -1
}

if [ "${PIBASE_DRILL_POSTREBOOT:-}" = "1" ]; then
  output_file="pi-drill-$(date -I)-postreboot.txt"
  project_id="$(state_value project_id)"
  anon_key="$(state_value anon_key)"
  [ -n "$project_id" ] && [ -n "$anon_key" ] || { echo "missing $state_file; run the pre-reboot drill first" >&2; exit 1; }
  {
  echo "=== pibase Pi drill post-reboot checks ==="
  echo "Date: $(date -Is)"
  run docker version
  run docker compose -f compose.yaml -f compose.pi.yaml ps
  run curl --fail --silent http://127.0.0.1:18080/healthz
  run curl --fail --silent http://127.0.0.1:18080/metrics
  run findmnt -n -o SOURCE,FSTYPE,TARGET --target "$data_root"
  run stat -c '%U:%G %a %n' "$data_root/postgres" "$data_root/files" "$data_root/backups"
  row="$(curl --fail --silent "http://127.0.0.1:18080/rest/v1/drill_probe?select=note" -H "apikey: $anon_key")"
  [ "$row" = '[{"note":"hello from drill"}]' ] || { echo "FAIL: pre-reboot row missing: $row"; exit 1; }
  echo "OK: project $project_id and pre-reboot row persist"
  echo "=== Post-reboot checks complete ==="
  } 2>&1 | tee "$output_file"
  echo "Drill output saved to $output_file"
  exit 0
fi

if [ -f "$data_root/postgres/PG_VERSION" ]; then
  echo "refusing to run against an existing PostgreSQL data directory: $data_root/postgres" >&2
  exit 1
fi

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
if ! docker version >/dev/null 2>&1; then
  echo "Docker group membership is not active; log out and back in, then rerun this drill."
  exit 1
fi
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
# Read secrets without evaluating .env content.
operator_token="$(sed -n 's/^PIBASE_OPERATOR_TOKEN=//p' .env)"
project_json="$(curl --fail --silent -X POST http://127.0.0.1:18080/admin/projects \
  -H "Authorization: Bearer $operator_token" \
  -H 'Content-Type: application/json' \
  -d '{"name":"drill-project"}')"
project_id="$(echo "$project_json" | json_field id)"
anon_key="$(echo "$project_json" | json_field anon_key)"
echo "Created project: id=$project_id"

printf 'CREATE TABLE public.drill_probe(note text NOT NULL);' | docker compose -f compose.yaml -f compose.pi.yaml --profile tools run --rm -T \
  ops migration-apply "$project_id" 0001_pi_drill_probe
inserted="$(curl --fail --silent -X POST "http://127.0.0.1:18080/rest/v1/drill_probe" \
  -H "apikey: $anon_key" \
  -H "Authorization: Bearer $anon_key" \
  -H 'Content-Type: application/json' \
  -H 'Prefer: return=representation' \
  -d '{"note":"hello from drill"}')"
[ "$inserted" = '[{"note":"hello from drill"}]' ] || { echo "FAIL: REST insert: $inserted"; exit 1; }
read_back="$(curl --fail --silent "http://127.0.0.1:18080/rest/v1/drill_probe?select=note" -H "apikey: $anon_key")"
[ "$read_back" = '[{"note":"hello from drill"}]' ] || { echo "FAIL: REST read: $read_back"; exit 1; }
printf 'project_id=%s\nanon_key=%s\n' "$project_id" "$anon_key" >"$state_file"
chmod 600 "$state_file"

echo ""

echo "--- 7. Backup ---"
run docker compose -f compose.yaml -f compose.pi.yaml --profile tools run --rm \
  ops backup "$project_id" /data/backups
echo ""

echo "--- 8. Reboot instructions ---"
echo ">>> Reboot the Pi now:  sudo reboot"
echo ">>> After reboot, run this script again with PIBASE_DRILL_POSTREBOOT=1"
echo ">>> to execute the post-reboot checks."
echo ""

echo ""
echo "=== Pi drill complete ==="
} 2>&1 | tee -a "$output_file"

echo ""
echo "Drill output saved to $output_file"
echo "Paste the contents back for review."
