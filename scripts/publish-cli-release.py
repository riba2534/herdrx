#!/usr/bin/env python3
"""Publish only verified, clean, tagged CLI assets; verify draft downloads before visibility."""
import argparse
import hashlib
import importlib.util
import json
from pathlib import Path
import re
import subprocess
import tempfile

ROOT = Path(__file__).resolve().parents[1]
REPOSITORY = "riba2534/herdrx"


def gh(*args, check=True):
    return subprocess.run(["gh", *args], cwd=ROOT, check=check, text=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("directory", type=Path)
    parser.add_argument("--version", required=True)
    parser.add_argument("--revision", required=True)
    args = parser.parse_args()
    if not re.fullmatch(r"v\d+\.\d+\.\d+(?:-[0-9A-Za-z]+(?:[.-][0-9A-Za-z]+)*)?", args.version):
        parser.error("invalid version tag")
    spec = importlib.util.spec_from_file_location("verify_cli", ROOT / "scripts/verify-cli-release.py")
    verifier = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(verifier)
    metadata = verifier.verify(args.directory)
    assert metadata["version"] == args.version and metadata["revision"] == args.revision
    assert metadata["source_dirty"] is False, "refuse to publish uncommitted source"
    revision = subprocess.check_output(["git", "rev-parse", f"{args.version}^{{commit}}"], cwd=ROOT, text=True).strip()
    assert revision == args.revision, "tag does not identify the packaged source"
    remote_tag = json.loads(gh("api", f"repos/{REPOSITORY}/git/ref/tags/{args.version}").stdout)["object"]
    for _ in range(4):
        if remote_tag["type"] == "commit":
            break
        assert remote_tag["type"] == "tag", "unexpected tag object"
        remote_tag = json.loads(gh("api", f"repos/{REPOSITORY}/git/tags/{remote_tag['sha']}").stdout)["object"]
    assert remote_tag["type"] == "commit" and remote_tag["sha"] == args.revision, "remote tag moved"
    found = gh("release", "view", args.version, "--repo", REPOSITORY, "--json", "isDraft", check=False)
    if found.returncode == 0:
        assert json.loads(found.stdout)["isDraft"], "published releases are immutable; use a new version"
    else:
        with tempfile.TemporaryDirectory(prefix="herdrx-release-notes-") as temporary:
            notes = Path(temporary) / "notes.md"
            notes.write_text(f"herdrx {args.version} 提供远程主机 CLI：下载安装、配置后台服务，再通过网站三步引导绑定主机。\n\n支持 Linux x86_64 / ARM64。下载对应安装包，或使用附件 install-herdrx.sh；完整命令见 README-CLI.md，SHA256SUMS 提供校验值。Herdr 需要提前独立安装和运行。\n\n源码提交：{args.revision}\n")
            gh("release", "create", args.version, "--repo", REPOSITORY, "--verify-tag", "--draft", "--title", f"herdrx {args.version}", "--notes-file", str(notes))
    assets = sorted(args.directory.resolve().iterdir())
    gh("release", "upload", args.version, "--repo", REPOSITORY, "--clobber", *(str(p) for p in assets))
    with tempfile.TemporaryDirectory(prefix="herdrx-release-download-") as temporary:
        gh("release", "download", args.version, "--repo", REPOSITORY, "--dir", temporary)
        downloaded = Path(temporary)
        assert {p.name for p in downloaded.iterdir()} == verifier.ASSETS, "draft contains unexpected assets"
        for asset in assets:
            assert hashlib.sha256((downloaded / asset.name).read_bytes()).digest() == hashlib.sha256(asset.read_bytes()).digest(), f"uploaded content differs: {asset.name}"
    # A newly published older stable tag must not move the installer backward.
    latest = False
    if "-" not in args.version:
        pages = json.loads(gh("api", f"repos/{REPOSITORY}/releases?per_page=100", "--paginate", "--slurp").stdout)
        stable = [tuple(map(int, release["tag_name"][1:].split("."))) for page in pages for release in page
                  if not release["draft"] and not release["prerelease"] and re.fullmatch(r"v\d+\.\d+\.\d+", release["tag_name"])]
        latest = tuple(map(int, args.version[1:].split("."))) >= max(stable, default=(0, 0, 0))
    gh("release", "edit", args.version, "--repo", REPOSITORY, "--draft=false", f"--prerelease={'true' if '-' in args.version else 'false'}", f"--latest={str(latest).lower()}")
    print(f"Published https://github.com/{REPOSITORY}/releases/tag/{args.version}; all downloaded assets verified")


if __name__ == "__main__":
    main()
