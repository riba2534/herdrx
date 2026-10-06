#!/usr/bin/env node
// Isolated browser regression for the pane screen view (画面视图). A fake
// workbench WebSocket answers `screen.watch` with rendered ANSI lines the way
// the Go server does (full frame, then drop/set deltas tagged with a watch
// generation); it never connects to a real Herdr host, pane or PTY.
//
// Pins: the view is reachable on phones and desktops, reflows long lines to
// the window width without horizontal scrolling, keeps the remote size alone
// (no control/resize messages), sends input through the local composer only,
// drops frames of a replaced watch generation, and stops watching on exit.
import assert from 'node:assert/strict'
import { createServer } from 'node:http'
import { readFile, mkdir } from 'node:fs/promises'
import { fileURLToPath } from 'node:url'
import { join } from 'node:path'
import { chromium, firefox, webkit, expect } from '../web/node_modules/@playwright/test/index.mjs'

const dist = fileURLToPath(new URL('../web/dist/', import.meta.url))
const artifacts = process.env.HERDRX_SCREEN_ARTIFACTS
const host = { id: 'screen-test', name: '画面测试', transport: 'ssh' }
const snapshot = {
  protocol: 1, version: 'test', focused_workspace_id: 'w1', focused_tab_id: 't1', focused_pane_id: 'p1',
  workspaces: [{ workspace_id: 'w1', active_tab_id: 't1', label: '画面测试', number: 1, pane_count: 1, tab_count: 1, agent_status: 'working' }],
  tabs: [{ workspace_id: 'w1', tab_id: 't1', label: '开发终端', number: 1, pane_count: 1, agent_status: 'working' }],
  panes: [{ workspace_id: 'w1', tab_id: 't1', pane_id: 'p1', label: '构建', cwd: '/workspace/example', terminal_id: 'term1', revision: 1, agent_status: 'working', agent: 'claude' }],
  layouts: [{ workspace_id: 'w1', tab_id: 't1', area: { x: 0, y: 0, width: 220, height: 50 }, focused_pane_id: 'p1', splits: [], zoomed: false, panes: [{ pane_id: 'p1', rect: { x: 0, y: 0, width: 220, height: 50 } }] }],
  agents: [],
}
// A wide remote task: one logical line far wider than a phone.
const LONG = 'pnpm build › ' + 'src/components/really/deep/module/path/'.repeat(5) + 'index.tsx 构建完成'
const screenLines = (count) => [
  ...Array.from({ length: Math.max(0, count - 3) }, (_, index) => `\x1b[0m\x1b[38;5;2mstep ${index}\x1b[0m done`),
  `\x1b[0m\x1b[1m\x1b[38;5;1merror\x1b[0m ${LONG}`,
  '',
  '\x1b[0m\x1b[1m\x1b[38;5;4m$ \x1b[0m',
]
const ansi = Array.from({ length: 24 }, (_, i) => `\x1b[${i + 1};1HScreen terminal ${i}`).join('')

const server = createServer(async (req, res) => {
  const path = new URL(req.url, 'http://localhost').pathname
  const json = path === '/api/bootstrap/status' ? { required: false } : path === '/api/me' ? { user: { id: 'screen-user', email: 'screen@example.test', display_name: 'Screen', role: 'admin' }, csrf_token: 'screen-fixture', session_id: 'screen-session' } : path === '/api/me/workbench-session' ? { session: null } : path === '/api/hosts/' ? { hosts: [host] } : path === '/api/hosts/screen-test/' ? { host } : null
  if (json) { res.setHeader('content-type', 'application/json'); res.end(JSON.stringify(json)); return }
  if (path.startsWith('/api/')) { res.writeHead(404); res.end(); return }
  try {
    const file = (path.startsWith('/assets/') || path.startsWith('/brand/')) || ['/boot.js', '/sw.js', '/manifest.webmanifest', '/favicon.ico'].includes(path) ? path.slice(1) : 'index.html'
    res.setHeader('content-type', file.endsWith('.js') ? 'application/javascript' : file.endsWith('.css') ? 'text/css' : file.endsWith('.png') ? 'image/png' : file.endsWith('.ico') ? 'image/x-icon' : file.endsWith('.webmanifest') ? 'application/manifest+json' : 'text/html')
    res.end(await readFile(join(dist, file)))
  } catch { res.writeHead(404); res.end() }
})
await new Promise((done) => server.listen(0, '127.0.0.1', done))
const base = `http://127.0.0.1:${server.address().port}`

async function fixture(browser, options) {
  const context = await browser.newContext(options)
  const page = await context.newPage()
  page.setDefaultTimeout(10_000)
  const messages = [], errors = []
  page.on('pageerror', (error) => errors.push(error.message))
  let gen = 0
  await page.routeWebSocket('**/api/hosts/*/ws', (ws) => {
    let lines = []
    let nextStreamID = 0
    let watchGen = 0, seq = 0
    const send = (message) => ws.send(JSON.stringify(message))
    ws.onMessage((raw) => {
      if (typeof raw !== 'string') return
      const message = JSON.parse(raw)
      messages.push(message)
      if (message.t === 'hello') {
        send({ t: 'server_info', features: { terminal_control: 1, terminal_resize_v2: 1 } })
        send({ t: 'conn', state: 'ready' })
        send({ t: 'snapshot', snapshot })
      } else if (message.t === 'terminal.open') {
        const bytes = Buffer.from(ansi)
        const frame = Buffer.alloc(20 + bytes.length)
        frame.set([0x74, 1, 1, 1])
        const streamID = ++nextStreamID
        frame.writeUInt32LE(streamID, 4)
        frame.writeBigUInt64LE(1n, 8)
        frame.writeUInt16LE(220, 16)
        frame.writeUInt16LE(50, 18)
        bytes.copy(frame, 20)
        send({ t: 'terminal.opened', id: message.id, stream_id: streamID })
        ws.send(frame)
      } else if (message.t === 'screen.watch') {
        const previousGen = watchGen
        watchGen = ++gen
        seq = 0
        lines = screenLines(message.lines >= 500 ? message.lines : 200)
        send({ t: 'screen.watching', id: message.id, pane_id: message.pane_id, lines: message.lines, gen: watchGen })
        // A frame of the replaced generation arriving late must be ignored.
        if (previousGen) send({ t: 'screen', pane_id: message.pane_id, gen: previousGen, seq: 99, total: 1, set: [[0, 'STALE GENERATION']] })
        send({ t: 'screen', pane_id: message.pane_id, gen: watchGen, seq: ++seq, full: true, lines, total: lines.length, requested: message.lines })
      } else if (message.t === 'call' && message.method === 'pane.send_input') {
        send({ t: 'result', id: message.id, result: { type: 'ok' } })
        if (message.params?.text) {
          // Echo like a shell: the typed text lands on the prompt line, then a new prompt.
          const echo = `\x1b[0m\x1b[1m\x1b[38;5;4m$ \x1b[0m${message.params.text}`
          setTimeout(() => {
            lines = [...lines.slice(1, -1), echo, '\x1b[0m\x1b[1m\x1b[38;5;4m$ \x1b[0m']
            send({ t: 'screen', pane_id: 'p1', gen: watchGen, seq: ++seq, drop: 1, total: lines.length, set: [[lines.length - 2, echo], [lines.length - 1, lines.at(-1)]] })
          }, 60)
        }
      } else if (message.t === 'call') {
        send({ t: 'result', id: message.id, result: {} })
      }
    })
  })
  await page.goto(base + '/h/screen-test')
  await expect(page.locator('.xterm-rows').first()).toContainText('Screen terminal')
  return { context, page, messages, errors }
}

async function withFixture(browser, options, body) {
  const f = await fixture(browser, options)
  try { return await body(f) } finally { await f.context.close() }
}

async function screenshot(page, name) {
  if (!artifacts) return
  await mkdir(artifacts, { recursive: true })
  await page.screenshot({ path: join(artifacts, name + '.png') })
}

const ofType = (messages, type) => messages.filter((item) => item.t === type)
const sizing = (messages) => messages.filter((item) => item.t === 'terminal.control.acquire' || item.t === 'terminal.resize_v2')

async function logGeometry(page) {
  return page.evaluate(() => {
    const log = document.querySelector('.screen-log')
    const lines = [...document.querySelectorAll('.screen-line')]
    return {
      scrollWidth: log.scrollWidth, clientWidth: log.clientWidth,
      fontSize: parseFloat(getComputedStyle(log).fontSize),
      longLineHeight: lines.find((line) => line.textContent.includes('构建完成'))?.getBoundingClientRect().height || 0,
      lineHeight: lines[0]?.getBoundingClientRect().height || 0,
      page: { scroll: document.documentElement.scrollWidth, client: document.documentElement.clientWidth },
    }
  })
}

const mobile = { viewport: { width: 390, height: 844 }, hasTouch: true, isMobile: true, deviceScaleFactor: 2 }
const desktop = { viewport: { width: 1280, height: 800 } }
const launchArgs = ['--disable-gpu', '--disable-software-rasterizer', '--renderer-process-limit=2', '--js-flags=--max-old-space-size=384']
const engines = process.env.HERDRX_TEST_ENGINES?.split(',') || ['chromium']
try {
  for (const name of engines) {
    const browser = await ({ chromium, firefox, webkit })[name].launch({ headless: true, args: name === 'chromium' ? launchArgs : [] })
    try {
      // 手机：从切换面板进入画面视图，长行按手机宽度折行、文字可读，输入走本地输入框。
      await withFixture(browser, { ...(name === 'firefox' ? { ...mobile, isMobile: false } : mobile) }, async (phone) => {
        await phone.page.getByRole('button', { name: '切换工作区或终端', exact: true }).click()
        await phone.page.getByRole('button', { name: /切换到画面视图/ }).click()
        const view = phone.page.getByRole('region', { name: '画面视图' })
        await expect(view.getByRole('log', { name: 'Pane 画面' })).toContainText('构建完成')
        assert.equal(ofType(phone.messages, 'screen.watch').at(-1).lines, 200)
        await expect(view.getByText(/画面 · 实时/)).toBeAttached()
        const wrapped = await logGeometry(phone.page)
        assert.ok(wrapped.scrollWidth <= wrapped.clientWidth + 1, `${name}: wrapped screen scrolls horizontally ${JSON.stringify(wrapped)}`)
        assert.ok(wrapped.fontSize >= 12, `${name}: screen text is not readable on a phone (${wrapped.fontSize}px)`)
        assert.ok(wrapped.longLineHeight > wrapped.lineHeight * 2, `${name}: the long line did not reflow ${JSON.stringify(wrapped)}`)
        assert.ok(wrapped.page.scroll <= wrapped.page.client + 1, `${name}: page overflows horizontally`)
        await screenshot(phone.page, `${name}-mobile-screen`)

        // 本地输入框可见且只能本地输入；发送后回显由增量帧带回。
        const box = phone.page.getByRole('textbox', { name: '本地输入内容' })
        await expect(box).toBeVisible()
        await expect(phone.page.getByRole('button', { name: /输入方式/ })).toHaveCount(0)
        const boxRect = await box.boundingBox()
        assert.ok(boxRect && boxRect.x <= 16 && boxRect.width > 240, `${name}: local-only composer leaves an empty mode column ${JSON.stringify(boxRect)}`)
        await box.fill('echo 画面发送')
        await phone.page.getByRole('button', { name: '发送', exact: true }).click()
        await expect.poll(() => phone.messages.filter((item) => item.t === 'call' && item.method === 'pane.send_input').length).toBeGreaterThan(0)
        await expect(view.locator('.screen-line').filter({ hasText: 'echo 画面发送' })).toHaveCount(1)
        await expect(view.getByText('STALE GENERATION')).toHaveCount(0)

        // 原样排版：长行保持一行，改为横向滚动。
        await view.getByRole('button', { name: '自动折行' }).click()
        const raw = await logGeometry(phone.page)
        assert.ok(raw.scrollWidth > raw.clientWidth + 20, `${name}: raw layout should keep the remote line width ${JSON.stringify(raw)}`)
        await view.getByRole('button', { name: '自动折行' }).click()

        assert.deepEqual(sizing(phone.messages), [], `${name}: the screen view must not take terminal size control`)
        await view.getByRole('button', { name: '显示终端' }).click()
        await expect(view).toHaveCount(0)
        await expect.poll(() => ofType(phone.messages, 'screen.unwatch').length).toBe(1)
        assert.deepEqual(phone.errors, [])
      })

      // 桌面：终端工具栏进入，本地输入框自动出现；更早内容分步加载并丢弃旧代次的帧；刷新后保持画面视图。
      await withFixture(browser, desktop, async (desk) => {
        await desk.page.getByRole('button', { name: '终端工具', exact: true }).first().click()
        await desk.page.getByRole('button', { name: '画面视图', exact: true }).click()
        const view = desk.page.getByRole('region', { name: '画面视图' })
        await expect(view.getByRole('log', { name: 'Pane 画面' })).toContainText('step 0')
        await expect(desk.page.getByRole('textbox', { name: '本地输入内容' })).toBeVisible()
        await expect(desk.page.getByRole('button', { name: '直接输入终端' })).toHaveCount(0)
        const selected = await desk.page.evaluate(() => {
          const line = [...document.querySelectorAll('.screen-line')].find((item) => item.textContent.includes('构建完成'))
          const range = document.createRange()
          range.selectNodeContents(line)
          const selection = getSelection()
          selection.removeAllRanges()
          selection.addRange(range)
          return selection.toString()
        })
        assert.ok(selected.includes('error pnpm build'), `${name}: screen text is not selectable: ${selected}`)

        await view.getByRole('button', { name: '加载更早内容（最近 500 行）' }).click()
        await expect.poll(() => ofType(desk.messages, 'screen.watch').at(-1)?.lines).toBe(500)
        await expect(view.locator('.screen-line')).toHaveCount(500)
        await expect(view.getByText('STALE GENERATION')).toHaveCount(0)
        await screenshot(desk.page, `${name}-desktop-screen`)

        await desk.page.reload()
        await expect(desk.page.getByRole('region', { name: '画面视图' }).getByRole('log', { name: 'Pane 画面' })).toContainText('构建完成')
        assert.deepEqual(sizing(desk.messages), [], `${name}: the screen view must not take terminal size control`)
        assert.deepEqual(desk.errors, [])
      })
    } finally {
      await browser.close()
    }
  }
  console.log('screen view browser smoke passed')
} finally {
  await new Promise((done) => server.close(done))
}
