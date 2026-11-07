#!/usr/bin/env python3
"""Validate a candidate archive without extracting or executing its contents."""
import hashlib
import json
import pathlib
import re
import sys
import tarfile


def verify(filename):
    contents = {}
    total = 0
    with tarfile.open(filename, "r:gz") as archive:
        for member in archive:
            name = member.name.removeprefix("./")
            if name in ("", ".") and member.isdir():
                continue
            path = pathlib.PurePosixPath(name)
            if path.is_absolute() or ".." in path.parts or str(path) != name:
                raise ValueError("unsafe archive path")
            if member.isdir():
                continue
            if not member.isfile() or name in contents:
                raise ValueError("unsupported or duplicate archive member")
            if member.mode & 0o6000 or (name.startswith("bin/") and (not member.mode & 0o111 or member.mode & 0o022)):
                raise ValueError("unsafe binary permissions")
            total += member.size
            if total > 1024 * 1024 * 1024:
                raise ValueError("archive exceeds 1 GiB limit")
            stream = archive.extractfile(member)
            digest = hashlib.sha256()
            data = bytearray() if name in ("SHA256SUMS", "manifest.json") else None
            while block := stream.read(65536):
                digest.update(block)
                if data is not None:
                    data.extend(block)
                    if len(data) > 1024 * 1024:
                        raise ValueError("metadata too large")
            contents[name] = (digest.hexdigest(), data)
    manifest = json.loads(contents["manifest.json"][1])
    if (not isinstance(manifest, dict) or manifest.get("format_version") != 1 or manifest.get("engine") != "native"
            or manifest.get("os") != "linux" or manifest.get("arch") not in ("amd64", "arm64")
            or manifest.get("release_status") != "development_candidate"
            or not re.fullmatch(r"[0-9a-f]{40}", manifest.get("source_commit", ""))):
        raise ValueError("unsupported manifest")
    checks = {}
    for line in contents["SHA256SUMS"][1].decode().splitlines():
        digest, name = line.split("  ", 1)
        if name in checks or not re.fullmatch(r"[0-9a-f]{64}", digest):
            raise ValueError("invalid checksum inventory")
        checks[name] = digest
    if set(checks) != set(contents) - {"SHA256SUMS"}:
        raise ValueError("checksum inventory differs from archive")
    for name, digest in checks.items():
        if digest != contents[name][0]:
            raise ValueError("file checksum mismatch")
    required = {"bin/aigw", "bin/aigw-gateway", "bin/aigw-migrate", "bin/aigw-local-admin",
                "bin/aigw-content-audit", "bin/aigw-host-agent", "web/index.html",
                "config.env.example", "go-modules.json", "go.sum", "package-lock.json"}
    if not required <= set(contents):
        raise ValueError("required package component missing")
    print("Verified candidate", manifest["source_commit"], manifest["arch"], len(checks), "files")


if __name__ == "__main__":
    try:
        if len(sys.argv) != 2:
            raise ValueError("usage: python3 scripts/verify-package.py archive.tar.gz")
        verify(sys.argv[1])
    except (ValueError, KeyError, OSError, tarfile.TarError) as error:
        sys.exit("Package verification failed: " + str(error))
