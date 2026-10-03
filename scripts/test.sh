#!/bin/sh
set -eu

cd "$(dirname "$0")/.."
./scripts/test-setup-pi.sh
./scripts/init-env.sh
test_image="${PIBASE_TEST_IMAGE:-pibase:test-go1.23-pg16}"
docker build --target test --tag "$test_image" .
docker run --rm -v "$PWD:/src" -v pibase-go-mod:/go/pkg/mod -v pibase-go-build:/root/.cache/go-build -w /src "$test_image" sh -c 'go mod tidy && gofmt -w cmd internal && go vet ./... && go test ./...'
production_image="${PIBASE_IMAGE:-pibase:gate}"
docker build --tag "$production_image" .
postgres_password="${POSTGRES_PASSWORD:-$(sed -n 's/^POSTGRES_PASSWORD=//p' .env)}"
test_postgres_image="${PIBASE_TEST_POSTGRES_IMAGE:-pibase-postgres:test}"
docker build --file Dockerfile.postgres --tag "$test_postgres_image" .
test_postgres="pibase-gate-postgres-$$"
cleanup_test_postgres() {
  docker rm --force "$test_postgres" >/dev/null 2>&1 || true
}
trap cleanup_test_postgres EXIT INT TERM
docker run --rm --detach --name "$test_postgres" \
  --env POSTGRES_PASSWORD="$postgres_password" \
  --publish 127.0.0.1::5432 \
  "$test_postgres_image" \
  postgres -c shared_buffers=128MB -c max_connections=40 -c work_mem=4MB >/dev/null
i=0
until docker exec "$test_postgres" pg_isready -U postgres >/dev/null 2>&1; do
  i=$((i+1))
  [ "$i" -lt 30 ] || { docker logs "$test_postgres"; exit 1; }
  sleep 1
done
test_postgres_endpoint="$(docker port "$test_postgres" 5432/tcp)"
test_postgres_port="${test_postgres_endpoint##*:}"
docker run --rm --network host -v "$PWD:/src" -v pibase-go-mod:/go/pkg/mod -v pibase-go-build:/root/.cache/go-build -w /src \
  -e PIBASE_TEST_ADMIN_DATABASE_URL="postgres://postgres:$postgres_password@127.0.0.1:$test_postgres_port/postgres" \
  "$test_image" go test -p 1 -count=1 ./internal/control ./internal/gateway
cleanup_test_postgres
trap - EXIT INT TERM
docker run --rm -v "$PWD:/src" -v pibase-go-mod:/go/pkg/mod -v pibase-go-build:/root/.cache/go-build -w /src -e GOOS=linux -e GOARCH=arm64 -e CGO_ENABLED=0 \
  "$test_image" go build -buildvcs=false -trimpath -o /tmp/pibase-linux-arm64 ./cmd/pibase
