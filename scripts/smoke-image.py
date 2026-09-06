#!/usr/bin/env python3
"""在临时 bind 目录中启动候选镜像，验证健康、初始化和重建后的持久化。"""
import json
import http.cookiejar
import os
from pathlib import Path
import re
import secrets
import shutil
import subprocess
import sys
import tempfile
import time
import urllib.request

image = sys.argv[1]
expected_version = sys.argv[2] if len(sys.argv) > 2 else None
root = Path(tempfile.mkdtemp(prefix="herdrx-image-smoke-"))
data = root / "data"
name = "herdrx-smoke-" + secrets.token_hex(6)
opener = urllib.request.build_opener(urllib.request.ProxyHandler({}), urllib.request.HTTPCookieProcessor(http.cookiejar.CookieJar()))
sudo = [] if os.geteuid() == 0 else ["sudo", "-n"]


def run(*args):
    return subprocess.check_output(args, text=True).strip()


def request(base, path, body=None):
    encoded = json.dumps(body).encode() if body is not None else None
    req = urllib.request.Request(base + path, data=encoded, headers={"Content-Type": "application/json", "Origin": "http://127.0.0.1:8080"})
    with opener.open(req, timeout=5) as response:
        if path in ("/api/bootstrap", "/api/login"):
            assert "; Secure" not in response.headers.get("Set-Cookie", ""), "default image must support its HTTP entrypoint"
        return response.read()


def start():
    run("docker", "run", "-d", "--name", name, "--security-opt", "no-new-privileges:true",
        "--publish", "127.0.0.1::8080", "--mount", f"type=bind,source={data},target=/data",
        "--health-interval", "1s",
        "--health-start-period", "1s", image)
    address = run("docker", "port", name, "8080/tcp").splitlines()[0]
    base = "http://" + address
    deadline = time.monotonic() + 60
    while time.monotonic() < deadline:
        try:
            health = json.loads(request(base, "/healthz"))
            if expected_version:
                assert health["version"] == expected_version, health
            if run("docker", "inspect", "--format", "{{.State.Health.Status}}", name) == "healthy":
                return base
        except (OSError, ValueError):
            pass
        time.sleep(1)
    raise RuntimeError("候选容器在 60 秒内未就绪")


try:
    metadata = json.loads(run("docker", "image", "inspect", image))[0]
    assert metadata["Config"]["User"] in ("nonroot:nonroot", "65532:65532")
    assert not metadata["Config"].get("Volumes"), "镜像不能声明匿名卷"
    run(*sudo, "install", "-d", "-m", "0700", str(data))
    run(*sudo, "chown", "65532:65532", str(data))
    base = start()
    assert json.loads(request(base, "/api/bootstrap/status"))["required"]
    html = request(base, "/").decode()
    assets = re.findall(r'(?:src|href)="(/assets/[^\"]+)"', html)
    assert assets
    for asset in assets:
        assert request(base, asset)

    # 此 token 和密码仅用于隔离测试，不输出到日志。
    token = run(*sudo, "cat", str(data / "bootstrap-token"))
    password = secrets.token_urlsafe(24)
    admin = {"email": "smoke@example.test", "display_name": "Smoke", "password": password, "token": token}
    assert json.loads(request(base, "/api/bootstrap", admin))["user"]["role"] == "admin"
    assert json.loads(request(base, "/api/me"))["user"]["role"] == "admin"
    for filename in ("herdrx.db", "master.key", "vapid.json"):
        run(*sudo, "test", "-f", str(data / filename))
    mounts = json.loads(run("docker", "inspect", "--format", "{{json .Mounts}}", name))
    assert len(mounts) == 1 and mounts[0]["Type"] == "bind" and mounts[0]["Source"] == str(data)

    run("docker", "rm", "-f", name)
    base = start()
    assert not json.loads(request(base, "/api/bootstrap/status"))["required"]
    assert json.loads(request(base, "/api/login", {"email": admin["email"], "password": password}))["user"]["role"] == "admin"
    print(f"{image}: nonroot、bind 挂载、健康检查、静态资源、初始化及容器重建后登录全部通过")
finally:
    subprocess.run(["docker", "rm", "-f", name], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    if data.exists():
        subprocess.run(sudo + ["chown", "-R", f"{os.getuid()}:{os.getgid()}", str(data)], check=True)
    shutil.rmtree(root)
