#!/usr/bin/env node
// Real website + isolated Herdr + actual React/xterm in two browser contexts.
// Requires a verified Herdr release via HERDRX_TEST_HERDR. No user session is used.
import assert from 'node:assert/strict'
import { spawn } from 'node:child_process'
import { mkdtemp, readFile, writeFile, rm } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'
import { createConnection, createServer } from 'node:net'
import { chromium, firefox, webkit, expect } from '../web/node_modules/@playwright/test/index.mjs'

assert.ok(process.env.HERDRX_TEST_HERDR, 'HERDRX_TEST_HERDR must point to a verified real binary')
const binary = resolve(process.argv[2] || 'bin/herdrx-server')
const herdr = resolve(process.env.HERDRX_TEST_HERDR)
const dir = await mkdtemp(join(tmpdir(), 'hx-control-'))
const session = 'browser-control'
const env = Object.fromEntries(Object.entries(process.env).filter(([key]) => !key.startsWith('HERDR_')))
Object.assign(env, { XDG_CONFIG_HOME: dir, HERDR_CONFIG_PATH: join(dir, 'config.toml') })
await writeFile(env.HERDR_CONFIG_PATH, 'onboarding = false\n[terminal]\ndefault_shell = "/bin/sh"\nshell_mode = "non_login"\n')
const socketPath = join(dir, 'herdr', 'sessions', session, 'herdr.sock')
let website, daemon, browser
let logs = ''
const start = (command, args, options) => {
  const process = spawn(command, args, { ...options, stdio: ['ignore', 'pipe', 'pipe'] })
  process.on('error', (error) => { logs += String(error) + '\n' })
  process.stdout.on('data', (data) => { logs += data.toString() })
  process.stderr.on('data', (data) => { logs += data.toString() })
  return process
}
const delay = (ms) => new Promise((done) => setTimeout(done, ms))
async function until(check, label) {
  const end = Date.now() + 15000
  while (Date.now() < end) { if (await check().catch(() => false)) return; await delay(40) }
  throw new Error(`Timed out: ${label}`)
}
function call(method, params = {}) {
  return new Promise((done, reject) => {
    const connection = createConnection(socketPath)
    let result = ''
    connection.setTimeout(3000, () => connection.destroy(new Error(`Herdr ${method} timeout`)))
    connection.on('error', reject)
    connection.on('connect', () => connection.write(JSON.stringify({ id: 'fixture', method, params }) + '\n'))
    connection.on('data', (data) => {
      result += data.toString()
      if (!result.includes('\n')) return
      connection.end()
      try { const response = JSON.parse(result.slice(0, result.indexOf('\n'))); if (response.error) reject(new Error(JSON.stringify(response.error))); else done(response.result) }
      catch (error) { reject(error) }
    })
  })
}
async function state() { return JSON.parse(await readFile(join(dir, 'state.json'), 'utf8')) }
const geometry = (value) => ({ cols: value.cols, rows: value.rows })
const password = 'terminal-control-test-password'
async function instrument(context) {
  await context.addInitScript(() => {
    const NativeSocket = window.WebSocket
    window.__terminalWire = { sent: [], received: [], frames: [], socket: null }
    window.WebSocket = class extends NativeSocket {
      constructor(...args) {
        super(...args)
        if (!String(args[0]).includes('/ws')) return
        window.__terminalWire.socket = this
        this.addEventListener('message', ({ data }) => { if (typeof data === 'string') { try { window.__terminalWire.received.push({ ...JSON.parse(data), received_at: performance.now() }) } catch {} } else if (data instanceof ArrayBuffer && data.byteLength >= 20) { const view = new DataView(data); window.__terminalWire.frames.push({ cols: view.getUint16(16, true), rows: view.getUint16(18, true), seq: Number(view.getBigUint64(8, true)) }) } })
      }
      send(data) {
        if (typeof data === 'string') { try { window.__terminalWire.sent.push(JSON.parse(data)) } catch {} }
        else if (data instanceof Uint8Array) window.__terminalWire.sent.push({ opcode: data[2] })
        return super.send(data)
      }
    }
  })
}
async function frameGeometry(page) { return page.evaluate(() => { const frame = window.__terminalWire.frames.at(-1); return frame && { cols: frame.cols, rows: frame.rows } }) }
async function messages(page, direction, type) { return page.evaluate(({ direction, type }) => window.__terminalWire[direction].filter((message) => message.t === type), { direction, type }) }
async function showTools(page) {
  const toolbar = page.locator('.terminal-titlebar')
  if (!await toolbar.isVisible()) await page.getByRole('button', { name: '终端工具', exact: true }).click()
}
async function acquire(page) {
  await showTools(page)
  await page.getByRole('button', { name: '使用此窗口尺寸', exact: true }).click()
  await expect(page.getByRole('button', { name: '释放尺寸控制' })).toBeVisible()
  const request = (await messages(page, 'sent', 'terminal.control.acquire')).at(-1)
  await until(async () => JSON.stringify(geometry(await state())) === JSON.stringify(geometry(request)), 'real PTY follows acquired target')
}
async function stop(process) {
  if (!process || process.exitCode !== null) return
  process.kill('SIGTERM')
  await Promise.race([new Promise((done) => process.once('exit', done)), delay(2000)])
  if (process.exitCode === null) { process.kill('SIGKILL'); await new Promise((done) => process.once('exit', done)) }
}
try {
  daemon = start(herdr, ['--session', session, 'server'], { env })
  await until(async () => Boolean(await call('session.snapshot')), 'isolated Herdr ready')
  const created = await call('workspace.create', { cwd: dir, label: '真实尺寸回归', focus: true })
  const paneID = created.root_pane.pane_id
  await writeFile(join(dir, 'app.py'), String.raw`import os, tty, json, signal, threading
lock=threading.RLock()
tty.setraw(0)
received=b''
wins=0
def draw(*args):
    global wins
    with lock:
        if args: wins+=1
        size=os.get_terminal_size(0)
        state={'pid':os.getpid(),'cols':size.columns,'rows':size.lines,'wins':wins,'text':received.decode('utf-8','replace')}
        with open('state.tmp','w') as output: json.dump(state,output)
        os.replace('state.tmp','state.json')
        os.write(1,('\x1b[2J\x1b[H真实终端：中文输入与尺寸交接\r\n%d x %d\r\n'%(size.columns,size.lines)).encode())
signal.signal(signal.SIGWINCH,draw)
draw()
while True:
    chunk=os.read(0,4096)
    if not chunk: break
    received+=chunk
    draw()
`)
  await call('pane.send_text', { pane_id: paneID, text: 'python3 app.py\r' })
  await until(async () => (await state()).pid > 0, 'PTY fixture starts')
  const pid = (await state()).pid
  const port = await new Promise((done) => { const probe = createServer(); probe.listen(0, '127.0.0.1', () => { const port = probe.address().port; probe.close(() => done(port)) }) })
  const base = `http://127.0.0.1:${port}`
  website = start(binary, [], { env: { ...env, HERDRX_DATA_DIR: join(dir, 'website'), HERDRX_ADDR: `127.0.0.1:${port}`, HERDRX_HERDR_BIN: herdr, HERDRX_PUBLIC_URL: base, HERDRX_COOKIE_SECURE: 'false', HERDRX_BOOTSTRAP_TOKEN: '', HERDRX_ALLOWED_ORIGINS: '', HERDRX_TRUSTED_PROXIES: '' } })
  await until(async () => (await fetch(base + '/healthz')).ok, 'isolated website ready')
  const token = (await readFile(join(dir, 'website', 'bootstrap-token'), 'utf8')).trim()
  const initialized = await fetch(base + '/api/bootstrap', { method: 'POST', headers: { 'content-type': 'application/json', Origin: base }, body: JSON.stringify({ token, email: 'control@example.test', password, display_name: '尺寸测试' }) })
  assert.equal(initialized.status, 200, await initialized.text())
  let hostID
  for (const engine of (process.env.HERDRX_TEST_ENGINES || 'chromium').split(',')) {
    assert.ok({ chromium, firefox, webkit }[engine], `Unknown browser engine ${engine}`)
    browser = await { chromium, firefox, webkit }[engine].launch({ headless: true, ...(engine === 'chromium' && process.env.HERDRX_TEST_CHROMIUM ? { executablePath: process.env.HERDRX_TEST_CHROMIUM } : {}) })
    const contexts = await Promise.all([browser.newContext({ viewport: { width: 1280, height: 800 } }), browser.newContext({ viewport: { width: 900, height: 600 } })])
    for (const context of contexts) {
      await instrument(context)
      const login = await context.request.post(base + '/api/login', { headers: { Origin: base }, data: { email: 'control@example.test', password } })
      assert.equal(login.status(), 200)
      if (!hostID) {
        const { csrf_token } = await login.json()
        const response = await context.request.post(base + '/api/hosts/', { headers: { Origin: base, 'X-CSRF-Token': csrf_token }, data: { name: '隔离尺寸主机', transport: 'local', session_name: session } })
        assert.equal(response.status(), 201, await response.text())
        hostID = (await response.json()).host.id
      }
    }
    const [a, b] = await Promise.all(contexts.map((context) => context.newPage()))
    const errors = []
    for (const page of [a, b]) page.on('pageerror', (error) => errors.push(error.message))
    const before = await state()
    for (const page of [a, b]) {
      await page.goto(`${base}/h/${hostID}`)
      await expect(page.locator('.terminal-pane[data-terminal-status="可输入"]')).toHaveCount(1)
      await expect(page.locator('.xterm-rows')).toContainText('真实终端')
      assert.equal((await messages(page, 'sent', 'terminal.control.acquire')).length, 0)
      assert.equal((await messages(page, 'sent', 'terminal.resize_v2')).length, 0)
    }
    await a.setViewportSize({ width: 1180, height: 760 })
    await b.setViewportSize({ width: 960, height: 620 })
    await delay(180)
    assert.deepEqual(geometry(await state()), geometry(before), 'observers or viewport changes resized the real task')
    assert.equal((await state()).wins, before.wins, 'observer caused SIGWINCH')
    await acquire(a)
    const acquiredGeometry = geometry(await state())
    await until(async () => [await frameGeometry(a), await frameGeometry(b)].every((frame) => JSON.stringify(frame) === JSON.stringify(acquiredGeometry)), 'both observers display the actual acquired grid')
    const oldLease = (await messages(a, 'received', 'terminal.control.acquired')).at(-1)
    const aOpens = (await messages(a, 'sent', 'terminal.open')).length
    await a.setViewportSize({ width: 1100, height: 730 })
    await expect.poll(async () => (await messages(a, 'sent', 'terminal.resize_v2')).length).toBeGreaterThan(0)
    const latest = (await messages(a, 'sent', 'terminal.resize_v2')).at(-1)
    await until(async () => JSON.stringify(geometry(await state())) === JSON.stringify(geometry(latest)), 'real resize convergence')
    const durations = []
    for (let index = 0; index < 20; index++) {
      const previous = (await messages(a, 'sent', 'terminal.resize_v2')).at(-1)?.resize_seq || 0
      const started = await a.evaluate(() => performance.now())
      await a.setViewportSize({ width: 1120 + index * 16, height: 730 })
      await until(async () => ((await messages(a, 'sent', 'terminal.resize_v2')).at(-1)?.resize_seq || 0) > previous, 'viewport resize submitted')
      const target = (await messages(a, 'sent', 'terminal.resize_v2')).at(-1)
      await until(async () => (await messages(a, 'received', 'terminal.resize.status')).some((message) => message.resize_seq === target.resize_seq && message.status === 'observed'), 'latest target observed')
      const observed = (await messages(a, 'received', 'terminal.resize.status')).find((message) => message.resize_seq === target.resize_seq && message.status === 'observed')
      durations.push(observed.received_at - started)
      await until(async () => JSON.stringify(geometry(await state())) === JSON.stringify(geometry(target)), 'application receives latest PTY size')
      await until(async () => [await frameGeometry(a), await frameGeometry(b)].every((frame) => JSON.stringify(frame) === JSON.stringify(geometry(target))), 'both observers follow latest real grid')
    }
    const settledCount = (await messages(a, 'sent', 'terminal.resize_v2')).length
    await delay(350)
    assert.equal((await messages(a, 'sent', 'terminal.resize_v2')).length, settledCount, 'stationary viewport kept sending resize')
    const settledGeometry = geometry(await state())
    const pongCount = (await messages(a, 'received', 'pong')).length
    await a.evaluate((lease) => {
      window.__terminalWire.socket.send(JSON.stringify({ t: 'terminal.resize_v2', stream_id: lease.stream_id, stream_epoch: lease.stream_epoch, control_generation: lease.control_generation, resize_seq: 1, cols: 23, rows: 7 }))
      window.__terminalWire.socket.send(JSON.stringify({ t: 'ping' }))
    }, oldLease)
    await until(async () => (await messages(a, 'received', 'pong')).length > pongCount, 'old sequence processed before barrier')
    assert.deepEqual(geometry(await state()), settledGeometry, 'old sequence changed current target')
    durations.sort((a, b) => a - b)
    console.log(`${engine}: 20 local viewport→observed samples p50=${durations[9].toFixed(1)}ms p95=${durations[18].toFixed(1)}ms (loopback, no network throttling)`)

    await showTools(b)
    await b.getByRole('button', { name: '转到此窗口控制', exact: true }).click()
    await expect(b.getByRole('alertdialog')).toBeVisible()
    assert.equal((await messages(b, 'sent', 'terminal.control.acquire')).length, 0, 'handoff ran before confirmation')
    await b.getByRole('alertdialog').getByRole('button', { name: '取消', exact: true }).click()
    await b.getByRole('button', { name: '转到此窗口控制', exact: true }).click()
    await b.getByRole('alertdialog').getByRole('button', { name: '转到此窗口控制', exact: true }).click()
    await expect(b.getByRole('button', { name: '释放尺寸控制' })).toBeVisible()
    await expect(a.getByRole('button', { name: '释放尺寸控制' })).toHaveCount(0)
    const transferred = (await messages(b, 'sent', 'terminal.control.acquire')).at(-1)
    assert.equal(transferred.transfer, true)
    await until(async () => JSON.stringify(geometry(await state())) === JSON.stringify(geometry(transferred)), 'transferred real PTY size')
    const settled = await state()
    await until(async () => [await frameGeometry(a), await frameGeometry(b)].every((frame) => JSON.stringify(frame) === JSON.stringify(geometry(settled))), 'both observers display the transferred grid')
    await a.evaluate((oldLease) => window.__terminalWire.socket.send(JSON.stringify({ t: 'terminal.resize_v2', stream_id: oldLease.stream_id, stream_epoch: oldLease.stream_epoch, control_generation: oldLease.control_generation, resize_seq: 9999, cols: 23, rows: 7 })), oldLease)
    await expect.poll(async () => (await messages(a, 'received', 'error')).filter((message) => message.code === 'control_expired').length).toBeGreaterThan(0)
    assert.deepEqual(geometry(await state()), geometry(settled), 'old generation resized new owner')
    assert.equal((await messages(a, 'sent', 'terminal.open')).length, aOpens, 'acquire or handoff replaced observer stream')
    await showTools(a)
    await a.getByRole('button', { name: '聚焦终端输入', exact: true }).click()
    await expect(a.locator('.xterm-helper-textarea')).toBeFocused()
    const inputText = `中文输入验证${engine}`
    await a.keyboard.insertText(inputText)
    await until(async () => (await state()).text.includes(inputText), 'observer still sends Chinese input')
    assert.equal((await state()).text.split(inputText).length - 1, 1, 'input was duplicated')
    assert.deepEqual(geometry(await state()), geometry(settled), 'observer input reclaimed the new owner size')
    await b.evaluate(() => { Object.defineProperty(document, 'visibilityState', { configurable: true, get: () => 'hidden' }); document.dispatchEvent(new Event('visibilitychange')) })
    await expect.poll(async () => (await messages(b, 'sent', 'terminal.control.release')).length).toBeGreaterThan(0)
    await expect(b.getByRole('button', { name: '释放尺寸控制' })).toHaveCount(0)
    await expect.poll(async () => (await messages(a, 'received', 'terminal.control.state')).at(-1)?.state).toBe('available')
    await b.evaluate(() => { delete document.visibilityState; document.dispatchEvent(new Event('visibilitychange')) })
    assert.equal((await messages(b, 'sent', 'terminal.control.acquire')).length, 1, 'returning foreground reacquired control')
    await b.reload()
    await expect(b.locator('.terminal-pane[data-terminal-status="可输入"]')).toHaveCount(1)
    assert.equal((await messages(b, 'sent', 'terminal.control.acquire')).length, 0, 'refresh restored temporary control')
    assert.equal((await state()).pid, pid, 'access changes recreated the task')
    assert.deepEqual(errors, [])
    for (const context of contexts) await context.close()
    await browser.close(); browser = null
    console.log(`${engine}: real PTY observe, resize, confirmed handoff, old generation rejection, Chinese input, hidden release and refresh observation passed`)
  }
} catch (error) {
  for (const context of browser?.contexts() || []) for (const page of context.pages()) console.error(await page.evaluate(() => ({ active: document.activeElement?.className, inputs: window.__terminalWire?.sent.filter((m) => m.opcode === 3).length, frames: window.__terminalWire?.frames.slice(-15), sent: window.__terminalWire?.sent.filter((m) => m.t?.startsWith('terminal.')).slice(-15), received: window.__terminalWire?.received.filter((m) => m.t?.startsWith('terminal.') || m.t === 'error').slice(-15) })).catch(() => ({})))
  console.error(await state().catch(() => ({})))
  console.error(logs.slice(-12000))
  throw error
} finally {
  await browser?.close()
  await stop(website)
  // Explicitly stop only the named fixture server that this script created.
  if (daemon) {
    const shutdown = spawn(herdr, ['--session', session, 'server', 'stop'], { env, stdio: 'ignore' })
    await Promise.race([new Promise((done) => shutdown.once('exit', done)), delay(2000)])
    if (shutdown.exitCode === null) shutdown.kill('SIGKILL')
  }
  await stop(daemon)
  await rm(dir, { recursive: true, force: true })
}
