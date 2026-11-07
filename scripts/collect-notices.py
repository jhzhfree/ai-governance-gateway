#!/usr/bin/env python3
"""Collect available top-level notices, without making a license-clearance claim."""
import hashlib
import json
import pathlib
import sys


def objects(path):
    raw = path.read_text()
    decoder = json.JSONDecoder()
    while raw.strip():
        value, end = decoder.raw_decode(raw.lstrip())
        raw = raw.lstrip()[end:]
        yield value


def collect(source, destination, label, records):
    record = {"component": label, "files": [], "status": "collected_not_cleared"}
    key = hashlib.sha256(label.encode()).hexdigest()[:24]
    if source is not None and source.is_dir():
        for path in sorted(source.iterdir()):
            if (path.is_symlink() or not path.is_file()
                    or not path.name.lower().startswith(("license", "licence", "notice", "copying", "copyright", "authors", "patents"))):
                continue
            data = path.read_bytes()
            if len(data) > 2 * 1024 * 1024:
                raise ValueError("notice file exceeds collection bound")
            relative = pathlib.Path("licenses") / key / path.name
            target = destination / relative
            target.parent.mkdir(parents=True, exist_ok=True)
            target.write_bytes(data)
            record["files"].append({"path": relative.as_posix(), "sha256": hashlib.sha256(data).hexdigest()})
    if not record["files"]:
        record["status"] = "no_top_level_notice_found"
    records.append(record)


def main():
    source, work, bundle, goroot = map(pathlib.Path, sys.argv[1:])
    directories = {}
    for name in ("modules.json", "engine-modules.json"):
        for module in objects(work / name):
            if "Dir" in module:
                directories[module["Path"], module.get("Version", "")] = pathlib.Path(module["Dir"])
    linked = set()
    for binary in objects(work / "binaries.json"):
        for module in binary.get("Deps", []):
            if module["Path"] in ("github.com/jhzhfree/ai-governance-gateway", "github.com/jhzhfree/ai-governance-gateway/modules/engine"):
                continue
            if "Replace" in module:
                raise ValueError("third-party replacement requires explicit notice review")
            linked.add((module["Path"], module["Version"]))
    records = []
    for key in sorted(linked):
        collect(directories.get(key), bundle, "go:" + "@".join(key), records)
    collect(goroot, bundle, "go-runtime:" + (source / ".go-version").read_text().strip(), records)
    lock = json.loads((source / "web/package-lock.json").read_text())
    for path, package in sorted(lock["packages"].items()):
        if not path:
            continue
        folder = source / "web" / path
        # Platform-optional packages absent from this frozen install are not
        # components of this build. Include installed build tools conservatively.
        if not folder.is_dir():
            continue
        label = "npm:" + path + "@" + package["version"]
        collect(folder, bundle, label, records)
        records[-1]["declared_license"] = package.get("license", "UNDECLARED")
        records[-1]["scope"] = "build_test" if package.get("dev") else "runtime_declared"
    (bundle / "third-party-notices.json").write_text(json.dumps(records, indent=2) + "\n")
    missing = [item["component"] for item in records if not item["files"]]
    (bundle / "THIRD_PARTY_NOTICES.md").write_text(
        "# Third-party notices (development candidate)\n\n"
        "Collected top-level license/notice files are in `licenses/`, indexed by "
        "`third-party-notices.json`. This includes linked Go dependencies, the Go "
        "runtime and installed npm runtime/build packages. It is not a complete "
        "file-level provenance audit, legal clearance or a standards-based SBOM. "
        "Nested third-party notices and actual bundle attribution still require review. "
        "See `docs/compliance/reuse-review.md`. Original application distribution "
        "terms remain to be determined by its rights holder.\n\n"
        "## Components without a top-level notice\n\n"
        + ("\n".join("- " + name for name in missing) if missing else "None in the collected scope.") + "\n")
    print("Collected notices for", len(records), "components;", len(missing), "need notice review")


if __name__ == "__main__":
    main()
