#!/usr/bin/env bash
# Build an embedded-engine candidate from committed source only. Never installs it.
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
for command in migrate local-admin content-audit aigw host-agent; do
  name="aigw-$command"
  [[ "$command" != aigw ]] || name=aigw
  "$go_bin" build -mod=readonly -trimpath -buildvcs=false -o "$bundle/bin/$name" "./cmd/$command"
done
(cd modules/engine && "$go_bin" mod verify && "$go_bin" build -mod=readonly -trimpath -buildvcs=false -o "$bundle/bin/aigw-gateway" ./cmd/gateway-engine)
npm --prefix web ci --no-audit --no-fund
npm --prefix web run api:check
npm --prefix web run build
cp -R web/dist/. "$bundle/web/"
# Keep one maintained common template, omitting legacy execution-only settings.
python3 - "$bundle/config.env.example" <<'PY'
import pathlib,sys
source=pathlib.Path('.env.example').read_text()
if source.count('# Explicit new-api mode:')!=1: raise SystemExit('Legacy template boundary missing')
text=source.split('# Explicit new-api mode:')[0]
text=text.replace('# Test-only legacy mock upstream; unused by normal real-provider startup.\nAIGW_UPSTREAM_URL=\n','')
text=text.replace('# --providers <reviewed JSON file> remains a read-only compatibility mode.\n','')
if 'AIGW_NEWAPI_' in text or 'AIGW_UPSTREAM_URL' in text: raise SystemExit('Legacy settings in embedded candidate')
pathlib.Path(sys.argv[1]).write_text('# Single-process product: embedded Bifrost; no separate inference service.\n'+text)
PY
cp -R docs/. "$bundle/docs/"
mkdir -p "$bundle/api/openapi"
cp api/openapi/management.json "$bundle/api/openapi/"
cp deploy/systemd/aigw-host-agent.service "$bundle/deploy/"
"$go_bin" list -m -json all > "$work/modules.json"
(cd modules/engine && "$go_bin" list -m -json all) > "$work/engine-modules.json"
"$go_bin" version -m -json "$bundle/bin/aigw-gateway" > "$work/engine-build.json"
"$go_bin" version -m -json "$bundle"/bin/* > "$work/binaries.json"
python3 scripts/collect-notices.py "$work/source" "$work" "$bundle" "$("$go_bin" env GOROOT)"
cp go.sum web/package-lock.json "$bundle/"
cp modules/engine/go.sum "$bundle/engine-go.sum"
[[ ! -f docs/linux-package.md ]] || cp docs/linux-package.md "$bundle/docs/"
python3 - "$bundle" "$commit" "$arch" "$("$go_bin" env GOVERSION)" "$(node --version)" <<'PY'
import hashlib,json,pathlib,sys
root=pathlib.Path(sys.argv[1])
build=json.loads((root.parent/'engine-build.json').read_text())
core=[m for m in build['Deps'] if m['Path']=='github.com/maximhq/bifrost/core']
if len(core)!=1 or 'Replace' in core[0]: raise SystemExit('Expected one unmodified Bifrost Core dependency')
if build['Path']!='github.com/jhzhfree/ai-governance-gateway/modules/engine/cmd/gateway-engine':
    raise SystemExit('Wrong gateway entrypoint in candidate')
metadata=dict(format_version=2,execution_engines=['bifrost'],process_model='embedded',
    bifrost_core={k:core[0][k] for k in ('Path','Version','Sum')},source_commit=sys.argv[2],
    os='linux',arch=sys.argv[3],go=sys.argv[4],node=sys.argv[5],engine='bifrost',release_status='development_candidate')
(root/'manifest.json').write_text(json.dumps(metadata,indent=2)+'\n')
decoder=json.JSONDecoder(); modules=[]
for filename,scope in [('modules.json','administration_tools'),('engine-modules.json','embedded_gateway')]:
    raw=(root.parent/filename).read_text()
    while raw.strip():
        item,end=decoder.raw_decode(raw.lstrip());raw=raw.lstrip()[end:]
        modules.append(dict(scope=scope,**{k:item[k] for k in ('Path','Version','Sum','GoModSum') if k in item}))
(root/'go-modules.json').write_text(json.dumps(modules,indent=2)+'\n')
(root/'gateway-build.json').write_text(json.dumps(build,indent=2)+'\n')
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
printf 'Packaged committed source %s (Linux %s; embedded Bifrost only); no services installed.\n' "$commit" "$arch"
