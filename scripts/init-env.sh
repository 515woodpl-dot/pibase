#!/bin/sh
set -eu

cd "$(dirname "$0")/.."
if [ "$(id -u)" -eq 0 ]; then echo "run init-env.sh as the non-root pibase operator" >&2; exit 1; fi

# On a Pi or droplet (setup-pi.sh or setup-droplet.sh created the data
# folders), record the server layout so plain `docker compose ...` uses the
# data under PIBASE_DATA_ROOT instead of the development volumes.
record_server_layout() {
  root="$(sed -n 's/^PIBASE_DATA_ROOT=//p' .env | tail -1)"
  if [ -d "${root:-/srv/pibase}/files" ] && ! grep -q '^COMPOSE_FILE=' .env; then
    printf 'COMPOSE_FILE=compose.yaml:compose.pi.yaml\n' >> .env
  fi
}
if [ -e .env ]; then
  umask 077
  if ! grep -q '^PIBASE_BACKUP_KEY=' .env; then
    printf 'PIBASE_BACKUP_KEY=%s\n' "$(head -c 32 /dev/urandom | base64 | tr -d '\n')" >> .env
  fi
  if ! grep -q '^PIBASE_OPERATOR_TOKEN=' .env; then
    printf 'PIBASE_OPERATOR_TOKEN=%s\n' "$(head -c 32 /dev/urandom | base64 | tr -d '\n')" >> .env
  fi
  if ! grep -q '^PIBASE_UID=' .env; then printf 'PIBASE_UID=%s\nPIBASE_GID=%s\n' "$(id -u)" "$(id -g)" >> .env; fi
  record_server_layout
  exit 0
fi

umask 077
data_root="${PIBASE_DATA_ROOT:-/srv/pibase}"
case "$data_root" in
  *'|'*) echo "PIBASE_DATA_ROOT must not contain |" >&2; exit 1 ;;
  *'/../'*|*'/..') echo "PIBASE_DATA_ROOT must not contain .." >&2; exit 1 ;;
  /media/*|/mnt/*|/srv/*) ;;
  *) echo "PIBASE_DATA_ROOT must be under /media, /mnt, or /srv" >&2; exit 1 ;;
esac
postgres_password="$(od -An -N32 -tx1 /dev/urandom | tr -d ' \n')"
authenticator_password="$(od -An -N32 -tx1 /dev/urandom | tr -d ' \n')"
master_key="$(head -c 32 /dev/urandom | base64 | tr -d '\n')"
backup_key="$(head -c 32 /dev/urandom | base64 | tr -d '\n')"
operator_token="$(head -c 32 /dev/urandom | base64 | tr -d '\n')"
operator_uid="$(id -u)"
operator_gid="$(id -g)"
sed \
  -e "s|^POSTGRES_PASSWORD=.*|POSTGRES_PASSWORD=$postgres_password|" \
  -e "s|^PIBASE_AUTHENTICATOR_PASSWORD=.*|PIBASE_AUTHENTICATOR_PASSWORD=$authenticator_password|" \
  -e "s|^PIBASE_MASTER_KEY=.*|PIBASE_MASTER_KEY=$master_key|" \
  -e "s|^PIBASE_BACKUP_KEY=.*|PIBASE_BACKUP_KEY=$backup_key|" \
  -e "s|^PIBASE_OPERATOR_TOKEN=.*|PIBASE_OPERATOR_TOKEN=$operator_token|" \
  -e "s|^PIBASE_DATA_ROOT=.*|PIBASE_DATA_ROOT=$data_root|" \
  -e "s|^PIBASE_UID=.*|PIBASE_UID=$operator_uid|" \
  -e "s|^PIBASE_GID=.*|PIBASE_GID=$operator_gid|" \
  .env.example > .env
chmod 600 .env
record_server_layout
