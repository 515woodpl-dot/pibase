#!/usr/bin/env bash
# Put the whole of PIBASE on the internet from a Raspberry Pi (or any machine
# behind a home router), under your own domain names, like the droplet setup:
#
#   ./scripts/pi-public.sh --api api.example.com --dashboard app.example.com
#   ./scripts/pi-public.sh --off
#
# It uses a Cloudflare Tunnel: the Pi connects out to Cloudflare, so no router
# ports are opened, it works when your provider shares one IP between homes,
# and Cloudflare serves HTTPS for both names. The API serves websites; the
# dashboard is where people sign up, pay (Stripe) and install the phone app.
#
# Before running it, once, in Cloudflare (your domain must use Cloudflare DNS):
#   Zero Trust > Networks > Tunnels > Create a tunnel > Cloudflared, name it,
#   and copy its token (the long string after --token). Then, on the tunnel's
#   Public hostnames tab, add:
#     api.example.com  ->  HTTP  gateway:8080
#     app.example.com  ->  HTTP  admin:18081
#
# The token is asked for (it is not shown while you type) or read from
# PIBASE_TUNNEL_TOKEN, and kept in .env. Run as the pibase operator, not root,
# from the repository folder, after PIBASE has been started once.
set -euo pipefail

api="" dashboard="" off=0
while [ $# -gt 0 ]; do
  case "$1" in
    --api) api="${2:-}"; shift 2 ;;
    --dashboard) dashboard="${2:-}"; shift 2 ;;
    --off) off=1; shift ;;
    -h|--help) sed -n '2,24p' "$0"; exit 0 ;;
    *) echo "unknown option: $1 (see --help)" >&2; exit 2 ;;
  esac
done
die() { printf 'error: %s\n' "$*" >&2; exit 1; }
say() { printf '\n==> %s\n' "$*"; }
[ "$(id -u)" -ne 0 ] || die "run as the pibase operator account, not root"
cd "$(dirname "$0")/.."
[ -f .env ] || die "no .env here; run scripts/init-env.sh and start PIBASE first"
# Same server layout as backup-all.sh, update.sh and pi-funnel.sh.
if [ -z "${COMPOSE_FILE:-}" ] && ! grep -q '^COMPOSE_FILE=' .env; then
  export COMPOSE_FILE=compose.yaml:compose.pi.yaml
fi

set_env() { # set_env KEY VALUE: replace or add one line of .env, keeping the rest
  local tmp
  tmp="$(mktemp .env.XXXXXX)"
  grep -v "^$1=" .env > "$tmp" || true
  printf '%s=%s\n' "$1" "$2" >> "$tmp"
  chmod 600 "$tmp"
  mv "$tmp" .env
}
get_env() { sed -n "s/^$1=//p" .env | tail -1; }
# COMPOSE_PROFILES may hold other profiles; add or remove only "tunnel".
profiles_with() { printf '%s' "$(get_env COMPOSE_PROFILES)" | tr ',' '\n' | grep -v '^tunnel$' | grep -v '^$' | { cat; [ "$1" = add ] && echo tunnel; true; } | paste -sd, -; }

if [ "$off" -eq 1 ]; then
  say "Taking PIBASE off the internet"
  docker compose --profile tunnel stop tunnel >/dev/null 2>&1 || true
  docker compose --profile tunnel rm -f tunnel >/dev/null 2>&1 || true
  set_env COMPOSE_PROFILES "$(profiles_with remove)"
  set_env PIBASE_TUNNEL_TOKEN ""
  set_env PIBASE_PUBLIC_URL ""
  set_env PIBASE_DASHBOARD_URL ""
  # The dashboard is private again (http on this machine or Tailscale).
  set_env PIBASE_ADMIN_ENV development
  docker compose up -d >/dev/null
  echo "The tunnel is off. The API and dashboard are reachable on this network only."
  echo "Delete the tunnel in Cloudflare (Zero Trust > Networks > Tunnels) if you no longer need it."
  exit 0
fi

name_ok() { printf '%s' "$1" | grep -Eq '^[A-Za-z0-9]([A-Za-z0-9-]*[A-Za-z0-9])?(\.[A-Za-z0-9]([A-Za-z0-9-]*[A-Za-z0-9])?)+$'; }
[ -n "$api" ] && [ -n "$dashboard" ] || die "give both names: --api api.example.com --dashboard app.example.com"
name_ok "$api" || die "--api must be a host name such as api.example.com"
name_ok "$dashboard" || die "--dashboard must be a host name such as app.example.com"
[ "$api" != "$dashboard" ] || die "the API and the dashboard need different names"
api="$(printf '%s' "$api" | tr 'A-Z' 'a-z')"; dashboard="$(printf '%s' "$dashboard" | tr 'A-Z' 'a-z')"

# ------------------------------------------------------------- tunnel token
token="${PIBASE_TUNNEL_TOKEN:-}"
[ -n "$token" ] || token="$(get_env PIBASE_TUNNEL_TOKEN)"
if [ -z "$token" ]; then
  [ -t 0 ] || die "no tunnel token: set PIBASE_TUNNEL_TOKEN or run this in a terminal"
  printf 'Cloudflare tunnel token (not shown): '
  IFS= read -rs token
  printf '\n'
fi
token="$(printf '%s' "$token" | tr -d '[:space:]')"
# Tokens are base64 JSON ({"a":account,"t":tunnel,"s":secret}).
printf '%s' "$token" | grep -Eq '^eyJ[A-Za-z0-9+/=_-]{40,}$' \
  || die "that does not look like a tunnel token; copy the long string after --token in Cloudflare"

# ------------------------------------------------------------- settings
say "Settings (.env)"
set_env PIBASE_TUNNEL_TOKEN "$token"
set_env COMPOSE_PROFILES "$(profiles_with add)"
set_env PIBASE_PUBLIC_URL "https://$api"
set_env PIBASE_DASHBOARD_URL "https://$dashboard"
# Served over HTTPS by Cloudflare: secure cookies, HSTS.
set_env PIBASE_ADMIN_ENV production
# Public sign-up asks new accounts to confirm their email; without an email
# service that layer of spam protection is missing, so sign-up waits for it.
if [ -n "$(get_env PIBASE_SMTP_HOST)" ] && [ -n "$(get_env PIBASE_SMTP_FROM)" ]; then
  [ -n "$(get_env PIBASE_SIGNUP)" ] || set_env PIBASE_SIGNUP open
else
  set_env PIBASE_SIGNUP closed
fi
case "$(get_env PIBASE_ADMIN_BIND)$(get_env PIBASE_GATEWAY_BIND)" in
  *0.0.0.0*) echo "warning: PIBASE_ADMIN_BIND or PIBASE_GATEWAY_BIND is 0.0.0.0; with the tunnel nothing needs it, set them back to 127.0.0.1" >&2 ;;
esac
echo "API $api, dashboard $dashboard, sign-up $(get_env PIBASE_SIGNUP)"

# ------------------------------------------------------------- start
say "Starting PIBASE with the tunnel"
docker compose up -d >/dev/null || die "PIBASE did not start. See: docker compose logs --tail 40"
# Cloudflare's connector reaches the gateway and dashboard on the Docker
# network. Trust that network to report the real visitor (X-Forwarded-For,
# set by Cloudflare), so sign-up and sign-in limits count each visitor.
network="$(docker compose ps -q gateway | head -1 | xargs -r docker inspect -f '{{range .NetworkSettings.Networks}}{{.NetworkID}}{{end}}')"
subnet="$( [ -n "$network" ] && docker network inspect -f '{{range .IPAM.Config}}{{.Subnet}} {{end}}' "$network" | awk '{print $1}' || true)"
[ -n "$subnet" ] || die "could not find PIBASE's Docker network; is it running?"
if [ "$(get_env PIBASE_TRUSTED_PROXIES)" != "$subnet" ]; then
  set_env PIBASE_TRUSTED_PROXIES "$subnet"
  docker compose up -d >/dev/null
fi
sleep 3
if ! docker compose ps --status running --services 2>/dev/null | grep -qx tunnel; then
  docker compose logs --tail 20 tunnel >&2 || true
  die "the tunnel did not stay up; check the token (docker compose logs tunnel)"
fi
if docker compose logs --tail 50 tunnel 2>&1 | grep -qi 'Unauthorized\|invalid token'; then
  die "Cloudflare refused the token; copy it again from the tunnel's page"
fi

# ------------------------------------------------------------- check
say "Checking https://$api and https://$dashboard"
ok=0
for _ in $(seq 1 24); do
  if curl -fsS --max-time 10 "https://$api/healthz" >/dev/null 2>&1 && curl -fsS --max-time 10 -o /dev/null "https://$dashboard/"; then ok=1; break; fi
  sleep 5
done
backups="$(systemctl is-enabled pibase-backup.timer 2>/dev/null || true)"
if [ "$ok" -eq 1 ]; then
  echo "PIBASE is public: API https://$api, dashboard https://$dashboard"
else
  cat >&2 <<EOF
The tunnel is running, but https://$api or https://$dashboard does not answer
yet. In Cloudflare, Zero Trust > Networks > Tunnels > this tunnel > Public
hostnames, make sure there are exactly these two:
    $api  ->  HTTP  gateway:8080
    $dashboard  ->  HTTP  admin:18081
then check again: curl https://$api/healthz
EOF
fi
cat <<EOF

Next:
  * Phones: open https://$dashboard and install it (Chrome: Install app;
    iPhone Safari: Share, Add to Home Screen).
  * Operator view: https://$dashboard/app#operator with PIBASE_OPERATOR_TOKEN from .env.
EOF
if [ "$(get_env PIBASE_SIGNUP)" = closed ]; then
  cat <<EOF
  * Public sign-up is CLOSED until email is set up. Add PIBASE_SMTP_HOST, _PORT,
    _USER, _PASS, _FROM and PIBASE_SIGNUP=open to .env, then: docker compose up -d
EOF
fi
[ "$backups" = enabled ] || echo "  * Schedule daily backups (once, asks for sudo): ./scripts/pi-backups.sh"
echo "  * Billing with Stripe: docs/BILLING.md (webhook: https://$dashboard/_pibase/billing/webhook)"
echo "  * Turn it off: ./scripts/pi-public.sh --off"
[ "$ok" -eq 1 ]
