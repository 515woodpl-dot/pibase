#!/bin/sh
set -eu

if [ "$(id -u)" -eq 0 ]; then echo "run as the pibase operator account, not root" >&2; exit 1; fi
if [ "$(uname -m)" != "aarch64" ]; then echo "warning: expected 64-bit Raspberry Pi OS (aarch64)" >&2; fi
command -v sudo >/dev/null || { echo "sudo is required for host setup" >&2; exit 1; }

echo "Installing Docker, PostgreSQL client, and host tools"
sudo apt-get update
sudo DEBIAN_FRONTEND=noninteractive apt-get install -y ca-certificates curl docker.io docker-compose-plugin postgresql-client
sudo systemctl enable --now docker
sudo usermod -aG docker "$(id -un)"

data_root="${PIBASE_DATA_ROOT:-/srv/pibase}"
case "$data_root" in /media/*|/mnt/*|/srv/*) ;; *) echo "PIBASE_DATA_ROOT must be under /media, /mnt, or /srv" >&2; exit 1;; esac
sudo install -d -m 0750 -o "$(id -un)" -g "$(id -gn)" "$data_root" "$data_root/postgres" "$data_root/files" "$data_root/backups"

filesystem="$(findmnt -n -o FSTYPE --target "$data_root")"
case "$filesystem" in ext4|xfs|btrfs) ;; *) echo "warning: $data_root uses $filesystem; ext4 on a USB3 SSD is recommended" >&2;; esac

echo "Host setup complete. Log out/in for Docker group membership, copy the repository to the SSD, run scripts/init-env.sh, then docker compose up --build -d."
