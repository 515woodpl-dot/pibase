#!/usr/bin/env bash
# Put the PIBASE API on the public internet through Tailscale Funnel, without
# opening ports on your router. Run as the pibase operator (not root), from
# the repository folder, after PIBASE is running:
#
#   ./scripts/pi-funnel.sh               # https://<pi>.<tailnet>.ts.net:10000
#   ./scripts/pi-funnel.sh --port 443    # Funnel allows only 443, 8443 or 10000
#   ./scripts/pi-funnel.sh --off         # stop sharing it
#
# Only the API (gateway, port 18080) is shared. The dashboard (18081) never is.
# Once per Pi, Tailscale must let this account change its settings
# (setup-pi.sh --funnel does it, or: sudo tailscale set --operator=$USER), and
# the tailnet must allow HTTPS certificates and Funnel for this device.
set -euo pipefail

port=10000 off=0
while [ $# -gt 0 ]; do
  case "$1" in
    --port) port="${2:-}"; shift 2 ;;
    --off) off=1; shift ;;
    -h|--help) sed -n '2,15p' "$0"; exit 0 ;;
    *) echo "unknown option: $1 (see --help)" >&2; exit 2 ;;
  esac
done
die() { printf 'error: %s\n' "$*" >&2; exit 1; }
case "$port" in
  443|8443|10000) ;;
  *) die "Tailscale Funnel can only use public ports 443, 8443 and 10000 (not $port)" ;;
esac
[ "$(id -u)" -ne 0 ] || die "run as the pibase operator account, not root"
cd "$(dirname "$0")/.."
[ -f .env ] || die "no .env here; run scripts/init-env.sh and start PIBASE first"
command -v tailscale >/dev/null || die "Tailscale is not installed (https://tailscale.com/download/linux)"
tailscale status >/dev/null 2>&1 || die "Tailscale is not connected; run: sudo tailscale up"
# Same server layout as backup-all.sh and update.sh.
if [ -z "${COMPOSE_FILE:-}" ] && ! grep -q '^COMPOSE_FILE=' .env; then
  export COMPOSE_FILE=compose.yaml:compose.pi.yaml
fi

# The gateway listens where PIBASE_GATEWAY_BIND says (empty means loopback;
# a Pi reached over Tailscale often sets its Tailscale address instead).
gateway_host="$(sed -n 's/^PIBASE_GATEWAY_BIND=//p' .env | tail -1)"
case "$gateway_host" in ""|0.0.0.0|"*") gateway_host=127.0.0.1 ;; esac
gateway="http://$gateway_host:18080"

set_env() { # set_env KEY VALUE: replace or add one line of .env, keeping the rest
  if grep -q "^$1=" .env; then
    sed -i "s|^$1=.*|$1=$2|" .env
  else
    printf '%s=%s\n' "$1" "$2" >> .env
  fi
}

if [ "$off" -eq 1 ]; then
  tailscale funnel --https="$port" off || true
  set_env PIBASE_PUBLIC_URL ""
  docker compose up -d >/dev/null
  echo "Funnel on port $port is off. PIBASE is reachable on your tailnet only."
  exit 0
fi

# Share nothing until PIBASE itself starts cleanly.
if ! docker compose up -d >/dev/null; then
  die "PIBASE did not start, so Funnel was left off. See: docker compose logs bootstrap --tail 40"
fi
healthy=0
for _ in $(seq 1 30); do
  if curl -fsS --max-time 5 "$gateway/healthz" >/dev/null 2>&1; then healthy=1; break; fi
  sleep 2
done
[ "$healthy" -eq 1 ] || die "PIBASE is not answering on $gateway_host:18080, so Funnel was left off. See: docker compose logs gateway --tail 40"

name="$(tailscale status --json | python3 -c 'import json,sys; print(json.load(sys.stdin)["Self"]["DNSName"].rstrip("."))' 2>/dev/null || true)"
[ -n "$name" ] || die "could not read this device's Tailscale name (is MagicDNS on?)"
url="https://$name"
[ "$port" = 443 ] || url="$url:$port"

echo "==> Sharing the API at $url"
if ! tailscale funnel --bg --https="$port" "$gateway"; then
  cat >&2 <<EOF

Tailscale did not turn Funnel on. Usually one of these, once:
  * this account may not change Tailscale settings:
        sudo tailscale set --operator=$(id -un)
  * HTTPS certificates or Funnel are not allowed for this device: open the
    link Tailscale printed above, or in the admin console turn on
    DNS > HTTPS Certificates and add the "funnel" attribute for this device.
Then run this script again.
EOF
  exit 1
fi

echo "==> Telling PIBASE its public address and who forwards visitors"
set_env PIBASE_PUBLIC_URL "$url"
docker compose up -d >/dev/null
# Tailscale reaches the gateway through Docker's port mapping, which arrives
# from the Docker network's gateway address. Trust that network to report the
# real visitor (X-Forwarded-For), so sign-in limits count each visitor.
network="$(docker compose ps -q gateway | head -1 | xargs -r docker inspect -f '{{range .NetworkSettings.Networks}}{{.NetworkID}}{{end}}')"
subnet="$( [ -n "$network" ] && docker network inspect -f '{{range .IPAM.Config}}{{.Subnet}} {{end}}' "$network" | awk '{print $1}' || true)"
[ -n "$subnet" ] || die "could not find the gateway's Docker network; is PIBASE running?"
trusted="$subnet"
# When the gateway listens on this Pi's own Tailscale address, Funnel's
# requests may come from that address. Only this machine can use it as a
# source, so trusting it is as safe as trusting loopback.
[ "$gateway_host" = 127.0.0.1 ] || trusted="$trusted,$gateway_host"
set_env PIBASE_TRUSTED_PROXIES "$trusted"
docker compose up -d >/dev/null

echo "==> Checking $url/healthz (the first certificate can take a minute)"
for _ in $(seq 1 30); do
  if curl -fsS --max-time 10 "$url/healthz" >/dev/null 2>&1; then
    cat <<EOF

PIBASE's API is public at $url
  * Apps use it as the project URL; the dashboard's API keys page shows it.
  * Webhooks (Stripe, Resend) can now reach routes PIBASE forwards with
    /functions/v1/<name>.
  * The dashboard stays private: open it over Tailscale as before.
  * Turn it off with: ./scripts/pi-funnel.sh --port $port --off
EOF
    exit 0
  fi
  sleep 5
done
echo "Funnel is set, but $url/healthz did not answer yet. Check again in a few minutes:" >&2
echo "  curl $url/healthz      and      tailscale funnel status" >&2
exit 1
