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
    def test_valid_and_hostile_archives(self):
        files = {name: b"synthetic" for name in (
            "bin/aigw", "bin/aigw-gateway", "bin/aigw-migrate", "bin/aigw-local-admin",
            "bin/aigw-content-audit", "bin/aigw-host-agent", "web/index.html",
            "config.env.example", "go-modules.json", "go.sum", "package-lock.json")}
        files["manifest.json"] = json.dumps(dict(format_version=1, engine="native", os="linux",
            arch="amd64", release_status="development_candidate", source_commit="a" * 40)).encode()
        files["SHA256SUMS"] = "".join(hashlib.sha256(data).hexdigest() + "  " + name + "\n"
            for name, data in files.items()).encode()
        for case in ("valid", "changed", "extra", "missing", "traversal", "absolute", "symlink", "duplicate", "nonexecutable"):
            with self.subTest(case=case), tempfile.TemporaryDirectory() as directory:
                archive = pathlib.Path(directory) / "fixture.tar.gz"
                payload = files.copy()
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
                else:
                    with self.assertRaises(ValueError): verifier.verify(archive)
                self.assertFalse((pathlib.Path(directory).parent / "escape").exists())


if __name__ == "__main__":
    unittest.main()
