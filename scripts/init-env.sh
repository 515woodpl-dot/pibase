#!/bin/sh
set -eu

cd "$(dirname "$0")/.."
if [ -e .env ]; then
  exit 0
fi

umask 077
postgres_password="$(od -An -N32 -tx1 /dev/urandom | tr -d ' \n')"
authenticator_password="$(od -An -N32 -tx1 /dev/urandom | tr -d ' \n')"
master_key="$(head -c 32 /dev/urandom | base64 | tr -d '\n')"
sed \
  -e "s|^POSTGRES_PASSWORD=.*|POSTGRES_PASSWORD=$postgres_password|" \
  -e "s|^PIBASE_AUTHENTICATOR_PASSWORD=.*|PIBASE_AUTHENTICATOR_PASSWORD=$authenticator_password|" \
  -e "s|^PIBASE_MASTER_KEY=.*|PIBASE_MASTER_KEY=$master_key|" \
  .env.example > .env
chmod 600 .env
