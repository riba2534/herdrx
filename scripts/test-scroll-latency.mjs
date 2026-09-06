#!/usr/bin/env node
// Compare browser-to-app rendering with isolated real Herdr and website
// processes. Usage: HERDRX_TEST_HERDR=/path/to/herdr node scripts/test-scroll-latency.mjs baseline-binary candidate-binary
import assert from 'node:assert/strict'
import { spawn, execFile } from 'node:child_process'
import { promisify } from 'node:util'
import { mkdtemp, readFile, writeFile, rm } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'
import { createServer, createConnection } from 'node:net'
import { once } from 'node:events'
import { chromium, expect } from '../web/node_modules/@playwright/test/index.mjs'
const run = promisify(execFile)
const herdr = process.env.HERDRX_TEST_HERDR
assert.ok(herdr, 'HERDRX_TEST_HERDR must specify the test binary')
assert.equal(process.argv.length, 4, 'Pass baseline and candidate website binaries')
const browser = await chromium.launch({ headless: true })
const results = []
async function stop(child) {
  if (!child || child.exitCode !== null || child.signalCode !== null) return
  child.kill('SIGTERM')
  const timer = setTimeout(() => child.kill('SIGKILL'), 2000)
  await once(child, 'exit')
  clearTimeout(timer)
}
try {
  const cases = [0, 60].flatMap((delay) => process.argv.slice(2).map((binary, index) => ({ binary, index, delay })))
  for (const { binary, index, delay } of cases) {
    const dir = await mkdtemp(join(tmpdir(), 'herdrx-scroll-latency-'))
    const session = 'scroll-web-test'
    const env = { ...process.env, XDG_CONFIG_HOME: dir, HERDR_CONFIG_PATH: join(dir, 'config.toml') }
    await writeFile(env.HERDR_CONFIG_PATH, 'onboarding = false\n[terminal]\ndefault_shell = "/bin/sh"\nshell_mode = "non_login"\n')
    const socket = join(dir, 'herdr', 'sessions', session, 'herdr.sock')
    const api = (method, params = {}) => new Promise((done, fail) => {
      const conn = createConnection(socket)
      let text = ''
      conn.setTimeout(3000, () => conn.destroy(new Error('Herdr API timeout')))
      conn.on('error', fail)
      conn.on('connect', () => conn.write(JSON.stringify({ id: 'scroll-test', method, params }) + '\n'))
      conn.on('data', (bytes) => {
        text += bytes
        if (!text.includes('\n')) return
        conn.end()
        const response = JSON.parse(text.split('\n')[0])
        if (response.error) fail(new Error(JSON.stringify(response.error)))
        else done(response.result)
      })
    })
    const daemon = spawn(herdr, ['--session', session, 'server'], { env, stdio: 'ignore' })
    let website, pane, context, view
    try {
      await expect.poll(async () => api('session.snapshot').then(() => true).catch(() => false)).toBe(true)
      pane = (await api('workspace.create', { cwd: dir, label: 'Scroll benchmark', focus: false })).root_pane.pane_id
      const snapshot = (await api('session.snapshot')).snapshot
      const rect = snapshot.layouts.flatMap((layout) => layout.panes).find((p) => p.pane_id === pane).rect
      view = spawn(herdr, ['--session', session, 'terminal', 'session', 'control', pane, '--cols', String(rect.width), '--rows', String(rect.height)], { env, stdio: ['pipe', 'pipe', 'ignore'] })
      await once(view.stdout, 'data')
      view.stdout.resume()
      view.stdin.end('{"type":"terminal.release"}\n')
      await once(view, 'exit')
      await writeFile(join(dir, 'wheel.py'), `import os,tty,re,json
os.chdir(${JSON.stringify(dir)})
tty.setraw(0)
position=0
def record():
    size=os.get_terminal_size(0)
    with open("state.tmp","w") as f: json.dump({"pid":os.getpid(),"position":position,"cols":size.columns,"rows":size.lines},f)
    os.replace("state.tmp","state.json")
    os.write(1,("\\x1b[HWheel position: %d\\x1b[K"%position).encode())
os.write(1,b"\\x1b[?1049h\\x1b[?1000h\\x1b[?1006h")
record()
data=b""
while True:
    data+=os.read(0,4096)
    while True:
        match=re.search(rb"\\x1b\\[<(64|65);\\d+;\\d+M",data)
        if not match: break
        position+=-1 if match[1]==b"64" else 1
        data=data[match.end():]
        record()
`)
      await api('pane.send_text', { pane_id: pane, text: `python3 ${JSON.stringify(join(dir, 'wheel.py'))}\n` })
      const state = async () => JSON.parse(await readFile(join(dir, 'state.json'), 'utf8'))
      await expect.poll(async () => state().then((s) => s.position).catch(() => null)).toBe(0)
      const original = await state()
      const port = await new Promise((done) => { const probe = createServer(); probe.listen(0, '127.0.0.1', () => { const port = probe.address().port; probe.close(() => done(port)) }) })
      const base = `http://127.0.0.1:${port}`
      const data = join(dir, 'website')
      website = spawn(resolve(binary), [], { env: { ...env, HERDRX_DATA_DIR: data, HERDRX_HERDR_BIN: herdr, HERDRX_ADDR: `127.0.0.1:${port}`, HERDRX_PUBLIC_URL: base, HERDRX_COOKIE_SECURE: 'false', HERDRX_BOOTSTRAP_TOKEN: '', HERDRX_ALLOWED_ORIGINS: '', HERDRX_TRUSTED_PROXIES: '' }, stdio: 'ignore' })
      await expect.poll(async () => fetch(base + '/healthz').then((r) => r.ok).catch(() => false)).toBe(true)
      context = await browser.newContext({ viewport: { width: 1440, height: 900 } })
      const bootstrap = await context.request.post(base + '/api/bootstrap', { data: { email: 'scroll@example.test', display_name: 'Scroll test', password: 'isolated-scroll-password', token: (await readFile(join(data, 'bootstrap-token'), 'utf8')).trim() }, headers: { Origin: base } })
      assert.ok(bootstrap.ok(), await bootstrap.text())
      const auth = await bootstrap.json()
      const hostResponse = await context.request.post(base + '/api/hosts/', { data: { name: 'Isolated scroll', transport: 'local', session_name: session }, headers: { Origin: base, 'X-CSRF-Token': auth.csrf_token } })
      assert.ok(hostResponse.ok(), await hostResponse.text())
      const host = (await hostResponse.json()).host
      const page = await context.newPage()
      if (delay) await page.addInitScript((delay) => {
        const NativeSocket = window.WebSocket
        window.WebSocket = class extends NativeSocket {
          send(data) { setTimeout(() => { if (this.readyState === NativeSocket.OPEN) super.send(data) }, delay) }
          set onmessage(handler) {
            super.onmessage = handler ? (event) => setTimeout(() => handler.call(this, event), delay) : null
          }
        }
      }, delay)
      const errors = []
      page.on('pageerror', (e) => errors.push(e.message))
      await page.goto(base + '/h/' + host.id)
      await expect(page.locator('.xterm-rows')).toContainText('Wheel position: 0')
      await page.evaluate(() => {
        const viewport = document.querySelector('.terminal-viewport')
        const rows = viewport.querySelector('.xterm-rows')
        const events = [], updates = []
        window.scrollMeasurement = { events, updates }
        window.addEventListener('wheel', () => events.push(performance.now()), { capture: true })
        let previous = 0
        new MutationObserver(() => {
          const match = rows.textContent.match(/Wheel position: (-?\d+)/)
          if (!match) return
          const position = -Number(match[1])
          if (position === previous || position <= 0) return
          previous = position
          updates.push({ position, time: performance.now() })
        }).observe(rows, { childList: true, subtree: true, characterData: true })
      })
      // Deterministic 60 Hz, three-line DOM wheel events make each gesture one
      // native mouse report. Real hardware wheel dispatch is covered separately
      // by test-workbench-display.mjs in Chromium, Firefox and WebKit.
      await page.evaluate(async () => {
        const viewport = document.querySelector('.terminal-viewport')
        const rect = viewport.querySelector('.xterm-screen').getBoundingClientRect()
        for (let i = 0; i < 60; i++) {
          viewport.dispatchEvent(new WheelEvent('wheel', { deltaY: -3, deltaMode: 1, clientX: rect.left + 20, clientY: rect.top + 20, bubbles: true, cancelable: true }))
          await new Promise((done) => setTimeout(done, 16))
        }
      })
      await expect(page.locator('.xterm-rows')).toContainText('Wheel position: -60')
      const measurement = await page.evaluate(() => window.scrollMeasurement)
      const latencies = measurement.updates.map((u) => u.time - measurement.events[u.position - 1]).sort((a, b) => a - b)
      assert.ok(latencies.length > 3 && latencies.every(Number.isFinite))
      const final = await state()
      assert.equal(final.pid, original.pid)
      assert.equal(final.cols, original.cols)
      assert.equal(final.rows, original.rows)
      assert.deepEqual(errors, [])
      const result = { variant: index === 0 ? 'baseline' : 'candidate', simulatedRTTMs: delay * 2, gestures: measurement.events.length, visibleUpdates: measurement.updates.length, domMedianMs: Number(latencies[Math.floor(latencies.length / 2)].toFixed(2)), domP95Ms: Number(latencies[Math.floor(latencies.length * .95)].toFixed(2)), tailMs: Number((measurement.updates.at(-1).time - measurement.events.at(-1)).toFixed(2)) }
      results.push(result)
      console.log(JSON.stringify(result))
      await context.close()
      context = null
      await new Promise((done) => setTimeout(done, 350))
      // Known SGR bytes are used only by this synthetic fixture to prove the
      // application is still alive after its Web access has been removed.
      await api('pane.send_text', { pane_id: pane, text: '\x1b[<65;1;1M' })
      await expect.poll(async () => (await state()).position).toBe(-59)
      assert.equal((await state()).pid, original.pid, 'closing the Web page ended the application')
    } finally {
      await context?.close()
      await stop(website)
      await stop(view)
      if (pane) await api('pane.close', { pane_id: pane }).catch(() => {})
      await run(herdr, ['--session', session, 'server', 'stop'], { env, timeout: 2000 }).catch(() => {})
      await stop(daemon)
      await rm(dir, { recursive: true, force: true })
    }
  }
} finally { await browser.close() }
for (let index = 0; index < results.length; index += 2) {
  assert.ok(results[index + 1].domMedianMs < results[index].domMedianMs, 'candidate did not improve median browser response')
  assert.ok(results[index + 1].visibleUpdates > results[index].visibleUpdates, 'candidate did not produce more continuous updates')
}
