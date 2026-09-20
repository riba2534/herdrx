#!/usr/bin/env python3
"""Download a pinned Herdr and require real-instance tests to execute and pass."""

import argparse
import hashlib
import json
import os
from pathlib import Path
import re
import shutil
import subprocess
import sys
import tempfile
import urllib.request

ROOT = Path(__file__).resolve().parents[1]
MANIFEST = Path(__file__).with_name("herdr-releases.json")


def checksum(path):
    with path.open("rb") as source:
        return hashlib.file_digest(source, "sha256").hexdigest()


def pinned_binary(manifest, version, target, cache):
    release = manifest["versions"][version][target]
    path = cache / version / target / "herdr"
    if path.exists():
        if checksum(path) != release["sha256"]:
            raise RuntimeError("cached Herdr checksum mismatch; remove the corrupt cache entry")
    else:
        path.parent.mkdir(parents=True, exist_ok=True)
        url = f'https://github.com/{manifest["repository"]}/releases/download/v{version}/{release["asset"]}'
        request = urllib.request.Request(url, headers={"User-Agent": "herdrx-compatibility-ci"})
        with tempfile.NamedTemporaryFile(dir=path.parent, delete=False) as output:
            temporary = Path(output.name)
            try:
                with urllib.request.urlopen(request, timeout=90) as response:
                    shutil.copyfileobj(response, output)
                output.close()
                if checksum(temporary) != release["sha256"]:
                    raise RuntimeError(f"downloaded Herdr {version}/{target} checksum mismatch")
                temporary.chmod(0o700)
                temporary.replace(path)
            finally:
                temporary.unlink(missing_ok=True)
    path.chmod(0o700)
    return path.resolve()


def verify_events(events, required, returncode):
    outcomes = {}
    packages = {}
    failures = []
    for event in events:
        action, package, test = event.get("Action"), event.get("Package"), event.get("Test")
        if test and action in {"pass", "fail", "skip"}:
            outcomes[(package, test)] = action
            if action != "pass":
                failures.append(f"{package}/{test}: {action}")
        elif not test and action in {"pass", "fail", "skip"}:
            packages[package] = action
    for package, names in required.items():
        if packages.get(package) != "pass":
            failures.append(f"{package}: package did not pass")
        for name in names:
            if outcomes.get((package, name)) != "pass":
                failures.append(f"{package}/{name}: required real-instance test did not pass")
    if returncode:
        failures.append(f"go test exited {returncode}")
    if failures:
        raise RuntimeError("\n".join(failures))
    return sum(1 for (_, test), action in outcomes.items() if "/" not in test and action == "pass")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--version", required=True)
    parser.add_argument("--cache", type=Path, required=True)
    parser.add_argument("--output", type=Path)
    parser.add_argument("--download-only", action="store_true", help="verify the pinned binary and print only its absolute path")
    parser.add_argument("--race", action="store_true")
    args = parser.parse_args()
    if not args.download_only and args.output is None:
        parser.error("--output is required unless --download-only is set")
    manifest = json.loads(MANIFEST.read_text())
    goos, arch = subprocess.check_output(["go", "env", "GOOS", "GOARCH"], text=True).split()
    target = f"{goos}-{arch}"
    if target not in manifest["versions"].get(args.version, {}):
        raise RuntimeError(f"no pinned release for {args.version}/{target}")
    if shutil.which("python3") is None:
        raise RuntimeError("python3 is required by the isolated terminal fixtures")
    binary = pinned_binary(manifest, args.version, target, args.cache)
    version_text = subprocess.check_output([str(binary), "--version"], text=True).strip()
    if not re.search(rf"\b{re.escape(args.version)}\b", version_text):
        raise RuntimeError(f"Herdr version mismatch: {version_text}")
    if args.download_only:
        print(binary)
        return
    excluded = manifest.get("not_applicable", {}).get(goos, {})
    required = {package: [name for name in names if name not in excluded]
                for package, names in manifest["required_tests"].items()}
    packages = ["./" + package.split("/herdrx/", 1)[1] for package in required]
    # Discover new fixtures too. The explicit manifest detects removed/renamed coverage.
    listing = subprocess.check_output(["go", "test", *packages, "-list", "WithRealHerdr$"], cwd=ROOT, text=True)
    tests = sorted({line for line in listing.splitlines()
                    if re.fullmatch(r"Test\w*WithRealHerdr", line) and line not in excluded})
    if not tests:
        raise RuntimeError("no real-instance tests discovered")
    selector = "^(" + "|".join(tests) + ")$"
    command = ["go", "test", "-json", "-count=1", "-timeout=8m", *packages, "-run", selector]
    if args.race:
        command.insert(2, "-race")
    args.output.mkdir(parents=True, exist_ok=True)
    # Never inherit a parent Herdr session or socket override into a fixture.
    env = {key: value for key, value in os.environ.items() if not key.startswith("HERDR_")}
    env.update(HERDRX_TEST_HERDR=str(binary), TMPDIR="/tmp")
    events = []
    with (args.output / "go-test.jsonl").open("w") as log:
        process = subprocess.Popen(command, cwd=ROOT, env=env, stdout=subprocess.PIPE, stderr=subprocess.STDOUT, text=True)
        for line in process.stdout:
            log.write(line)
            log.flush()
            try:
                event = json.loads(line)
            except json.JSONDecodeError:
                print(line, end="", flush=True)
                continue
            events.append(event)
            if event.get("Output"):
                print(event["Output"], end="", flush=True)
        returncode = process.wait()
    count = verify_events(events, required, returncode)
    summary = {"herdr": args.version, "platform": target, "sha256": checksum(binary),
               "passed_top_level_tests": count, "not_applicable": excluded, "race": args.race}
    (args.output / "summary.json").write_text(json.dumps(summary, indent=2) + "\n")
    print(json.dumps(summary), flush=True)


if __name__ == "__main__":
    try:
        main()
    except (RuntimeError, subprocess.CalledProcessError, OSError, KeyError) as error:
        print(f"Herdr compatibility gate failed: {error}", file=sys.stderr)
        sys.exit(1)
