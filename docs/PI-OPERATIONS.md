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

The one-shot `bootstrap` service also applies idempotent gateway-owned auth
schema upgrades to existing initialized projects before the gateway starts.
Back up first for normal production upgrades; M0 measurement-only databases,
which intentionally have no auth schema, are left unchanged.

Expose port 18080 only through the trusted LAN/Tailnet or an HTTPS reverse
proxy. The dashboard is at `/dashboard`; its token is entered in memory and is
never persisted by the page. Rotate the operator token after suspected browser
or host compromise.

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
project. Stop the gateway during restore so no writes race with the swap.
Restore targets an existing pibase project database. Its operator-owned
extension guard lives in `pg_catalog`, is intentionally absent from `pg_dump`,
and remains in place while the archive is restored; the archive neither creates
nor replaces that guard. Provision projects with `project-create` rather than
restoring into a manually created database.

For unattended backups, use a root-owned environment file and systemd timer per
project. Copy encrypted `.pib` files to a second device or S3-compatible remote;
local SSD backups alone do not protect against device loss.

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
