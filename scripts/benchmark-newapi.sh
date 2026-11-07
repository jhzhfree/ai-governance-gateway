#!/usr/bin/env bash
# Synthetic two-process baseline; never run against a production database.
set -euo pipefail
cd "$(dirname "$0")/.."
go_bin="${GO_BIN:-go}"
required="go$(tr -d '\n' < .go-version)"
if ! actual="$("$go_bin" version 2>/dev/null)" || [[ "$actual" != "go version $required "* ]]; then
  echo "Pinned Go toolchain required; set GO_BIN." >&2
  exit 1
fi
if [[ -z "${TEST_DATABASE_URL:-}" || ! -x "${TEST_NEWAPI_SERVER_BINARY:-}" ]]; then
  echo "Dedicated TEST_DATABASE_URL and built TEST_NEWAPI_SERVER_BINARY required." >&2
  exit 1
fi
results="$(mktemp)"
trap 'rm -f "$results"' EXIT
export GOTOOLCHAIN=local
"$go_bin" test -mod=readonly ./internal/newapibridge -run '^$' \
  -bench '^BenchmarkNewAPIGovernance$' -benchtime=50x -cpu=1,4 \
  -count=1 -benchmem -timeout=300s | tee "$results"
# Some Go benchmark runs report a passing package despite failed sub-benchmarks.
if grep -q -- '--- FAIL:' "$results"; then
  echo "A benchmark scenario failed; reject this baseline." >&2
  exit 1
fi
