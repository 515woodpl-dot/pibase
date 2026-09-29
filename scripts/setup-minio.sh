#!/usr/bin/env bash
# Set up MinIO for DATBase backups.
# Run once after "docker compose --profile minio up -d minio" and MinIO is healthy.
# Prints the BACKUP_S3_* lines to add to .env, then restart the app.
set -euo pipefail

HERE="$(cd "$(dirname "$0")" && pwd)"
ENV="$HERE/../.env"

# ── load .env ────────────────────────────────────────────────────────────────
if [[ ! -f "$ENV" ]]; then
  echo "ERROR: .env not found at $ENV — run from the datbase repo root." >&2
  exit 1
fi
# source only the vars we need, ignoring comment lines
eval "$(grep -E '^(MINIO_ROOT_USER|MINIO_ROOT_PASSWORD|DOMAIN|PUBLIC_SCHEME)=' "$ENV" | sed 's/^/export /')"

ROOT_USER="${MINIO_ROOT_USER:-datbase}"
ROOT_PASS="${MINIO_ROOT_PASSWORD:-}"
DOMAIN="${DOMAIN:-localhost}"
SCHEME="${PUBLIC_SCHEME:-https}"

if [[ -z "$ROOT_PASS" ]]; then
  echo "ERROR: MINIO_ROOT_PASSWORD is not set in .env" >&2
  exit 1
fi

BUCKET="datbase-backups"
ACCESS_KEY_NAME="datbase-backup-key"

# MinIO S3 API is on port 9000 of the host
MINIO_ENDPOINT="http://localhost:9000"

# ── wait for MinIO to be ready ────────────────────────────────────────────────
echo "Waiting for MinIO..."
for i in $(seq 1 30); do
  if curl -sf "$MINIO_ENDPOINT/minio/health/ready" > /dev/null 2>&1; then
    echo "MinIO is ready."
    break
  fi
  if [[ $i -eq 30 ]]; then
    echo "ERROR: MinIO did not become ready in 30 s. Is 'docker compose --profile minio up -d minio' running?" >&2
    exit 1
  fi
  sleep 1
done

# ── run mc inside the MinIO container (avoids needing mc installed on host) ──
MC="docker compose --profile minio exec -T minio mc"

# Configure mc alias
$MC alias set local http://localhost:9000 "$ROOT_USER" "$ROOT_PASS" --api s3v4

# Create bucket if it doesn't exist
if ! $MC ls "local/$BUCKET" > /dev/null 2>&1; then
  $MC mb "local/$BUCKET"
  echo "Created bucket: $BUCKET"
else
  echo "Bucket already exists: $BUCKET"
fi

# Enable versioning (protects against accidental deletes)
$MC version enable "local/$BUCKET"

# Create a dedicated access key (not the root credentials)
# mc admin accesskey create outputs JSON: {"accessKey":"...","secretKey":"..."}
KEY_JSON=$($MC admin accesskey create local --name "$ACCESS_KEY_NAME" --policy '{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":["s3:GetObject","s3:PutObject","s3:DeleteObject","s3:ListBucket"],"Resource":["arn:aws:s3:::'"$BUCKET"'","arn:aws:s3:::'"$BUCKET"'/*"]}]}' 2>/dev/null || true)

if [[ -z "$KEY_JSON" ]]; then
  # Fallback: list existing keys and use root creds if key creation not supported in this build
  echo "NOTE: Could not create a scoped access key (MinIO version may not support it)."
  echo "Using root credentials instead. Consider upgrading MinIO."
  ACCESS_KEY="$ROOT_USER"
  SECRET_KEY="$ROOT_PASS"
else
  ACCESS_KEY=$(echo "$KEY_JSON" | grep -o '"accessKey":"[^"]*"' | cut -d'"' -f4)
  SECRET_KEY=$(echo "$KEY_JSON" | grep -o '"secretKey":"[^"]*"' | cut -d'"' -f4)
fi

# The app reaches MinIO on the internal Docker network as "minio:9000"
INTERNAL_ENDPOINT="http://minio:9000"

echo ""
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo " Add these lines to your .env file:"
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo ""
echo "BACKUP_S3_ENDPOINT=$INTERNAL_ENDPOINT"
echo "BACKUP_S3_REGION=us-east-1"
echo "BACKUP_S3_BUCKET=$BUCKET"
echo "BACKUP_S3_KEY=$ACCESS_KEY"
echo "BACKUP_S3_SECRET=$SECRET_KEY"
echo ""
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo ""
echo "Then restart the app:  docker compose up -d app"
echo ""
echo "MinIO console: http://${DOMAIN}:9001  (login: $ROOT_USER / your MINIO_ROOT_PASSWORD)"
echo ""
echo "To verify backups are working: trigger one from the dashboard (Backups tab on any project)."
