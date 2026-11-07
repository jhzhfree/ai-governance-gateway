#!/usr/bin/env bash
# Builds and executes only our own current-commit candidate; never installs it.
set -euo pipefail
cd "$(dirname "$0")/.."
python3 scripts/test-verify-package.py
work="$(mktemp -d)"
trap 'rm -rf -- "$work"' EXIT
bash scripts/package-linux.sh "$work/candidate.tar.gz"
python3 scripts/verify-package.py "$work/candidate.tar.gz" --expect-commit "$(git rev-parse HEAD)"
python3 - "$work" <<'PY'
import os,pathlib,subprocess,sys,tarfile
root=pathlib.Path(sys.argv[1])
# The archive above was just built locally from our reviewed source. This smoke
# runner is not an execution facility for arbitrary downloaded archives.
with tarfile.open(root/'candidate.tar.gz','r:gz') as archive:
    for name, expected in [('aigw-gateway',0),('aigw-host-agent',0),('aigw-migrate',1)]:
        member=archive.getmember('./bin/'+name)
        binary=root/name
        binary.write_bytes(archive.extractfile(member).read())
        binary.chmod(0o700)
        env={k:v for k,v in os.environ.items() if not k.startswith('AIGW_')}
        run=subprocess.run([str(binary),'--help'],env=env,capture_output=True,timeout=10)
        if run.returncode!=expected:
            raise SystemExit(name+' unexpected help exit status')
        if name=='aigw-gateway' and b'newapi' not in run.stdout+run.stderr:
            raise SystemExit('declared newapi mode absent from gateway help')
        if name=='aigw-migrate' and b'usage: migrate' not in run.stderr:
            raise SystemExit('migration usage output missing')
        print(name+' startup smoke passed; no service or migration started')
PY
