#!/usr/bin/env python3
"""Exercise the real POSIX installer with isolated, deterministic Release downloads."""
import hashlib
import io
import os
from pathlib import Path
import shutil
import subprocess
import tarfile
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[1]


class InstallerTest(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory(prefix="herdrx-installer-")
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name)
        self.tools = self.root / "tools"
        self.tools.mkdir()
        self.downloads = self.root / "release"
        self.downloads.mkdir()
        self.target = self.root / "install with spaces"
        self.target.mkdir()
        self.old = self.target / "herdrx"
        self.old.write_text("previous CLI\n")
        self.protected = self.root / "existing config"
        self.protected.write_text("existing identity fixture\n")
        curl = self.tools / "curl"
        curl.write_text(f'''#!{shutil.which("python3")}
import os, pathlib, shutil, sys
args = sys.argv[1:]
url = next(x for x in args if x.startswith('https://'))
assert url.startswith('https://github.com/riba2534/herdrx/releases/')
assert args[args.index('--proto') + 1] == '=https'
assert args[args.index('--proto-redir') + 1] == '=https'
with open(os.environ['FIXTURE_CALLS'], 'a') as log: log.write(url + '\\n')
source = pathlib.Path(os.environ['FIXTURE_RELEASE']) / url.rsplit('/', 1)[1]
if not source.is_file(): sys.exit(22)
shutil.copyfile(source, args[args.index('-o') + 1])
''')
        curl.chmod(0o755)
        uname = self.tools / "uname"
        uname.write_text('#!/bin/sh\ncase "$1" in -s) echo "${FIXTURE_OS:-Linux}";; -m) echo "${FIXTURE_ARCH:-x86_64}";; esac\n')
        uname.chmod(0o755)
        self.calls = self.root / "calls"
        self.env = {**os.environ, "PATH": str(self.tools) + ":" + os.environ["PATH"], "FIXTURE_RELEASE": str(self.downloads), "FIXTURE_CALLS": str(self.calls)}
        self.package()

    def package(self, arch="amd64", version="v0.1.0-rc.1", binary_version=None, symlink=False):
        name = f"herdrx-linux-{arch}.tar.gz"
        with tarfile.open(self.downloads / name, "w:gz") as archive:
            for filename, value in (("herdrx", f'#!/bin/sh\n[ "$1" = version ] || exit 9\nprintf "herdrx {binary_version or version}\\n"\n'), ("VERSION", version + "\n")):
                member = tarfile.TarInfo(filename)
                member.mode = 0o755 if filename == "herdrx" else 0o644
                if symlink and filename == "herdrx":
                    member.type = tarfile.SYMTYPE
                    member.linkname = str(self.protected)
                    archive.addfile(member)
                else:
                    data = value.encode()
                    member.size = len(data)
                    archive.addfile(member, io.BytesIO(data))
        checksum = hashlib.sha256((self.downloads / name).read_bytes()).hexdigest()
        (self.downloads / "SHA256SUMS").write_text(f"{checksum}  {name}\n")

    def run_install(self, *args, success=True):
        result = subprocess.run(["sh", str(ROOT / "install-herdrx.sh"), "--install-dir", str(self.target), *args], env=self.env, text=True, capture_output=True)
        self.assertEqual(result.returncode == 0, success, result.stdout + result.stderr)
        self.assertEqual(self.protected.read_text(), "existing identity fixture\n")
        self.assertFalse(list(self.target.glob(".herdrx*")), "staging files leaked")
        return result

    def assert_unchanged(self):
        self.assertEqual(self.old.read_text(), "previous CLI\n")
        self.assertFalse((self.target / "herdrx.previous").exists())

    def test_verified_install_pinned_version_spaces_backup_and_idempotence(self):
        # A preexisting backup symlink must not cause an unrelated file write.
        (self.target / "herdrx.previous").symlink_to(self.protected)
        self.run_install("--version", "v0.1.0-rc.1")
        self.assertEqual((self.target / "herdrx.previous").read_text(), "previous CLI\n")
        self.assertFalse((self.target / "herdrx.previous").is_symlink())
        self.assertIn("/download/v0.1.0-rc.1/", self.calls.read_text())
        before = self.old.stat()
        self.run_install("--version", "v0.1.0-rc.1")
        self.assertEqual(self.old.stat().st_ino, before.st_ino, "identical installs should not replace the running binary")
        self.assertEqual(self.old.stat().st_mode & 0o777, 0o755)
        self.assertEqual((self.target / "herdrx.previous").read_text(), "previous CLI\n")

    def test_arm64_and_latest(self):
        self.env["FIXTURE_ARCH"] = "aarch64"
        self.package(arch="arm64")
        self.run_install()
        self.assertIn("/latest/download/herdrx-linux-arm64.tar.gz", self.calls.read_text())

    def test_bad_checksum_keeps_installed_binary(self):
        (self.downloads / "herdrx-linux-amd64.tar.gz").write_bytes(b"broken")
        self.run_install(success=False)
        self.assert_unchanged()

    def test_duplicate_or_missing_checksum_keeps_binary(self):
        path = self.downloads / "SHA256SUMS"
        path.write_text(path.read_text() * 2)
        self.run_install(success=False)
        self.assert_unchanged()
        path.write_text("0  unrelated.tar.gz\n")
        self.run_install(success=False)
        self.assert_unchanged()

    def test_version_mismatch_keeps_binary(self):
        self.run_install("--version", "v0.2.0", success=False)
        self.assert_unchanged()
        self.package(binary_version="v9.9.9")
        self.run_install(success=False)
        self.assert_unchanged()

    def test_symlink_member_rejected(self):
        self.package(symlink=True)
        self.run_install(success=False)
        self.assert_unchanged()

    def test_download_failure_keeps_binary(self):
        (self.downloads / "herdrx-linux-amd64.tar.gz").unlink()
        self.run_install(success=False)
        self.assert_unchanged()

    def test_invalid_platform_arch_or_version_does_not_download(self):
        for key, value in (("FIXTURE_OS", "Darwin"), ("FIXTURE_ARCH", "riscv64")):
            self.env[key] = value
            self.run_install(success=False)
            self.env.pop(key)
        for version in ("v1", "../../main", "v1.2.3;false", "main"):
            self.run_install("--version", version, success=False)
        self.assertFalse(self.calls.exists())
        self.assert_unchanged()


if __name__ == "__main__":
    unittest.main()
