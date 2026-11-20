#!/usr/bin/env bash
# Schedule a daily encrypted backup of every project on a Raspberry Pi (the
# droplet setup does this for you). Run once as the pibase operator, from the
# repository folder where PIBASE runs; it asks for sudo to add the timer.
#
#   ./scripts/pi-backups.sh            # every day at 03:30
#   ./scripts/pi-backups.sh --off
#
# Backups land in $PIBASE_DATA_ROOT/backups and are kept 14 days. They are on
# the same SSD, so also copy them somewhere else (see docs/PI-OPERATIONS.md).
set -euo pipefail
off=0
case "${1:-}" in
  "") ;;
  --off) off=1 ;;
  -h|--help) sed -n '2,11p' "$0"; exit 0 ;;
  *) echo "unknown option: $1 (see --help)" >&2; exit 2 ;;
esac
die() { printf 'error: %s\n' "$*" >&2; exit 1; }
[ "$(id -u)" -ne 0 ] || die "run as the pibase operator account, not root (it uses sudo for the timer)"
command -v systemctl >/dev/null || die "this needs systemd"
cd "$(dirname "$0")/.."
app="$(pwd)" user="$(id -un)"
[ -f .env ] || die "no .env here; run this from the folder PIBASE runs in"

if [ "$off" -eq 1 ]; then
  sudo systemctl disable --now pibase-backup.timer 2>/dev/null || true
  echo "Daily backups are off."
  exit 0
fi

sudo tee /etc/systemd/system/pibase-backup.service >/dev/null <<EOF
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
sudo tee /etc/systemd/system/pibase-backup.timer >/dev/null <<'EOF'
[Unit]
Description=Daily PIBASE backup

[Timer]
OnCalendar=*-*-* 03:30:00
RandomizedDelaySec=15m
Persistent=true

[Install]
WantedBy=timers.target
EOF
sudo systemctl daemon-reload
sudo systemctl enable --now pibase-backup.timer >/dev/null
echo "Daily backups at 03:30: $(systemctl list-timers pibase-backup.timer --no-legend 2>/dev/null | awk '{print $1, $2, $3}')"
echo "Run one now with: ./scripts/backup-all.sh"
