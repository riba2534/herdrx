#!/usr/bin/env python3
"""Verify real Caddy forwarding, spoof resistance, HTTPS and secure cookies.

Uses only isolated containers, a temporary bind directory and generated data.
Usage: python3 scripts/test-caddy-auth.py <candidate image>
"""
import json
import base64
import os
from pathlib import Path
import re
import secrets
import shutil
import socket
import ssl
import subprocess
import sys
import tempfile
import time
import urllib.error
import urllib.request

root = Path(tempfile.mkdtemp(prefix='herdrx-caddy-auth-'))
data = root / 'data'
prefix = 'herdrx-proxy-' + secrets.token_hex(4)
network = prefix + '-net'
subnet = '172.30.95.0/29'
containers = []
sudo = [] if os.geteuid() == 0 else ['sudo', '-n']

def run(*args):
    try:
        return subprocess.check_output(args, text=True, stderr=subprocess.STDOUT).strip()
    except subprocess.CalledProcessError as error:
        raise RuntimeError(error.output) from None

def launch(suffix, *args):
    name = prefix + '-' + suffix
    containers.append(name)
    run('docker', 'run', '-d', '--name', name, *args)
    return name

def client_post(client, path, payload, forwarded):
    command = ['docker', 'exec', client, 'wget', '-S', '-O', '-', '--header=Origin: https://localhost',
               '--header=Content-Type: application/json', '--header=X-Forwarded-For: ' + forwarded,
               '--post-data=' + json.dumps(payload), 'http://proxy:80' + path]
    result = subprocess.run(command, text=True, stdout=subprocess.PIPE, stderr=subprocess.STDOUT)
    statuses = re.findall(r'HTTP/\S+ (\d+)', result.stdout)
    assert statuses, 'No HTTP response from isolated proxy'
    return int(statuses[-1]), result.stdout

try:
    run('docker', 'network', 'create', '--subnet', subnet, network)
    run(*sudo, 'install', '-d', '-m', '0700', str(data))
    run(*sudo, 'chown', '65532:65532', str(data))
    app = launch('app', '--network', network, '--network-alias', 'app',
                 '--mount', f'type=bind,source={data},target=/data',
                 '-e', 'HERDRX_PUBLIC_URL=https://localhost', '-e', 'HERDRX_COOKIE_SECURE=true',
                 '-e', 'HERDRX_TRUSTED_PROXIES=172.30.95.6/32', sys.argv[1])
    (root / 'Caddyfile').write_text('''{
  auto_https disable_redirects
}
https://localhost {
  tls internal
  reverse_proxy app:8080
}
:80 {
  reverse_proxy app:8080
}
''')
    # Caddy's own persistence is also explicit bind storage, with no named volumes.
    for directory in ['caddy-data', 'caddy-config']:
        (root / directory).mkdir()
    proxy = launch('proxy', '--network', network, '--network-alias', 'proxy', '--ip', '172.30.95.6',
                   '-p', '127.0.0.1::443',
                   '--mount', f'type=bind,source={root / "Caddyfile"},target=/etc/caddy/Caddyfile,readonly',
                   '--mount', f'type=bind,source={root / "caddy-data"},target=/data',
                   '--mount', f'type=bind,source={root / "caddy-config"},target=/config', 'caddy:2.10-alpine')
    address = run('docker', 'port', proxy, '443/tcp').splitlines()[0]
    port = address.rsplit(':', 1)[1]
    base = 'https://localhost:' + port
    context = ssl._create_unverified_context()  # Test-only Caddy internal CA.
    opener = urllib.request.build_opener(urllib.request.ProxyHandler({}), urllib.request.HTTPSHandler(context=context))
    for _ in range(100):
        try:
            with opener.open(base + '/healthz', timeout=2) as response:
                assert response.status == 200
            break
        except OSError:
            time.sleep(.2)
    else:
        raise RuntimeError('Caddy HTTPS endpoint did not become ready')
    token = run(*sudo, 'cat', str(data / 'bootstrap-token'))
    password = secrets.token_urlsafe(20)
    body = json.dumps({'email': 'proxy@example.test', 'display_name': 'Proxy test', 'password': password, 'token': token}).encode()
    request = urllib.request.Request(base + '/api/bootstrap', data=body, headers={'Origin': 'https://localhost', 'Content-Type': 'application/json'})
    with opener.open(request, timeout=10) as response:
        assert response.status == 200
        cookie = response.headers.get('Set-Cookie', '')
        assert 'Secure' in cookie and 'HttpOnly' in cookie and 'SameSite=Lax' in cookie
        admin = json.loads(response.read())
    # The upgrade must retain Cookie auth and exact Origin checks through Caddy.
    # No hello is sent, so this fixture never opens a remote transport.
    request = urllib.request.Request(base + '/api/hosts/', data=json.dumps({'name': 'Upgrade fixture', 'transport': 'local'}).encode(), headers={
        'Origin': 'https://localhost', 'Content-Type': 'application/json', 'Cookie': cookie.split(';', 1)[0], 'X-CSRF-Token': admin['csrf_token'],
    })
    with opener.open(request, timeout=10) as response:
        host = json.loads(response.read())['host']
    for origin, expected in [('https://localhost', 101), ('https://attacker.example', 403)]:
        with context.wrap_socket(socket.create_connection(('127.0.0.1', int(port)), timeout=5), server_hostname='localhost') as connection:
            key = base64.b64encode(secrets.token_bytes(16)).decode()
            lines = [f'GET /api/hosts/{host["id"]}/ws HTTP/1.1', f'Host: localhost:{port}', 'Connection: Upgrade', 'Upgrade: websocket',
                     'Sec-WebSocket-Version: 13', 'Sec-WebSocket-Key: ' + key, 'Origin: ' + origin, 'Cookie: ' + cookie.split(';', 1)[0], '', '']
            connection.sendall('\r\n'.join(lines).encode())
            with connection.makefile('rb') as reader:
                status = int(reader.readline().split()[1])
            assert status == expected, f'Unexpected WSS upgrade status: {status}'
    clients = [launch('client-' + str(i), '--network', network, '--ip', f'172.30.95.{i}', 'alpine:latest', 'sleep', '300') for i in [4, 5]]
    bad = {'email': 'proxy@example.test', 'password': 'incorrect-password'}
    for i in range(10):
        status, _ = client_post(clients[0], '/api/login', bad, f'198.51.100.{i + 1}')
        assert status == 401, f'Unexpected initial login response: {status}'
    status, _ = client_post(clients[0], '/api/login', bad, '203.0.113.1')
    assert status == 429, 'Forged forwarded IP bypassed per-client account limit'
    status, _ = client_post(clients[1], '/api/login', {'email': 'proxy@example.test', 'password': password}, '203.0.113.1')
    assert status == 200, 'Another real client was blocked by the first client'
    print('Caddy: HTTPS, secure cookie, authenticated WSS, Origin rejection, sanitized forwarding, spoof-resistant rate limit and independent clients passed')
finally:
    for name in reversed(containers):
        subprocess.run(['docker', 'rm', '-f', name], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    subprocess.run(['docker', 'network', 'rm', network], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    subprocess.run(sudo + ['chown', '-R', f'{os.getuid()}:{os.getgid()}', str(root)], check=True)
    shutil.rmtree(root)
