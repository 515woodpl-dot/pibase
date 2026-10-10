# Running PIBASE on a DigitalOcean droplet (or any Ubuntu VPS)

One script sets up a fresh server: swap, Docker, HTTPS, firewall, automatic
security updates, PIBASE itself and daily backups. Client websites live
elsewhere (Vercel, Bluehost, another server) and talk to PIBASE over HTTPS.

```
 visitors ──► client.com (the website, hosted anywhere)
                 │  supabase-js / pibase-auth.js
                 ▼
          pibase.client.com ─┐
          api.yourname.com  ─┼─► droplet: Caddy (HTTPS) ─► PIBASE gateway ─► Postgres
                             │                    (dashboard: SSH tunnel only)
```

## 1. Create the droplet

* **Image:** Ubuntu 24.04 LTS (x86).
* **Size:** 2 GB RAM, 1 vCPU, 50 GB disk is enough for 5 to 10 small projects.
  1 GB is too tight. The setup adds 2 GB of swap as a safety net.
* **Authentication:** SSH key.
* **Optional:** turn on DigitalOcean's weekly droplet backups and monitoring.
* **Firewall:** in Networking, Firewalls, allow inbound TCP 22, 80 and 443 only.
  The setup also turns on the server's own firewall (`ufw`) with the same rules.

## 2. Point a name at it

In your DNS, add an A record for your API name (for example
`api.yourname.com`) to the droplet's IP address. HTTPS needs this before the
certificate can be issued.

## 3. Install

```sh
ssh root@DROPLET_IP
git clone https://github.com/515woodpl-dot/datbase.git
cd datbase
./scripts/setup-droplet.sh --domain api.yourname.com --email you@yourname.com
```

It takes about 5 to 15 minutes, most of it building PIBASE. It:

* adds a 2 GB swap file (`--swap 0` skips it) and keeps swapping to a minimum;
* installs Docker with the Compose and Buildx plugins, Caddy, `ufw`, `jq` and
  automatic security updates;
* creates the `pibase` account, which runs everything, and lets your SSH key
  log in to it;
* puts the code in `/home/pibase/pibase` and the data in `/srv/pibase`
  (`postgres`, `files`, `backups`);
* generates every secret into `/home/pibase/pibase/.env` (never paste that file
  anywhere);
* builds and starts PIBASE, which comes back by itself after a reboot;
* serves `https://api.yourname.com` through Caddy, which renews the certificate
  by itself;
* schedules an encrypted backup of every project at 03:30 each day.
* asks for the password of your main dashboard login, `omnia@local.com` by
  default (`--account you@example.com` to change it, `--no-account` to skip).

Running it again is safe; it skips what is already done.

## 4. Open the dashboard

The dashboard never faces the internet. From your own computer:

```sh
ssh -L 18081:127.0.0.1:18081 pibase@DROPLET_IP
```

Then open http://127.0.0.1:18081/, log in and create a project. Forgot the
password? `./scripts/create-account.sh --reset omnia@local.com` as `pibase`
in `/home/pibase/pibase`, or see PI-OPERATIONS.md for email reset links. For the
operator view (`/app#operator`), the token is the `PIBASE_OPERATOR_TOKEN` line
in `/home/pibase/pibase/.env`.

## 5. Give a client their own API name

For `pibase.client.com`:

1. In the client's DNS, add an A record for `pibase.client.com` to the droplet's
   IP (or a CNAME to `api.yourname.com`).
2. On the droplet, as root: `/home/pibase/pibase/scripts/add-domain.sh pibase.client.com`
3. In the dashboard, the client's project, **Settings, API address**:
   `https://pibase.client.com`. Its API keys page then shows that address.

The name is only a label: the project is found from its key, so a client's key
opens only their own project whatever name it is sent to.
`add-domain.sh --remove pibase.client.com` takes a name away.

## 6. Server-side code

If the client's site runs server code (for example Next.js on Vercel), set its
address in **Settings, Server-side code**; see
[SERVER-SIDE-CODE.md](SERVER-SIDE-CODE.md). A public address can be set by the
project owner.

## 7. Backups

* Daily, into `/srv/pibase/backups`, kept 14 days (`PIBASE_BACKUP_KEEP_DAYS`).
* Run one now: `sudo -u pibase /home/pibase/pibase/scripts/backup-all.sh`
* **Copy them off the droplet.** A dead droplet takes local backups with it.
  Install `rclone`, configure a remote (for example a DigitalOcean Spaces
  bucket, `rclone config`, as the pibase user) and add
  `PIBASE_BACKUP_REMOTE=spaces:your-bucket/pibase` to `.env`. The daily backup
  then copies new files there.
* **Keep `PIBASE_BACKUP_KEY` from `.env` somewhere safe off the server**, such
  as a password manager. Without it the backups can't be restored.
* Restore steps are in [PI-OPERATIONS.md](PI-OPERATIONS.md); test one restore
  every few months.

## 8. Update

```sh
sudo -u pibase /home/pibase/pibase/scripts/update.sh
```

It backs everything up, pulls the latest `main`, rebuilds, restarts and checks
the gateway answers.

### Updates from a private repository

The server needs read access to GitHub to pull. Give the `pibase` account a
read-only deploy key once:

```sh
sudo -u pibase ssh-keygen -t ed25519 -N "" -f /home/pibase/.ssh/id_ed25519
sudo cat /home/pibase/.ssh/id_ed25519.pub
```

In GitHub, the repository's **Settings, Deploy keys, Add deploy key**: paste it
and leave "Allow write access" off. Then:

```sh
sudo -u pibase git -C /home/pibase/pibase remote set-url origin git@github.com:515woodpl-dot/datbase.git
sudo -u pibase ssh -o StrictHostKeyChecking=accept-new -T git@github.com   # says "successfully authenticated"
```

For the first clone in step 3, use the same idea for root, or clone with a
GitHub personal access token that only has read access to this repository.

## 9. Keep an eye on it

* `free -h`: if swap is in use most of the time, resize the droplet to 4 GB.
* `df -h /srv`: keep a quarter of the disk free.
* `sudo -u pibase docker compose -f /home/pibase/pibase/compose.yaml -f /home/pibase/pibase/compose.pi.yaml ps`
  (or `cd /home/pibase/pibase && docker compose ps` as pibase) shows the
  services; `docker compose logs -f gateway` follows the log.
* `systemctl list-timers pibase-backup.timer` shows the next backup.
* An uptime monitor on `https://api.yourname.com/healthz` tells you when it
  stops answering.

## Notes

* The gateway sits behind Caddy and reads each visitor's address from
  `X-Forwarded-For` only when the request comes from the Docker network Caddy
  uses (`PIBASE_TRUSTED_PROXIES`, set by the setup). Sign-in limits therefore
  count each visitor separately.
* Ports 18080 (API) and 18081 (dashboard) listen on the droplet itself only.
  Never set `PIBASE_GATEWAY_BIND` or `PIBASE_ADMIN_BIND` to `0.0.0.0` here.
* Root login with an SSH key still works after setup. Once you can log in as
  `pibase`, you may turn off root SSH login, but keep a way in (DigitalOcean's
  web console) before you do.
