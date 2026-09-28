# Test DATBase on a Raspberry Pi 4 (home network)

A cheap way to run the whole stack before paying for a server. Everything works the same as on
DigitalOcean except HTTPS: on a home network there's no public domain, so the Pi serves plain HTTP.

## You need
- Raspberry Pi 4 with **2 GB+** RAM (4 GB+ recommended: each project uses up to 128 MB).
- **64-bit Raspberry Pi OS Lite** (the 32-bit OS won't work).
- A **USB SSD** for the system/data, not the SD card (Postgres and backups wear SD cards out).
- A fixed IP for the Pi (DHCP reservation in your router), e.g. `192.168.1.50`.

## Install (one command)
```bash
git clone <your repo> ~/datbase && cd ~/datbase
./scripts/setup-pi.sh
```
That script does everything by itself: installs Docker if it's missing, installs the
`docker compose`/`buildx` plugins if apt doesn't have them (common on newer Debian/Pi OS releases —
see `docs/DEPLOY-RASPBERRY-PI.md` for why), adds you to the `docker` group, creates `.env` and
generates every secret, guesses `DOMAIN` from the Pi's LAN IP, picks a different port automatically
if 80/443 are already taken by something else on the box, and builds and starts the stack. It asks
for your sudo password **once** (only if Docker isn't installed yet or you're not in the `docker`
group), not once per step. Re-running it later (e.g. after `git pull`) is safe — it only fills in
what's missing.

It still leaves two things for you to fill in by hand in `.env` before the first run if you want
them right away: `ADMIN_EMAIL` (whoever signs up with this address gets **Server status**) and the
`SMTP_*` fields (leave blank to skip email for now — see `docs/RASPBERRY-PI.md` below).

When it finishes it prints the exact URL to open.

### Manual install (if you'd rather not run the script)
```bash
curl -fsSL https://get.docker.com | sh
sudo usermod -aG docker $USER && newgrp docker
cp .env.example .env
```
Edit `.env`:
```
DOMAIN=192-168-1-50.sslip.io      # your Pi's IP with dashes; sslip.io maps it (and *.it) to that IP
PUBLIC_SCHEME=http
POSTGRES_PASSWORD=<openssl rand -hex 32>
BETTER_AUTH_SECRET=<openssl rand -hex 32>
ENCRYPTION_KEY=<openssl rand -base64 32>
ADMIN_EMAIL=matosgeorgen@gmail.com
MONITOR_TOKEN=<openssl rand -hex 32>
# Email: fill SMTP_* (e.g. a free Brevo/Resend account, or a Gmail app password)
```
Then `docker compose up -d --build` (the first build takes ~10 minutes on a Pi) and open
`http://192-168-1-50.sslip.io` from any device on your network. Sign up with ADMIN_EMAIL to see
**Server status**. If `docker compose` or `docker buildx` isn't found, see
`docs/DEPLOY-RASPBERRY-PI.md` for the manual plugin-install commands (`setup-pi.sh` does this for
you automatically).

If your router blocks DNS answers with private IPs ("DNS rebinding protection"), sslip.io names won't
resolve; allow `sslip.io` in the router or use a local DNS (Pi-hole: wildcard `*.datbase.lan` → Pi IP,
then `DOMAIN=datbase.lan`).

## Check it
- Disk numbers: DATBase's monitor measures the disk holding Docker's data (where every project lives).
  Compare `/api/monitor` with `df -h $(docker info -f '{{.DockerRootDir}}')`, not `df -h /`.
- Stress test on the Pi's CPU (use a throwaway project and a server key):
  `PB_URL=http://<slug>.192-168-1-50.sslip.io DATBASE_SERVER_KEY=<key> node app/tests/e2e/pb-stress.mjs`
- Home Assistant: see `integrations/home-assistant/datbase.yaml` (monitor URL
  `http://192-168-1-50.sslip.io/api/monitor`).
- Backups: any S3-compatible bucket works; for a Pi-only test you can leave `BACKUP_S3_*` empty.

## Moving to DigitalOcean later
Set a real `DOMAIN`, remove `PUBLIC_SCHEME`, point `A yourdomain` and `A *.yourdomain` at the droplet,
copy `.env` (keep `ENCRYPTION_KEY`!) and restore from backups (README → Backups & disaster recovery).
