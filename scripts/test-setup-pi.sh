#!/bin/sh
set -eu

cd "$(dirname "$0")/.."
setup_script="$PWD/scripts/setup-pi.sh"
test_root="$(mktemp -d)"
trap 'rm -rf "$test_root"' EXIT HUP INT TERM
mock_bin="$test_root/bin"
mkdir -p "$mock_bin"

cat >"$mock_bin/id" <<'EOF'
#!/bin/sh
case "$1" in
  -u) echo 1000 ;;
  -un) echo operator ;;
  -gn) echo operator ;;
  *) exit 2 ;;
esac
EOF

cat >"$mock_bin/uname" <<'EOF'
#!/bin/sh
echo aarch64
EOF

cat >"$mock_bin/sudo" <<'EOF'
#!/bin/sh
printf '%s\n' "$*" >>"$MOCK_LOG/sudo"
EOF

cat >"$mock_bin/apt-cache" <<'EOF'
#!/bin/sh
printf 'docker-compose:\n  Candidate: %s\n' "$MOCK_COMPOSE_CANDIDATE"
EOF

cat >"$mock_bin/docker" <<'EOF'
#!/bin/sh
if [ "${MOCK_COMPOSE_AVAILABLE:-0}" = 1 ] || [ -x "$HOME/.docker/cli-plugins/docker-compose" ]; then
  echo 'Docker Compose version test'
  exit 0
fi
exit 1
EOF

cat >"$mock_bin/curl" <<'EOF'
#!/bin/sh
printf '%s\n' "$*" >>"$MOCK_LOG/curl"
while [ "$#" -gt 0 ]; do
  if [ "$1" = --output ]; then
    cp /bin/true "$2"
    exit 0
  fi
  shift
done
exit 2
EOF

cat >"$mock_bin/sha256sum" <<'EOF'
#!/bin/sh
checksum_line="$(cat)"
case "$checksum_line" in
  ff42489f5a9b879d5d117c5ffea6defc27390b3286da8ad52cbc9c6ab5df590e*'/.docker-compose.'*) ;;
  *) echo "unexpected Compose checksum input: $checksum_line" >&2; exit 1 ;;
esac
EOF

cat >"$mock_bin/findmnt" <<'EOF'
#!/bin/sh
echo ext4
EOF

chmod +x "$mock_bin"/*

run_case() {
  case_name="$1"
  data_root="$2"
  candidate="$3"
  compose_available="$4"
  shift 4
  case_root="$test_root/$case_name"
  mkdir -p "$case_root/home" "$case_root/log"
  : >"$case_root/log/sudo"
  : >"$case_root/log/curl"
  HOME="$case_root/home" \
    MOCK_LOG="$case_root/log" \
    MOCK_COMPOSE_CANDIDATE="$candidate" \
    MOCK_COMPOSE_AVAILABLE="$compose_available" \
    PIBASE_DATA_ROOT="$data_root" \
    PATH="${MOCK_PATH:-$mock_bin}:/usr/bin:/bin" \
    "$setup_script" "$@" >"$case_root/stdout" 2>"$case_root/stderr"
}

invalid_root="$test_root/invalid-root"
mkdir -p "$invalid_root/home" "$invalid_root/log"
: >"$invalid_root/log/sudo"
if HOME="$invalid_root/home" MOCK_LOG="$invalid_root/log" \
  MOCK_COMPOSE_CANDIDATE=2.26.1 MOCK_COMPOSE_AVAILABLE=1 \
  PIBASE_DATA_ROOT=/tmp/pibase PATH="$mock_bin:/usr/bin:/bin" \
  "$setup_script" >"$invalid_root/stdout" 2>"$invalid_root/stderr"; then
  echo "setup accepted an unsafe data root" >&2
  exit 1
fi
test ! -s "$invalid_root/log/sudo"

run_case packaged /srv/pibase 2.26.1-4 1
grep -q 'docker-compose' "$test_root/packaged/log/sudo"
grep -q -- '-o operator -g operator /srv/pibase /srv/pibase/postgres /srv/pibase/files /srv/pibase/backups' "$test_root/packaged/log/sudo"
grep -q -- '-R operator:operator /srv/pibase/files /srv/pibase/backups' "$test_root/packaged/log/sudo"
test ! -s "$test_root/packaged/log/curl"

run_case fallback /mnt/pibase 1.29.2-3 0
if grep -q 'docker-compose' "$test_root/fallback/log/sudo"; then
  echo "setup tried to install legacy Compose v1" >&2
  exit 1
fi
grep -q 'docker-compose-linux-aarch64' "$test_root/fallback/log/curl"
test -x "$test_root/fallback/home/.docker/cli-plugins/docker-compose"

# Without --funnel, Tailscale is left alone.
if grep -q tailscale "$test_root/packaged/log/sudo"; then
  echo "setup changed Tailscale without --funnel" >&2
  exit 1
fi

# --funnel lets the operator manage Funnel later without sudo.
funnel_bin="$test_root/funnel-bin"
mkdir -p "$funnel_bin"
cp "$mock_bin"/* "$funnel_bin/"
printf '#!/bin/sh\nexit 0\n' >"$funnel_bin/tailscale"
chmod +x "$funnel_bin/tailscale"
MOCK_PATH="$funnel_bin" run_case funnel /srv/pibase 2.26.1-4 1 --funnel
grep -q -- 'tailscale set --operator=operator' "$test_root/funnel/log/sudo"
grep -q 'pi-funnel.sh' "$test_root/funnel/stdout"

# --funnel without Tailscale stops before changing anything.
if MOCK_PATH="$mock_bin" run_case funnel-missing /srv/pibase 2.26.1-4 1 --funnel; then
  echo "setup accepted --funnel without Tailscale" >&2
  exit 1
fi
test ! -s "$test_root/funnel-missing/log/sudo"

# Unknown options are refused.
if run_case bad-option /srv/pibase 2.26.1-4 1 --nope; then
  echo "setup accepted an unknown option" >&2
  exit 1
fi

echo "setup-pi regression checks passed"
