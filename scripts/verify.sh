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

# Nested Go modules are not included by the root ./... pattern.
GO_BIN="$go_bin" bash modules/engine/scripts/verify.sh

"$go_bin" mod verify
GO_BIN="$go_bin" bash scripts/check-go-api.sh
"$go_bin" vet -mod=readonly ./api/... ./cmd/... ./internal/...
# Go's timeout covers the entire package, not each individual test. The growing
# PostgreSQL/race and dual-process recovery suites can exceed ten minutes on
# shared runners. Keep one default for CI and local runs; per-operation and
# per-scenario failure deadlines remain unchanged.
test_timeout="${AIGW_GO_TEST_TIMEOUT:-900s}"
printf 'Backend package timeout: %s (race enabled)\n' "$test_timeout"
"$go_bin" test -mod=readonly -race -count=1 -timeout="$test_timeout" -v ./api/... ./cmd/... ./internal/...

if [[ "${AIGW_VERIFY_WEB:-0}" == 1 ]]; then
  npm --prefix web ci --no-audit --no-fund
  npm --prefix web run api:check
  npm --prefix web run build
  "$go_bin" test -mod=readonly -race -tags=web_e2e -run '^Test(ConsoleBrowser|LocalConsoleBrowser|NewAPIConsoleBrowser)$' -count=1 -timeout=360s -v ./internal/browser
fi

# Optional recovery gate requires compatible local PostgreSQL client tools.
# It operates only on generated fixture schemas in the dedicated test database.
if [[ "${AIGW_VERIFY_RESTORE:-0}" == 1 ]]; then
  GO_BIN="$go_bin" bash scripts/verify-restore.sh
fi

# Optional pinned SDK contract gate shares the same isolated database lifecycle.
if [[ "${AIGW_VERIFY_SDK:-0}" == 1 ]]; then
  "$go_bin" test -mod=readonly -race -tags=sdk_e2e -run '^Test(Responses|Messages)' -count=1 -timeout=120s -v ./internal/httpapi
fi
