# Raspberry Pi Deployment — Issues & Fixes

First-time deployment on Raspberry Pi OS (Debian Trixie, `aarch64`) with Docker 26 pre-installed.

> **All four issues below are now handled automatically by `scripts/setup-pi.sh`** — it installs
> the compose/buildx plugins (to `~/.docker/cli-plugins`, no root needed), asks for `sudo` at
> most once, and detects port conflicts and reassigns `CADDY_HTTP_PORT`/`CADDY_HTTPS_PORT` in
> `.env` instead of requiring a hand edit to `docker-compose.yml`. This page is kept as reference
> for what the script does and why, and as a manual fallback if you'd rather not run it.

---

## Issues Encountered

### 1. `docker-compose-plugin` not in apt repos

```
E: Unable to locate package docker-compose-plugin
```

The `docker-compose-plugin` package is not available in Raspberry Pi OS / Debian Trixie repos.
`docker compose` (v2 syntax) is therefore also missing:

```
docker: 'compose' is not a docker command.
```

**Fix:** install the Compose v2 binary manually from GitHub releases (see below).

---

### 2. `docker buildx` too old

```
compose build requires buildx 0.17.0 or later
```

The system buildx (v0.13.1 from apt) is too old for Compose v5.

**Fix:** install the latest buildx binary from GitHub releases (see below).

---

### 3. Docker socket permission denied (non-root user)

```
permission denied while trying to connect to the Docker daemon socket at unix:///var/run/docker.sock
```

The `gejg` user is not in the `docker` group.

**Fix (applied):** prefix commands with `sudo`.
**Fix (permanent, optional):** add the user to the docker group so `sudo` isn't needed:

```bash
sudo usermod -aG docker $USER
# then log out and back in
```

---

### 4. Port conflicts — Caddy can't bind 80 or 443

```
Error starting userland proxy: listen tcp4 0.0.0.0:443: bind: address already in use
```

This Pi already runs other services that own the standard ports:

| Port | Held by | Why |
|------|---------|-----|
| 80 | nginx | Serves a separate app (`yaya`) |
| 443 | tailscaled | Tailscale HTTPS on its LAN IP |
| 8080 | Next.js dev server | Another project |

The original `docker-compose.yml` mapped Caddy to `80:80` and `443:443` — both blocked.

**Fix applied then, generalized since:** `docker-compose.yml`'s Caddy ports are now
`${CADDY_HTTP_PORT:-80}:80` / `${CADDY_HTTPS_PORT:-443}:443` — no file edit needed. On this Pi,
`CADDY_HTTP_PORT=9080` is set in `.env` (`scripts/setup-pi.sh` detects a taken port 80 and picks
the next free one automatically on any future fresh Pi, whatever the reason it's taken).
- An nginx vhost `/etc/nginx/sites-available/datbase` proxies `192-168-1-171.sslip.io → localhost:9080`
- Caddy handles internal routing between containers; nginx is the public front door

The nginx config lives at `/etc/nginx/sites-enabled/datbase` — the template is
`scripts/nginx-datbase.conf.example` (fill in `DOMAIN` and `CADDY_HTTP_PORT`):

```nginx
server {
    listen 80;
    server_name 192-168-1-171.sslip.io;

    location / {
        proxy_pass http://127.0.0.1:9080;
        proxy_http_version 1.1;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
    }
}
```

> On a fresh Pi with no other services, `CADDY_HTTP_PORT`/`CADDY_HTTPS_PORT` stay unset, Caddy
> binds 80/443 directly, and the nginx proxy step isn't needed at all.

---

## One-Time Setup (run once per fresh Pi)

Run all of these as the user who will run Docker, or prefix with `sudo`.

### Install Docker Compose v2 plugin

```bash
COMPOSE_VERSION=$(curl -s https://api.github.com/repos/docker/compose/releases/latest \
  | grep '"tag_name"' | cut -d'"' -f4)
sudo mkdir -p /usr/local/lib/docker/cli-plugins
sudo curl -SL \
  "https://github.com/docker/compose/releases/download/${COMPOSE_VERSION}/docker-compose-linux-aarch64" \
  -o /usr/local/lib/docker/cli-plugins/docker-compose
sudo chmod +x /usr/local/lib/docker/cli-plugins/docker-compose
docker compose version   # verify
```

### Install Docker Buildx plugin

```bash
BUILDX_VERSION=$(curl -s https://api.github.com/repos/docker/buildx/releases/latest \
  | grep '"tag_name"' | cut -d'"' -f4)
sudo curl -SL \
  "https://github.com/docker/buildx/releases/download/${BUILDX_VERSION}/buildx-${BUILDX_VERSION}.linux-arm64" \
  -o /usr/local/lib/docker/cli-plugins/docker-buildx
sudo chmod +x /usr/local/lib/docker/cli-plugins/docker-buildx
docker buildx version   # verify
```

> Both plugins drop into `/usr/local/lib/docker/cli-plugins/`, which Docker 26 picks up automatically.

### (Optional) Add user to docker group

Avoids needing `sudo` on every docker command:

```bash
sudo usermod -aG docker $USER
# log out and back in to activate
```

---

## nginx proxy setup (this Pi only)

Because port 80 is taken by nginx for another app, a proxy vhost must be created:

```bash
sudo nano /etc/nginx/sites-available/datbase
# paste the server block from Issue #4 above, adjusting server_name to match your DOMAIN

sudo ln -s /etc/nginx/sites-available/datbase /etc/nginx/sites-enabled/datbase
sudo nginx -t && sudo systemctl reload nginx
```

---

## Normal Deploy (after setup above)

```bash
cd /home/gejg/datbase
sudo docker compose up -d --build
```

Check running containers:

```bash
sudo docker compose ps
sudo docker compose logs -f
```

App will be at: `http://192-168-1-171.sslip.io`

---

## Versions Confirmed Working (2026-09-28)

| Component | Version |
|-----------|---------|
| OS | Raspberry Pi OS Trixie (`aarch64`) |
| Docker | 26.1.5 |
| Docker Compose plugin | v5.5.1 |
| Docker Buildx plugin | v0.37.1 |
| nginx | system (Trixie) |

---

## Done: `scripts/setup-pi.sh`

All of the above — plugin install, docker group, `.env` + secrets, port-conflict detection — is
now automated by `scripts/setup-pi.sh`. It downloads the plugin binaries at run time (matching
whatever architecture it's running on) rather than committing fixed binaries to the repo, so it
stays correct across architectures and versions without needing updates here. It needs network
access to GitHub the first time it runs on a machine; after that, re-runs are instant since the
plugins are already in place. See the top of this document and `docs/RASPBERRY-PI.md` for usage.
