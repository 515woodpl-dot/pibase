# Raspberry Pi operations

Use 64-bit Raspberry Pi OS on a Pi 4 with at least 4 GB RAM. Put PostgreSQL,
object storage, and backups on a USB3 SSD using ext4; an SD card is not an
acceptable database volume.

Run `scripts/setup-pi.sh` once as the non-root operator. After logging in again:

```sh
PIBASE_DATA_ROOT=/mnt/pibase-ssd/pibase ./scripts/init-env.sh
# init-env.sh generates independent operator and backup secrets and persists
# PIBASE_DATA_ROOT in .env. Use the same SSD path passed to setup-pi.sh.
docker compose -f compose.yaml -f compose.pi.yaml up --build -d
curl --fail http://127.0.0.1:18080/healthz
curl --fail http://127.0.0.1:18080/metrics
```

The application services run as the non-root host operator's UID and GID under
Compose. `init-env.sh` writes those IDs into `.env`, so the mounted `files` and
`backups` directories remain operator-owned: uploads and encrypted backups work
and the operator can list or copy `.pib` files without `sudo`. For an older
installation with fixed-container-owned files, rerun `setup-pi.sh` with the same
`PIBASE_DATA_ROOT` before starting the stack.

The one-shot `bootstrap` service also applies idempotent gateway-owned auth
schema upgrades to existing initialized projects before the gateway starts.
Back up first for normal production upgrades; M0 measurement-only databases,
which intentionally have no auth schema, are left unchanged.

Expose port 18080 only through the trusted LAN/Tailnet or an HTTPS reverse
proxy. The dashboard is a separate local listener at
`http://127.0.0.1:18081/`: a landing page where people sign up or log in, and the app at `/app`.
Operators use `/app#operator`; the token is entered in memory and is
never persisted by the page. By default it answers only on the Pi itself, so the Pi's LAN or Tailscale
address will not load it. Two ways to reach it from another machine:

- Tailscale: add `PIBASE_ADMIN_BIND=<Pi Tailscale IP>` to `.env` (find the IP
  with `tailscale ip -4`), run `docker compose up -d admin`, then open
  `http://<Pi Tailscale IP>:18081/`. Only devices on your tailnet can
  connect, and every action still needs a login or the operator token.
- SSH tunnel, no configuration change: `ssh -L 18081:127.0.0.1:18081 pi@PI_HOST`,
  then open `http://127.0.0.1:18081/` on your own machine.

Never set `PIBASE_ADMIN_BIND=0.0.0.0` on a machine reachable from the internet,
and never route the dashboard through the public gateway. Rotate the operator token after suspected browser
or host compromise.

The droplet helper scripts also work on the Pi, from the repository folder:
`./scripts/backup-all.sh` backs up every project into `$PIBASE_DATA_ROOT/backups`
(keeping 14 days), and `./scripts/update.sh` backs up, pulls `main`, rebuilds and
restarts. Both use the same `compose.yaml` + `compose.pi.yaml` layout as the
commands below.

Backups are encrypted before they reach the operator-selected destination:

```sh
docker compose -f compose.yaml -f compose.pi.yaml --profile tools run --rm \
  ops backup PROJECT_ID /data/backups

PIBASE_RESTORE_CONFIRM=PROJECT_ID docker compose \
  -f compose.yaml -f compose.pi.yaml --profile tools run --rm \
  -e PIBASE_RESTORE_CONFIRM ops restore PROJECT_ID \
  /data/backups/pibase-PROJECT_ID-TIME.pib
```

Keep the backup key outside the backup destination and test a restore quarterly.
A restore replaces the selected project database and file tree; the command
requires an explicit project-id confirmation and rejects backups for a different
project. Before changing anything, restore writes an encrypted
`pre-restore-PROJECT_ID-TIME.pib` safety backup beside the selected archive;
if that backup fails, restore stops without changing the project. Ensure the
backup filesystem has free space for this additional full archive. Stop the
gateway during restore so no writes race with the swap. Restore drops the four
managed schemas (`auth`, `storage`, `realtime`, and `cron`) with `CASCADE`,
then restores the archive and reapplies the canonical schema upgrade. This
allows archives from before later managed-schema additions to be restored
without mixing old and new rows.
Restore targets an existing pibase project database. Its operator-owned
extension guard lives in `pg_catalog`, is intentionally absent from `pg_dump`,
and remains in place while the archive is restored; the archive neither creates
nor replaces that guard. Restore reapplies the canonical project-schema upgrade
so archives taken before M6c return with operator-owned managed schemas.
If the post-restore upgrade fails (for example, because the archive contains
customer-created objects in managed schemas), restore still completes the
database and file swap, prints a warning on stderr naming the project, and
exits zero. Run `project-list` afterward: a non-empty `schema_error` means the
project needs operator attention before the gateway serves it.
Provision projects with `project-create` rather than restoring into a manually
created database.

### New public tables

Tables created by the project owner in `public` automatically grant the API
roles table access and enable row-level security. The service key bypasses RLS;
anon and authenticated keys return no rows until the owner creates a policy.
Owners who deliberately disable RLS expose the table to the anon key because it
already has table privileges. Tables that existed before this upgrade are not
changed. A table moved into `public` later is also unchanged and unreachable
by API roles until its owner grants access. To expose an existing or moved
table, run as the project owner:

```sql
GRANT SELECT, INSERT, UPDATE, DELETE ON public.your_table
  TO p_PROJECT_anon, p_PROJECT_authenticated, p_PROJECT_service_role;
```

Emergency M6c ownership rollback is per project and restores only the version 7
schema ownership boundary. Stop the gateway first. This deliberately gives the
project admin `CREATE` again and should be used only while diagnosing an upgrade:

```sh
psql "$PROJECT_DATABASE_URL" -v admin_role="$PROJECT_ADMIN_ROLE" <<'SQL'
BEGIN;
ALTER SCHEMA auth OWNER TO :"admin_role";
ALTER SCHEMA storage OWNER TO :"admin_role";
ALTER SCHEMA realtime OWNER TO :"admin_role";
ALTER SCHEMA cron OWNER TO :"admin_role";
COMMIT;
SQL
psql "$CONTROL_DATABASE_URL" -v project_id="$PROJECT_ID" <<'SQL'
UPDATE control.projects
SET schema_version=7, schema_error=NULL
WHERE id=:'project_id';
SQL
```

Restarting bootstrap reapplies version 8. Back up before rollback; database
ownership and managed-object ownership are unchanged by this snippet.

For unattended backups, use a root-owned environment file and systemd timer per
project. Copy encrypted `.pib` files to a second device or S3-compatible remote;
local SSD backups alone do not protect against device loss.

### Optional Docker memory cgroup

PSS measurements from `/proc` work without changing boot configuration. Owners
who also want Docker memory limits and `docker stats` memory figures can add
`cgroup_enable=memory cgroup_memory=1` to the single line in `/boot/cmdline.txt`
and reboot. This is optional and is not changed by `setup-pi.sh`.

Project branches copy only `public` schema definitions and rows into a new
project with fresh roles and API/JWT keys:

```sh
docker compose -f compose.yaml -f compose.pi.yaml --profile tools run --rm ops \
  project-branch SOURCE_PROJECT_ID "feature-name"
```

Auth users, sessions, storage metadata, and object files are intentionally not
copied. This prevents a development branch from inheriting production identity
or private-file data. Role-specific public RLS policies are rewritten only in
policy declarations; ordinary row values are never text-rewritten.
