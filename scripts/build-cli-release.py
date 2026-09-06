#!/usr/bin/env python3
"""Build reproducible Linux CLI archives and GitHub Release assets."""
import argparse
import gzip
import hashlib
import io
import json
import os
from pathlib import Path
import re
import shutil
import subprocess
import tarfile
import tempfile
from datetime import datetime, timezone

import release_signing

ROOT = Path(__file__).resolve().parents[1]
VERSION = release_signing.VERSION


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--version", required=True)
    parser.add_argument("--out", type=Path, required=True)
    parser.add_argument("--signing-key", type=Path, required=True, help="owner-only Ed25519 PEM file outside the repository")
    parser.add_argument("--created-at", help="UTC signing time for repeatable artifacts, e.g. 2026-09-07T00:00:00Z")
    args = parser.parse_args()
    if not VERSION.fullmatch(args.version):
        parser.error("version must be a tag such as v0.1.0 or v0.1.0-rc.1")
    signing_key = release_signing.validate_private_key(args.signing_key)
    created = datetime.strptime(args.created_at, "%Y-%m-%dT%H:%M:%SZ").replace(tzinfo=timezone.utc) if args.created_at else datetime.now(timezone.utc).replace(microsecond=0)
    output = args.out.resolve()
    output.mkdir(parents=True, exist_ok=True)
    if any(output.iterdir()):
        parser.error("output directory must be empty so releases cannot mix")
    revision = subprocess.check_output(["git", "rev-parse", "HEAD"], cwd=ROOT, text=True).strip()
    dirty = bool(subprocess.check_output(["git", "status", "--porcelain"], cwd=ROOT))
    guide = (ROOT / "docs/tailcat-quickstart.md").read_text().replace(
        "(update-and-recovery.md)",
        f"(https://github.com/riba2534/herdrx/blob/{args.version}/docs/update-and-recovery.md)",
    ).encode()
    metadata = {"version": args.version, "revision": revision, "source_dirty": dirty, "platforms": []}
    with tempfile.TemporaryDirectory(prefix="herdrx-cli-release-") as build_dir:
        for arch in ("amd64", "arm64"):
            binary = Path(build_dir) / f"herdrx-{arch}"
            env = {**os.environ, "CGO_ENABLED": "0", "GOOS": "linux", "GOARCH": arch}
            subprocess.run(["go", "build", "-trimpath", "-buildvcs=false", "-ldflags", f"-s -w -X main.version={args.version}", "-o", str(binary), "./cmd/herdrx"], cwd=ROOT, env=env, check=True)
            data = binary.read_bytes()
            manifest_name = f"herdrx-linux-{arch}.manifest.json"
            manifest = release_signing.sign(data, args.version, arch, signing_key, created)
            (output / manifest_name).write_text(json.dumps(manifest, indent=2) + "\n")
            archive_name = f"herdrx-linux-{arch}.tar.gz"
            with (output / archive_name).open("wb") as stream:
                with gzip.GzipFile(filename="", mode="wb", fileobj=stream, mtime=0) as compressed:
                    with tarfile.open(fileobj=compressed, mode="w") as archive:
                        for name, content, mode in (("herdrx", data, 0o755), ("VERSION", (args.version + "\n").encode(), 0o644), ("README.md", guide, 0o644), ("THIRD_PARTY_NOTICES.md", (ROOT / "THIRD_PARTY_NOTICES.md").read_bytes(), 0o644)):
                            member = tarfile.TarInfo(name)
                            member.size, member.mode, member.mtime = len(content), mode, 0
                            archive.addfile(member, io.BytesIO(content))
            metadata["platforms"].append({"os": "linux", "arch": arch, "asset": archive_name, "manifest": manifest_name, "binary_sha256": hashlib.sha256(data).hexdigest()})
    shutil.copyfile(ROOT / "install-herdrx.sh", output / "install-herdrx.sh")
    (output / "README-CLI.md").write_bytes(guide)
    shutil.copyfile(release_signing.PUBLIC_KEY_FILE, output / "RELEASE-PUBLIC-KEY")
    for name in ("THIRD_PARTY_NOTICES.md", "sbom.cdx.json"):
        shutil.copyfile(ROOT / name, output / name)
    (output / "release.json").write_text(json.dumps(metadata, indent=2) + "\n")
    checksums = [f"{hashlib.sha256(path.read_bytes()).hexdigest()}  {path.name}\n" for path in sorted(output.iterdir()) if path.is_file()]
    (output / "SHA256SUMS").write_text("".join(checksums))
    print(f"Built herdrx {args.version}: signed Linux amd64/arm64, installer, guide and SHA256SUMS")


if __name__ == "__main__":
    main()
