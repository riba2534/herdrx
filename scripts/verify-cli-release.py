#!/usr/bin/env python3
"""Verify complete CLI assets and execute the native binary in an isolated directory."""
import hashlib
import json
import os
from pathlib import Path
import platform
import subprocess
import sys
import tarfile
import tempfile

import release_signing

ASSETS = {"herdrx-linux-amd64.tar.gz", "herdrx-linux-arm64.tar.gz", "herdrx-linux-amd64.manifest.json", "herdrx-linux-arm64.manifest.json", "RELEASE-PUBLIC-KEY", "LICENSE", "THIRD_PARTY_NOTICES.md", "sbom.cdx.json", "install-herdrx.sh", "README-CLI.md", "release.json", "SHA256SUMS"}


def verify(directory):
    directory = Path(directory).resolve()
    assert {p.name for p in directory.iterdir()} == ASSETS, "unexpected or missing release asset"
    entries = (directory / "SHA256SUMS").read_text().splitlines()
    assert len(entries) == len(ASSETS) - 1, "missing or duplicate checksum"
    hashes = {}
    for line in entries:
        digest, name = line.split("  ")
        assert name in ASSETS - {"SHA256SUMS"} and name not in hashes
        assert hashlib.sha256((directory / name).read_bytes()).hexdigest() == digest, f"checksum mismatch: {name}"
        hashes[name] = digest
    metadata = json.loads((directory / "release.json").read_text())
    assert (directory / "RELEASE-PUBLIC-KEY").read_bytes() == release_signing.PUBLIC_KEY_FILE.read_bytes(), "downloaded key differs from trusted source"
    assert {p["arch"] for p in metadata["platforms"]} == {"amd64", "arm64"}
    native = {"x86_64": "amd64", "aarch64": "arm64"}.get(platform.machine())
    assert native and platform.system() == "Linux", "native smoke check requires supported Linux CPU"
    for entry in metadata["platforms"]:
        assert entry["asset"] == f"herdrx-linux-{entry['arch']}.tar.gz"
        assert entry["manifest"] == f"herdrx-linux-{entry['arch']}.manifest.json"
        with tarfile.open(directory / entry["asset"], "r:gz") as archive:
            members = archive.getmembers()
            assert [m.name for m in members] == ["herdrx", "VERSION", "README.md", "LICENSE", "THIRD_PARTY_NOTICES.md"]
            assert archive.extractfile("LICENSE").read() == (directory / "LICENSE").read_bytes()
            assert archive.extractfile("THIRD_PARTY_NOTICES.md").read() == (directory / "THIRD_PARTY_NOTICES.md").read_bytes()
            assert all(m.isfile() for m in members), "archives must contain regular files only"
            version = archive.extractfile("VERSION").read().decode().strip()
            binary = archive.extractfile("herdrx").read()
            assert version == metadata["version"]
            assert hashlib.sha256(binary).hexdigest() == entry["binary_sha256"]
            release_signing.verify(json.loads((directory / entry["manifest"]).read_text()), binary, version, entry["arch"])
            assert binary[:4] == b"\x7fELF"
            assert int.from_bytes(binary[18:20], "little") == {"amd64": 62, "arm64": 183}[entry["arch"]]
            if entry["arch"] == native:
                with tempfile.TemporaryDirectory(prefix="herdrx-cli-smoke-") as temporary:
                    path = Path(temporary) / "herdrx"
                    path.write_bytes(binary)
                    path.chmod(0o755)
                    env = {**os.environ, "XDG_CONFIG_HOME": temporary + "/config", "XDG_RUNTIME_DIR": temporary + "/runtime"}
                    assert subprocess.check_output([path, "version"], text=True, env=env).strip() == f"herdrx {version}"
                    assert "setup" in subprocess.check_output([path, "--help"], text=True, env=env)
                    assert subprocess.run([path, "status", "--json"], env=env, stdout=subprocess.PIPE, stderr=subprocess.PIPE).returncode == 1
    return metadata


if __name__ == "__main__":
    result = verify(sys.argv[1])
    print(f"CLI release {result['version']}: checksums, archive members, CPU and native version/help/offline status passed")
