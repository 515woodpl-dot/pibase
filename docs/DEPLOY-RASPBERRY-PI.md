# Raspberry Pi Deployment — Issues & Fixes

First-time deployment on Raspberry Pi OS (Debian Trixie, `aarch64`) with Docker 26 pre-installed.

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

**Fix applied:**
- Changed Caddy's port binding to `9080:80` in `docker-compose.yml`
- Added an nginx vhost `/etc/nginx/sites-available/datbase` that proxies `192-168-1-171.sslip.io → localhost:9080`
- Caddy handles internal routing between containers; nginx is the public front door

The nginx config lives at `/etc/nginx/sites-enabled/datbase`:

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

> If deploying on a fresh Pi with no other services, revert the port change to `80:80` and skip the nginx proxy step.

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

## TODO: Bundle plugins in repo (optional)

To avoid the manual download step on future deploys, the two plugin binaries could be committed to `scripts/docker-plugins/` and installed via a setup script:

```
scripts/
  setup-pi.sh        ← runs the curl commands above automatically
  docker-plugins/
    docker-compose   ← aarch64 binary (28 MB)
    docker-buildx    ← aarch64 binary (59 MB)
```

This would make the deploy fully offline-capable after a `git clone`.
