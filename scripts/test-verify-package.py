#!/usr/bin/env python3
import contextlib
import hashlib
import importlib.util
import io
import json
import pathlib
import tarfile
import tempfile
import unittest

spec = importlib.util.spec_from_file_location("package_verifier", pathlib.Path(__file__).with_name("verify-package.py"))
verifier = importlib.util.module_from_spec(spec)
spec.loader.exec_module(verifier)


class ArchiveBoundary(unittest.TestCase):
    def test_embedded_build_identity_and_inventory(self):
        required = {"bin/aigw", "bin/aigw-gateway", "bin/aigw-migrate", "bin/aigw-local-admin",
                    "bin/aigw-content-audit", "bin/aigw-host-agent", "web/index.html",
                    "config.env.example", "go.sum", "engine-go.sum", "package-lock.json",
                    "THIRD_PARTY_NOTICES.md", "third-party-notices.json",
                    "api/openapi/management.json", "docs/engine-entrypoint.md",
                    "docs/single-process-commercial-plan.md", "docs/compliance/reuse-review.md",
                    "docs/compliance/dependency-baseline.json"}
        core = dict(Path="github.com/maximhq/bifrost/core", Version="v1.14.1", Sum="h1:" + "a" * 43 + "=")
        for case in ("valid", "legacy_mode", "legacy_file", "wrong_process", "missing_lock",
                     "wrong_entry", "wrong_compiler", "wrong_sdk", "replaced_sdk", "missing_sdk",
                     "missing_inventory", "wrong_inventory", "development_sdk", "bad_dependencies", "missing_notices",
                     "missing_license", "wrong_license_hash", "no_sdk_notice"):
            with self.subTest(case=case), tempfile.TemporaryDirectory() as directory:
                files = {name: b"synthetic" for name in required}
                manifest = dict(format_version=2, engine="bifrost", os="linux", arch="amd64",
                    go="go1.27.3", release_status="development_candidate", source_commit="a" * 40,
                    execution_engines=["bifrost"], process_model="embedded", bifrost_core=core.copy())
                build = dict(GoVersion="go1.27.3",
                    Path="github.com/jhzhfree/ai-governance-gateway/modules/engine/cmd/gateway-engine", Deps=[core.copy()])
                modules = [dict(scope="embedded_gateway", **core)]
                if case == "legacy_mode": manifest["execution_engines"].append("newapi")
                if case == "legacy_file": files["deploy/newapi-compatibility.json"] = b"{}"
                if case == "wrong_process": manifest["process_model"] = "subprocess"
                if case == "missing_lock": del files["engine-go.sum"]
                if case == "missing_notices": del files["THIRD_PARTY_NOTICES.md"]
                if case == "wrong_entry": build["Path"] = "github.com/jhzhfree/ai-governance-gateway/cmd/gateway"
                if case == "wrong_compiler": build["GoVersion"] = "go1.26.0"
                if case == "wrong_sdk": build["Deps"][0]["Version"] = "v1.14.2"
                if case == "replaced_sdk": build["Deps"][0]["Replace"] = dict(Path="../unreviewed")
                if case == "missing_sdk": build["Deps"] = []
                if case == "bad_dependencies": build["Deps"] = [17]
                if case == "wrong_inventory": modules[0]["Sum"] = "h1:" + "b" * 43 + "="
                if case == "development_sdk": manifest["bifrost_core"]["Version"] = "dev"
                files["manifest.json"] = json.dumps(manifest).encode()
                files["gateway-build.json"] = json.dumps(build).encode()
                files["go-modules.json"] = json.dumps(modules).encode()
                license_path = "licenses/core/LICENSE"
                files[license_path] = b"synthetic license fixture"
                notices = [dict(component="go:"+core["Path"]+"@"+core["Version"], status="collected_not_cleared",
                    files=[dict(path=license_path, sha256=hashlib.sha256(files[license_path]).hexdigest())])]
                if case == "missing_license": del files[license_path]
                if case == "wrong_license_hash": notices[0]["files"][0]["sha256"] = "b" * 64
                if case == "no_sdk_notice": notices[0]["component"] = "go:other@v1.0.0"
                files["third-party-notices.json"] = json.dumps(notices).encode()
                if case == "missing_inventory": del files["go-modules.json"]
                files["SHA256SUMS"] = "".join(hashlib.sha256(data).hexdigest()+"  "+name+"\n" for name,data in files.items()).encode()
                archive = pathlib.Path(directory)/"candidate.tar.gz"
                with tarfile.open(archive,"w:gz") as output:
                    for name,data in files.items():
                        item=tarfile.TarInfo(name); item.size=len(data)
                        item.mode=0o755 if name.startswith("bin/") else 0o644
                        output.addfile(item,io.BytesIO(data))
                if case == "valid":
                    with contextlib.redirect_stdout(io.StringIO()): result=verifier.verify(archive,"a"*40)
                    self.assertEqual(result["engine"], "bifrost")
                else:
                    with self.assertRaises(ValueError): verifier.verify(archive,"a"*40)

    def test_valid_and_hostile_archives(self):
        files = {name: b"synthetic" for name in (
            "bin/aigw", "bin/aigw-gateway", "bin/aigw-migrate", "bin/aigw-local-admin",
            "bin/aigw-content-audit", "bin/aigw-host-agent", "web/index.html",
            "config.env.example", "go-modules.json", "go.sum", "package-lock.json")}
        files["manifest.json"] = json.dumps(dict(format_version=1, engine="native", os="linux",
            arch="amd64", release_status="development_candidate", source_commit="a" * 40)).encode()
        files["SHA256SUMS"] = "".join(hashlib.sha256(data).hexdigest() + "  " + name + "\n"
            for name, data in files.items()).encode()
        for case in ("valid", "changed", "extra", "missing", "traversal", "absolute", "symlink", "duplicate", "nonexecutable", "writable_directory", "directory_collision", "wrong_commit", "invalid_manifest", "writable_root", "too_many_members"):
            with self.subTest(case=case), tempfile.TemporaryDirectory() as directory:
                archive = pathlib.Path(directory) / "fixture.tar.gz"
                payload = files.copy()
                if case == "invalid_manifest": payload["manifest.json"] = json.dumps({**json.loads(files["manifest.json"]), "source_commit": 17}).encode()
                if case == "changed": payload["web/index.html"] = b"tampered"
                if case == "extra": payload["unexpected"] = b"extra"
                if case == "missing": del payload["bin/aigw"]
                if case == "traversal": payload["../escape"] = b"escape"
                if case == "absolute": payload["/escape"] = b"escape"
                with tarfile.open(archive, "w:gz") as output:
                    for name, data in payload.items():
                        item = tarfile.TarInfo(name)
                        item.size = len(data)
                        if name.startswith("bin/") and case != "nonexecutable": item.mode = 0o755
                        output.addfile(item, io.BytesIO(data))
                    if case in ("writable_directory", "directory_collision"):
                        item = tarfile.TarInfo("folder" if case == "writable_directory" else "web/index.html")
                        item.type = tarfile.DIRTYPE
                        item.mode = 0o777 if case == "writable_directory" else 0o755
                        output.addfile(item)
                    if case == "writable_root":
                        item = tarfile.TarInfo(".")
                        item.type = tarfile.DIRTYPE
                        item.mode = 0o777
                        output.addfile(item)
                    if case == "too_many_members":
                        for number in range(10001):
                            output.addfile(tarfile.TarInfo("extra-" + str(number)))
                    if case == "symlink":
                        item = tarfile.TarInfo("link")
                        item.type = tarfile.SYMTYPE
                        item.linkname = "/tmp/escape"
                        output.addfile(item)
                    if case == "duplicate":
                        item = tarfile.TarInfo("web/index.html")
                        output.addfile(item, io.BytesIO())
                if case == "valid":
                    with contextlib.redirect_stdout(io.StringIO()): verifier.verify(archive)
                elif case == "too_many_members":
                    with self.assertRaisesRegex(ValueError, "too many archive members"): verifier.verify(archive)
                else:
                    with self.assertRaises(ValueError): verifier.verify(archive, "b" * 40 if case == "wrong_commit" else None)
                self.assertFalse((pathlib.Path(directory).parent / "escape").exists())

    def test_control_plane_declaration_and_delivery_materials(self):
        declaration = dict(source_repository="jhzhfree/new-api", validated_commit="b" * 40, bundled=False)
        docs = {"docs/new-api-deployment.md", "docs/new-api-integration.md", "docs/product/user-guide.md",
                "docs/product/technical-specification.md", "docs/product/deployment-acceptance.md", "api/openapi/management.json"}
        names = {"bin/aigw", "bin/aigw-gateway", "bin/aigw-migrate", "bin/aigw-local-admin",
                 "bin/aigw-content-audit", "bin/aigw-host-agent", "web/index.html",
                 "config.env.example", "go-modules.json", "go.sum", "package-lock.json"} | docs
        for case in ("valid", "missing_doc", "missing_descriptor", "mismatch", "bad_pin", "bundled", "bad_modes", "missing_modes"):
            with self.subTest(case=case), tempfile.TemporaryDirectory() as directory:
                files = {name: b"synthetic" for name in names}
                manifest = dict(format_version=1, engine="native", os="linux", arch="amd64",
                    release_status="development_candidate", source_commit="a" * 40,
                    execution_engines=["native", "newapi"], newapi=declaration.copy())
                files["deploy/newapi-compatibility.json"] = json.dumps(declaration).encode()
                if case == "missing_doc": del files["docs/new-api-deployment.md"]
                if case == "missing_descriptor": del files["deploy/newapi-compatibility.json"]
                if case == "mismatch": files["deploy/newapi-compatibility.json"] = json.dumps({**declaration,"validated_commit":"c"*40}).encode()
                if case == "bad_pin": manifest["newapi"]["validated_commit"] = "main"
                if case == "bundled": manifest["newapi"]["bundled"] = True
                if case == "bad_modes": manifest["execution_engines"] = ["native", "bifrost", "newapi"]
                if case == "missing_modes": del manifest["execution_engines"]
                files["manifest.json"] = json.dumps(manifest).encode()
                # Recompute all hashes so rejection proves semantic validation,
                # rather than merely noticing a stale inventory.
                files["SHA256SUMS"] = "".join(hashlib.sha256(data).hexdigest()+"  "+name+"\n" for name,data in files.items()).encode()
                archive = pathlib.Path(directory)/"candidate.tar.gz"
                with tarfile.open(archive,"w:gz") as output:
                    for name,data in files.items():
                        item=tarfile.TarInfo(name); item.size=len(data)
                        item.mode=0o755 if name.startswith("bin/") else 0o644
                        output.addfile(item,io.BytesIO(data))
                if case == "valid":
                    with contextlib.redirect_stdout(io.StringIO()): result=verifier.verify(archive,"a"*40)
                    self.assertEqual(result["newapi"],declaration)
                else:
                    with self.assertRaises(ValueError): verifier.verify(archive,"a"*40)


if __name__ == "__main__":
    unittest.main()
