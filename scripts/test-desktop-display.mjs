#!/usr/bin/env node
// Desktop 295-column dual-pane display/input regression. Synthetic HTTP/WebSocket only.
import assert from 'node:assert/strict'
import { createServer } from 'node:http'
import { readFile, mkdir } from 'node:fs/promises'
import { fileURLToPath } from 'node:url'
import { join } from 'node:path'
import { chromium, firefox, webkit, expect } from '../web/node_modules/@playwright/test/index.mjs'

const dist = fileURLToPath(new URL('../web/dist/', import.meta.url))
const artifacts = process.env.HERDRX_DESKTOP_ARTIFACTS
const host = { id: 'display-test', name: '测试主机', transport: 'ssh' }
const splitSnapshot = {
  protocol: 1, version: 'test', focused_workspace_id: 'w1', focused_tab_id: 't1', focused_pane_id: 'p1',
  workspaces: [{ workspace_id: 'w1', active_tab_id: 't1', label: '295双分屏', number: 1, pane_count: 2, tab_count: 1, agent_status: 'working' }],
  tabs: [{ workspace_id: 'w1', tab_id: 't1', label: '开发终端', number: 1, pane_count: 2, agent_status: 'working' }],
  panes: [
    { workspace_id: 'w1', tab_id: 't1', pane_id: 'p1', label: '上屏', cwd: '/workspace/example', terminal_id: 'term1', revision: 1, agent_status: 'working', scroll: { max_offset_from_bottom: 0, offset_from_bottom: 0, viewport_rows: 38 } },
    { workspace_id: 'w1', tab_id: 't1', pane_id: 'p2', label: '下屏', cwd: '/workspace/example', terminal_id: 'term2', revision: 1, agent_status: 'working', scroll: { max_offset_from_bottom: 0, offset_from_bottom: 0, viewport_rows: 37 } },
  ],
  layouts: [{
    workspace_id: 'w1', tab_id: 't1', area: { x: 0, y: 0, width: 295, height: 79 }, focused_pane_id: 'p1', splits: [], zoomed: false,
    panes: [
      { pane_id: 'p1', rect: { x: 0, y: 0, width: 295, height: 40 } },
      { pane_id: 'p2', rect: { x: 0, y: 40, width: 295, height: 39 } },
    ],
  }],
  agents: [],
}

function gridANSI(cols, rows, tag) {
  return '\x1b[2J' + Array.from({ length: rows }, (_, i) => {
    const body = `${tag} r${String(i + 1).padStart(2, '0')} `.padEnd(Math.max(0, cols - 4), i % 2 ? '.' : 'x')
    return `\x1b[${i + 1};1H${body.slice(0, Math.max(0, cols - 3))}\x1b[${i + 1};${Math.max(1, cols - 2)}HEND`
  }).join('') + `\x1b[${rows};${cols}H`
}

const server = createServer(async (req, res) => {
  const path = new URL(req.url, 'http://localhost').pathname
  const json = path === '/api/bootstrap/status' ? { required: false }
    : path === '/api/me' ? { user: { id: 'display-user', email: 'display@example.test', display_name: 'Display', role: 'admin' }, csrf_token: 'display-fixture', session_id: 'display-session' }
    : path === '/api/hosts/' ? { hosts: [host] }
    : path === '/api/hosts/display-test/' ? { host } : null
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

async function fixture(browser, options, snapshot = splitSnapshot) {
  let live = structuredClone(snapshot)
  const context = await browser.newContext({ hasTouch: false, isMobile: false, ...options })
  const page = await context.newPage()
  const messages = []
  const errors = []
  page.on('pageerror', (error) => errors.push(error.message))
  let emitSized = () => {}
  let pushSnapshot = () => {}
  const paneStreams = new Map()
  await page.routeWebSocket('**/api/hosts/*/ws', (ws) => {
    let streamID = 0
    const sizes = new Map()
    const emitFrame = (id, text, cols, rows) => {
      const size = { cols: cols ?? sizes.get(id)?.cols ?? 80, rows: rows ?? sizes.get(id)?.rows ?? 40 }
      sizes.set(id, { ...sizes.get(id), ...size })
      const bytes = Buffer.from(text)
      const frame = Buffer.alloc(20 + bytes.length)
      frame.set([0x74, 1, 1, 1]); frame.writeUInt32LE(id, 4); frame.writeBigUInt64LE(1n, 8)
      frame.writeUInt16LE(size.cols, 16); frame.writeUInt16LE(size.rows, 18); bytes.copy(frame, 20)
      ws.send(frame)
    }
    emitSized = (cols, rows, text) => {
      for (const [paneID, id] of paneStreams) {
        const tag = paneID === 'p1' ? 'TOP' : 'BOT'
        emitFrame(id, text || gridANSI(cols, rows, tag), cols, rows)
      }
    }
    pushSnapshot = (next) => {
      live = next
      ws.send(JSON.stringify({ t: 'snapshot', snapshot: live }))
    }
    ws.onMessage((raw) => {
      if (typeof raw !== 'string') {
        const data = Buffer.from(raw), id = data.readUInt32LE(4)
        const size = data[2] === 4 ? { cols: data.readUInt16LE(16), rows: data.readUInt16LE(18) } : null
        messages.push({ op: data[2], stream_id: id, ...size })
        return
      }
      const message = JSON.parse(raw)
      messages.push(message)
      if (message.t === 'hello') {
        ws.send(JSON.stringify({ t: 'server_info' }))
        ws.send(JSON.stringify({ t: 'conn', state: 'ready' }))
        ws.send(JSON.stringify({ t: 'snapshot', snapshot: live }))
      } else if (message.t === 'terminal.open') {
        const id = ++streamID
        paneStreams.set(message.pane_id, id)
        sizes.set(id, { cols: message.cols, rows: message.rows, responsive: message.responsive })
        ws.send(JSON.stringify({ t: 'terminal.opened', id: message.id, stream_id: id, pane_id: message.pane_id }))
        const pane = live.panes.find((item) => item.pane_id === message.pane_id)
        const rows = pane?.scroll?.viewport_rows || message.rows
        emitFrame(id, gridANSI(message.cols, rows, message.pane_id === 'p1' ? 'TOP' : 'BOT'), message.cols, rows)
      } else if (message.t === 'call') {
        ws.send(JSON.stringify({ t: 'result', id: message.id, result: message.method === 'pane.read' ? { read: { text: Array.from({ length: 40 }, (_, i) => `history line ${i}`).join('\n') } } : message.method === 'pane.send_input' ? { type: 'ok' } : {} }))
      }
    })
  })
  await page.goto(base + '/h/display-test')
  await page.locator('.xterm-rows').first().waitFor({ timeout: 15000 })
  return { context, page, messages, errors, paneStreams, emitSized: (...args) => emitSized(...args), pushSnapshot: (next) => pushSnapshot(next) }
}

async function metrics(page, index = 0) {
  return page.locator('.terminal-viewport').nth(index).evaluate((viewport) => {
    const screen = viewport.querySelector('.xterm-screen').getBoundingClientRect()
    const row = viewport.querySelector('.xterm-rows > div')
    const rows = viewport.querySelector('.xterm-rows')
    return {
      width: viewport.clientWidth, height: viewport.clientHeight, contentWidth: viewport.scrollWidth, contentHeight: viewport.scrollHeight,
      screenWidth: screen.width, screenHeight: screen.height, font: parseFloat(getComputedStyle(rows).fontSize),
      rowCount: rows.childElementCount, rowHeight: row?.getBoundingClientRect().height || 0,
    }
  })
}

async function gridState(page, index = 0) {
  return page.locator('.terminal-viewport').nth(index).evaluate((el) => {
    const screen = el.querySelector('.xterm-screen').getBoundingClientRect()
    const rowsEl = el.querySelector('.xterm-rows')
    const rows = [...el.querySelectorAll('.xterm-rows > div')]
    const first = rows[0]
    const last = rows[37]
    const trim = (row) => (row?.textContent || '').replace(/\s+$/, '')
    const endBox = (row) => {
      if (!row) return null
      const raw = row.textContent || ''
      const match = raw.match(/END\s*$/)
      if (!match) return null
      const start = raw.length - match[0].length
      const walker = document.createTreeWalker(row, NodeFilter.SHOW_TEXT)
      let pos = 0
      let beginNode = null, beginOff = 0, endNode = null, endOff = 0
      let node
      while ((node = walker.nextNode())) {
        const next = pos + node.data.length
        if (!beginNode && start >= pos && start < next) { beginNode = node; beginOff = start - pos }
        if (start + 3 > pos && start + 3 <= next) { endNode = node; endOff = start + 3 - pos; break }
        pos = next
      }
      if (!beginNode || !endNode) return null
      const range = document.createRange()
      range.setStart(beginNode, beginOff)
      range.setEnd(endNode, endOff)
      const rects = [...range.getClientRects()]
      const rect = rects.at(-1)
      return rect ? { right: rect.right, bottom: rect.bottom } : null
    }
    const lastEnd = endBox(last)
    const box = el.getBoundingClientRect()
    const firstText = trim(first)
    const lastText = trim(last)
    return {
      clientWidth: el.clientWidth, clientHeight: el.clientHeight,
      scrollWidth: el.scrollWidth, scrollHeight: el.scrollHeight,
      scrollLeft: el.scrollLeft, scrollTop: el.scrollTop,
      screenWidth: screen.width, screenHeight: screen.height,
      screenRight: screen.right, screenBottom: screen.bottom,
      font: parseFloat(getComputedStyle(rowsEl).fontSize),
      rowCount: rowsEl.childElementCount,
      firstText, lastText, firstLen: firstText.length, lastLen: lastText.length,
      lastEnd: !!lastEnd,
      endRight: lastEnd?.right ?? null, endBottom: lastEnd?.bottom ?? null,
      boxRight: box.right, boxBottom: box.bottom,
    }
  })
}

function gridReady(g) {
  return !!g && g.rowCount === 38 && g.font === 14
    && g.firstLen === 295 && g.lastLen === 295
    && g.firstText.startsWith('TOP r01') && g.firstText.endsWith('END')
    && g.lastText.startsWith('TOP r38') && g.lastText.endsWith('END')
    && g.lastEnd && g.endRight != null && g.endBottom != null
}

async function screenshot(page, name) {
  if (!artifacts) return
  await mkdir(artifacts, { recursive: true })
  await page.screenshot({ path: join(artifacts, name + '.png') })
}

async function clipGeometry(pane) {
  return pane.locator('.terminal-viewport').evaluate((el) => {
    const screen = el.querySelector('.xterm-screen').getBoundingClientRect()
    const view = el.getBoundingClientRect()
    const row0 = el.querySelector('.xterm-rows > div')?.getBoundingClientRect()
    return {
      scrollTop: el.scrollTop, clientHeight: el.clientHeight, scrollHeight: el.scrollHeight,
      screenTop: screen.top, screenHeight: screen.height, viewTop: view.top, viewBottom: view.bottom,
      row0Top: row0?.top ?? 0, clippedAbove: screen.top < view.top - 1,
    }
  })
}

async function revealClippedTop(page, pane) {
  const viewport = pane.locator('.terminal-viewport')
  await viewport.evaluate((el) => { el.scrollTop = el.scrollHeight })
  const before = await clipGeometry(pane)
  assert.ok(before.clippedAbove && before.row0Top < before.viewTop - 1, `top content was not clipped: ${JSON.stringify(before)}`)
  await viewport.hover({ force: true })
  for (let i = 0; i < 40; i++) {
    await page.mouse.wheel(0, -2400)
    const current = await clipGeometry(pane)
    if (!current.clippedAbove) return { before, after: current, wheels: i + 1 }
  }
  const after = await clipGeometry(pane)
  assert.fail(`top row still clipped after wheel: ${JSON.stringify({ before, after })}`)
}

function opens(messages) { return messages.filter((item) => item.t === 'terminal.open') }
function resizes(messages) { return messages.filter((item) => item.op === 4) }

async function openPaneTools(page, index = 0) {
  const pane = page.locator('.terminal-pane').nth(index)
  if (!(await pane.locator('.terminal-titlebar').isVisible())) {
    await pane.hover()
    await pane.getByRole('button', { name: '分屏工具', exact: true }).click()
  }
  await expect(pane.locator('.terminal-titlebar')).toBeVisible()
}

const cases = [
  { name: '1440x900-dpr1', viewport: { width: 1440, height: 900 }, deviceScaleFactor: 1 },
  { name: '1920x1080-dpr1', viewport: { width: 1920, height: 1080 }, deviceScaleFactor: 1 },
  { name: '2560x1320-dpr1', viewport: { width: 2560, height: 1320 }, deviceScaleFactor: 1 },
  { name: '1440x900-dpr2', viewport: { width: 1440, height: 900 }, deviceScaleFactor: 2 },
  { name: '1920x1080-dpr2', viewport: { width: 1920, height: 1080 }, deviceScaleFactor: 2 },
  { name: '2560x1320-dpr2', viewport: { width: 2560, height: 1320 }, deviceScaleFactor: 2 },
]

const engines = process.env.HERDRX_TEST_ENGINES?.split(',') || ['chromium']
try {
  for (const name of engines) {
    const browser = await ({ chromium, firefox, webkit })[name].launch({ headless: true, ...(name === 'chromium' && process.env.HERDRX_TEST_CHROMIUM ? { executablePath: process.env.HERDRX_TEST_CHROMIUM } : {}) })
    try {
      for (const c of cases) {
        const f = await fixture(browser, { viewport: c.viewport, deviceScaleFactor: c.deviceScaleFactor })
        const first = await metrics(f.page, 0)
        assert.equal(first.font, 14, `${c.name} desktop default shrunk to ${first.font}`)
        assert.ok(await f.page.evaluate(() => document.documentElement.scrollWidth <= innerWidth + 1), `${c.name} page overflow`)
        await openPaneTools(f.page)
        await expect(f.page.getByRole('button', { name: '适应窗口', exact: true })).toHaveAttribute('aria-pressed', 'false')
        await f.page.getByRole('button', { name: '收起终端工具', exact: true }).click()
        await expect(f.page.locator('.composer')).toHaveCount(0)
        assert.equal(resizes(f.messages).length, 0, `${c.name} sent remote resize`)
        assert.equal(opens(f.messages).length, 2, `${c.name} unexpected terminal.open`)
        assert.ok(opens(f.messages).every((item) => !item.responsive), `${c.name} opened responsive`)
        await expect.poll(async () => {
          const g = await gridState(f.page)
          return gridReady(g) ? true : g
        }, { timeout: 15000 }).toBe(true)
        const viewport = f.page.locator('.terminal-viewport').first()
        await viewport.evaluate((el) => { el.scrollLeft = el.scrollWidth; el.scrollTop = el.scrollHeight })
        const edge = await gridState(f.page)
        assert.ok(gridReady(edge), `${c.name} 295×38 END not presented: ${JSON.stringify(edge)}`)
        const screenOverflows = edge.screenWidth > edge.clientWidth + 1
        if (c.viewport.width === 1440) {
          assert.ok(screenOverflows, `${c.name} 1440 295-col screen did not exceed viewport: ${JSON.stringify(edge)}`)
        }
        if (screenOverflows) {
          assert.ok(edge.endRight <= edge.boxRight + 2, `${c.name} right edge not reachable: ${JSON.stringify(edge)}`)
        } else {
          assert.ok(edge.screenRight <= edge.boxRight + 2, `${c.name} fitted 295-col screen clipped: ${JSON.stringify(edge)}`)
          assert.ok(edge.endRight <= edge.boxRight + 2, `${c.name} fitted 295-col END clipped: ${JSON.stringify(edge)}`)
        }
        assert.ok(edge.endBottom <= edge.boxBottom + 2, `${c.name} last-row END not reachable: ${JSON.stringify(edge)}`)
        await screenshot(f.page, `${name}-${c.name}-fixed`)
        await openPaneTools(f.page)
        await f.page.getByRole('button', { name: '适应窗口', exact: true }).click()
        await expect.poll(async () => { const m = await metrics(f.page); return m.screenWidth <= m.width + 1 && m.screenHeight <= m.height + 1 }).toBe(true)
        await expect(f.page.getByRole('button', { name: '适应窗口', exact: true })).toHaveAttribute('aria-pressed', 'true')
        await screenshot(f.page, `${name}-${c.name}-fit`)
        await f.page.getByRole('button', { name: '适应窗口', exact: true }).click()
        await expect.poll(async () => (await metrics(f.page)).font).toBe(14)
        assert.equal(resizes(f.messages).length, 0, `${c.name} fit toggle sent remote resize`)
        assert.equal(opens(f.messages).length, 2)
        assert.deepEqual(f.errors, [], `${c.name} page errors: ${f.errors.join(' | ')}`)
        await f.context.close()
      }

      const observed = await fixture(browser, { viewport: { width: 1920, height: 1080 } })
      await expect.poll(async () => (await metrics(observed.page)).rowCount).toBe(38)
      const openCount = opens(observed.messages).length
      observed.emitSized(80, 24)
      await expect.poll(async () => (await metrics(observed.page)).rowCount).toBe(24)
      assert.equal((await metrics(observed.page)).font, 14)
      const split = structuredClone(splitSnapshot)
      split.layouts[0].area = { x: 0, y: 0, width: 147, height: 79 }
      split.layouts[0].panes = [{ pane_id: 'p1', rect: { x: 0, y: 0, width: 147, height: 79 } }, { pane_id: 'p2', rect: { x: 147, y: 0, width: 148, height: 79 } }]
      observed.pushSnapshot(split)
      await observed.page.waitForTimeout(200)
      assert.equal((await metrics(observed.page)).rowCount, 24, 'layout snapshot overrode observed frame grid')
      observed.emitSized(80, 24, `${gridANSI(80, 24, 'LONG')}${'\n extra output'.repeat(12)}`)
      await expect(observed.page.locator('.xterm-rows').first()).toContainText('LONG')
      observed.emitSized(295, 38)
      await expect.poll(async () => (await metrics(observed.page)).rowCount).toBe(38)
      assert.equal(opens(observed.messages).length, openCount, 'observed geometry reopened the stream')
      assert.equal(resizes(observed.messages).length, 0, 'observed geometry sent remote resize')
      assert.deepEqual(observed.errors, [])
      await observed.context.close()

      const typing = await fixture(browser, { viewport: { width: 1440, height: 900 } })
      await typing.page.locator('.terminal-pane').nth(1).locator('.terminal-viewport').click({ position: { x: 30, y: 30 } })
      await typing.page.locator('.terminal-pane-active .xterm-helper-textarea').focus()
      const beforeType = typing.messages.filter((item) => item.op === 3).length
      await typing.page.keyboard.type('xyz')
      await expect.poll(() => typing.messages.filter((item) => item.op === 3).length).toBe(beforeType + 3)
      const secondStream = typing.paneStreams.get('p2')
      assert.ok(secondStream, `missing p2 stream ${JSON.stringify([...typing.paneStreams])}`)
      assert.ok(typing.messages.filter((item) => item.op === 3).slice(beforeType).every((item) => item.stream_id === secondStream), 'typed into the wrong pane')
      assert.equal(resizes(typing.messages).length, 0)
      await typing.context.close()

      const nativeClip = await fixture(browser, { viewport: { width: 1440, height: 900 } })
      const nativePane = nativeClip.page.locator('.terminal-pane').first()
      const nativeScrolls = () => nativeClip.messages.filter((item) => item.method === 'terminal.scroll').length
      const beforeNative = nativeScrolls()
      const nativeReveal = await revealClippedTop(nativeClip.page, nativePane)
      assert.ok(!nativeReveal.after.clippedAbove, `fullscreen top still clipped: ${JSON.stringify(nativeReveal)}`)
      assert.ok(nativeReveal.after.row0Top >= nativeReveal.after.viewTop - 2, `fullscreen row0 still above viewport: ${JSON.stringify(nativeReveal)}`)
      assert.ok(nativeReveal.after.scrollTop < nativeReveal.before.scrollTop, `fullscreen scrollTop did not decrease: ${JSON.stringify(nativeReveal)}`)
      const afterRevealScrolls = nativeScrolls()
      await nativePane.locator('.terminal-viewport').hover({ force: true })
      await expect.poll(async () => {
        await nativeClip.page.mouse.wheel(0, -200)
        return nativeScrolls()
      }).toBeGreaterThan(afterRevealScrolls)
      await screenshot(nativeClip.page, `${name}-wheel-native-top`)
      assert.deepEqual(nativeClip.errors, [])
      await nativeClip.context.close()

      const historySnap = structuredClone(splitSnapshot)
      historySnap.panes = historySnap.panes.map((pane) => ({ ...pane, scroll: { max_offset_from_bottom: 80, offset_from_bottom: 0, viewport_rows: 38 } }))
      const history = await fixture(browser, { viewport: { width: 1440, height: 900 } }, historySnap)
      const historyPane = history.page.locator('.terminal-pane').first()
      const historyReads = () => history.messages.filter((item) => item.method === 'pane.read').length
      const beforeHistory = historyReads()
      const liveReveal = await revealClippedTop(history.page, historyPane)
      assert.ok(!liveReveal.after.clippedAbove, `history-capable top still clipped: ${JSON.stringify(liveReveal)}`)
      assert.ok(liveReveal.after.row0Top >= liveReveal.after.viewTop - 2, `history-capable row0 still above viewport: ${JSON.stringify(liveReveal)}`)
      assert.ok(liveReveal.after.scrollTop < liveReveal.before.scrollTop)
      await historyPane.locator('.terminal-viewport').hover({ force: true })
      if (historyReads() === beforeHistory) await history.page.mouse.wheel(0, -240)
      await expect(historyPane.getByRole('button', { name: '返回实时' })).toBeVisible()
      await expect(historyPane.locator('.xterm-rows > div').first()).toContainText('history line')
      const firstHistoryRow = () => historyPane.locator('.xterm-rows > div').first().innerText()
      const historyViewport = historyPane.locator('.terminal-viewport')
      await historyViewport.evaluate((el) => { el.scrollTop = 0 })
      await historyViewport.hover({ force: true })
      let historyAtStart = await firstHistoryRow()
      if (historyAtStart === 'history line 0') {
        await historyViewport.evaluate((el) => { el.scrollTop = el.scrollHeight })
        await history.page.mouse.wheel(0, 300)
        await expect.poll(firstHistoryRow).not.toBe('history line 0')
        historyAtStart = await firstHistoryRow()
        await historyViewport.evaluate((el) => { el.scrollTop = 0 })
      }
      await historyViewport.hover({ force: true })
      await history.page.mouse.wheel(0, -240)
      await expect.poll(firstHistoryRow).not.toBe(historyAtStart)
      const historyAfterUp = await firstHistoryRow()
      await historyViewport.evaluate((el) => { el.scrollTop = el.scrollHeight })
      await history.page.mouse.wheel(0, 180)
      await expect.poll(firstHistoryRow).not.toBe(historyAfterUp)
      await screenshot(history.page, `${name}-wheel-history-top`)
      assert.deepEqual(history.errors, [])
      await history.context.close()

      const roundtrip = await fixture(browser, { viewport: { width: 1440, height: 900 } })
      await expect(roundtrip.page.locator('.composer')).toHaveCount(0)
      await roundtrip.page.setViewportSize({ width: 390, height: 844 })
      await expect(roundtrip.page.locator('.composer')).toBeVisible()
      await expect(roundtrip.page.locator('.terminal-pane')).toHaveCount(1)
      await roundtrip.page.setViewportSize({ width: 1440, height: 900 })
      await expect(roundtrip.page.locator('.composer')).toHaveCount(0)
      await roundtrip.page.getByRole('button', { name: '本地输入框' }).click()
      await expect(roundtrip.page.locator('.composer')).toBeVisible()
      await roundtrip.page.setViewportSize({ width: 390, height: 844 })
      await expect(roundtrip.page.locator('.composer')).toBeVisible()
      await roundtrip.page.setViewportSize({ width: 1440, height: 900 })
      await expect(roundtrip.page.locator('.composer')).toBeVisible()
      await roundtrip.page.getByRole('textbox', { name: '本地输入内容' }).fill('keep-desktop-draft')
      await roundtrip.page.getByRole('button', { name: '发送', exact: true }).click()
      await expect.poll(() => roundtrip.messages.filter((item) => item.t === 'call' && item.method === 'pane.send_input').length).toBe(1)
      assert.deepEqual(roundtrip.errors, [])
      await roundtrip.context.close()

      const mobile = await fixture(browser, { viewport: { width: 390, height: 844 }, hasTouch: true, isMobile: name !== 'firefox', deviceScaleFactor: 2 })
      await expect(mobile.page.locator('.terminal-pane')).toHaveCount(1)
      await expect(mobile.page.getByRole('textbox', { name: '本地输入内容' })).toBeVisible()
      await mobile.page.getByRole('textbox', { name: '本地输入内容' }).fill('mobile-once')
      await mobile.page.getByRole('button', { name: '发送', exact: true }).click()
      await expect.poll(() => mobile.messages.filter((item) => item.t === 'call' && item.method === 'pane.send_input').length).toBe(1)
      await mobile.page.setViewportSize({ width: 844, height: 390 })
      await expect(mobile.page.locator('.terminal-pane')).toHaveCount(1)
      await expect(mobile.page.getByRole('textbox', { name: '本地输入内容' })).toBeVisible()
      assert.deepEqual(mobile.errors, [])
      await mobile.context.close()

      console.log(`${name}: desktop fixed 14px 295-col split, fit toggle, observed frames, typing, compact isolation, mobile composer passed`)
    } finally { await browser.close() }
  }
} finally { await new Promise((done) => server.close(done)) }
