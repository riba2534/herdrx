#!/usr/bin/env python3
"""Isolated C2/D experiments; never connect to an existing Herdr session.

The small generation-1 codec below is an experiment, not a product adapter.
It inspects surface headers and raw delivery, not complete terminal rendering.
"""
import argparse
import collections
import contextlib
import json
import os
from pathlib import Path
import select
import shlex
import socket
import struct
import subprocess
import sys
import tempfile
import time

APPLICATION = r'''import os,tty,signal,json,fcntl,termios,struct
tty.setraw(0)
winches=0
received=b''
def record():
    size=struct.unpack('HHHH',fcntl.ioctl(0,termios.TIOCGWINSZ,bytes(8)))
    with open('state.tmp','w') as f:
        json.dump(dict(pid=os.getpid(),size=size,winches=winches,input=received.hex()),f)
    os.replace('state.tmp','state.json')
def resize(*args):
    global winches
    winches+=1
    record()
signal.signal(signal.SIGWINCH,resize)
record()
os.write(1,b'\x1b[?1000h\x1b[?1006hPROTOCOL FIXTURE')
while True:
    data=os.read(0,4096)
    received+=data
    record()
    if data == b'q':
        os.write(1,b'\x1b[14t\x1b[16t\x1b[18t')
    else:
        os.write(1,data)
'''


def uint(value):
    if value < 251:
        return bytes([value])
    if value <= 65535:
        return b'\xfb' + struct.pack('<H', value)
    if value <= 2**32 - 1:
        return b'\xfc' + struct.pack('<I', value)
    return b'\xfd' + struct.pack('<Q', value)


def string(value):
    data = value.encode()
    return uint(len(data)) + data


class Reader:
    def __init__(self, data):
        self.data, self.offset = data, 0

    def integer(self):
        first = self.data[self.offset]
        self.offset += 1
        if first < 251:
            return first
        widths = {251: ('<H', 2), 252: ('<I', 4), 253: ('<Q', 8)}
        fmt, width = widths[first]
        value = struct.unpack_from(fmt, self.data, self.offset)[0]
        self.offset += width
        return value

    def text(self):
        length = self.integer()
        value = self.data[self.offset:self.offset + length].decode()
        self.offset += length
        return value


class Shell:
    def __init__(self, fixture, cols, rows, active):
        self.socket = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
        self.socket.connect(str(fixture.client_socket))
        self.socket.settimeout(3)
        self.buffer = b''
        self.counts = collections.Counter()
        self.messages = []
        self.snapshot = None
        self.welcome = None
        self.surface_headers = []
        hello = dict(generation=1, cell_width_px=8, cell_height_px=16,
                     surface_size=dict(cols=cols, rows=rows), pixel_mouse=False,
                     direct_graphics=False, endpoint_keybindings=False,
                     mouse_capture=False, surface_active=active,
                     surface_reuse=True, surface_delta=True,
                     snapshot_codecs=['shell.snapshot.v1'], surface_codecs=['shell.surface.v1'],
                     input_codecs=['shell.input.semantic.v1'], blob_codecs=['shell.blob.v1'])
        self.send(uint(20) + string('endpoint.hello.v1') + string(json.dumps(hello)))
        self.drain(.5)
        assert self.welcome and not self.welcome.get('error'), self.welcome
        assert self.welcome['generation'] == 1
        assert self.snapshot and self.snapshot['boot_id']

    def send(self, payload):
        self.socket.sendall(struct.pack('<I', len(payload)) + payload)

    def drain(self, duration=.25):
        deadline = time.monotonic() + duration
        while time.monotonic() < deadline:
            if not select.select([self.socket], [], [], min(.05, max(0, deadline - time.monotonic())))[0]:
                continue
            chunk = self.socket.recv(1024 * 1024)
            if not chunk:
                raise RuntimeError('ClientShell closed unexpectedly')
            self.buffer += chunk
            while len(self.buffer) >= 4:
                size = struct.unpack_from('<I', self.buffer)[0]
                if size > 32 * 1024 * 1024:
                    raise RuntimeError('invalid surface size')
                if len(self.buffer) < size + 4:
                    break
                payload, self.buffer = self.buffer[4:size + 4], self.buffer[size + 4:]
                reader = Reader(payload)
                tag = reader.integer()
                self.counts[str(tag)] += 1
                if tag == 20:
                    kind, data = reader.text(), reader.text()
                    self.counts[kind] += 1
                    # Optional surface codecs carry their own encoded payload;
                    # this bounded probe does not pretend to render them.
                    decoded = json.loads(data) if kind in {'endpoint.welcome.v1', 'shell.snapshot.v1'} else None
                    if decoded is not None:
                        self.messages.append((kind, decoded))
                    if kind == 'endpoint.welcome.v1':
                        self.welcome = decoded
                    if kind == 'shell.snapshot.v1':
                        self.snapshot = decoded
                elif tag == 13:
                    self.surface_headers.append(dict(boot_id=reader.text(),
                                                     projection_revision=reader.integer(),
                                                     surface_revision=reader.integer()))

    def focus(self, value):
        self.send(uint(18) + bytes([value]))

    def text(self, pane, value):
        self.send(uint(13) + string(pane) + uint(1) + uint(1) + string(value))

    def wheel(self, pane):
        # One semantic ScrollUp at zero-based cell (3, 2), no pixel geometry.
        self.send(uint(13) + string(pane) + uint(1) + uint(2) + uint(4)
                  + uint(0) + uint(3) + uint(2) + b'\x00' + uint(0) + uint(1))

    def resize(self, cols, rows):
        self.send(uint(12) + uint(8) + uint(16) + uint(cols) + uint(rows) + b'\x00')

    def active(self, value):
        assert 'client_shell.surface.set' in self.welcome['methods']
        request = dict(id='lease-probe', method='client_shell.surface.set', params=dict(active=value))
        self.send(uint(15) + string(self.snapshot['boot_id']) + string(json.dumps(request)))
        self.drain()

    def close(self):
        self.socket.close()


class Fixture:
    def __init__(self, binary, root):
        self.root = root
        self.args = [str(binary), '--session', 'protocol-probe']
        self.env = {key: value for key, value in os.environ.items() if not key.startswith('HERDR_')}
        self.env.update(XDG_CONFIG_HOME=str(root), HERDR_CONFIG_PATH=str(root / 'config.toml'))
        for key in ('SSH_CONNECTION', 'SSH_CLIENT', 'SSH_TTY', 'TMUX', 'STY'):
            self.env.pop(key, None)
        (root / 'config.toml').write_text('onboarding = false\n[terminal]\ndefault_shell = "/bin/sh"\nshell_mode = "non_login"\n')
        (root / 'app.py').write_text(APPLICATION)
        self.api_socket = root / 'herdr/sessions/protocol-probe/herdr.sock'
        self.client_socket = root / 'herdr/sessions/protocol-probe/herdr-client.sock'
        self.log = (root / 'server.log').open('w')
        self.daemon = subprocess.Popen(self.args + ['server'], env=self.env, stdout=self.log, stderr=self.log)

    def start(self):
        self.wait(lambda: self.api_socket.exists())
        self.call('ping')
        created = self.call('workspace.create', dict(cwd=str(self.root), label='Isolated protocol fixture', focus=True))
        self.pane = created['root_pane']['pane_id']
        self.call('pane.send_text', dict(pane_id=self.pane, text=f'{shlex.quote(sys.executable)} app.py\r'))
        self.wait(lambda: (self.root / 'state.json').exists())

    def call(self, method, params=None):
        with socket.socket(socket.AF_UNIX, socket.SOCK_STREAM) as connection:
            connection.settimeout(3)
            connection.connect(str(self.api_socket))
            connection.sendall((json.dumps(dict(id='probe', method=method, params=params or {})) + '\n').encode())
            response = json.loads(connection.makefile('rb').readline())
            if 'error' in response:
                raise RuntimeError(response)
            return response['result']

    def wait(self, condition):
        deadline = time.monotonic() + 8
        while not condition():
            if time.monotonic() > deadline:
                raise RuntimeError('fixture condition timed out')
            time.sleep(.02)

    def state(self):
        return json.loads((self.root / 'state.json').read_text())

    def settled(self):
        time.sleep(.25)
        return self.state()

    def close(self):
        with contextlib.suppress(subprocess.TimeoutExpired):
            subprocess.run(self.args + ['server', 'stop'], env=self.env, stdout=subprocess.DEVNULL,
                           stderr=subprocess.DEVNULL, timeout=3)
        try:
            self.daemon.wait(timeout=3)
        except subprocess.TimeoutExpired:
            self.daemon.kill()
            self.daemon.wait()
        self.log.close()


def shell_probe(fixture):
    clients = []
    results = {}
    try:
        a = Shell(fixture, 140, 50, True)
        clients.append(a)
        a.focus(True)
        a.drain()
        results['a_active'] = fixture.settled()
        b = Shell(fixture, 50, 25, False)
        clients.append(b)
        b.resize(50, 25)
        b.active(True)
        results['b_active_without_focus'] = fixture.settled()
        assert results['a_active']['size'] == results['b_active_without_focus']['size']
        b.text(fixture.pane, '中文')
        b.drain()
        a.drain()
        results['b_semantic_input_without_focus'] = fixture.settled()
        assert results['b_semantic_input_without_focus']['size'] != results['a_active']['size']
        assert '中文'.encode().hex() in results['b_semantic_input_without_focus']['input']
        b.wheel(fixture.pane)
        fixture.wait(lambda: b'\x1b[<64;4;3M'.hex() in fixture.state()['input'])
        results['semantic_wheel_received'] = True
        b.active(False)
        a.drain()
        results['b_inactive'] = fixture.settled()
        assert results['b_inactive']['size'] == results['a_active']['size']
        before = dict(b.counts)
        fixture.call('pane.send_text', dict(pane_id=fixture.pane, text='json-input'))
        a.drain()
        b.drain()
        results['json_input_preserves_a_geometry'] = fixture.settled()
        assert results['json_input_preserves_a_geometry']['size'] == results['a_active']['size']
        assert b.counts.get('13', 0) == before.get('13', 0)
        assert b.counts.get('19', 0) == before.get('19', 0)
        for kind, count in b.counts.items():
            if kind.startswith('endpoint.surface'):
                assert count == before.get(kind, 0)
        # Reconnect creates a new access connection to the existing pane.
        b.close()
        clients.remove(b)
        b = Shell(fixture, 50, 25, False)
        clients.append(b)
        assert b.snapshot['boot_id'] == a.snapshot['boot_id']
        results['reconnected_pid_unchanged'] = fixture.state()['pid'] == results['a_active']['pid']
        results['negotiation'] = a.welcome
        results['a_message_counts'] = dict(a.counts)
        results['b_reconnect_message_counts'] = dict(b.counts)
        results['surface_header_count'] = len(a.surface_headers)
        assert a.surface_headers or a.counts.get('shell.surface.reuse.v1', 0)
        assert a.counts.get('19', 0) + a.counts.get('endpoint.surface-delta.v1', 0) > 0
        results['decision'] = 'not_adopted: semantic pane input acquires tab geometry even without Focus(true)'
        return results
    finally:
        for client in clients:
            client.close()


def pixel_probe(fixture):
    a = Shell(fixture, 120, 40, True)
    process = None
    try:
        a.focus(True)
        a.drain()
        before = fixture.settled()
        rows, cols, _, _ = before['size']
        process = subprocess.Popen(fixture.args + ['terminal', 'session', 'control', fixture.pane,
                                  '--cols', str(cols), '--rows', str(rows)], env=fixture.env,
                                  stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.PIPE,
                                  text=True)
        # Drain initial frame as proof the direct controller was accepted.
        if not select.select([process.stdout], [], [], 5)[0]:
            raise RuntimeError('direct controller did not confirm within 5 seconds')
        first_frame = json.loads(process.stdout.readline())
        assert first_frame['type'] == 'terminal.frame', first_frame
        zero = fixture.settled()
        assert zero['size'][2:] == [0, 0], zero
        command = dict(type='terminal.resize', cols=cols, rows=rows, cell_width_px=9, cell_height_px=18)
        process.stdin.write(json.dumps(command) + '\n')
        process.stdin.flush()
        fixture.wait(lambda: fixture.state()['size'] == [rows, cols, cols * 9, rows * 18])
        pixels = fixture.settled()
        process.stdin.write(json.dumps(command) + '\n')
        process.stdin.flush()
        repeated = fixture.settled()
        assert repeated['winches'] == pixels['winches']
        process.stdin.write(json.dumps(dict(type='terminal.input', text='q')) + '\n')
        process.stdin.flush()
        fixture.wait(lambda: b'\x1b[6;18;9t'.hex() in fixture.state()['input'])
        queried = fixture.settled()
        received = bytes.fromhex(queried['input'])
        assert f'\x1b[4;{rows * 18};{cols * 9}t'.encode() in received
        assert f'\x1b[8;{rows};{cols}t'.encode() in received
        return dict(before=before, initial_control=zero, after_pixels=pixels,
                    repeated_identical=repeated, queries=queried,
                    initial_control_resize_signals=zero['winches'] - before['winches'],
                    followup_pixel_resize_signals=pixels['winches'] - zero['winches'],
                    decision='disabled: initial JSON control zeroes existing pixels; cross-transport and physical-device units not validated')
    finally:
        if process is not None:
            process.terminate()
            try:
                process.wait(timeout=3)
            except subprocess.TimeoutExpired:
                process.kill()
                process.wait()
        a.close()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--herdr', type=Path, required=True)
    parser.add_argument('--output', type=Path, required=True)
    args = parser.parse_args()
    binary = args.herdr.resolve(strict=True)
    version = subprocess.check_output([str(binary), '--version'], text=True).strip()
    if version != 'herdr 0.9.1':
        raise RuntimeError('this reviewed experimental codec requires exactly herdr 0.9.1')
    results = dict(version=version, platform=sys.platform)
    for name, probe in [('client_shell', shell_probe), ('pixel_geometry', pixel_probe)]:
        with tempfile.TemporaryDirectory(prefix='hxp-', dir='/tmp') as folder:
            fixture = Fixture(binary, Path(folder))
            try:
                fixture.start()
                results[name] = probe(fixture)
            finally:
                fixture.close()
    args.output.parent.mkdir(parents=True, exist_ok=True)
    args.output.write_text(json.dumps(results, indent=2, ensure_ascii=False) + '\n')
    print(json.dumps(results, ensure_ascii=False))


if __name__ == '__main__':
    main()
