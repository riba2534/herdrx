#!/usr/bin/env python3
"""Exercise publisher guards against a fake gh/git command surface; never contact GitHub."""
import hashlib
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[1]
CANDIDATE = Path(sys.argv.pop(1)).resolve()


class PublisherTest(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory(prefix="herdrx-publish-cli-")
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name)
        self.scripts = self.root / "scripts"
        self.scripts.mkdir()
        for script in ("publish-cli-release.py", "verify-cli-release.py", "release_signing.py"):
            shutil.copyfile(ROOT / "scripts" / script, self.scripts / script)
        public_dir = self.root / "internal/updater"
        public_dir.mkdir(parents=True)
        shutil.copyfile(ROOT / "internal/updater/release.pub", public_dir / "release.pub")
        self.assets = self.root / "assets"
        shutil.copytree(CANDIDATE, self.assets)
        metadata = json.loads((self.assets / "release.json").read_text())
        self.version = metadata["version"]
        # Synthetic clean metadata belongs only to this isolated test fixture.
        metadata.update(source_dirty=False, revision="a" * 40)
        (self.assets / "release.json").write_text(json.dumps(metadata))
        self.rehash()
        self.tools = self.root / "tools"
        self.tools.mkdir()
        self.remote = self.root / "remote"
        self.remote.mkdir()
        self.calls = self.root / "calls.jsonl"
        fake_git = self.tools / "git"
        fake_git.write_text('#!/bin/sh\nprintf "%s\\n" aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\n')
        fake_git.chmod(0o755)
        fake_gh = self.tools / "gh"
        fake_gh.write_text(f'''#!{shutil.which("python3")}
import json, os, pathlib, shutil, sys
a = sys.argv[1:]
with open(os.environ['FIXTURE_CALLS'], 'a') as stream: stream.write(json.dumps(a) + '\\n')
remote = pathlib.Path(os.environ['FIXTURE_REMOTE'])
if a[:2] == ['release', 'view']:
    if os.environ.get('FIXTURE_PUBLISHED') == '1': print(json.dumps({{'isDraft': False}}))
    else: sys.exit(1)
elif a[0] == 'api':
    if '/git/ref/' in a[1]: print(json.dumps({{'object': {{'type': 'commit', 'sha': ('b' if os.environ.get('FIXTURE_MOVED') == '1' else 'a') * 40}}}}))
    elif '/releases?' in a[1]: print('[[]]')
    else: sys.exit(99)
elif a[:2] == ['run', 'list']:
    state = os.environ.get('FIXTURE_MAIN_CI', 'success')
    if state == 'missing': print('[]')
    else: print(json.dumps([{{'headSha': 'a' * 40, 'status': 'in_progress' if state == 'pending' else 'completed', 'conclusion': state}}]))
elif a[:2] == ['release', 'create']: pass
elif a[:2] == ['release', 'upload']:
    for asset in a[a.index('--clobber') + 1:]: shutil.copyfile(asset, remote / pathlib.Path(asset).name)
elif a[:2] == ['release', 'download']:
    target = pathlib.Path(a[a.index('--dir') + 1])
    for asset in remote.iterdir(): shutil.copyfile(asset, target / asset.name)
    if os.environ.get('FIXTURE_CORRUPT') == '1': (target / 'README-CLI.md').write_text('damaged download')
elif a[:2] == ['release', 'edit']: pass
else: sys.exit(99)
''')
        fake_gh.chmod(0o755)
        self.env = {**os.environ, "PATH": str(self.tools) + ":" + os.environ["PATH"], "FIXTURE_REMOTE": str(self.remote), "FIXTURE_CALLS": str(self.calls), "GH_TOKEN": "unused-fixture-token"}

    def rehash(self):
        paths = sorted(p for p in self.assets.iterdir() if p.name != "SHA256SUMS")
        (self.assets / "SHA256SUMS").write_text("".join(f"{hashlib.sha256(p.read_bytes()).hexdigest()}  {p.name}\n" for p in paths))

    def publish(self, success):
        result = subprocess.run([sys.executable, str(self.scripts / "publish-cli-release.py"), str(self.assets), "--version", self.version, "--revision", "a" * 40], env=self.env, capture_output=True, text=True)
        self.assertEqual(result.returncode == 0, success, result.stdout + result.stderr)
        return [json.loads(line) for line in self.calls.read_text().splitlines()] if self.calls.exists() else []

    def test_draft_upload_download_verify_then_publish(self):
        calls = self.publish(True)
        verbs = [call[1] for call in calls if call[0] == "release"]
        self.assertEqual(verbs, ["view", "create", "upload", "download", "edit"])
        edit = calls[-1]
        self.assertIn("--draft=false", edit)
        self.assertIn("--prerelease=" + str("-" in self.version).lower(), edit)
        self.assertIn("--latest=" + str("-" not in self.version).lower(), edit)

    def test_dirty_source_rejected_before_network(self):
        path = self.assets / "release.json"
        metadata = json.loads(path.read_text())
        metadata["source_dirty"] = True
        path.write_text(json.dumps(metadata))
        self.rehash()
        self.assertEqual(self.publish(False), [])

    def test_moved_tag_rejected_before_upload(self):
        self.env["FIXTURE_MOVED"] = "1"
        calls = self.publish(False)
        self.assertFalse(any(call[:2] == ["release", "upload"] for call in calls))

    def test_incomplete_or_failed_main_ci_cannot_publish(self):
        for state in ("missing", "pending", "failure", "cancelled"):
            with self.subTest(state=state):
                self.env["FIXTURE_MAIN_CI"] = state
                self.calls.unlink(missing_ok=True)
                calls = self.publish(False)
                self.assertFalse(any(call[0] == "release" for call in calls))

    def test_rehashed_tampered_signature_rejected_before_network(self):
        path = self.assets / "herdrx-linux-amd64.manifest.json"
        manifest = json.loads(path.read_text())
        manifest["signature"] = "00" * 64
        path.write_text(json.dumps(manifest))
        self.rehash()
        self.assertEqual(self.publish(False), [])

    def test_published_version_never_overwritten(self):
        self.env["FIXTURE_PUBLISHED"] = "1"
        calls = self.publish(False)
        self.assertFalse(any(call[:2] == ["release", "upload"] for call in calls))

    def test_corrupt_download_keeps_release_as_draft(self):
        self.env["FIXTURE_CORRUPT"] = "1"
        calls = self.publish(False)
        self.assertTrue(any(call[:2] == ["release", "download"] for call in calls))
        self.assertFalse(any(call[:2] == ["release", "edit"] for call in calls))


if __name__ == "__main__":
    unittest.main()
