#!/bin/sh
set -eu

cd "$(dirname "$0")/.."
if [ -e .env ]; then
  umask 077
  if ! grep -q '^PIBASE_BACKUP_KEY=' .env; then
    printf 'PIBASE_BACKUP_KEY=%s\n' "$(head -c 32 /dev/urandom | base64 | tr -d '\n')" >> .env
  fi
  if ! grep -q '^PIBASE_OPERATOR_TOKEN=' .env; then
    printf 'PIBASE_OPERATOR_TOKEN=%s\n' "$(head -c 32 /dev/urandom | base64 | tr -d '\n')" >> .env
  fi
  exit 0
fi

umask 077
postgres_password="$(od -An -N32 -tx1 /dev/urandom | tr -d ' \n')"
authenticator_password="$(od -An -N32 -tx1 /dev/urandom | tr -d ' \n')"
master_key="$(head -c 32 /dev/urandom | base64 | tr -d '\n')"
backup_key="$(head -c 32 /dev/urandom | base64 | tr -d '\n')"
operator_token="$(head -c 32 /dev/urandom | base64 | tr -d '\n')"
sed \
  -e "s|^POSTGRES_PASSWORD=.*|POSTGRES_PASSWORD=$postgres_password|" \
  -e "s|^PIBASE_AUTHENTICATOR_PASSWORD=.*|PIBASE_AUTHENTICATOR_PASSWORD=$authenticator_password|" \
  -e "s|^PIBASE_MASTER_KEY=.*|PIBASE_MASTER_KEY=$master_key|" \
  -e "s|^PIBASE_BACKUP_KEY=.*|PIBASE_BACKUP_KEY=$backup_key|" \
  -e "s|^PIBASE_OPERATOR_TOKEN=.*|PIBASE_OPERATOR_TOKEN=$operator_token|" \
  .env.example > .env
chmod 600 .env
