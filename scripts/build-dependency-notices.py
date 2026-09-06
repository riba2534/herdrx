#!/usr/bin/env python3
"""Generate a reproducible CycloneDX inventory and available dependency notices."""
import argparse
import csv
import hashlib
import io
import json
import os
from pathlib import Path
import re
import subprocess
from urllib.parse import quote

ROOT = Path(__file__).resolve().parents[1]
PROJECT = "github.com/riba2534/herdrx"
NOTICE_NAME = re.compile(r"^(?:licen[cs]e|copying|notice|copyright)(?:$|[._-])", re.I)


def run(*args, env=None):
    return subprocess.check_output(args, cwd=ROOT, env=env, text=True, stderr=subprocess.PIPE)


def stream(raw):
    decoder = json.JSONDecoder()
    while raw.strip():
        value, end = decoder.raw_decode(raw.lstrip())
        raw = raw.lstrip()[end:]
        yield value


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--check", action="store_true")
    args = parser.parse_args()
    packages = {}
    for arch in ("amd64", "arm64"):
        env = {**os.environ, "GOOS": "linux", "GOARCH": arch, "CGO_ENABLED": "0"}
        for package in stream(run("go", "list", "-deps", "-json", "./cmd/herdrx", "./cmd/herdrx-server", env=env)):
            if package.get("Module", {}).get("Path") != PROJECT and package.get("Module"):
                packages[package["ImportPath"]] = package
    report = run("go", "run", "github.com/google/go-licenses@v1.6.0", "report", "./cmd/herdrx", "./cmd/herdrx-server", "--ignore", PROJECT)
    go_licenses = {}
    for name, source, license_id in csv.reader(io.StringIO(report)):
        if license_id == "Unknown" and name == "modernc.org/mathutil":
            # v1.7.1's complete three-clause text was inspected; the older
            # classifier misses its formatting. The exact text is bundled.
            license_id = "BSD-3-Clause"
        assert license_id != "Unknown", f"unreviewed dependency license: {name}"
        go_licenses[name] = license_id
    modules = {}
    notices = {}

    def add_notice(path, label):
        if not path.is_file() or path.is_symlink():
            return
        raw = path.read_bytes()
        assert len(raw) < 2 << 20, f"unexpected notice size: {label}"
        text = raw.decode("utf-8")
        digest = hashlib.sha256(raw).hexdigest()
        record = notices.setdefault(digest, {"text": text, "labels": set()})
        record["labels"].add(label)

    for package in packages.values():
        module = package["Module"]
        current = modules.setdefault(module["Path"], {**module, "licenses": set()})
        for name, value in go_licenses.items():
            if package["ImportPath"] == name or package["ImportPath"].startswith(name + "/"):
                current["licenses"].add(value)
        root = Path(module["Dir"])
        directory = Path(package["Dir"])
        while directory.is_relative_to(root):
            for path in directory.iterdir():
                if NOTICE_NAME.match(path.name):
                    add_notice(path, f"{module['Path']}@{module['Version']}/{path.relative_to(root)}")
            if directory == root:
                break
            directory = directory.parent
    components = []
    for name, module in sorted(modules.items()):
        assert module["licenses"], f"no reviewed license for imported Go module: {name}"
        ref = f"pkg:golang/{quote(name, safe='/')}@{module['Version']}"
        components.append({"type": "library", "name": name, "version": module["Version"], "bom-ref": ref, "purl": ref,
                           "licenses": [{"expression": " AND ".join(sorted(module["licenses"]))}],
                           "properties": [{"name": "herdrx:inventory", "value": "Go Linux amd64/arm64 imported modules; CLI and website"}]})
    npm = json.loads(run("pnpm", "--dir", "web", "licenses", "list", "--json"))
    npm_entries = {entry["name"]: entry for entries in npm.values() for entry in entries}
    npm_seen = set()
    for license_id, entries in sorted(npm.items()):
        assert license_id not in ("Unknown", "UNLICENSED"), "unreviewed npm dependency license"
        for entry in entries:
            for version in entry["versions"]:
                identity = (entry["name"], version)
                if identity in npm_seen:
                    continue
                npm_seen.add(identity)
                ref = f"pkg:npm/{quote(entry['name'], safe='/')}@{version}"
                components.append({"type": "library", "name": entry["name"], "version": version, "bom-ref": ref, "purl": ref,
                                   "licenses": [{"expression": "MIT AND BSD-3-Clause" if entry["name"] == "stackback" else license_id}],
                                   "properties": [{"name": "herdrx:inventory", "value": "Installed frontend packages, including build/test tools"}]})
            found = False
            for folder in entry["paths"]:
                root = Path(folder)
                for path in root.iterdir():
                    if NOTICE_NAME.match(path.name) and path.is_file():
                        add_notice(path, f"{entry['name']}@{','.join(entry['versions'])}/{path.name}")
                        found = True
            if not found:
                parent = next((parent for prefix, parent in (("@esbuild/", "esbuild"), ("@rollup/rollup-", "rollup"), ("@oxlint/binding-", "oxlint")) if entry["name"].startswith(prefix)), None)
                if parent:
                    for folder in npm_entries[parent]["paths"]:
                        for path in Path(folder).iterdir():
                            if NOTICE_NAME.match(path.name) and path.is_file():
                                add_notice(path, f"{entry['name']}@{','.join(entry['versions'])} / parent {parent}/{path.name}")
                                found = True
                elif entry["name"] == "saxes" and entry["versions"] == ["6.0.0"]:
                    add_notice(ROOT / "licenses/saxes-6.0.0.txt", "saxes@6.0.0/LICENSE (upstream v6.0.0 source)")
                    found = True
                elif (entry["name"], tuple(entry["versions"]), license_id) in {
                    ("react-remove-scroll-bar", ("2.3.8",), "MIT"),
                    ("stackback", ("0.0.2",), "MIT"),
                    ("@napi-rs/lzma-linux-x64-gnu", ("1.5.1",), "MIT"),
                }:
                    # These npm archives declare MIT but omit a standalone
                    # license file. Preserve their actual declaration, without
                    # inventing an upstream copyright notice. See audit notes.
                    metadata = json.loads((Path(entry["paths"][0]) / "package.json").read_text())
                    declaration = {field: metadata[field] for field in ("name", "version", "license", "author", "repository") if field in metadata}
                    body = json.dumps(declaration, ensure_ascii=False, indent=2) + "\n"
                    digest = hashlib.sha256(body.encode()).hexdigest()
                    notices[digest] = {"text": body, "labels": {f"{entry['name']}@{','.join(entry['versions'])} / upstream package.json license declaration (no standalone license shipped)"}}
                    if entry["name"] == "stackback":
                        header = (Path(entry["paths"][0]) / "formatstack.js").read_text().split("\n\n", 1)[0] + "\n"
                        notices[hashlib.sha256(header.encode()).hexdigest()] = {"text": header, "labels": {"stackback@0.0.2/formatstack.js / V8 BSD-3-Clause notice"}}
                    found = True
            assert found, f"missing bundled npm license text: {entry['name']}"
    go_root = Path(run("go", "env", "GOROOT").strip())
    add_notice(go_root / "LICENSE", "Go runtime / LICENSE")
    components.sort(key=lambda component: component["bom-ref"])
    bom = {"bomFormat": "CycloneDX", "specVersion": "1.6", "version": 1,
           "metadata": {"component": {"type": "application", "name": "herdrx", "version": "source"},
                        "properties": [{"name": "herdrx:" + str(path) + ":sha256", "value": hashlib.sha256((ROOT / path).read_bytes()).hexdigest()} for path in (Path("go.mod"), Path("go.sum"), Path("web/pnpm-lock.yaml"))]},
           "components": components}
    text = "# 第三方依赖声明\n\n本文件由 `scripts/build-dependency-notices.py` 从固定依赖版本生成，保留上游随包附带的许可与声明原文。包含 Linux amd64/arm64 网站与 CLI 的 Go 导入模块，以及前端安装依赖（含构建和测试工具），不表示所有列出的代码都会进入最终二进制。主项目许可单独见 LICENSE。\n\n机器可读清单见 `sbom.cdx.json`。caniuse-lite 的兼容性数据采用 CC-BY-4.0，仅用于前端构建，署名及完整许可保留于下列原文。三个未附独立许可文件的 npm 包保留其真实 package.json 许可声明，具体边界见 `docs/dependency-review-2026-09-07.md`，未生成或冒充上游版权原文。\n"
    for digest, item in sorted(notices.items(), key=lambda pair: sorted(pair[1]["labels"])[0]):
        text += "\n## " + sorted(item["labels"])[0] + "\n\n"
        text += "\n".join("- " + label for label in sorted(item["labels"])) + "\n\n````text\n" + item["text"].rstrip() + "\n````\n"
    for path, content in ((ROOT / "sbom.cdx.json", json.dumps(bom, ensure_ascii=False, indent=2) + "\n"), (ROOT / "THIRD_PARTY_NOTICES.md", text)):
        if args.check:
            assert path.read_bytes() == content.encode(), f"stale dependency artifact: {path.name}"
        else:
            path.write_text(content)
    print(f"Dependency inventory: {len(modules)} Go modules, {len(npm_seen)} npm versions, {len(notices)} distinct notice texts")


if __name__ == "__main__":
    main()
