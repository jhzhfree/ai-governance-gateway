#!/usr/bin/env bash
# Linux/local Docker verification. Uses a disposable database unless an explicit
# TEST_DATABASE_URL is supplied; never prints connection strings or credentials.
set -euo pipefail
cd "$(dirname "$0")/.."

go_bin="${GO_BIN:-go}"
if ! "$go_bin" version >/dev/null 2>&1; then
  echo "Go compiler unavailable; install the version in .go-version or set GO_BIN." >&2
  exit 1
fi
gofmt_bin="$("$go_bin" env GOROOT)/bin/gofmt"
formatting="$(find api cmd internal -name '*.go' -print0 | xargs -0 "$gofmt_bin" -l)"
if [[ -n "$formatting" ]]; then
  echo "Run gofmt on:" >&2
  echo "$formatting" >&2
  exit 1
fi

container_id=""
docker_local() {
  env -u DOCKER_HOST -u DOCKER_CONTEXT -u DOCKER_TLS -u DOCKER_TLS_VERIFY -u DOCKER_CERT_PATH \
    docker --host=unix:///var/run/docker.sock "$@"
}
cleanup() {
  if [[ -n "$container_id" ]]; then
    docker_local rm -f -v "$container_id" >/dev/null || true
  fi
}
trap cleanup EXIT

if [[ -z "${TEST_DATABASE_URL:-}" ]]; then
  # Trust is limited to this throwaway fixture on loopback, never production.
  container_id="$(docker_local run -d --rm \
    -e POSTGRES_HOST_AUTH_METHOD=trust -e POSTGRES_DB=aigw_test \
    -p 127.0.0.1::5432 \
    postgres:17.6-bookworm@sha256:f3bd19c606e442c3d7bdfa8002e03fe260a1023351e0ea4598032022b68dd6e3)"
  ready=false
  for ((attempt=0; attempt<30; attempt++)); do
    if docker_local exec "$container_id" pg_isready -h 127.0.0.1 -U postgres -d aigw_test >/dev/null 2>&1; then
      ready=true
      break
    fi
    sleep 1
  done
  if [[ "$ready" != true ]]; then
    echo "Disposable PostgreSQL did not become ready." >&2
    exit 1
  fi
  published="$(docker_local port "$container_id" 5432/tcp)"
  port="${published##*:}"
  export TEST_DATABASE_URL="postgres://postgres@127.0.0.1:${port}/aigw_test?sslmode=disable"
fi

"$go_bin" mod verify
"$go_bin" vet -mod=readonly ./...
"$go_bin" test -mod=readonly -race -count=1 -timeout=120s -v ./...
