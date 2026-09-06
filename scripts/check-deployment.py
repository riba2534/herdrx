#!/usr/bin/env python3
"""检查发布配置：只拉镜像、明确目录挂载、没有隐式持久卷。"""
import json
import os
from pathlib import Path
import subprocess

ROOT = Path(__file__).resolve().parents[1]
env = {**os.environ, "HERDRX_DERP_HOSTNAME": "derp.example.com"}
configs = [
    ("production", ["deploy/compose.yml"], False),
    ("production alias", ["deploy/compose.prod.yml"], False),
    ("development", ["deploy/compose.yml", "deploy/compose.dev.yml"], True),
    ("DERP", ["deploy/compose.derp.yml"], True),
]
for name, files, allow_build in configs:
    command = ["docker", "compose", "--env-file", "deploy/.env.example"]
    for file in files:
        command += ["-f", file]
    config = json.loads(subprocess.check_output(command + ["config", "--format", "json"], cwd=ROOT, env=env))
    assert not config.get("volumes"), f"{name}: 禁止命名卷"
    for service, spec in config["services"].items():
        assert allow_build or "build" not in spec, f"{name}/{service}: 部署配置不能要求源码构建"
        for mount in spec.get("volumes", []):
            assert mount["type"] == "bind" and mount.get("source"), f"{name}/{service}: 必须使用显式 bind mount"
    if "herdrx" in config["services"]:
        mounts = config["services"]["herdrx"]["volumes"]
        assert any(m["target"] == "/data" and m["source"] == str(ROOT / "deploy/data") for m in mounts)
    if name in {"production", "production alias"}:
        assert set(config["services"]) == {"herdrx"}, "默认部署只运行 HTTP 网站，不附带 TLS 代理"
        app = config["services"]["herdrx"]
        assert str(app["environment"]["HERDRX_COOKIE_SECURE"]).lower() == "false"
        assert app["environment"]["HERDRX_PUBLIC_URL"].startswith("http://")
        assert app["environment"]["HERDRX_TRUSTED_PROXIES"] == ""
        assert len(app["ports"]) == 1 and app["ports"][0]["target"] == 8080
    print(f"{name}: OK")

assert not any(line.strip().upper().startswith("VOLUME ") for line in (ROOT / "deploy/Dockerfile").read_text().splitlines())
assert "HERDRX_COOKIE_SECURE=false" in (ROOT / "deploy/Dockerfile").read_text(), "镜像默认必须可通过 HTTP 登录"
assert (ROOT / "AGENTS.md").is_symlink() and os.readlink(ROOT / "AGENTS.md") == "CLAUDE.md"
print("Dockerfile 与 Agent 指引链接：OK")
