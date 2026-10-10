#!/usr/bin/env bash
# Create a dashboard account (or give an existing one a new password) and
# type its password at the prompt. Run as the account that runs PIBASE, from
# the repository folder, while PIBASE is running:
#
#   ./scripts/create-account.sh                    # asks, suggesting omnia@local.com
#   ./scripts/create-account.sh you@example.com    # this email
#   ./scripts/create-account.sh --reset you@example.com   # new password for an existing account
set -euo pipefail
default_email="${PIBASE_MAIN_ACCOUNT_EMAIL:-omnia@local.com}"
reset=0
if [ "${1:-}" = --reset ]; then reset=1; shift; fi
cd "$(dirname "$0")/.."
[ "$(id -u)" -ne 0 ] || { echo "run as the pibase operator account, not root" >&2; exit 1; }
[ -t 0 ] || { echo "run this in a terminal: it asks for a password" >&2; exit 1; }
if [ -z "${COMPOSE_FILE:-}" ] && ! grep -q '^COMPOSE_FILE=' .env 2>/dev/null; then
  export COMPOSE_FILE=compose.yaml:compose.pi.yaml
fi

email="${1:-}"
if [ -z "$email" ]; then
  read -r -p "Dashboard account email [$default_email]: " email
  email="${email:-$default_email}"
fi
case "$email" in *@*.*) ;; *) echo "that doesn't look like an email address: $email" >&2; exit 1 ;; esac
organization=""
if [ "$reset" -eq 0 ]; then
  read -r -p "Organization name [Omnia]: " organization
  organization="${organization:-Omnia}"
fi
while :; do
  read -r -s -p "Password (at least 8 characters): " password; echo
  read -r -s -p "Enter it again: " again; echo
  if [ "${#password}" -lt 8 ]; then echo "Too short; try again."; continue; fi
  if [ "$password" != "$again" ]; then echo "The two passwords differ; try again."; continue; fi
  break
done
# The ops image is only built on request (profile "tools"), so bring it up to
# date first; after an update it would otherwise still be the old version.
echo "Preparing the tools image (quick unless PIBASE was just updated)..."
docker compose --profile tools build -q ops >/dev/null
if [ "$reset" -eq 1 ]; then
  printf '%s' "$password" | docker compose --profile tools run --rm --no-deps -T ops account-password-reset "$email"
else
  printf '%s' "$password" | docker compose --profile tools run --rm --no-deps -T ops account-create "$email" "$organization"
fi
echo "Log in on the dashboard with $email."
