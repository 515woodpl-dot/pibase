#!/bin/sh
# Regression checks for scripts/pi-funnel.sh with stand-ins for tailscale,
# docker and curl, so it runs anywhere (no Pi or Tailscale account needed).
set -eu
cd "$(dirname "$0")/.."
repo="$PWD"
test_root="$(mktemp -d)"
trap 'rm -rf "$test_root"' EXIT HUP INT TERM
bin="$test_root/bin"
mkdir -p "$bin"

cat >"$bin/tailscale" <<'MOCK'
#!/bin/sh
printf '%s\n' "$*" >>"$MOCK_LOG/tailscale"
case "$1" in
  status) [ "${2:-}" = --json ] && echo '{"Self":{"DNSName":"pi.tail1234.ts.net."}}'; exit 0 ;;
  funnel) [ "${MOCK_FUNNEL_FAIL:-0}" = 1 ] && { echo "Funnel is not enabled on your tailnet." >&2; exit 1; }; exit 0 ;;
esac
MOCK
cat >"$bin/docker" <<'MOCK'
#!/bin/sh
printf '%s COMPOSE_FILE=%s\n' "$*" "${COMPOSE_FILE:-}" >>"$MOCK_LOG/docker"
case "$1 $2" in
  "compose ps") echo abc123 ;;
  "inspect -f") echo net456 ;;
  "network inspect") echo "172.18.0.0/16 " ;;
esac
exit 0
MOCK
cat >"$bin/curl" <<'MOCK'
#!/bin/sh
printf '%s\n' "$*" >>"$MOCK_LOG/curl"
exit 0
MOCK
cat >"$bin/id" <<'MOCK'
#!/bin/sh
case "${1:-}" in -u) echo 1000 ;; -un) echo operator ;; *) exit 2 ;; esac
MOCK
cat >"$bin/sleep" <<'MOCK'
#!/bin/sh
exit 0
MOCK
chmod +x "$bin"/*

new_case() { # new_case NAME: a fresh copy of the folder layout with an .env
  dir="$test_root/$1"
  mkdir -p "$dir/scripts" "$dir/log"
  cp "$repo/scripts/pi-funnel.sh" "$dir/scripts/"
  printf 'PIBASE_PUBLIC_URL=\nPIBASE_OPERATOR_TOKEN=keep-me\n' >"$dir/.env"
  : >"$dir/log/tailscale"
}
run() { # run NAME ARGS...
  name="$1"; shift
  MOCK_LOG="$test_root/$name/log" PATH="$bin:/usr/bin:/bin" "$test_root/$name/scripts/pi-funnel.sh" "$@" \
    >"$test_root/$name/stdout" 2>"$test_root/$name/stderr"
}
fail() { echo "FAIL: $*" >&2; exit 1; }

new_case default
run default || fail "default run failed: $(cat "$test_root/default/stderr")"
grep -qx 'funnel --bg --https=10000 http://127.0.0.1:18080' "$test_root/default/log/tailscale" || fail "funnel not started on 10000"
grep -qx 'PIBASE_PUBLIC_URL=https://pi.tail1234.ts.net:10000' "$test_root/default/.env" || fail "public URL not set"
grep -qx 'PIBASE_TRUSTED_PROXIES=172.18.0.0/16' "$test_root/default/.env" || fail "trusted proxy not set"
grep -qx 'PIBASE_OPERATOR_TOKEN=keep-me' "$test_root/default/.env" || fail "other settings changed"
grep -q 'COMPOSE_FILE=compose.yaml:compose.pi.yaml' "$test_root/default/log/docker" || fail "not using the server layout"
grep -q 'https://pi.tail1234.ts.net:10000/healthz' "$test_root/default/log/curl" || fail "public address not checked"
if grep -q 18081 "$test_root/default/log/tailscale"; then fail "dashboard was shared"; fi

new_case port443
run port443 --port 443 || fail "port 443 run failed"
grep -qx 'PIBASE_PUBLIC_URL=https://pi.tail1234.ts.net' "$test_root/port443/.env" || fail "443 URL should have no port"

new_case port9999
if run port9999 --port 9999; then fail "accepted port 9999"; fi
grep -q '443, 8443 and 10000' "$test_root/port9999/stderr" || fail "no explanation for port 9999"
test ! -s "$test_root/port9999/log/tailscale" || fail "touched Tailscale for an invalid port"

new_case refused
if MOCK_FUNNEL_FAIL=1 run refused; then fail "ignored a Funnel failure"; fi
grep -q 'tailscale set --operator=' "$test_root/refused/stderr" || fail "no fix suggested when Funnel is refused"
grep -qx 'PIBASE_PUBLIC_URL=' "$test_root/refused/.env" || fail ".env changed although Funnel failed"

new_case off
run off
run off --off || fail "--off failed"
grep -qx 'funnel --https=10000 off' "$test_root/off/log/tailscale" || fail "Funnel not turned off"
grep -qx 'PIBASE_PUBLIC_URL=' "$test_root/off/.env" || fail "public URL not cleared"

echo "pi-funnel regression checks passed"
