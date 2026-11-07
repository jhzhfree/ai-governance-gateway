#!/usr/bin/env bash
# Build a native-engine candidate from committed source only. Never installs it.
set -euo pipefail
repo="$(git -C "$(dirname "$0")/.." rev-parse --show-toplevel)"
if [[ $# -ne 1 || "$1" != /* || -e "$1" || -L "$1" ]]; then
  echo 'Usage: bash scripts/package-linux.sh /absolute/new-artifact.tar.gz' >&2
  exit 1
fi
output="$1"
parent="$(dirname "$output")"
[[ -d "$parent" ]] || { echo 'Output parent directory must exist.' >&2; exit 1; }
[[ "$(uname -s)" == Linux ]] || { echo 'Linux build host required.' >&2; exit 1; }
go_bin="${GO_BIN:-go}"
commit="$(git -C "$repo" rev-parse HEAD)"
work="$(mktemp -d "$parent/.aigw-package.XXXXXX")"
trap 'rm -rf -- "$work"' EXIT
mkdir "$work/source" "$work/bundle"
git -C "$repo" archive "$commit" | tar -x -C "$work/source"
cd "$work/source"
[[ "$("$go_bin" env GOVERSION)" == "go$(cat .go-version)" ]] || { echo 'Pinned Go compiler required.' >&2; exit 1; }
[[ "$(node --version)" == "v$(cat .node-version)" ]] || { echo 'Pinned Node runtime required.' >&2; exit 1; }
# Local Vite environment overrides must never enter a distributable console.
while IFS= read -r variable; do unset "$variable"; done < <(compgen -v VITE_ || true)
unset GOFLAGS GOOS GOARCH CGO_ENABLED GOWORK
export CGO_ENABLED=0 GOWORK=off
arch="$("$go_bin" env GOARCH)"
case "$arch" in amd64|arm64) ;; *) echo 'Only native Linux amd64/arm64 builds are supported.' >&2; exit 1;; esac
bundle="$work/bundle"
mkdir "$bundle/bin" "$bundle/web" "$bundle/docs" "$bundle/deploy"
"$go_bin" mod verify
for command in gateway migrate local-admin content-audit aigw host-agent; do
  name="aigw-$command"
  [[ "$command" != aigw ]] || name=aigw
  "$go_bin" build -mod=readonly -trimpath -buildvcs=false -o "$bundle/bin/$name" "./cmd/$command"
done
npm --prefix web ci --no-audit --no-fund
npm --prefix web run api:check
npm --prefix web run build
cp -R web/dist/. "$bundle/web/"
cp .env.example "$bundle/config.env.example"
cp -R docs/. "$bundle/docs/"
mkdir -p "$bundle/api/openapi"
cp api/openapi/management.json "$bundle/api/openapi/"
cp deploy/newapi-compatibility.json "$bundle/deploy/"
cp deploy/systemd/aigw-host-agent.service "$bundle/deploy/"
"$go_bin" list -m -json all > "$work/modules.json"
cp go.sum web/package-lock.json "$bundle/"
[[ ! -f docs/linux-package.md ]] || cp docs/linux-package.md "$bundle/docs/"
python3 - "$bundle" "$commit" "$arch" "$("$go_bin" env GOVERSION)" "$(node --version)" <<'PY'
import hashlib,json,pathlib,sys
root=pathlib.Path(sys.argv[1])
newapi=json.loads((root/'deploy/newapi-compatibility.json').read_text())
metadata=dict(format_version=1,execution_engines=['native','newapi'],newapi=newapi,source_commit=sys.argv[2],os='linux',arch=sys.argv[3],go=sys.argv[4],node=sys.argv[5],engine='native',release_status='development_candidate')
(root/'manifest.json').write_text(json.dumps(metadata,indent=2)+'\n')
raw=(root.parent/'modules.json').read_text()
decoder=json.JSONDecoder(); modules=[]
while raw.strip():
    item,end=decoder.raw_decode(raw.lstrip());raw=raw.lstrip()[end:]
    modules.append({k:item[k] for k in ('Path','Version','Sum','GoModSum') if k in item})
(root/'go-modules.json').write_text(json.dumps(modules,indent=2)+'\n')
lines=[]
for path in sorted(root.rglob('*')):
    if path.is_symlink(): raise SystemExit('Unexpected symlink in package')
    if path.is_file():
        lines.append(hashlib.sha256(path.read_bytes()).hexdigest()+'  '+path.relative_to(root).as_posix())
(root/'SHA256SUMS').write_text('\n'.join(lines)+'\n')
PY
# Refuse to replace an artifact even if another builder created it concurrently.
tar -czf "$work/candidate.tar.gz" -C "$bundle" .
python3 "$repo/scripts/verify-package.py" "$work/candidate.tar.gz"
ln "$work/candidate.tar.gz" "$output"
sha256sum "$output"
printf 'Packaged committed source %s (control-plane Linux %s; native default, explicit newapi); no services installed.\n' "$commit" "$arch"
