#!/bin/sh
set -eu

cd "$(dirname "$0")/.."
./scripts/init-env.sh
docker run --rm -v "$PWD:/src" -v pibase-go-mod:/go/pkg/mod -v pibase-go-build:/root/.cache/go-build -w /src golang:1.23-bookworm sh -c 'go mod tidy && gofmt -w cmd internal && go vet ./... && go test ./...'
if ! docker image inspect pibase:m2 >/dev/null 2>&1; then
  docker build --tag pibase:m2 .
fi
docker tag pibase:m2 pibase-m0-bootstrap:latest
docker tag pibase:m2 pibase-m0-gateway:latest
docker compose up -d postgres
docker compose run --rm bootstrap
postgres_password="${POSTGRES_PASSWORD:-$(sed -n 's/^POSTGRES_PASSWORD=//p' .env)}"
restore_authenticator() {
  docker compose run --rm bootstrap >/dev/null
}
trap restore_authenticator EXIT INT TERM
docker run --rm --network host -v "$PWD:/src" -v pibase-go-mod:/go/pkg/mod -v pibase-go-build:/root/.cache/go-build -w /src \
  -e PIBASE_TEST_ADMIN_DATABASE_URL="postgres://postgres:$postgres_password@127.0.0.1:55432/postgres" \
  golang:1.23-bookworm go test -p 1 -count=1 ./internal/control ./internal/gateway
restore_authenticator
trap - EXIT INT TERM
docker run --rm -v "$PWD:/src" -v pibase-go-mod:/go/pkg/mod -v pibase-go-build:/root/.cache/go-build -w /src -e GOOS=linux -e GOARCH=arm64 -e CGO_ENABLED=0 \
  golang:1.23-bookworm go build -buildvcs=false -trimpath -o /tmp/pibase-linux-arm64 ./cmd/pibase
