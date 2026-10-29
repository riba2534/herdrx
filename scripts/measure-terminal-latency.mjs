#!/usr/bin/env node
// Measure keystroke-to-echo latency through a real herdrx website: one key is
// sent as a terminal input frame and the time until a terminal frame
// containing that character arrives is recorded. The script starts its own
// website with a temporary data directory and only uses a Herdr session it is
// told about (local: a temporary one it starts itself; SSH: a named isolated
// session the operator started on the remote host). It is a measurement, not
// a CI gate. See docs/operations.md (终端延迟测量) for usage.
//
//   node scripts/measure-terminal-latency.mjs --server ./herdrx-server [--samples 60]
//   node scripts/measure-terminal-latency.mjs --server ./herdrx-server \
//     --ssh remote.example.test --ssh-user dev --remote-session herdrx-latency
import assert from 'node:assert/strict'
import { spawn, execFile } from 'node:child_process'
import { promisify } from 'node:util'
import { mkdtemp, readFile, writeFile, rm } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'
import { createServer, createConnection } from 'node:net'
import { once } from 'node:events'
import { parseArgs } from 'node:util'
import { randomBytes } from 'node:crypto'

const run = promisify(execFile)
const { values: options } = parseArgs({
  options: {
    server: { type: 'string' },
    herdr: { type: 'string', default: 'herdr' },
    samples: { type: 'string', default: '60' },
    'interval-ms': { type: 'string', default: '120' },
    ssh: { type: 'string' },
    'ssh-user': { type: 'string' },
    'ssh-port': { type: 'string', default: '22' },
    'ssh-bin': { type: 'string', default: 'ssh' },
    'remote-session': { type: 'string' },
    json: { type: 'boolean', default: false },
  },
})
assert.ok(options.server, '--server must point to a built herdrx-server binary')
const samples = Number(options.samples)
const interval = Number(options['interval-ms'])
assert.ok(Number.isInteger(samples) && samples > 0 && samples <= 2000, '--samples must be 1..2000')
if (options.ssh) {
  // Never measure inside a session someone works in: require an explicit one.
  assert.ok(options['remote-session'], '--remote-session is required with --ssh; start an isolated session on the remote host first')
}

const sleep = (ms) => new Promise((done) => setTimeout(done, ms))
async function stop(child) {
  if (!child || child.exitCode !== null || child.signalCode !== null) return
  child.kill('SIGTERM')
  const timer = setTimeout(() => child.kill('SIGKILL'), 2000)
  await once(child, 'exit')
  clearTimeout(timer)
}
async function until(check, what, timeout = 15_000) {
  const deadline = Date.now() + timeout
  for (;;) {
    try { if (await check()) return } catch {}
    if (Date.now() > deadline) throw new Error(`timed out waiting for ${what}`)
    await sleep(100)
  }
}
const freePort = () => new Promise((done) => { const probe = createServer(); probe.listen(0, '127.0.0.1', () => { const { port } = probe.address(); probe.close(() => done(port)) }) })

// Terminal wire frames: kind 0x74, version 1, opcode, flags, u32 stream, u64 seq.
const OPCODE_FRAME = 1, OPCODE_ACK = 2, OPCODE_INPUT = 3
function encodeFrame(opcode, stream, seq, payload = Buffer.alloc(0)) {
  const frame = Buffer.alloc(16 + payload.length)
  frame.set([0x74, 1, opcode, 0])
  frame.writeUInt32LE(stream, 4)
  frame.writeBigUInt64LE(BigInt(seq), 8)
  payload.copy(frame, 16)
  return frame
}

const dir = await mkdtemp(join(tmpdir(), 'herdrx-latency-'))
const env = { ...process.env, XDG_CONFIG_HOME: dir, HERDR_CONFIG_PATH: join(dir, 'config.toml') }
const localSession = 'herdrx-latency'
let daemon, website, ws, workspaceID
const call = (() => {
  let next = 0
  const pending = new Map()
  return {
    pending,
    send(method, params) {
      const id = 'c' + (++next)
      ws.send(JSON.stringify({ t: 'call', id, method, params }))
      return new Promise((done, fail) => {
        pending.set(id, { done, fail })
        setTimeout(() => { if (pending.delete(id)) fail(new Error(method + ' timed out')) }, 15_000)
      })
    },
  }
})()

try {
  if (!options.ssh) {
    await writeFile(env.HERDR_CONFIG_PATH, 'onboarding = false\n[terminal]\ndefault_shell = "/bin/sh"\nshell_mode = "non_login"\n')
    const socket = join(dir, 'herdr', 'sessions', localSession, 'herdr.sock')
    daemon = spawn(options.herdr, ['--session', localSession, 'server'], { env, stdio: 'ignore' })
    await until(() => new Promise((done) => {
      const conn = createConnection(socket)
      conn.on('error', () => done(false))
      conn.on('connect', () => { conn.end(); done(true) })
    }), 'the isolated Herdr session')
  }

  const port = await freePort()
  const base = `http://127.0.0.1:${port}`
  const data = join(dir, 'website')
  website = spawn(resolve(options.server), [], {
    env: { ...env, HERDRX_DATA_DIR: data, HERDRX_HERDR_BIN: options.herdr, HERDRX_SSH_BIN: options.ssh ? options['ssh-bin'] : '', HERDRX_ADDR: `127.0.0.1:${port}`, HERDRX_PUBLIC_URL: base, HERDRX_COOKIE_SECURE: 'false', HERDRX_BOOTSTRAP_TOKEN: '', HERDRX_ALLOWED_ORIGINS: '', HERDRX_TRUSTED_PROXIES: '' },
    stdio: 'ignore',
  })
  await until(async () => (await fetch(base + '/healthz')).ok, 'the website')
  const token = (await readFile(join(data, 'bootstrap-token'), 'utf8')).trim()
  const bootstrap = await fetch(base + '/api/bootstrap', { method: 'POST', headers: { Origin: base, 'Content-Type': 'application/json' }, body: JSON.stringify({ email: 'latency@example.test', display_name: 'Latency', password: randomBytes(18).toString('base64url'), token }) })
  if (!bootstrap.ok) throw new Error('bootstrap failed: ' + await bootstrap.text())
  const cookie = bootstrap.headers.getSetCookie().map((value) => value.split(';')[0]).join('; ')
  const { csrf_token: csrf } = await bootstrap.json()
  const hostBody = options.ssh
    ? { name: 'Latency SSH', transport: 'ssh', auth_method: 'system_ssh', hostname: options.ssh, username: options['ssh-user'] || '', port: Number(options['ssh-port']), session_name: options['remote-session'] }
    : { name: 'Latency local', transport: 'local', session_name: localSession }
  const created = await fetch(base + '/api/hosts/', { method: 'POST', headers: { Origin: base, Cookie: cookie, 'X-CSRF-Token': csrf, 'Content-Type': 'application/json' }, body: JSON.stringify(hostBody) })
  if (!created.ok) throw new Error('adding the host failed: ' + await created.text())
  const host = (await created.json()).host

  ws = new WebSocket(`ws://127.0.0.1:${port}/api/hosts/${host.id}/ws`, { headers: { Origin: base, Cookie: cookie } })
  ws.binaryType = 'arraybuffer'
  const frames = []
  let ready, opened, snapshot
  const waiters = []
  ws.onmessage = (event) => {
    if (typeof event.data !== 'string') {
      const frame = Buffer.from(event.data)
      if (frame[2] !== OPCODE_FRAME) return
      const stream = frame.readUInt32LE(4), seq = frame.readBigUInt64LE(8)
      // Match printable output only: escape sequences contain letters too.
      frames.push({ at: performance.now(), text: frame.subarray(20).toString('latin1').replace(/\x1b(?:\[[0-?]*[ -\/]*[@-~]|\][^\x07\x1b]*(?:\x07|\x1b\\)?|[@-Z\\-_])/g, '') })
      ws.send(encodeFrame(OPCODE_ACK, stream, seq))
      waiters.splice(0).forEach((wake) => wake())
      return
    }
    const message = JSON.parse(event.data)
    if (message.t === 'conn' && message.state === 'ready') ready = true
    if (message.t === 'snapshot') snapshot = message.snapshot
    if (message.t === 'terminal.opened') opened = message
    if (message.t === 'error' && !message.id) console.error('workbench error:', message.message)
    const waiting = message.id && call.pending.get(message.id)
    if (waiting) {
      call.pending.delete(message.id)
      if (message.t === 'error') waiting.fail(new Error(message.message))
      else waiting.done(message.result)
    }
    if (message.t === 'terminal.closed') console.error('terminal closed:', message.reason)
  }
  await once(ws, 'open')
  ws.send(JSON.stringify({ t: 'hello', protocol: 1, browser_instance_id: randomBytes(16).toString('hex') }))
  await until(() => ready && snapshot, 'the workbench connection')

  const workspace = await call.send('workspace.create', { ...(options.ssh ? {} : { cwd: dir }), label: 'herdrx latency', focus: false })
  const pane = workspace.root_pane.pane_id
  workspaceID = workspace.workspace?.workspace_id || workspace.root_pane.workspace_id
  // The line discipline echoes each key; cat keeps the shell from running input.
  await call.send('pane.send_text', { pane_id: pane, text: 'cat >/dev/null\n' })
  await sleep(500)
  ws.send(JSON.stringify({ t: 'terminal.open', id: 'open', pane_id: pane, mode: 'observe', cols: 100, rows: 30 }))
  await until(() => opened, 'the terminal stream')
  const stream = opened.stream_id
  await sleep(500)

  const alphabet = 'abcdefghijklmnopqrstuvwxyz'
  const latencies = []
  let misses = 0
  for (let index = 0; index < samples; index++) {
    if (index > 0 && index % 40 === 0) {
      ws.send(encodeFrame(OPCODE_INPUT, stream, 0, Buffer.from('\r')))
      await sleep(300)
    }
    const key = alphabet[index % alphabet.length]
    const from = frames.length
    const sent = performance.now()
    ws.send(encodeFrame(OPCODE_INPUT, stream, 0, Buffer.from(key)))
    const arrived = await new Promise((done) => {
      const timer = setTimeout(() => done(null), 3000)
      const check = () => {
        const hit = frames.slice(from).find((frame) => frame.text.includes(key))
        if (hit) { clearTimeout(timer); done(hit.at) } else waiters.push(check)
      }
      check()
    })
    if (arrived === null) misses++
    else latencies.push(arrived - sent)
    await sleep(interval)
  }
  latencies.sort((a, b) => a - b)
  const pick = (q) => latencies.length ? Number(latencies[Math.min(latencies.length - 1, Math.floor(latencies.length * q))].toFixed(1)) : null
  const result = { transport: options.ssh ? 'ssh' : 'local', samples, received: latencies.length, misses, minMs: pick(0), p50Ms: pick(0.5), p90Ms: pick(0.9), p99Ms: pick(0.99), maxMs: pick(1) }
  if (options.json) console.log(JSON.stringify(result))
  else {
    console.log(`传输：${result.transport}，样本 ${samples}，收到 ${result.received}，超时 ${misses}`)
    console.log(`按键到回显（毫秒）：min ${result.minMs} · p50 ${result.p50Ms} · p90 ${result.p90Ms} · p99 ${result.p99Ms} · max ${result.maxMs}`)
  }
} finally {
  // Only the workspace this script created is closed; the session stays.
  if (ws?.readyState === WebSocket.OPEN && workspaceID) await call.send('workspace.close', { workspace_id: workspaceID }).catch(() => {})
  ws?.close()
  await stop(website)
  if (daemon) {
    await run(options.herdr, ['--session', localSession, 'server', 'stop'], { env, timeout: 3000 }).catch(() => {})
    await stop(daemon)
  }
  await rm(dir, { recursive: true, force: true })
}
