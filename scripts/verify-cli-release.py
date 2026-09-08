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

PLATFORMS = {("linux", "amd64"), ("linux", "arm64"), ("darwin", "amd64"), ("darwin", "arm64")}
ASSETS = {f"herdrx-{goos}-{arch}.{suffix}" for goos, arch in PLATFORMS for suffix in ("tar.gz", "manifest.json")} | {
    "RELEASE-PUBLIC-KEY", "LICENSE", "THIRD_PARTY_NOTICES.md", "sbom.cdx.json", "install-herdrx.sh", "README-CLI.md", "release.json", "SHA256SUMS"}

# Mach-O 64-bit little-endian magic (cffaedfe) plus the CPU types Go emits.
MACHO_MAGIC = bytes.fromhex("cffaedfe")
MACHO_CPU = {"amd64": 0x01000007, "arm64": 0x0100000C}
ELF_MACHINE = {"amd64": 62, "arm64": 183}


def assert_binary_format(binary, goos, arch):
    """Reject a binary built for the wrong OS or CPU, whatever the file name claims."""
    if goos == "linux":
        assert binary[:4] == b"\x7fELF", "linux asset is not an ELF binary"
        assert int.from_bytes(binary[18:20], "little") == ELF_MACHINE[arch], "ELF CPU does not match the asset name"
        return
    assert binary[:4] == MACHO_MAGIC, "darwin asset is not a 64-bit little-endian Mach-O binary"
    assert int.from_bytes(binary[4:8], "little") == MACHO_CPU[arch], "Mach-O CPU does not match the asset name"


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
    assert {(p["os"], p["arch"]) for p in metadata["platforms"]} == PLATFORMS, "release must cover linux and darwin on amd64/arm64"
    native_arch = {"x86_64": "amd64", "amd64": "amd64", "aarch64": "arm64", "arm64": "arm64"}.get(platform.machine())
    native_os = {"Linux": "linux", "Darwin": "darwin"}.get(platform.system())
    assert native_arch and native_os, "native smoke check requires a supported Linux or macOS CPU"
    smoke_ran = False
    for entry in metadata["platforms"]:
        goos = entry["os"]
        assert entry["asset"] == f"herdrx-{goos}-{entry['arch']}.tar.gz"
        assert entry["manifest"] == f"herdrx-{goos}-{entry['arch']}.manifest.json"
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
            release_signing.verify(json.loads((directory / entry["manifest"]).read_text()), binary, version, entry["arch"], goos)
            assert_binary_format(binary, goos, entry["arch"])
            if entry["arch"] == native_arch and goos == native_os:
                smoke_ran = True
                with tempfile.TemporaryDirectory(prefix="herdrx-cli-smoke-") as temporary:
                    path = Path(temporary) / "herdrx"
                    path.write_bytes(binary)
                    path.chmod(0o755)
                    env = {**os.environ, "XDG_CONFIG_HOME": temporary + "/config", "XDG_RUNTIME_DIR": temporary + "/runtime"}
                    assert subprocess.check_output([path, "version"], text=True, env=env).strip() == f"herdrx {version}"
                    assert "setup" in subprocess.check_output([path, "--help"], text=True, env=env)
                    assert subprocess.run([path, "status", "--json"], env=env, stdout=subprocess.PIPE, stderr=subprocess.PIPE).returncode == 1
    # 静默跳过原生冒烟等于没验证；必须确认本机架构那一份真的跑过。
    assert smoke_ran, f"no asset matched {native_os}/{native_arch}; the native smoke check did not run"
    return metadata


if __name__ == "__main__":
    result = verify(sys.argv[1])
    print(f"CLI release {result['version']}: checksums, archive members, binary format and native version/help/offline status passed")
