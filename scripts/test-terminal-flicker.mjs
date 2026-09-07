#!/usr/bin/env node
// Replay full and delta ANSI frames through the real app, WebSocket decoder and
// xterm renderer. Sample each browser animation frame, not only the final DOM.
// All HTTP/WebSocket data is synthetic; this never connects to a real Herdr.
import assert from 'node:assert/strict'
import { createServer } from 'node:http'
import { mkdir, readFile, writeFile } from 'node:fs/promises'
import { fileURLToPath } from 'node:url'
import { join } from 'node:path'
import { chromium, firefox, webkit, expect } from '../web/node_modules/@playwright/test/index.mjs'

const dist = fileURLToPath(new URL('../web/dist/', import.meta.url))
const cols = 150, rows = 45
const host = { id: 'flicker-test', name: '绘制测试', transport: 'ssh' }
const snapshot = {
  protocol: 1, version: 'test', focused_workspace_id: 'w1', focused_tab_id: 't1', focused_pane_id: 'p1',
  workspaces: [{ workspace_id: 'w1', active_tab_id: 't1', label: '隔离绘制测试', number: 1, pane_count: 1, tab_count: 1 }],
  tabs: [{ workspace_id: 'w1', tab_id: 't1', label: '测试终端', number: 1, pane_count: 1 }],
  panes: [{ workspace_id: 'w1', tab_id: 't1', pane_id: 'p1', label: '测试终端', cwd: '/workspace/test', revision: 1, scroll: { max_offset_from_bottom: 0, offset_from_bottom: 0, viewport_rows: rows } }],
  layouts: [{ workspace_id: 'w1', tab_id: 't1', area: { x: 0, y: 0, width: cols, height: rows }, focused_pane_id: 'p1', splits: [], zoomed: false, panes: [{ pane_id: 'p1', rect: { x: 0, y: 0, width: cols, height: rows } }] }],
  agents: [],
}

const server = createServer(async (req, res) => {
  const path = new URL(req.url, 'http://localhost').pathname
  const json = path === '/api/bootstrap/status' ? { required: false } : path === '/api/me' ? { user: { id: 'test', email: 'test@example.test', display_name: 'Test', role: 'admin' }, csrf_token: 'test', session_id: 'test' } : path === '/api/hosts/' ? { hosts: [host] } : path === '/api/hosts/flicker-test/' ? { host } : null
  if (json) { res.setHeader('content-type', 'application/json'); res.end(JSON.stringify(json)); return }
  if (path.startsWith('/api/')) { res.writeHead(404); res.end(); return }
  try {
    const file = path.startsWith('/assets/') || ['/boot.js', '/sw.js', '/manifest.webmanifest'].includes(path) ? path.slice(1) : 'index.html'
    res.setHeader('content-type', file.endsWith('.js') ? 'application/javascript' : file.endsWith('.css') ? 'text/css' : 'text/html')
    res.end(await readFile(join(dist, file)))
  } catch { res.writeHead(404); res.end() }
})
await new Promise((done) => server.listen(0, '127.0.0.1', done))
const base = `http://127.0.0.1:${server.address().port}`
const pause = (ms) => new Promise((done) => setTimeout(done, ms))

function fullScreen(generation, large = false) {
  let ansi = '\x1b[?2026h\x1b[?25l\x1b]8;;\x1b\\'
  for (let row = 0; row < rows; row++) {
    const text = `FRAME ${String(generation).padStart(4, '0')} ROW ${String(row).padStart(2, '0')} `.padEnd(cols, 'X')
    ansi += `\x1b[${row + 1};1H`
    // Cross xterm's 128 KiB decoder buffer using valid color sequences. This
    // exercises real large frames without writing beyond the terminal grid.
    ansi += large ? [...text].map((c) => '\x1b[38;2;238;238;238m'.repeat(3) + c).join('') : text
  }
  return ansi + '\x1b[0m\x1b[45;1H\x1b[?25l\x1b[?2026l'
}

const report = []
try {
  for (const name of process.env.HERDRX_TEST_ENGINES?.split(',') || ['chromium', 'firefox', 'webkit']) {
    const browser = await ({ chromium, firefox, webkit })[name].launch({ headless: true, ...(name === 'chromium' && process.env.HERDRX_TEST_CHROMIUM ? { executablePath: process.env.HERDRX_TEST_CHROMIUM } : {}) })
    try {
      for (const mobile of [false, true]) {
        const context = await browser.newContext({ viewport: mobile ? { width: 390, height: 844 } : { width: 1500, height: 950 }, deviceScaleFactor: mobile ? 3 : 1, hasTouch: mobile, ...(name !== 'firefox' ? { isMobile: mobile } : {}) })
        const page = await context.newPage()
        await page.addInitScript(() => {
          // This suite exercises fixed source frames; reflow has its own geometry tests.
          localStorage.setItem('herdrx.terminal-display.v2', JSON.stringify({ mobile: { mode: 'fixed', fontSize: 14, zoom: 100 } }))
          const nativeRAF = window.requestAnimationFrame.bind(window)
          window.requestAnimationFrame = (callback) => nativeRAF((time) => {
            callback(time)
            if (!window.flickerPhase) return
            const rows = [...document.querySelectorAll('.xterm-rows > div')].map((el) => el.textContent)
            const generations = rows.map((row) => /^FRAME (\d{4}) ROW/.exec(row)?.[1] || null)
            const screen = document.querySelector('.xterm-screen').getBoundingClientRect()
            const sample = { phase: window.flickerPhase, time, missing: generations.filter((value) => value === null).length, generations: [...new Set(generations.filter(Boolean))], width: screen.width, height: screen.height }
            // All callbacks belonging to one browser paint share a timestamp.
            // Keep the last callback's DOM, including xterm's render callback;
            // a sampler running before that callback sees transient old DOM.
            const samples = window.flickerSamples
            if (samples.at(-1)?.time === time) samples[samples.length - 1] = sample
            else samples.push(sample)
          })
        })
        const errors = [], acknowledgements = []
        let sendFrame, sendSnapshot, streamCount = 0, wheelCount = 0, seq = 0
        page.on('pageerror', (error) => errors.push(error.message))
        await page.routeWebSocket('**/api/hosts/*/ws', (ws) => {
          sendSnapshot = (snapshot) => ws.send(JSON.stringify({ t: 'snapshot', snapshot }))
          sendFrame = (ansi, full = true) => {
            const bytes = Buffer.from(ansi), frame = Buffer.alloc(20 + bytes.length)
            frame.set([0x74, 1, 1, full ? 1 : 0]); frame.writeUInt32LE(streamCount, 4); frame.writeBigUInt64LE(BigInt(++seq), 8)
            frame.writeUInt16LE(cols, 16); frame.writeUInt16LE(rows, 18); bytes.copy(frame, 20)
            ws.send(frame)
          }
          ws.onMessage((raw) => {
            if (typeof raw !== 'string') { if (raw[2] === 2) acknowledgements.push(Number(raw.readBigUInt64LE(8))); return }
            const message = JSON.parse(raw)
            if (message.t === 'hello') {
              ws.send(JSON.stringify({ t: 'server_info' })); ws.send(JSON.stringify({ t: 'conn', state: 'ready' })); ws.send(JSON.stringify({ t: 'snapshot', snapshot }))
            } else if (message.t === 'terminal.open') {
              streamCount++
              ws.send(JSON.stringify({ t: 'terminal.opened', id: message.id, stream_id: streamCount }))
              sendFrame('\x1b[2J' + fullScreen(0))
            } else if (message.t === 'call') {
              if (message.method === 'terminal.scroll') wheelCount++
              const history = Array.from({ length: 300 }, (_, row) => `FRAME 0400 ROW ${String(row % 100).padStart(2, '0')} `.padEnd(cols - 1, 'H')).join('\n')
              ws.send(JSON.stringify({ t: 'result', id: message.id, result: message.method === 'pane.read' ? { read: { text: history } } : {} }))
            }
          })
        })
        await page.goto(base + '/h/flicker-test')
        await expect(page.locator('.xterm-rows')).toContainText('FRAME 0000')
        await page.evaluate(() => new Promise((done) => requestAnimationFrame(() => requestAnimationFrame(done))))
        await page.evaluate(() => {
          window.flickerSamples = []
          window.flickerPhase = ''
          const sample = () => {
            window.flickerRAF = requestAnimationFrame(sample)
          }
          window.flickerRAF = requestAnimationFrame(sample)
        })
        const run = async (phase, emit) => {
          await page.evaluate((phase) => { window.flickerPhase = phase }, phase)
          await emit()
          await expect.poll(() => acknowledgements.at(-1)).toBe(seq)
          await page.evaluate(() => new Promise((done) => requestAnimationFrame(() => requestAnimationFrame(done))))
          await page.evaluate(() => { window.flickerPhase = '' })
        }
        if (!mobile) {
          await page.locator('.terminal-viewport').evaluate((el) => { el.scrollTop = 0; el.scrollLeft = 0 })
          const viewport = await page.locator('.terminal-viewport').boundingBox()
          await page.mouse.move(viewport.x + 30, viewport.y + 30)
          await page.mouse.wheel(0, -120)
          await expect.poll(() => wheelCount).toBeGreaterThan(0)
        }
        await run('small-full', async () => {
          for (let n = 1; n <= 180; n++) { sendFrame(fullScreen(n)); await pause(5 + n % 5) }
        })
        assert.ok(Buffer.byteLength(fullScreen(200, true)) > 300_000)
        await run('large-full', async () => {
          for (let n = 200; n < 224; n++) { sendFrame(fullScreen(n, true)); await pause(7 + n % 5) }
        })
        await run('queued-full-delta', async () => {
          for (let n = 300; n < 324; n++) {
            sendFrame(fullScreen(n, n % 4 === 0))
            sendFrame('\x1b[1;140HDELTA\x1b[45;1H\x1b[?25l', false)
          }
        })
        await expect(page.locator('.xterm-rows > div').first()).toContainText('FRAME 0323')
        await expect(page.locator('.xterm-rows > div').first()).toContainText('DELTA')
        assert.equal(streamCount, 1, 'painting reopened or remounted the terminal')
        sendSnapshot({ ...snapshot, panes: snapshot.panes.map((pane) => ({ ...pane, scroll: { ...pane.scroll, max_offset_from_bottom: 300 } })) })
        if (mobile) await page.getByRole('button', { name: '终端工具', exact: true }).click()
        else {
          await page.locator('.terminal-pane').hover()
          await page.getByRole('button', { name: '分屏工具', exact: true }).click()
        }
        await page.evaluate(() => { window.flickerPhase = 'history-entry' })
        await page.getByRole('button', { name: '查看终端历史', exact: true }).click()
        await expect(page.locator('.xterm-rows > div').first()).toContainText('FRAME 0400')
        await page.evaluate(() => new Promise((done) => requestAnimationFrame(() => requestAnimationFrame(done))))
        await page.evaluate(() => { window.flickerPhase = 'history-live' })
        await page.getByRole('button', { name: '返回实时', exact: true }).click()
        await expect(page.locator('.xterm-rows > div').first()).toContainText('FRAME 0000')
        await page.evaluate(() => new Promise((done) => requestAnimationFrame(() => requestAnimationFrame(done))))
        assert.equal(streamCount, 2, 'history did not reopen exactly one authoritative live stream')
        const samples = await page.evaluate(() => { cancelAnimationFrame(window.flickerRAF); return window.flickerSamples })
        const summary = ['small-full', 'large-full', 'queued-full-delta', 'history-entry', 'history-live'].map((phase) => {
          const selected = samples.filter((sample) => sample.phase === phase)
          return { phase, frames: selected.length, blank: selected.filter((sample) => sample.missing === rows).length, partial: selected.filter((sample) => (sample.missing > 0 && sample.missing < rows) || sample.generations.length > 1).length }
        })
        report.push({ name, mobile, summary, samples })
        console.log(`${name} ${mobile ? 'mobile' : 'desktop'}: ${JSON.stringify(summary)}`)
        assert.deepEqual(errors, [])
        assert.equal(new Set(samples.map((sample) => `${sample.width}x${sample.height}`)).size, 1, 'frame painting or history controls changed the terminal grid geometry')
        assert.ok(summary.every((item) => item.frames > 0), 'scenario did not observe a browser paint')
        if (!process.env.HERDRX_FLICKER_BASELINE) assert.ok(summary.every((item) => item.blank === 0 && item.partial === 0), 'terminal painted a blank or partial frame')
        await context.close()
      }
    } finally { await browser.close() }
  }
} finally {
  if (process.env.HERDRX_FLICKER_REPORT) {
    await mkdir(process.env.HERDRX_FLICKER_REPORT, { recursive: true })
    await writeFile(join(process.env.HERDRX_FLICKER_REPORT, 'frames.json'), JSON.stringify(report, null, 2))
  }
  await new Promise((done) => server.close(done))
}
