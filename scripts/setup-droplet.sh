#!/usr/bin/env bash
# One-time setup of PIBASE on a fresh Ubuntu 24.04 (or Debian 12/13) server,
# such as a DigitalOcean droplet. Run it as root, from a clone of the
# repository:
#
#   ./scripts/setup-droplet.sh --domain api.example.com [--email you@example.com]
#
# It is safe to run again: every step checks what is already there.
#
# What it does:
#   1. adds swap (so builds and spikes don't run out of memory)
#   2. installs Docker with the Compose and Buildx plugins, Caddy, the
#      firewall and automatic security updates
#   3. creates the non-root "pibase" account that runs everything
#   4. creates /srv/pibase for the database, files and backups
#   5. generates secrets (.env), builds and starts PIBASE
#   6. puts Caddy in front of it with HTTPS for --domain
#   7. schedules a daily encrypted backup of every project
#
# Options:
#   --domain NAME     public name of the API (required), e.g. api.example.com
#   --email ADDRESS   contact for the HTTPS certificate (recommended)
#   --user NAME       account that runs PIBASE (default: pibase)
#   --swap SIZE       swap file size, e.g. 2G (default: 2G; 0 skips)
#   --tls-internal    use Caddy's own certificate authority instead of
#                     Let's Encrypt (for testing without public DNS)
#   --no-firewall     leave the firewall alone
set -euo pipefail

domain="" email="" user="pibase" swap="2G" tls_internal=0 firewall=1
while [ $# -gt 0 ]; do
  case "$1" in
    --domain) domain="${2:-}"; shift 2 ;;
    --email) email="${2:-}"; shift 2 ;;
    --user) user="${2:-}"; shift 2 ;;
    --swap) swap="${2:-}"; shift 2 ;;
    --tls-internal) tls_internal=1; shift ;;
    --no-firewall) firewall=0; shift ;;
    -h|--help) sed -n '2,30p' "$0"; exit 0 ;;
    *) echo "unknown option: $1 (see --help)" >&2; exit 2 ;;
  esac
done

say() { printf '\n==> %s\n' "$*"; }
die() { printf 'error: %s\n' "$*" >&2; exit 1; }

[ "$(id -u)" -eq 0 ] || die "run this as root (it is the one setup step that needs it)"
[ -n "$domain" ] || die "--domain is required, for example --domain api.example.com"
printf '%s' "$domain" | grep -Eq '^[A-Za-z0-9]([A-Za-z0-9-]*[A-Za-z0-9])?(\.[A-Za-z0-9]([A-Za-z0-9-]*[A-Za-z0-9])?)+$' \
  || die "--domain must be a host name such as api.example.com"
[ -z "$email" ] || printf '%s' "$email" | grep -Eq '^[^@[:space:]]+@[^@[:space:]]+\.[^@[:space:]]+$' || die "--email is not an email address"
printf '%s' "$user" | grep -Eq '^[a-z_][a-z0-9_-]{0,31}$' || die "--user must be a plain lowercase account name"
printf '%s' "$swap" | grep -Eq '^(0|[1-9][0-9]*[MG])$' || die "--swap must look like 2G, 512M or 0"
command -v apt-get >/dev/null || die "this script supports Ubuntu and Debian (apt-get not found)"
case "$(uname -m)" in x86_64|amd64|aarch64|arm64) ;; *) die "unsupported CPU architecture $(uname -m)" ;; esac

src="$(cd "$(dirname "$0")/.." && pwd)"
[ -f "$src/compose.yaml" ] && [ -f "$src/scripts/init-env.sh" ] || die "run this from inside a PIBASE clone"
home="/home/$user"
app="$home/pibase"
data="/srv/pibase"
export DEBIAN_FRONTEND=noninteractive

# ---------------------------------------------------------------- swap
if [ "$swap" != "0" ]; then
  say "Swap"
  if swapon --show --noheadings | grep -q .; then
    echo "swap already active: $(swapon --show --noheadings | awk '{print $1" "$3}' | tr '\n' ' ')"
  else
    if [ ! -f /swapfile ]; then
      fallocate -l "$swap" /swapfile 2>/dev/null || dd if=/dev/zero of=/swapfile bs=1M count="$(numfmt --from=iec "$swap" | awk '{print int($1/1048576)}')" status=none
      chmod 600 /swapfile
      mkswap /swapfile >/dev/null
    fi
    swapon /swapfile && echo "added /swapfile ($swap)" || echo "warning: could not enable swap here (some containers and VMs forbid it)" >&2
  fi
  if [ -f /swapfile ] && ! grep -q '^/swapfile ' /etc/fstab; then echo '/swapfile none swap sw 0 0' >> /etc/fstab; fi
  # Use swap only under real memory pressure, so the database stays in RAM.
  printf 'vm.swappiness=10\n' > /etc/sysctl.d/99-pibase.conf
  sysctl -q -p /etc/sysctl.d/99-pibase.conf 2>/dev/null || true
fi

# ---------------------------------------------------------------- packages
say "Packages: Docker, Compose, Buildx, Caddy, firewall, security updates"
apt-get update -q
apt-get install -y -q ca-certificates curl git jq ufw unattended-upgrades docker.io caddy >/dev/null
# Compose v2 and Buildx are separate packages; their names differ by release.
for want in compose buildx; do
  if docker "$want" version >/dev/null 2>&1; then continue; fi
  installed=0
  for pkg in "docker-$want-v2" "docker-$want-plugin" "docker-$want"; do
    if apt-cache show "$pkg" >/dev/null 2>&1; then
      apt-get install -y -q "$pkg" >/dev/null && docker "$want" version >/dev/null 2>&1 && { installed=1; break; }
    fi
  done
  [ "$installed" -eq 1 ] || die "could not install the Docker $want plugin from apt; install it and run this again"
done
docker compose version
docker buildx version
systemctl enable --now docker >/dev/null
# Automatic security updates.
cat > /etc/apt/apt.conf.d/20auto-upgrades <<'EOF'
APT::Periodic::Update-Package-Lists "1";
APT::Periodic::Unattended-Upgrade "1";
EOF

# ---------------------------------------------------------------- account
say "Account '$user'"
if ! id "$user" >/dev/null 2>&1; then
  useradd --create-home --shell /bin/bash "$user"
  echo "created $user"
fi
usermod -aG docker "$user"
# Let the same SSH keys that reach root reach this account, for the dashboard tunnel.
if [ -s /root/.ssh/authorized_keys ] && [ ! -s "$home/.ssh/authorized_keys" ]; then
  install -d -m 700 -o "$user" -g "$user" "$home/.ssh"
  install -m 600 -o "$user" -g "$user" /root/.ssh/authorized_keys "$home/.ssh/authorized_keys"
  echo "copied root's SSH keys to $user"
fi
as_user() { sudo -u "$user" -H bash -c "cd '$app' && $*"; }
command -v sudo >/dev/null || apt-get install -y -q sudo >/dev/null

# ---------------------------------------------------------------- code
say "Code in $app"
if [ ! -d "$app/.git" ]; then
  if [ "$src" != "$app" ]; then
    origin="$(git -C "$src" remote get-url origin 2>/dev/null || true)"
    git config --global --add safe.directory "$src" 2>/dev/null || true
    sudo -u "$user" -H git clone -q "$src" "$app" 2>/dev/null || { cp -a "$src" "$app" && chown -R "$user:$user" "$app"; }
    [ -z "$origin" ] || sudo -u "$user" -H git -C "$app" remote set-url origin "$origin"
    branch="$(git -C "$src" rev-parse --abbrev-ref HEAD 2>/dev/null || echo main)"
    [ "$branch" = "HEAD" ] || sudo -u "$user" -H git -C "$app" checkout -q "$branch" 2>/dev/null || true
  fi
else
  echo "already there; update later with: sudo -u $user $app/scripts/update.sh"
fi
chown -R "$user:$user" "$app"

# ---------------------------------------------------------------- data
say "Data in $data"
install -d -m 0750 -o "$user" -g "$user" "$data" "$data/files" "$data/backups"
install -d -m 0700 "$data/postgres"

# ---------------------------------------------------------------- settings
say "Settings (.env, secrets generated on this server)"
set_env() { # set_env KEY VALUE: replace or add one line, keeping the rest
  local file="$app/.env" key="$1" value="$2"
  if grep -q "^$key=" "$file"; then
    sed -i "s|^$key=.*|$key=$value|" "$file"
  else
    printf '%s=%s\n' "$key" "$value" >> "$file"
  fi
}
as_user "PIBASE_DATA_ROOT='$data' ./scripts/init-env.sh"
set_env PIBASE_DATA_ROOT "$data"
set_env PIBASE_PUBLIC_URL "https://$domain"
# `docker compose` in this folder uses the server layout (bind mounts under
# /srv/pibase and automatic restarts) without extra -f flags.
set_env COMPOSE_FILE "compose.yaml:compose.pi.yaml"
chown "$user:$user" "$app/.env"
chmod 600 "$app/.env"

# ---------------------------------------------------------------- start
say "Build and start PIBASE (the first build takes a few minutes)"
as_user "docker compose up --build -d"
# The backup and maintenance tool runs on demand; build it now, not during the first backup.
as_user "docker compose --profile tools build ops"
# Caddy on this machine reaches the gateway from the Docker network's gateway
# address. Trust that network to report the real visitor in X-Forwarded-For.
network="$(as_user "docker compose ps -q gateway" | head -1 | xargs -r docker inspect -f '{{range .NetworkSettings.Networks}}{{.NetworkID}}{{end}}' 2>/dev/null || true)"
subnet="$( [ -n "$network" ] && docker network inspect -f '{{range .IPAM.Config}}{{.Subnet}} {{end}}' "$network" | awk '{print $1}' || true)"
[ -n "$subnet" ] || die "could not find the gateway's Docker network"
if ! grep -q "^PIBASE_TRUSTED_PROXIES=$subnet\$" "$app/.env"; then
  set_env PIBASE_TRUSTED_PROXIES "$subnet"
  as_user "docker compose up -d"
fi
for i in $(seq 1 60); do
  curl -fsS http://127.0.0.1:18080/healthz >/dev/null 2>&1 && break
  [ "$i" -lt 60 ] || { as_user "docker compose logs --tail 30 gateway" >&2; die "the gateway did not become healthy"; }
  sleep 2
done
echo "gateway healthy on 127.0.0.1:18080; dashboard on 127.0.0.1:18081 (local only)"

# ---------------------------------------------------------------- HTTPS
say "Caddy (HTTPS) for $domain"
install -d -m 0755 /etc/caddy/pibase.d
{
  echo "# Managed by PIBASE setup-droplet.sh. Add client names with scripts/add-domain.sh."
  echo "{"
  [ -z "$email" ] || echo "	email $email"
  echo "}"
  echo "import /etc/caddy/pibase.d/*.caddy"
} > /etc/caddy/Caddyfile
domain_flags=(--no-reload)
[ "$tls_internal" -eq 0 ] || domain_flags+=(--tls-internal)
"$app/scripts/add-domain.sh" "${domain_flags[@]}" "$domain"
systemctl enable caddy >/dev/null
systemctl restart caddy

# ---------------------------------------------------------------- firewall
if [ "$firewall" -eq 1 ]; then
  say "Firewall: SSH, HTTP and HTTPS only"
  ufw allow 22/tcp >/dev/null
  ufw allow 80/tcp >/dev/null
  ufw allow 443/tcp >/dev/null
  ufw --force enable >/dev/null && ufw status | sed -n '1,12p' || echo "warning: could not enable ufw here" >&2
fi

# ---------------------------------------------------------------- backups
say "Daily encrypted backups at 03:30"
cat > /etc/systemd/system/pibase-backup.service <<EOF
[Unit]
Description=PIBASE encrypted backup of every project
After=docker.service
Requires=docker.service

[Service]
Type=oneshot
User=$user
WorkingDirectory=$app
ExecStart=$app/scripts/backup-all.sh
EOF
cat > /etc/systemd/system/pibase-backup.timer <<'EOF'
[Unit]
Description=Daily PIBASE backup

[Timer]
OnCalendar=*-*-* 03:30:00
RandomizedDelaySec=15m
Persistent=true

[Install]
WantedBy=timers.target
EOF
systemctl daemon-reload
systemctl enable --now pibase-backup.timer >/dev/null

# ---------------------------------------------------------------- check
say "Checking https://$domain"
ok=0
for i in $(seq 1 30); do
  if [ "$tls_internal" -eq 1 ]; then
    curl -fsS -k --resolve "$domain:443:127.0.0.1" "https://$domain/healthz" >/dev/null 2>&1 && { ok=1; break; }
  else
    curl -fsS "https://$domain/healthz" >/dev/null 2>&1 && { ok=1; break; }
  fi
  sleep 2
done
# DigitalOcean's metadata service knows the public address; elsewhere use the first local one.
ip="$(curl -fsS --max-time 2 http://169.254.169.254/metadata/v1/interfaces/public/0/ipv4/address 2>/dev/null || hostname -I | awk '{print $1}')"
if [ "$ok" -eq 1 ]; then
  echo "https://$domain is up."
else
  echo "https://$domain did not answer yet. Point a DNS A record for $domain at $ip,"
  echo "wait a few minutes, then check: curl https://$domain/healthz"
fi

cat <<EOF

Done. Next steps:
  * DNS: an A record for $domain -> $ip (and later one per client name).
  * Dashboard (local only). On your own computer run:
        ssh -L 18081:127.0.0.1:18081 $user@$ip
    then open http://127.0.0.1:18081/ and sign up. The operator token is the
    PIBASE_OPERATOR_TOKEN line in $app/.env; copy it from there, don't share it.
  * Add a client's API name:   $app/scripts/add-domain.sh pibase.client.com   (as root)
  * Update PIBASE:             sudo -u $user $app/scripts/update.sh
  * Backups: $data/backups, daily. Copy them off this server; see docs/VPS.md.
  * Keep PIBASE_BACKUP_KEY from $app/.env somewhere safe off this server, or
    the backups cannot be restored.
EOF
