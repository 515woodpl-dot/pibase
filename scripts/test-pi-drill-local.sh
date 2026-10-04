#!/bin/sh
set -eu

# Disposable local coverage for Pi-drill steps 5--7: boot, project creation,
# migration-backed REST round trip, and backup. It never uses the Pi data root.
cd "$(dirname "$0")/.."

project_name="pibase-pi-drill-test-$$"
env_file="$(mktemp)"
override_file="$(mktemp)"
cleanup() {
  docker compose --project-name "$project_name" --env-file "$env_file" -f compose.yaml -f "$override_file" down --volumes --remove-orphans >/dev/null 2>&1 || true
  rm -f "$env_file" "$override_file"
}
trap cleanup EXIT INT TERM

postgres_password="$(od -An -N16 -tx1 /dev/urandom | tr -d ' \n')"
auth_password="pi-drill-local-auth"
master_key="$(head -c 32 /dev/urandom | base64 | tr -d '\n')"
backup_key="$(head -c 32 /dev/urandom | base64 | tr -d '\n')"
cat >"$env_file" <<EOF
POSTGRES_PASSWORD=$postgres_password
PIBASE_AUTHENTICATOR_PASSWORD=$auth_password
PIBASE_MASTER_KEY=$master_key
PIBASE_BACKUP_KEY=$backup_key
EOF
cat >"$override_file" <<'EOF'
services:
  postgres:
    ports: !override ["127.0.0.1:28082:5432"]
  gateway:
    ports: !override ["127.0.0.1:28083:8080"]
EOF

compose() {
  docker compose --project-name "$project_name" --env-file "$env_file" -f compose.yaml -f "$override_file" "$@"
}

compose up --build -d
gateway_port="$(compose port gateway 8080 | sed 's/.*://')"
i=0
until curl --fail --silent "http://127.0.0.1:${gateway_port}/healthz" >/dev/null; do
  i=$((i + 1))
  [ "$i" -lt 30 ] || { compose logs; exit 1; }
  sleep 1
done

project_json="$(compose --profile tools run --rm -T ops project-create drill-project)"
json_field() { sed -n 's/.*"'"$1"'"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' | head -1; }
project_id="$(printf '%s\n' "$project_json" | json_field id)"
service_key="$(printf '%s\n' "$project_json" | json_field service_key)"
[ -n "$project_id" ] && [ -n "$service_key" ]
printf 'CREATE TABLE public.drill_probe(note text NOT NULL); GRANT SELECT, INSERT ON public.drill_probe TO p_%s_service_role;' "$project_id" | compose --profile tools run --rm -T ops migration-apply "$project_id" 0001_pi_drill_probe
inserted="$(curl --fail --silent -X POST "http://127.0.0.1:${gateway_port}/rest/v1/drill_probe" -H "apikey: $service_key" -H 'Content-Type: application/json' -H 'Prefer: return=representation' -d '{"note":"hello from drill"}')"
[ "$(printf '%s' "$inserted" | tr -d '[:space:]')" = '[{"note":"hellofromdrill"}]' ]
read_back="$(curl --fail --silent "http://127.0.0.1:${gateway_port}/rest/v1/drill_probe?select=note" -H "apikey: $service_key")"
[ "$(printf '%s' "$read_back" | tr -d '[:space:]')" = '[{"note":"hellofromdrill"}]' ]
compose --profile tools run --rm -T ops backup "$project_id" /data/backups
echo "Pi drill steps 5-7 local self-test passed"
