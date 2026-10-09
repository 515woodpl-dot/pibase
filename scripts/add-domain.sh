#!/usr/bin/env bash
# Serve PIBASE's API under another name, such as a client's pibase.client.com,
# with its own HTTPS certificate. Run as root on the server:
#
#   ./scripts/add-domain.sh pibase.client.com
#   ./scripts/add-domain.sh --remove pibase.client.com
#
# First point the name at this server (an A record to its IP, or a CNAME to
# your main API name). Then set the same address on the project in the
# dashboard (Settings, API address) so its API keys page shows it.
set -euo pipefail

reload=1 remove=0 tls_internal=0
names=()
while [ $# -gt 0 ]; do
  case "$1" in
    --no-reload) reload=0 ;;
    --remove) remove=1 ;;
    --tls-internal) tls_internal=1 ;;
    -h|--help) sed -n '2,12p' "$0"; exit 0 ;;
    -*) echo "unknown option: $1" >&2; exit 2 ;;
    *) names+=("$1") ;;
  esac
  shift
done
[ "$(id -u)" -eq 0 ] || { echo "run as root" >&2; exit 1; }
[ "${#names[@]}" -gt 0 ] || { echo "usage: $0 [--remove] NAME..." >&2; exit 2; }
install -d -m 0755 /etc/caddy/pibase.d
for name in "${names[@]}"; do
  name="$(printf '%s' "$name" | tr 'A-Z' 'a-z')"
  printf '%s' "$name" | grep -Eq '^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)+$' \
    || { echo "not a host name: $name" >&2; exit 2; }
  file="/etc/caddy/pibase.d/$name.caddy"
  if [ "$remove" -eq 1 ]; then
    rm -f "$file" && echo "removed $name"
    continue
  fi
  {
    echo "$name {"
    [ "$tls_internal" -eq 0 ] || echo "	tls internal"
    echo "	encode gzip"
    echo "	reverse_proxy 127.0.0.1:18080"
    echo "}"
  } > "$file"
  echo "serving $name"
done
caddy validate --config /etc/caddy/Caddyfile --adapter caddyfile >/dev/null 2>&1 || {
  caddy validate --config /etc/caddy/Caddyfile --adapter caddyfile >&2 || true
  echo "Caddy rejected the configuration; nothing was reloaded" >&2
  exit 1
}
if [ "$reload" -eq 1 ]; then systemctl reload caddy; fi
