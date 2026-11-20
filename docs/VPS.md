# Running PIBASE on a DigitalOcean droplet (or any Ubuntu VPS)

One script sets up a fresh server: swap, Docker, HTTPS, firewall, automatic
security updates, PIBASE itself and daily backups. Client websites live
elsewhere (Vercel, Bluehost, another server) and talk to PIBASE over HTTPS.

```
 visitors ──► client.com (the website, hosted anywhere)
                 │  JS client / pibase-auth.js 
                 ▼
          pibase.client.com ─┐
          api.yourname.com  ─┼─► droplet: Caddy (HTTPS) ─► PIBASE gateway ─► Postgres
                             │
 you, your clients ──► app.yourname.com ──► Caddy (HTTPS) ─► PIBASE dashboard
 (browser or phone app)    (optional; without it, SSH tunnel only)
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
git clone https://github.com/515woodpl-dot/pibase.git
cd pibase
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

Two ways, chosen when you run the setup:

* **Public** (`--dashboard app.yourname.com`): the dashboard has its own HTTPS
  address. People sign up and manage their projects from any browser, and can
  install it on their phone. See "Public dashboard" below for
  the spam protection and email it needs.
* **Private** (the default): the dashboard never faces the internet.

For the private dashboard, from your own computer:

```sh
ssh -L 18081:127.0.0.1:18081 pibase@DROPLET_IP
```

Then open http://127.0.0.1:18081/, log in and create a project. Forgot the
password? `./scripts/create-account.sh --reset omnia@local.com` as `pibase`
in `/home/pibase/pibase`, or see PI-OPERATIONS.md for email reset links. For the
operator view (`/app#operator`), the token is the `PIBASE_OPERATOR_TOKEN` line
in `/home/pibase/pibase/.env`.

## Public dashboard

Run the setup with `--dashboard app.yourname.com` (again on an existing server
is fine), after adding an A record for that name to the droplet. Caddy serves
it over HTTPS with HSTS, cookies are marked Secure, and links in emails point
to it.

**Email first.** Public sign-up asks every new account to confirm its email
before it can create projects, so the server needs an email service. Until
`PIBASE_SMTP_*` is set, the setup leaves sign-up **closed**
(`PIBASE_SIGNUP=closed`) and existing accounts can still log in. To open it,
add to `/home/pibase/pibase/.env` (any SMTP service works, for example Resend,
Postmark or Amazon SES):

```sh
PIBASE_SMTP_HOST=smtp.resend.com
PIBASE_SMTP_PORT=587
PIBASE_SMTP_USER=resend
PIBASE_SMTP_PASS=your-api-key
PIBASE_SMTP_FROM=no-reply@yourname.com
PIBASE_SIGNUP=open
```

then, as `pibase`: `cd /home/pibase/pibase && docker compose up -d`.

**Spam protection**, all on by default:

| Layer | What it stops |
|---|---|
| Signed form token, at least 3 seconds old | scripts that post straight to the sign-up endpoint |
| Hidden field people never see | form-filling bots |
| Throwaway email domains refused | disposable inboxes; add more with `PIBASE_BLOCKED_EMAIL_DOMAINS=a.com,b.com` |
| 3 new accounts per network address per day; 5 attempts a minute | one person making many accounts |
| 30 new accounts per hour for the whole server (`PIBASE_SIGNUP_HOURLY_LIMIT`) | a burst from many addresses filling the disk |
| Email confirmation before creating projects | fake addresses; unconfirmed accounts without projects are removed after 2 days |
| 2 projects per account (`PIBASE_MAX_PROJECTS_PER_ACCOUNT`), 1 GB each | one account using the whole server |

The operator view (`https://app.yourname.com/app#operator`, with
`PIBASE_OPERATOR_TOKEN` from `.env`) lists every account, newest first, with a
**Not confirmed only** filter, and deletes spam accounts with their projects.
To stop new sign-ups at any time, set `PIBASE_SIGNUP=closed` and run
`docker compose up -d`.

**On a phone.** Open `https://app.yourname.com` and install it: on Android,
Chrome's menu, **Install app** (or **Add to Home screen**); on iPhone, Safari's
Share button, **Add to Home Screen**. It opens full screen with its own icon.
It stores no data on the phone: everything comes from the server each time,
and without a connection it shows a short "You're offline" page.

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

## Moving projects from a Pi

Export each project on the Pi (Settings, Move to another server), then import
the file on the droplet (Projects, Import a project). Keys and signed-in users
come along, so the app only needs the new address. See "Moving a project to
another server" in [PI-OPERATIONS.md](PI-OPERATIONS.md).

## 8. Update

```sh
sudo -u pibase /home/pibase/pibase/scripts/update.sh
```

It backs everything up, pulls the latest version of the branch the server
is on (`main` normally), rebuilds, restarts and checks
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
sudo -u pibase git -C /home/pibase/pibase remote set-url origin git@github.com:515woodpl-dot/pibase.git
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
  Never set `PIBASE_GATEWAY_BIND` or `PIBASE_ADMIN_BIND` to `0.0.0.0` here; a
  public dashboard goes through Caddy (`--dashboard`), never a bare port.
* Root login with an SSH key still works after setup. Once you can log in as
  `pibase`, you may turn off root SSH login, but keep a way in (DigitalOcean's
  web console) before you do.
