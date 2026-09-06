#!/usr/bin/env python3
"""隔离发布命令的外部工具，验证失败时不会推进 latest；不访问真实仓库。"""
import json
import os
from pathlib import Path
import subprocess
import sys
import tarfile
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[1]
REVISION = "a" * 40
DIGEST = "sha256:" + "3" * 64
FAKE_TOOL = r'''
import json, os, sys
from pathlib import Path
a = sys.argv[1:]
mode = os.environ['TEST_PUBLISH_CASE']
with open(os.environ['TEST_PUBLISH_CALLS'], 'a') as f:
    f.write(json.dumps([Path(sys.argv[0]).name] + a) + '\n')
if Path(sys.argv[0]).name == 'gh':
    print('b' * 40 if mode == 'stale' else os.environ['IMAGE_REVISION'])
elif a[:2] == ['image', 'inspect']:
    fmt, target = a[-2:]
    arch = 'arm64' if target.endswith('arm64') or target.endswith('2' * 64) else 'amd64'
    if 'revision' in fmt:
        print('wrong' if mode == 'wrong-revision' and arch == 'arm64' else os.environ['IMAGE_REVISION'])
    elif 'Architecture' in fmt:
        print('linux/' + arch)
    else:
        print('mismatch' if mode == 'digest-mismatch' and '@sha256:' in target else 'id-' + arch)
elif a[:3] == ['buildx', 'imagetools', 'inspect']:
    target = a[3]
    if '--raw' in a:
        arches = ['amd64'] if mode == 'missing-platform' else ['amd64', 'arm64']
        print(json.dumps({'manifests': [{'platform': {'os': 'linux', 'architecture': arch}} for arch in arches]}))
    else:
        number = '1' if target.endswith('-amd64') else '2' if target.endswith('-arm64') else '3'
        print(json.dumps({'digest': 'sha256:' + number * 64}))
elif a[0] == 'push' and mode == 'push-failure' and a[-1].endswith('arm64'):
    sys.exit(1)
'''


class PublishTests(unittest.TestCase):
    def exercise(self, case):
        with tempfile.TemporaryDirectory(prefix="herdrx-publish-test-") as directory:
            temp = Path(directory)
            for tool in ("docker", "gh"):
                path = temp / tool
                path.write_text(f"#!{sys.executable}\n" + FAKE_TOOL)
                path.chmod(0o755)
            calls_file = temp / "calls.jsonl"
            output = temp / "release"
            env = {
                **os.environ,
                "PATH": str(temp) + os.pathsep + os.environ["PATH"],
                "ZOT_REGISTRY": "registry.example.test",
                "ZOT_REPOSITORY": "herdrx/server",
                "IMAGE_REVISION": REVISION,
                "GITHUB_REPOSITORY": "example/herdrx",
                "PUBLISH_OUTPUT": str(output),
                "TEST_PUBLISH_CASE": case,
                "TEST_PUBLISH_CALLS": str(calls_file),
            }
            result = subprocess.run(["bash", "scripts/publish-images.sh"], cwd=ROOT, env=env, capture_output=True, text=True)
            calls = [json.loads(line) for line in calls_file.read_text().splitlines()]
            promotions = [call for call in calls if call[:4] == ["docker", "buildx", "imagetools", "create"] and any(arg.endswith(":latest") for arg in call)]
            if case in ("success", "stale"):
                self.assertEqual(result.returncode, 0, result.stderr)
                self.assertEqual(len(promotions), 1 if case == "success" else 0)
                self.assertIn(f"HERDRX_IMAGE=registry.example.com/herdrx/server@{DIGEST}\n", (output / "release.env").read_text())
                for artifact in ("release.env", "verification.txt"):
                    self.assertNotIn(env["ZOT_REGISTRY"], (output / artifact).read_text())
                with tarfile.open(output / "herdrx-deploy.tar.gz") as archive:
                    alias = archive.getmember("compose.prod.yml")
                    self.assertTrue(alias.issym())
                    self.assertEqual(alias.linkname, "compose.yml")
            else:
                self.assertNotEqual(result.returncode, 0)
                self.assertFalse(promotions)
                self.assertFalse(output.exists())
                if case == "wrong-revision":
                    self.assertFalse(any(call[1] in ("push", "tag") for call in calls))

    def test_verified_main_is_promoted(self):
        self.exercise("success")

    def test_old_main_does_not_regress_latest(self):
        self.exercise("stale")

    def test_wrong_revision_blocks_all_pushes(self):
        self.exercise("wrong-revision")

    def test_downloaded_content_must_match_candidate(self):
        self.exercise("digest-mismatch")

    def test_partial_push_cannot_promote_latest(self):
        self.exercise("push-failure")

    def test_both_platforms_are_required(self):
        self.exercise("missing-platform")


if __name__ == "__main__":
    unittest.main()
