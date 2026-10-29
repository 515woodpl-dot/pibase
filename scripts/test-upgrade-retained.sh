#!/bin/sh
set -eu

cd "$(dirname "$0")/.."

# Disposable upgrade drill: builds the old M5b binary, creates projects with
# data, then builds the current binary and verifies upgrade, ownership, data,
# and restore. Uses only Docker — no host psql required.

repo_root="$PWD"
old_commit="origin/pibase/m5b"
postgres_password="$(od -An -N16 -tx1 /dev/urandom | tr -d ' \n')"
auth_password="upgrade-drill-auth"
volume_name="pibase-upgrade-drill-$$"
postgres_container="pibase-upgrade-drill-pg-$$"
gateway_container="pibase-upgrade-drill-gw-$$"
postgres_image="${PIBASE_TEST_POSTGRES_IMAGE:-pibase-postgres:test}"
old_image="pibase:upgrade-drill-old-$$"
old_ops_image="pibase:upgrade-drill-old-ops-$$"
new_image="${PIBASE_IMAGE:-pibase:upgrade-drill-new-$$}"
worktree=""
storage_root=""
backup_dir=""
backup_key="$(head -c 32 /dev/urandom | base64 | tr -d '\n')"
master_key="$(head -c 32 /dev/urandom | base64 | tr -d '\n')"

cleanup() {
  docker rm --force "$postgres_container" >/dev/null 2>&1 || true
  docker rm --force "$gateway_container" >/dev/null 2>&1 || true
  docker volume rm --force "$volume_name" >/dev/null 2>&1 || true
  if [ -n "$worktree" ] && [ -d "$worktree" ]; then
    git worktree remove --force "$worktree" 2>/dev/null || rm -rf "$worktree"
  fi
  if [ -n "$storage_root" ] && [ -d "$storage_root" ]; then
    rm -r "$storage_root"
  fi
  if [ -n "$backup_dir" ] && [ -d "$backup_dir" ]; then
    rm -r "$backup_dir"
  fi
}
trap cleanup EXIT INT TERM

# Run psql inside the postgres container
dbexec() {
  docker exec "$postgres_container" psql -U postgres -tAq "$@"
}

# Extract a JSON string field from single-line or pretty-printed JSON
json_field() {
  sed -n 's/.*"'"$1"'"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' | head -1
}

wait_gateway() {
  i=0
  until curl --fail --silent http://127.0.0.1:28081/healthz >/dev/null 2>&1; do
    i=$((i+1))
    if [ "$i" -ge 30 ]; then
      docker logs "$gateway_container"
      return 1
    fi
    sleep 1
  done
}

echo "=== Step 1: Build old M5b binary ==="
worktree="$(mktemp -d)"
git worktree add --detach "$worktree" "$old_commit"
docker build --target ops --tag "$old_ops_image" "$worktree"
docker build --tag "$old_image" "$worktree"

echo "=== Step 2: Build current binary ==="
new_ops_image="${new_image}-ops"
docker build --target ops --tag "$new_ops_image" "$repo_root"
docker build --tag "$new_image" "$repo_root"
docker build --file "$repo_root/Dockerfile.postgres" --tag "$postgres_image" "$repo_root"

echo "=== Step 3: Start disposable Postgres with named volume ==="
docker volume create "$volume_name"
docker run --rm --detach --name "$postgres_container" \
  --env POSTGRES_PASSWORD="$postgres_password" \
  --mount "type=volume,src=$volume_name,dst=/var/lib/postgresql/data" \
  --publish 127.0.0.1::5432 \
  "$postgres_image" \
  postgres -c shared_buffers=128MB -c max_connections=40 -c work_mem=4MB >/dev/null
i=0
until docker exec "$postgres_container" pg_isready -U postgres >/dev/null 2>&1; do
  i=$((i+1))
  [ "$i" -lt 30 ] || { docker logs "$postgres_container"; exit 1; }
  sleep 1
done
pg_port="$(docker port "$postgres_container" 5432 | sed 's/.*://')"
admin_url="postgres://postgres:${postgres_password}@127.0.0.1:${pg_port}/postgres"
internal_url="postgres://postgres:${postgres_password}@127.0.0.1:5432/postgres"

echo "=== Step 4: Bootstrap and create projects with old binary ==="
storage_root="$(mktemp -d "$(dirname "$repo_root")/.pibase-upgrade-storage.XXXXXX")"
backup_dir="$(mktemp -d "$(dirname "$repo_root")/.pibase-upgrade-backups.XXXXXX")"
chmod 777 "$storage_root" "$backup_dir"

docker run --rm --network host \
  --env PIBASE_ADMIN_DATABASE_URL="$admin_url" \
  --env PIBASE_AUTHENTICATOR_PASSWORD="$auth_password" \
  "$old_image" bootstrap

project1_json="$(docker run --rm --network host \
  --env PIBASE_ADMIN_DATABASE_URL="$admin_url" \
  --env PIBASE_MASTER_KEY="$master_key" \
  "$old_image" project-create drill-project-1)"
project1_id="$(echo "$project1_json" | json_field id)"
project1_db="$(echo "$project1_json" | json_field database)"
project1_anon="$(echo "$project1_json" | json_field anon_key)"
project1_service="$(echo "$project1_json" | json_field service_key)"
echo "Project 1: id=$project1_id db=$project1_db"

project2_json="$(docker run --rm --network host \
  --env PIBASE_ADMIN_DATABASE_URL="$admin_url" \
  --env PIBASE_MASTER_KEY="$master_key" \
  "$old_image" project-create drill-project-2)"
project2_id="$(echo "$project2_json" | json_field id)"
project2_db="$(echo "$project2_json" | json_field database)"
project2_service="$(echo "$project2_json" | json_field service_key)"
echo "Project 2: id=$project2_id db=$project2_db"

auth_db_url="postgres://authenticator:${auth_password}@127.0.0.1:${pg_port}/_control"

echo "=== Step 5: Create schema and retained data through the old HTTP API ==="
p1_admin="p_${project1_id}_admin"
p2_admin="p_${project2_id}_admin"
printf 'CREATE TABLE public.items(name text PRIMARY KEY); GRANT SELECT, INSERT ON public.items TO p_%s_service_role;' "$project1_id" | docker run --rm -i --network host \
  --env PIBASE_DATABASE_URL="$auth_db_url" "$old_ops_image" migration-apply "$project1_id" 0001_items
printf 'CREATE TABLE public.tasks(title text PRIMARY KEY); GRANT SELECT, INSERT ON public.tasks TO p_%s_service_role;' "$project2_id" | docker run --rm -i --network host \
  --env PIBASE_DATABASE_URL="$auth_db_url" "$old_ops_image" migration-apply "$project2_id" 0001_tasks
docker run --rm --detach --name "$gateway_container" --network host \
  --env PIBASE_DATABASE_URL="$auth_db_url" --env PIBASE_MASTER_KEY="$master_key" \
  --env PIBASE_AUTH_EXPOSE_TOKENS=true --env PIBASE_ENV=test \
  --env PIBASE_LISTEN_ADDR=:28081 --env PIBASE_STORAGE_ROOT="$storage_root" \
  -v "$storage_root:$storage_root" "$old_ops_image" serve
wait_gateway
signup_file="$(mktemp)"
if ! curl --fail --silent --show-error -X POST http://127.0.0.1:28081/auth/v1/signup -H "apikey: $project1_anon" -H 'Content-Type: application/json' -d '{"email":"drill@example.test","password":"correct horse battery staple"}' --output "$signup_file"; then
  echo "signup failed: $(cat "$signup_file")" >&2
  exit 1
fi
signup="$(cat "$signup_file")"
confirmation="$(echo "$signup" | json_field confirmation_token)"
curl --fail --silent "http://127.0.0.1:28081/auth/v1/verify?token=$confirmation&type=signup" -H "apikey: $project1_anon" >/dev/null
old_login="$(curl --fail --silent -X POST 'http://127.0.0.1:28081/auth/v1/token?grant_type=password' -H "apikey: $project1_anon" -H 'Content-Type: application/json' -d '{"email":"drill@example.test","password":"correct horse battery staple"}')"
user_token="$(echo "$old_login" | json_field access_token)"
curl --fail --silent -X POST http://127.0.0.1:28081/rest/v1/items -H "apikey: $project1_service" -H 'Content-Type: application/json' -H 'Prefer: return=representation' -d '{"name":"alpha"}' >/dev/null
curl --fail --silent -X POST http://127.0.0.1:28081/rest/v1/items -H "apikey: $project1_service" -H 'Content-Type: application/json' -H 'Prefer: return=representation' -d '{"name":"beta"}' >/dev/null
curl --fail --silent -X POST http://127.0.0.1:28081/rest/v1/tasks -H "apikey: $project2_service" -H 'Content-Type: application/json' -H 'Prefer: return=representation' -d '[{"title":"task-one"}]' >/dev/null
curl --fail --silent -X POST http://127.0.0.1:28081/storage/v1/bucket -H "apikey: $project1_service" -H 'Content-Type: application/json' -d '{"id":"drill-files","public":true}' >/dev/null
curl --fail-with-body --silent --show-error -X POST http://127.0.0.1:28081/storage/v1/object/drill-files/drill-object.txt -H "apikey: $project1_anon" -H "Authorization: Bearer $user_token" -H 'Content-Type: text/plain' --data 'drill-file-content'
dbexec -d "$project1_db" -c "INSERT INTO cron.jobs(schedule,sql) VALUES ('0 * * * *', 'SELECT 1');"

echo "=== Step 6: Backup project 1 with old binary (pre-upgrade) ==="
docker run --rm --network host \
  --env PIBASE_ADMIN_DATABASE_URL="$admin_url" \
  --env PIBASE_BACKUP_KEY="$backup_key" \
  --env PIBASE_STORAGE_ROOT="$storage_root" \
  -v "$storage_root:$storage_root" \
  -v "$backup_dir:$backup_dir" \
  "$old_ops_image" backup "$project1_id" "$backup_dir"

pre_upgrade_backup="$(ls "$backup_dir"/*.pib | head -1)"
echo "Pre-upgrade backup: $pre_upgrade_backup"

echo "=== Step 7: Bootstrap with current binary (upgrade) ==="
docker rm --force "$gateway_container" >/dev/null
docker run --rm --network host \
  --env PIBASE_ADMIN_DATABASE_URL="$admin_url" \
  --env PIBASE_AUTHENTICATOR_PASSWORD="$auth_password" \
  "$new_image" bootstrap

docker run --rm --detach --name "$gateway_container" --network host \
  --env PIBASE_DATABASE_URL="$auth_db_url" --env PIBASE_MASTER_KEY="$master_key" \
  --env PIBASE_LISTEN_ADDR=:28081 --env PIBASE_STORAGE_ROOT="$storage_root" \
  -v "$storage_root:$storage_root" "$new_ops_image" serve
wait_gateway

echo "=== Step 8: Verify upgrade ==="
passed=true
check() {
  if [ "$1" != "$2" ]; then
    echo "FAIL: $3: got '$1', want '$2'"
    passed=false
  else
    echo "OK: $3"
  fi
}

p1_version="$(dbexec -d _control -c "SELECT schema_version FROM control.projects WHERE id='$project1_id'")"
p2_version="$(dbexec -d _control -c "SELECT schema_version FROM control.projects WHERE id='$project2_id'")"
check "$p1_version" "15" "project 1 schema version"
check "$p2_version" "15" "project 2 schema version"

p1_owners="$(dbexec -d "$project1_db" -c "SELECT string_agg(nspname||'='||pg_get_userbyid(nspowner),',' ORDER BY nspname) FROM pg_namespace WHERE nspname IN ('auth','storage','realtime','cron')")"
operator="postgres"
check "$p1_owners" "auth=$operator,cron=$operator,realtime=$operator,storage=$operator" "project 1 schema owners"

p1_rows="$(dbexec -d "$project1_db" -c "SELECT count(*) FROM public.items")"
check "$p1_rows" "2" "project 1 data rows"

p2_rows="$(dbexec -d "$project2_db" -c "SELECT count(*) FROM public.tasks")"
check "$p2_rows" "1" "project 2 data rows"

p1_cron="$(dbexec -d "$project1_db" -c "SELECT count(*) FROM cron.jobs")"
check "$p1_cron" "1" "project 1 cron jobs"

new_login="$(curl --fail --silent -X POST 'http://127.0.0.1:28081/auth/v1/token?grant_type=password' -H "apikey: $project1_anon" -H 'Content-Type: application/json' -d '{"email":"drill@example.test","password":"correct horse battery staple"}')"
user_token="$(echo "$new_login" | json_field access_token)"
check "${user_token:+present}" "present" "project 1 old user sign-in"
check "$(curl --fail --silent http://127.0.0.1:28081/rest/v1/items?select=name -H "apikey: $project1_service" | tr -d '[:space:]')" '[{"name":"alpha"},{"name":"beta"}]' "project 1 REST read"
check "$(curl --fail --silent http://127.0.0.1:28081/storage/v1/object/public/drill-files/drill-object.txt)" "drill-file-content" "project 1 object download"

p1_forbidden="$(dbexec -d "$project1_db" -c "
SELECT count(*) FROM pg_class c
JOIN pg_namespace n ON n.oid = c.relnamespace
CROSS JOIN LATERAL aclexplode(COALESCE(c.relacl, acldefault('r', c.relowner))) privilege
JOIN pg_roles grantee ON grantee.oid = privilege.grantee
WHERE n.nspname IN ('storage','cron') AND c.relkind = 'r'
  AND grantee.rolname = '$p1_admin'
  AND privilege.privilege_type IN ('TRIGGER','REFERENCES','TRUNCATE')")"
check "$p1_forbidden" "0" "project 1 no TRIGGER/REFERENCES/TRUNCATE on storage/cron"

echo "=== Step 9: Restore old backup into project 1 ==="
docker run --rm --network host \
  --env PIBASE_ADMIN_DATABASE_URL="$admin_url" \
  --env PIBASE_BACKUP_KEY="$backup_key" \
  --env PIBASE_STORAGE_ROOT="$storage_root" \
  --env PIBASE_RESTORE_CONFIRM="$project1_id" \
  -v "$storage_root:$storage_root" \
  -v "$backup_dir:$backup_dir" \
  "$new_ops_image" restore "$project1_id" "$pre_upgrade_backup"

p1_restored_version="$(dbexec -d _control -c "SELECT schema_version FROM control.projects WHERE id='$project1_id'")"
check "$p1_restored_version" "15" "project 1 restored schema version"

p1_restored_rows="$(dbexec -d "$project1_db" -c "SELECT count(*) FROM public.items")"
check "$p1_restored_rows" "2" "project 1 restored data rows"

p1_restored_owners="$(dbexec -d "$project1_db" -c "SELECT string_agg(nspname||'='||pg_get_userbyid(nspowner),',' ORDER BY nspname) FROM pg_namespace WHERE nspname IN ('auth','storage','realtime','cron')")"
check "$p1_restored_owners" "auth=$operator,cron=$operator,realtime=$operator,storage=$operator" "project 1 restored schema owners"

restored_login="$(curl --fail --silent -X POST 'http://127.0.0.1:28081/auth/v1/token?grant_type=password' -H "apikey: $project1_anon" -H 'Content-Type: application/json' -d '{"email":"drill@example.test","password":"correct horse battery staple"}')"
check "$(echo "$restored_login" | json_field access_token | sed 's/.*/present/')" "present" "project 1 restored old user sign-in"
check "$(curl --fail --silent http://127.0.0.1:28081/rest/v1/items?select=name -H "apikey: $project1_service" | tr -d '[:space:]')" '[{"name":"alpha"},{"name":"beta"}]' "project 1 restored REST read"
check "$(curl --fail --silent http://127.0.0.1:28081/storage/v1/object/public/drill-files/drill-object.txt)" "drill-file-content" "project 1 restored object download"

echo ""
if [ "$passed" = true ]; then
  echo "=== UPGRADE DRILL PASSED ==="
else
  echo "=== UPGRADE DRILL FAILED ==="
  exit 1
fi
