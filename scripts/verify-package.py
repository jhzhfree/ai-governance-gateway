#!/usr/bin/env python3
"""Validate a candidate archive without extracting or executing its contents."""
import argparse
import hashlib
import json
import pathlib
import re
import sys
import tarfile


def verify(filename, expected_commit=None):
    contents = {}
    names = set()
    members = 0
    total = 0
    with tarfile.open(filename, "r:gz") as archive:
        for member in archive:
            members += 1
            if members > 10000:
                raise ValueError("too many archive members")
            name = member.name.removeprefix("./")
            if name in ("", ".") and member.isdir():
                if member.mode & 0o6022 or "." in names:
                    raise ValueError("unsafe or duplicate archive root")
                names.add(".")
                continue
            path = pathlib.PurePosixPath(name)
            if path.is_absolute() or ".." in path.parts or str(path) != name:
                raise ValueError("unsafe archive path")
            if name in names:
                raise ValueError("duplicate archive path")
            names.add(name)
            if member.mode & 0o6022:
                raise ValueError("unsafe archive permissions")
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
            data = bytearray() if name in ("SHA256SUMS", "manifest.json", "deploy/newapi-compatibility.json", "gateway-build.json", "go-modules.json", "third-party-notices.json") else None
            while block := stream.read(65536):
                digest.update(block)
                if data is not None:
                    data.extend(block)
                    if len(data) > 1024 * 1024:
                        raise ValueError("metadata too large")
            contents[name] = (digest.hexdigest(), data)
    manifest = json.loads(contents["manifest.json"][1])
    if (not isinstance(manifest, dict)
            or (manifest.get("format_version"), manifest.get("engine")) not in ((1, "native"), (2, "bifrost"))
            or manifest.get("os") != "linux" or manifest.get("arch") not in ("amd64", "arm64")
            or manifest.get("release_status") != "development_candidate"
            or not isinstance(manifest.get("source_commit"), str)
            or not re.fullmatch(r"[0-9a-f]{40}", manifest.get("source_commit", ""))):
        raise ValueError("unsupported manifest")
    if expected_commit is not None and manifest["source_commit"] != expected_commit:
        raise ValueError("source commit mismatch")
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
    # Legacy format-1 native candidates remain readable. New candidates declare
    # their actual execution modes and the separately deployed data-plane pin.
    if manifest["format_version"] == 2:
        core = manifest.get("bifrost_core")
        if (manifest.get("execution_engines") != ["bifrost"] or manifest.get("process_model") != "embedded"
                or "newapi" in manifest or "deploy/newapi-compatibility.json" in contents
                or not isinstance(core, dict) or set(core) != {"Path", "Version", "Sum"}
                or core["Path"] != "github.com/maximhq/bifrost/core"
                or not isinstance(core["Version"], str) or not re.fullmatch(r"v\d+\.\d+\.\d+", core["Version"])
                or not isinstance(core["Sum"], str) or not re.fullmatch(r"h1:[A-Za-z0-9+/]{43}=", core["Sum"])):
            raise ValueError("unsupported embedded-engine declaration")
        required |= {"gateway-build.json", "engine-go.sum", "api/openapi/management.json",
                     "THIRD_PARTY_NOTICES.md", "third-party-notices.json",
                     "docs/engine-entrypoint.md", "docs/single-process-commercial-plan.md",
                     "docs/compliance/reuse-review.md", "docs/compliance/dependency-baseline.json"}
        if not required <= set(contents):
            raise ValueError("required embedded package component missing")
        build = json.loads(contents["gateway-build.json"][1])
        if (not isinstance(build, dict)
                or build.get("Path") != "github.com/jhzhfree/ai-governance-gateway/modules/engine/cmd/gateway-engine"
                or build.get("GoVersion") != manifest.get("go")
                or not isinstance(build.get("Deps"), list)):
            raise ValueError("gateway build identity mismatch")
        dependencies = build["Deps"]
        if any(not isinstance(item, dict) for item in dependencies):
            raise ValueError("invalid gateway dependencies")
        linked = [item for item in dependencies if item.get("Path") == core["Path"]]
        if linked != [core]:
            raise ValueError("Bifrost build dependency mismatch")
        modules = json.loads(contents["go-modules.json"][1])
        if not isinstance(modules, list) or any(not isinstance(item, dict) for item in modules):
            raise ValueError("invalid module inventory")
        if not any(item.get("scope") == "embedded_gateway" and all(item.get(k) == v for k, v in core.items()) for item in modules):
            raise ValueError("Bifrost missing from module inventory")
        notices = json.loads(contents["third-party-notices.json"][1])
        if not isinstance(notices, list) or not notices:
            raise ValueError("invalid notice inventory")
        components = set()
        for record in notices:
            if (not isinstance(record, dict) or not isinstance(record.get("component"), str)
                    or record["component"] in components or not isinstance(record.get("files"), list)
                    or record.get("status") != ("collected_not_cleared" if record["files"] else "no_top_level_notice_found")):
                raise ValueError("invalid notice record")
            components.add(record["component"])
            for item in record["files"]:
                if (not isinstance(item, dict) or not isinstance(item.get("path"), str)
                        or not item["path"].startswith("licenses/") or item["path"] not in contents
                        or item.get("sha256") != contents[item["path"]][0]):
                    raise ValueError("notice file mismatch")
        sdk_notice = "go:" + core["Path"] + "@" + core["Version"]
        if not any(record["component"] == sdk_notice and record["files"] for record in notices):
            raise ValueError("Bifrost license notice missing")
    elif "execution_engines" in manifest or "newapi" in manifest:
        newapi = manifest.get("newapi")
        if (manifest.get("execution_engines") != ["native", "newapi"]
                or not isinstance(newapi, dict) or set(newapi) != {"bundled", "source_repository", "validated_commit"}
                or newapi.get("bundled") is not False
                or newapi.get("source_repository") != "jhzhfree/new-api"
                or not isinstance(newapi.get("validated_commit"), str)
                or not re.fullmatch(r"[0-9a-f]{40}", newapi["validated_commit"])):
            raise ValueError("unsupported data-plane declaration")
        required |= {"deploy/newapi-compatibility.json", "api/openapi/management.json",
                     "docs/new-api-deployment.md", "docs/new-api-integration.md",
                     "docs/product/user-guide.md", "docs/product/technical-specification.md",
                     "docs/product/deployment-acceptance.md"}
        if "deploy/newapi-compatibility.json" not in contents or json.loads(contents["deploy/newapi-compatibility.json"][1]) != newapi:
            raise ValueError("data-plane declaration mismatch")
    if not required <= set(contents):
        raise ValueError("required package component missing")
    print("Verified candidate", manifest["source_commit"], manifest["arch"], len(checks), "files")
    return manifest


if __name__ == "__main__":
    try:
        parser = argparse.ArgumentParser(description=__doc__)
        parser.add_argument("archive")
        parser.add_argument("--expect-commit")
        args = parser.parse_args()
        verify(args.archive, args.expect_commit)
    except (ValueError, KeyError, OSError, tarfile.TarError) as error:
        sys.exit("Package verification failed: " + str(error))
