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
iterations="${AIGW_BENCH_ITERATIONS:-50}"
cpus="${AIGW_BENCH_CPUS:-1,4}"
if [[ ! "$iterations" =~ ^[0-9]+$ ]] || (( iterations < 1 || iterations > 10000 )) || [[ ! "$cpus" =~ ^[0-9]+(,[0-9]+)*$ ]]; then
  echo "Invalid benchmark iterations or CPU list." >&2
  exit 1
fi
export GOTOOLCHAIN=local
"$go_bin" test -mod=readonly ./internal/newapibridge -run '^$' \
  -bench "${AIGW_BENCH_PATTERN:-^BenchmarkNewAPIGovernance$}" -benchtime="${iterations}x" -cpu="$cpus" \
  -count=1 -benchmem -timeout=300s | tee "$results"
# Some Go benchmark runs report a passing package despite failed sub-benchmarks.
if grep -q -- '--- FAIL:' "$results"; then
  echo "A benchmark scenario failed; reject this baseline." >&2
  exit 1
fi
