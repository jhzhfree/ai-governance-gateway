#!/usr/bin/env bash
# Fixed-size synthetic baseline; isolated PostgreSQL schemas are cleaned up.
set -euo pipefail
cd "$(dirname "$0")/.."
go_bin="${GO_BIN:-go}"
required="go$(tr -d '\n' < .go-version)"
if ! actual="$("$go_bin" version 2>/dev/null)" || [[ "$actual" != "go version $required "* ]]; then
  echo "Required Go compiler: $required; set GO_BIN to the compiler path." >&2
  exit 1
fi
if [[ -z "${TEST_DATABASE_URL:-}" ]]; then
  echo "TEST_DATABASE_URL required: use a dedicated disposable database." >&2
  exit 1
fi
export GOTOOLCHAIN=local
"$go_bin" test -mod=readonly ./gatewaybridge -run '^$' \
  -bench '^BenchmarkGovernedGateway$' -benchtime=50x -cpu=1,4 \
  -count=1 -benchmem -timeout=120s
