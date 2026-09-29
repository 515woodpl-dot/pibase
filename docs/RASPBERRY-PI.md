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

## Remote access via Tailscale (optional)

If Tailscale is already installed and running on the Pi when you run `./scripts/setup-pi.sh`, it's
picked up automatically — no per-Pi configuration needed, this isn't tied to any specific
Tailscale IP or account. The script runs `tailscale ip -4` on whatever machine it's on, and if that
returns an address, it adds a *second* way to reach the app (`EXTRA_CADDY_DOMAINS` /
`EXTRA_TRUSTED_ORIGINS` in `.env`) built from that Pi's own Tailscale IP as `<ip-with-dashes>.sslip.io`
— on top of the LAN address, not instead of it. Both work at the same time: the LAN URL for local access,
the Tailscale one for reaching the dashboard from anywhere your tailnet reaches (phone off-network,
a laptop elsewhere), including signing in — both the routing and the auth layer are configured, not
just the page load.

Two cases where you need to do something by hand:
- **Installing Tailscale *after* the first `setup-pi.sh` run:** the auto-detect only fills in
  `EXTRA_CADDY_DOMAINS`/`EXTRA_TRUSTED_ORIGINS` when they're not already set, and a first run without
  Tailscale leaves them empty (not "unset") — so it won't retroactively fill them in on a later run.
  Add them yourself in `.env` (swap in this Pi's own Tailscale IP and port, `tailscale ip -4` to check):
  ```
  EXTRA_CADDY_DOMAINS=, http://100-119-190-50.sslip.io:9081
  EXTRA_TRUSTED_ORIGINS=http://100-119-190-50.sslip.io:9081
  ```
  Match the scheme (`http`/`https`) to `PUBLIC_SCHEME`, and only add `:<port>` if `CADDY_HTTP_PORT`
  is set in `.env` (i.e. something else on the box already had 80 taken). Then rebuild the app so the
  new Caddy label and auth config take effect (a restart alone won't re-read compose labels):
  `docker compose up -d --build`.
- **A third hostname, or anything beyond one extra address** (a public domain as well as LAN and
  Tailscale, say): extend the comma list in both variables the same way —
  `EXTRA_CADDY_DOMAINS=, http://addr-two, http://addr-three` and matching entries (one per line, or
  comma-separated — `auth.ts` splits on commas) in `EXTRA_TRUSTED_ORIGINS`.

Tailscale's MagicDNS hostname (e.g. `http://gejg`) isn't used here on purpose — routing by that name
through Caddy would need its own TLS/cert handling, whereas the Tailscale IP's sslip.io address works
the same zero-config way the LAN address already does.

## Nginx proxy (when port 80 is taken)

When something else on the Pi already owns port 80 (Home Assistant, another web server, etc.),
`setup-pi.sh` automatically moves Caddy to a free port (`CADDY_HTTP_PORT` in `.env`, e.g. 9081).
If nginx is installed and running, the script also auto-generates an nginx site config that
forwards port 80 to Caddy — including a **wildcard** `*.DOMAIN` rule so every project subdomain
(e.g. `panda-xyz.100-119-190-50.sslip.io`) routes correctly. Without the wildcard, project URLs
get a 502 because nginx doesn't recognise the hostname and never forwards to Caddy.

This is fully automatic on a fresh `setup-pi.sh` run. If nginx wasn't installed at first run and
you install it later, just re-run `./scripts/setup-pi.sh` — it detects nginx, writes the config,
and reloads. You can also set it up manually from `scripts/nginx-datbase.conf.example`.

**If you change the Pi's IP** (or DOMAIN changes for any reason), re-run `./scripts/setup-pi.sh`
to regenerate the nginx config with the new hostname — otherwise the old `server_name` won't match
the new domain and project subdomains will 502 again.

## Check it
- Disk numbers: DATBase's monitor measures the disk holding Docker's data (where every project lives).
  Compare `/api/monitor` with `df -h $(docker info -f '{{.DockerRootDir}}')`, not `df -h /`.
- Stress test on the Pi's CPU (use a throwaway project and a server key):
  `PB_URL=http://<slug>.192-168-1-50.sslip.io DATBASE_SERVER_KEY=<key> node app/tests/e2e/pb-stress.mjs`
- Home Assistant: see `integrations/home-assistant/datbase.yaml` (monitor URL
  `http://192-168-1-50.sslip.io/api/monitor`).
- Backups: see **Backups with MinIO** below.

## Backups with MinIO

MinIO is a self-hosted S3-compatible store that runs as another Docker container on the same Pi.
It uses the `minio` Docker Compose profile so it only starts when you ask for it.

### First-time setup (one-off)

**1. Add a password for MinIO to `.env`:**
```
MINIO_ROOT_USER=datbase          # can leave as-is
MINIO_ROOT_PASSWORD=<openssl rand -hex 16>
```

**2. Start MinIO and run the setup script:**
```bash
docker compose --profile minio up -d minio
./scripts/setup-minio.sh
```
The script waits for MinIO to be ready, creates the `datbase-backups` bucket, creates a
scoped access key (not the root credentials), and prints five lines to paste into `.env`:
```
BACKUP_S3_ENDPOINT=http://minio:9000
BACKUP_S3_REGION=us-east-1
BACKUP_S3_BUCKET=datbase-backups
BACKUP_S3_KEY=<generated>
BACKUP_S3_SECRET=<generated>
```

**3. Paste those lines into `.env`, then restart the app:**
```bash
docker compose up -d app
```
The "Backups & alerts" section of Server status will now say "on" and the first backup of
each project runs within `BACKUP_CHECK_MIN` minutes (default 15).

### Day-to-day

- MinIO starts automatically with the rest of the stack: `docker compose --profile minio up -d`
- Web console at `http://<your-pi-ip>:9001` — login with `MINIO_ROOT_USER` / `MINIO_ROOT_PASSWORD`
- Backup files land in the `datbase-backups` bucket, encrypted with `ENCRYPTION_KEY`
- Restore from the Backups tab on any project in the dashboard

### Moving MinIO data to a real S3 bucket later

Change the five `BACKUP_S3_*` values in `.env` to point at Spaces / AWS S3 / Backblaze B2 and
restart the app. Old backups stay in MinIO; new ones go to the new bucket. You can copy old
files into the new bucket with `mc mirror` if you want them all in one place.

## Moving to DigitalOcean later
Set a real `DOMAIN`, remove `PUBLIC_SCHEME`, point `A yourdomain` and `A *.yourdomain` at the droplet,
copy `.env` (keep `ENCRYPTION_KEY`!) and restore from backups (README → Backups & disaster recovery).
