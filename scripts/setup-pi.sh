#!/usr/bin/env bash
# One-command Raspberry Pi (or any Debian/Ubuntu-ish Linux box) setup for DATBase.
#
# Handles everything that bit us on first deploy, automatically and idempotently:
#   - installs Docker if missing
#   - installs the `docker compose` v2 plugin if missing (apt doesn't ship it on some
#     Debian/Raspberry Pi OS releases)
#   - installs/updates `docker buildx` if missing or older than the version Compose needs
#   - adds the current user to the `docker` group (so sudo isn't needed every time)
#   - creates .env from .env.example on first run, generates all secrets, and tries to
#     auto-fill DOMAIN from this machine's LAN IP (sslip.io) when it's still a placeholder
#   - detects if 80/443 are already taken by another service and picks free ports instead
#     (written to .env as CADDY_HTTP_PORT / CADDY_HTTPS_PORT), so this never collides with
#     something else already running on the box
#   - builds and starts the stack
#
# Safe to re-run any time (e.g. after `git pull`) — it only fills in what's missing.
#
# Usage:  ./scripts/setup-pi.sh
set -euo pipefail

cd "$(dirname "${BASH_SOURCE[0]}")/.."
REPO_ROOT="$(pwd)"

log()  { printf '\n\033[1;34m==>\033[0m %s\n' "$1"; }
warn() { printf '\033[1;33m!!\033[0m %s\n' "$1"; }
ok()   { printf '\033[1;32m✓\033[0m %s\n' "$1"; }

# ---------------------------------------------------------------------------
# 0. Architecture detection (used for downloading the right plugin binaries)
# ---------------------------------------------------------------------------
UNAME_M="$(uname -m)"
case "$UNAME_M" in
  aarch64|arm64) COMPOSE_ARCH="aarch64"; BUILDX_ARCH="arm64" ;;
  x86_64|amd64)  COMPOSE_ARCH="x86_64";  BUILDX_ARCH="amd64" ;;
  armv7l)        COMPOSE_ARCH="armv7";   BUILDX_ARCH="arm-v7" ;;
  *) warn "Unrecognized architecture '$UNAME_M' — plugin auto-install may not find a matching binary."
     COMPOSE_ARCH="$UNAME_M"; BUILDX_ARCH="$UNAME_M" ;;
esac

# User-local plugin dir: no root needed, and the Docker CLI checks this ahead of the
# system-wide /usr/local/lib/docker/cli-plugins, so plugins installed here just work.
CLI_PLUGINS_DIR="$HOME/.docker/cli-plugins"
mkdir -p "$CLI_PLUGINS_DIR"

IN_DOCKER_GROUP=false
if id -nG "$USER" 2>/dev/null | grep -qw docker; then
  IN_DOCKER_GROUP=true
fi

# Only a few things in this script genuinely need root: installing Docker itself (if missing)
# and adding this user to the docker group (if not already in it). Ask for the password once,
# up front, and keep the ticket alive in the background — nothing later in the script prompts
# again. If neither of those is needed (already installed, already in the group), no sudo runs
# at all.
NEED_SUDO=false
command -v docker >/dev/null 2>&1 || NEED_SUDO=true
$IN_DOCKER_GROUP || NEED_SUDO=true

if $NEED_SUDO; then
  # Non-interactive runners (Claude Code's bash tool, cron, CI, etc.) have no TTY, so `sudo -v`
  # can't prompt — it would otherwise fail with a confusing raw sudo error. Detect that up front
  # and give a one-time, one-line fix instead of a stack of sudo noise.
  if ! sudo -n true 2>/dev/null; then
    if [ ! -t 0 ] && [ ! -t 1 ]; then
      echo
      warn "Root is needed once (to add $USER to the docker group), but this shell has no"
      warn "terminal to prompt for a password with. Run this single line in a real terminal"
      warn "(SSH session or the Pi's own console), once, ever:"
      echo
      echo "    sudo usermod -aG docker $USER"
      echo
      warn "Then re-run this script from wherever you like, including a non-interactive one —"
      warn "it re-checks group membership fresh each time and won't need sudo again."
      exit 1
    fi
    log "This run needs root once, for installing Docker and/or adding $USER to the docker group."
  fi
  sudo -v
  ( while true; do sudo -n true; sleep 60; kill -0 "$$" 2>/dev/null || exit; done ) &
  SUDO_KEEPALIVE_PID=$!
  trap 'kill "$SUDO_KEEPALIVE_PID" 2>/dev/null || true' EXIT
fi

# `docker` runs plain once we have group access in *this* shell. Until then (fresh usermod, no
# re-login yet), `sg docker -c "..."` activates the group immediately for one command — no sudo,
# no logout/login needed. All docker/compose/buildx calls below go through this helper.
run_docker() {
  if $IN_DOCKER_GROUP; then
    docker "$@"
  else
    sg docker -c "docker $*"
  fi
}

# ---------------------------------------------------------------------------
# 1. Docker itself
# ---------------------------------------------------------------------------
log "Checking Docker"
if ! command -v docker >/dev/null 2>&1; then
  log "Docker not found — installing via get.docker.com"
  curl -fsSL https://get.docker.com | sh
else
  ok "Docker already installed ($(docker --version))"
fi

if ! $IN_DOCKER_GROUP; then
  log "Adding $USER to the docker group"
  sudo usermod -aG docker "$USER"
  ok "Added — using 'sg docker' for the rest of this run, no logout needed."
  warn "For future runs without this script re-activating it, log out and back in once."
fi

# ---------------------------------------------------------------------------
# 2. docker compose v2 plugin
# ---------------------------------------------------------------------------
log "Checking docker compose plugin"
if ! run_docker compose version >/dev/null 2>&1; then
  log "docker compose (v2) not found — installing the plugin binary to ${CLI_PLUGINS_DIR} (no root needed; apt doesn't have it on all Debian/Pi OS releases)"
  COMPOSE_VERSION="$(curl -fsSL https://api.github.com/repos/docker/compose/releases/latest | grep '"tag_name"' | cut -d'"' -f4)"
  curl -fsSL \
    "https://github.com/docker/compose/releases/download/${COMPOSE_VERSION}/docker-compose-linux-${COMPOSE_ARCH}" \
    -o "${CLI_PLUGINS_DIR}/docker-compose"
  chmod +x "${CLI_PLUGINS_DIR}/docker-compose"
  run_docker compose version
else
  ok "docker compose already present ($(run_docker compose version --short 2>/dev/null || echo ok))"
fi

# ---------------------------------------------------------------------------
# 3. docker buildx (Compose needs >= 0.17.0; some distro packages are older)
# ---------------------------------------------------------------------------
log "Checking docker buildx version"
BUILDX_MIN="0.17.0"
BUILDX_HAVE="$(run_docker buildx version 2>/dev/null | grep -oE 'v[0-9]+\.[0-9]+\.[0-9]+' | head -1 | tr -d v || true)"
if [ -z "$BUILDX_HAVE" ] || [ "$(printf '%s\n%s\n' "$BUILDX_MIN" "$BUILDX_HAVE" | sort -V | head -1)" != "$BUILDX_MIN" ]; then
  log "buildx missing or older than ${BUILDX_MIN} (have: ${BUILDX_HAVE:-none}) — installing the latest release to ${CLI_PLUGINS_DIR} (no root needed)"
  BUILDX_VERSION="$(curl -fsSL https://api.github.com/repos/docker/buildx/releases/latest | grep '"tag_name"' | cut -d'"' -f4)"
  curl -fsSL \
    "https://github.com/docker/buildx/releases/download/${BUILDX_VERSION}/buildx-${BUILDX_VERSION}.linux-${BUILDX_ARCH}" \
    -o "${CLI_PLUGINS_DIR}/docker-buildx"
  chmod +x "${CLI_PLUGINS_DIR}/docker-buildx"
  run_docker buildx version
else
  ok "buildx ${BUILDX_HAVE} already satisfies >= ${BUILDX_MIN}"
fi

# ---------------------------------------------------------------------------
# 4. .env — create + generate secrets (idempotent: never overwrites a value already set)
# ---------------------------------------------------------------------------
log "Checking .env"
if [ ! -f .env ]; then
  cp .env.example .env
  ok "Created .env from .env.example"
fi

# Fills VAR in .env only if it is currently empty (VAR= with nothing after it).
fill_if_empty() {
  local var="$1" value="$2"
  if grep -qE "^${var}=\"?\"?$" .env; then
    # Escape sed-special characters in the value before substituting.
    local escaped
    escaped=$(printf '%s' "$value" | sed -e 's/[\/&]/\\&/g')
    sed -i "s/^${var}=.*/${var}=${escaped}/" .env
    ok "Generated ${var}"
  fi
}

fill_if_empty POSTGRES_PASSWORD "$(openssl rand -hex 32)"
fill_if_empty BETTER_AUTH_SECRET "$(openssl rand -hex 32)"
fill_if_empty ENCRYPTION_KEY "$(openssl rand -base64 32)"
fill_if_empty MONITOR_TOKEN "$(openssl rand -hex 32)"

# Auto-fill DOMAIN from this machine's LAN IP if it's still the placeholder.
if grep -q '^DOMAIN=example.com$' .env; then
  LAN_IP="$(hostname -I 2>/dev/null | awk '{print $1}')"
  if [ -n "$LAN_IP" ]; then
    SSLIP_DOMAIN="$(printf '%s' "$LAN_IP" | tr '.' '-').sslip.io"
    sed -i "s/^DOMAIN=example.com$/DOMAIN=${SSLIP_DOMAIN}/" .env
    sed -i 's/^# PUBLIC_SCHEME=http$/PUBLIC_SCHEME=http/' .env
    ok "Set DOMAIN=${SSLIP_DOMAIN} and PUBLIC_SCHEME=http (detected LAN IP ${LAN_IP})"
    warn "If this Pi is behind a router that blocks DNS answers for private IPs, sslip.io names"
    warn "won't resolve — see docs/RASPBERRY-PI.md for the Pi-hole/local-DNS fallback."
  else
    warn "Could not auto-detect a LAN IP — set DOMAIN in .env manually before continuing."
  fi
fi

if grep -qE '^ADMIN_EMAIL=(matosgeorgen@gmail.com)?$' .env; then
  warn "ADMIN_EMAIL in .env is still the template default — edit .env and set it to the email"
  warn "you'll sign up with, so that account gets Server status access."
fi

# ---------------------------------------------------------------------------
# 5. Port conflicts — pick free host ports if 80/443 are already taken
# ---------------------------------------------------------------------------
log "Checking for port conflicts on 80/443"
port_in_use() {
  # Works without root; falls back gracefully if `ss` isn't available.
  if command -v ss >/dev/null 2>&1; then
    ss -tln 2>/dev/null | awk '{print $4}' | grep -qE "[.:]$1\$"
  elif command -v netstat >/dev/null 2>&1; then
    netstat -tln 2>/dev/null | awk '{print $4}' | grep -qE "[.:]$1\$"
  else
    return 1
  fi
}

next_free_port() {
  local port="$1"
  while port_in_use "$port"; do port=$((port + 1)); done
  echo "$port"
}

set_env_port() {
  local var="$1" port="$2"
  if grep -q "^${var}=" .env; then
    sed -i "s/^${var}=.*/${var}=${port}/" .env
  elif grep -q "^# ${var}=" .env; then
    sed -i "s/^# ${var}=.*/${var}=${port}/" .env
  else
    printf '%s=%s\n' "$var" "$port" >> .env
  fi
}

if ! grep -q '^CADDY_HTTP_PORT=' .env && port_in_use 80; then
  FREE_HTTP="$(next_free_port 9080)"
  set_env_port CADDY_HTTP_PORT "$FREE_HTTP"
  warn "Port 80 is already in use on this machine — using ${FREE_HTTP} instead (CADDY_HTTP_PORT in .env)."
  warn "See scripts/nginx-datbase.conf.example if you want a friendly URL on port 80 via nginx."
fi
if ! grep -q '^CADDY_HTTPS_PORT=' .env && port_in_use 443; then
  FREE_HTTPS="$(next_free_port 9443)"
  set_env_port CADDY_HTTPS_PORT "$FREE_HTTPS"
  warn "Port 443 is already in use on this machine — using ${FREE_HTTPS} instead (CADDY_HTTPS_PORT in .env)."
fi
ok "Port check done"

# ---------------------------------------------------------------------------
# 6. Build and start
# ---------------------------------------------------------------------------
log "Building and starting the stack (this can take ~10 minutes on a Pi the first time)"
run_docker compose up -d --build

log "Containers:"
run_docker compose ps

DOMAIN_VAL="$(grep '^DOMAIN=' .env | cut -d= -f2-)"
SCHEME_VAL="$(grep '^PUBLIC_SCHEME=' .env | cut -d= -f2- || true)"
SCHEME_VAL="${SCHEME_VAL:-https}"
HTTP_PORT_VAL="$(grep '^CADDY_HTTP_PORT=' .env | cut -d= -f2- || true)"
URL="${SCHEME_VAL}://${DOMAIN_VAL}"
if [ -n "$HTTP_PORT_VAL" ] && [ "$SCHEME_VAL" = "http" ]; then
  URL="${URL}:${HTTP_PORT_VAL}"
fi

ok "Done. Open: ${URL}"
echo "   Sign up with the address you set as ADMIN_EMAIL in .env to see Server status."
