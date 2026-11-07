#!/usr/bin/env bash
# Destructive only to newly generated test fixture schemas. Never use production.
set -euo pipefail
cd "$(dirname "$0")/.."
if [[ -z "${TEST_DATABASE_URL:-}" ]]; then
  echo 'Set TEST_DATABASE_URL to a dedicated disposable test database.' >&2
  exit 1
fi
for tool in pg_dump pg_restore; do
  command -v "$tool" >/dev/null || { echo "$tool required on PATH (compatible with test server)." >&2; exit 1; }
done
go_bin="${GO_BIN:-go}"
expected="$(cat .go-version)"
if [[ "$("$go_bin" env GOVERSION)" != "go$expected" ]]; then
  echo "Use pinned Go $expected." >&2
  exit 1
fi
"$go_bin" test -mod=readonly -race -tags=restore_e2e -run '^TestLogicalBackupRestore$' -count=1 -timeout=240s -v ./internal/database
