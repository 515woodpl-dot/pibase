#!/bin/sh
set -eu

if [ "$(id -u)" -eq 0 ]; then echo "run as the pibase operator account, not root" >&2; exit 1; fi
if [ "$(uname -m)" != "aarch64" ]; then echo "warning: expected 64-bit Raspberry Pi OS (aarch64)" >&2; fi
command -v sudo >/dev/null || { echo "sudo is required for host setup" >&2; exit 1; }

data_root="${PIBASE_DATA_ROOT:-/srv/pibase}"
case "$data_root" in
  *'|'*) echo "PIBASE_DATA_ROOT must not contain |" >&2; exit 1 ;;
  *'/../'*|*'/..') echo "PIBASE_DATA_ROOT must not contain .." >&2; exit 1 ;;
  /media/*|/mnt/*|/srv/*) ;;
  *) echo "PIBASE_DATA_ROOT must be under /media, /mnt, or /srv" >&2; exit 1 ;;
esac

echo "Installing Docker, PostgreSQL client, and host tools"
sudo apt-get update

# docker-compose-plugin is provided by Docker's apt repository, not by a
# stock Debian/Raspberry Pi OS installation. Debian 13 instead ships the v2
# plugin as docker-compose; Debian 12's package is the incompatible v1 client.
compose_package=""
compose_candidate="$(apt-cache policy docker-compose 2>/dev/null | sed -n 's/^[[:space:]]*Candidate:[[:space:]]*//p')"
case "$compose_candidate" in 2.*) compose_package="docker-compose" ;; esac

sudo DEBIAN_FRONTEND=noninteractive apt-get install -y \
  ca-certificates curl docker.io postgresql-client ${compose_package:+"$compose_package"}

# Install the official plugin for distributions without a packaged Compose v2.
# Keep the version and digest together so this privileged-host setup never
# executes an unverified, moving "latest" download.
if ! docker compose version >/dev/null 2>&1; then
  compose_version="v5.5.0"
  case "$(uname -m)" in
    aarch64|arm64)
      compose_arch="aarch64"
      compose_sha256="ff42489f5a9b879d5d117c5ffea6defc27390b3286da8ad52cbc9c6ab5df590e"
      ;;
    x86_64|amd64)
      compose_arch="x86_64"
      compose_sha256="c57ab918abd5b05ca7e7d0f275875dd1330a695074f309dc9eab1b49efafcd4b"
      ;;
    *)
      echo "docker compose v2 is unavailable and no verified plugin is pinned for $(uname -m)" >&2
      exit 1
      ;;
  esac

  compose_dir="${DOCKER_CONFIG:-$HOME/.docker}/cli-plugins"
  mkdir -p "$compose_dir"
  compose_tmp="$(mktemp "$compose_dir/.docker-compose.XXXXXX")"
  trap 'rm -f "$compose_tmp"' EXIT HUP INT TERM
  curl --fail --location --retry 3 \
    "https://github.com/docker/compose/releases/download/$compose_version/docker-compose-linux-$compose_arch" \
    --output "$compose_tmp"
  printf '%s  %s\n' "$compose_sha256" "$compose_tmp" | sha256sum --check --status
  chmod 0755 "$compose_tmp"
  mv "$compose_tmp" "$compose_dir/docker-compose"
  trap - EXIT HUP INT TERM
fi

docker compose version
sudo systemctl enable --now docker
sudo usermod -aG docker "$(id -un)"

sudo install -d -m 0750 -o "$(id -un)" -g "$(id -gn)" "$data_root" "$data_root/postgres" "$data_root/files" "$data_root/backups"

filesystem="$(findmnt -n -o FSTYPE --target "$data_root")"
case "$filesystem" in ext4|xfs|btrfs) ;; *) echo "warning: $data_root uses $filesystem; ext4 on a USB3 SSD is recommended" >&2;; esac

echo "Host setup complete. Log out/in for Docker group membership, copy the repository to the SSD, run scripts/init-env.sh, then docker compose up --build -d."
