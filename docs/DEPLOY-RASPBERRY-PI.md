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

---

## Versions Confirmed Working (2026-09-28)

| Component | Version |
|-----------|---------|
| OS | Raspberry Pi OS Trixie (`aarch64`) |
| Docker | 26.1.5 |
| Docker Compose plugin | v5.5.1 |
| Docker Buildx plugin | v0.37.1 |

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
