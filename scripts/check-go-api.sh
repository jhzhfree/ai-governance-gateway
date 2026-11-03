#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
go_bin="${GO_BIN:-go}"
generated="$(mktemp)"
trap 'rm -f "$generated"' EXIT
"$go_bin" tool oapi-codegen -generate types -package clienttypes api/openapi/management.json > "$generated"
if ! cmp -s "$generated" api/clienttypes/types.gen.go; then
  echo 'Go API types differ from OpenAPI. Regenerate api/clienttypes/types.gen.go with the pinned oapi-codegen tool.' >&2
  exit 1
fi
