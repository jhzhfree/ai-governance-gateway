#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
go_bin="${GO_BIN:-go}"
required="go$(tr -d '\n' < .go-version)"
if ! actual="$("$go_bin" version 2>/dev/null)" || [[ "$actual" != "go version $required "* ]]; then
  echo "Required Go compiler: $required; set GO_BIN to the compiler path." >&2
  exit 1
fi
export GOTOOLCHAIN=local
export GOPATH="${GOPATH:-$PWD/.cache/go}"
export GOMODCACHE="${GOMODCACHE:-$GOPATH/pkg/mod}"
export GOCACHE="${GOCACHE:-$PWD/.cache/build}"
gofmt_bin="$("$go_bin" env GOROOT)/bin/gofmt"
formatting="$(find adapters execution gatewaybridge cmd -name '*.go' -print0 | xargs -0 "$gofmt_bin" -l)"
if [[ -n "$formatting" ]]; then printf 'Formatting required:\n%s\n' "$formatting"; exit 1; fi
if [[ -z "${TEST_DATABASE_URL:-}" ]]; then
  echo "TEST_DATABASE_URL required for governed execution integration; use root scripts/verify.sh." >&2
  exit 1
fi
"$go_bin" mod verify
"$go_bin" vet -mod=readonly ./...
# Timeout covers the whole package: the existing matrix measured up to 148s,
# and native Messages JSON measured 195s. The new native stream/fault matrix
# adds ~50s locally. Keep request
# and per-scenario deadlines unchanged; budget cumulative PostgreSQL/race work.
"$go_bin" test -mod=readonly -race -count=1 -timeout=360s ./...

"$go_bin" build -mod=readonly -o /dev/null ./cmd/gateway-engine
