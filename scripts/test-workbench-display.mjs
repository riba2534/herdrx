#!/usr/bin/env node
// Actual React/xterm/browser layout with synthetic HTTP/WebSocket data. Never
// connects to the user's Herdr or website. Run after `make web-build`.
import assert from 'node:assert/strict'
import { createServer } from 'node:http'
import { readFile, mkdir } from 'node:fs/promises'
import { fileURLToPath } from 'node:url'
import { join } from 'node:path'
import { chromium, firefox, webkit, expect } from '../web/node_modules/@playwright/test/index.mjs'

const dist = fileURLToPath(new URL('../web/dist/', import.meta.url))
const host = { id: 'display-test', name: '测试主机', transport: 'ssh' }
const snapshot = {
  protocol: 1, version: 'test', focused_workspace_id: 'w1', focused_tab_id: 't1', focused_pane_id: 'p1',
  workspaces: [{ workspace_id: 'w1', active_tab_id: 't1', label: '显示与交互测试', number: 1, pane_count: 2, tab_count: 1, agent_status: 'working' }],
  tabs: [{ workspace_id: 'w1', tab_id: 't1', label: '开发终端', number: 1, pane_count: 2, agent_status: 'working' }],
  panes: [1, 2].map((i) => ({ workspace_id: 'w1', tab_id: 't1', pane_id: `p${i}`, label: `终端 ${i}`, cwd: '/workspace/example', terminal_id: `term${i}`, revision: 1, agent_status: 'working' })),
  layouts: [{ workspace_id: 'w1', tab_id: 't1', area: { x: 0, y: 0, width: 160, height: 40 }, focused_pane_id: 'p1', splits: [], zoomed: false, panes: [1, 2].map((i) => ({ pane_id: `p${i}`, rect: { x: (i - 1) * 80, y: 0, width: 80, height: 40 } })) }],
  agents: [],
}
const ansi = Array.from({ length: 40 }, (_, i) => `\x1b[${i + 1};1H\x1b[0m${String(i + 1).padStart(2)}  ${i % 3 === 0 ? '\x1b[38;2;142;192;170m终端（Terminal），中文标点与对齐。' : 'const status = "ready"; // display settings'}\x1b[0m\x1b[${i + 1};70H\x1b[48;2;38;38;38m RIGHT EDGE`).join('') + '\x1b[0m\x1b[40;7H'
const server = createServer(async (req, res) => {
  const path = new URL(req.url, 'http://localhost').pathname
  const json = path === '/api/bootstrap/status' ? { required: false } : path === '/api/me' ? { user: { id: 'display-user', email: 'display@example.test', display_name: 'Display', role: 'admin' }, csrf_token: 'display-fixture', session_id: 'display-session' } : path === '/api/hosts/' ? { hosts: [host] } : path === '/api/hosts/display-test/' ? { host } : null
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

async function fixture(browser, options, fixtureSnapshot = snapshot) {
  fixtureSnapshot = structuredClone(fixtureSnapshot)
  const context = await browser.newContext(options)
  const page = await context.newPage()
  const messages = [], errors = []
  let closePane
  page.on('pageerror', (error) => errors.push(error.message))
  await page.routeWebSocket('**/api/hosts/*/ws', (ws) => {
    let streamID = 0
    const paneStreams = new Map()
    const sizes = new Map()
    closePane = (paneID, reason) => {
      assert.ok(paneStreams.has(paneID), 'fixture pane has no terminal stream')
      ws.send(JSON.stringify({ t: 'terminal.closed', stream_id: paneStreams.get(paneID), reason }))
    }
    const responsiveANSI = (cols, rows) => '\x1b[2J' + Array.from({ length: rows }, (_, i) => `\x1b[${i + 1};1H${i === 0 ? 'Terminal 自适应' : '中文内容'.repeat(Math.max(1, Math.floor((cols - 6) / 8)))}\x1b[${i + 1};${cols - 2}HEND`).join('') + '\x1b[H'
    const emitFrame = (id, text) => {
      const size = sizes.get(id) || { cols: 80, rows: 40 }
      const bytes = Buffer.from(text)
      const frame = Buffer.alloc(20 + bytes.length)
      frame.set([0x74, 1, 1, 1]); frame.writeUInt32LE(id, 4); frame.writeBigUInt64LE(1n, 8)
      frame.writeUInt16LE(size.cols, 16); frame.writeUInt16LE(size.rows, 18); bytes.copy(frame, 20)
      ws.send(frame)
    }
    ws.onMessage((raw) => {
      if (typeof raw !== 'string') {
        const data = Buffer.from(raw), id = data.readUInt32LE(4)
        const size = data[2] === 4 ? { cols: data.readUInt16LE(16), rows: data.readUInt16LE(18) } : null
        messages.push({ op: data[2], stream_id: id, ...size })
        if (size && sizes.get(id)?.responsive) { sizes.set(id, { ...size, responsive: true }); emitFrame(id, responsiveANSI(size.cols, size.rows)) }
        return
      }
      const message = JSON.parse(raw)
      messages.push(message)
      if (message.t === 'hello') {
        ws.send(JSON.stringify({ t: 'server_info' }))
        ws.send(JSON.stringify({ t: 'conn', state: 'ready' }))
        ws.send(JSON.stringify({ t: 'snapshot', snapshot: fixtureSnapshot }))
      } else if (message.t === 'terminal.open') {
        const id = ++streamID
        paneStreams.set(message.pane_id, id)
        sizes.set(id, { cols: message.cols, rows: message.rows, responsive: message.responsive })
        ws.send(JSON.stringify({ t: 'terminal.opened', id: message.id, stream_id: id }))
        emitFrame(id, message.responsive ? responsiveANSI(message.cols, message.rows) : ansi)
      } else if (message.t === 'call') {
        if (message.method === 'workspace.create') {
          const workspace = { ...fixtureSnapshot.workspaces[0], workspace_id: 'created-workspace', active_tab_id: 'created-tab', number: 2, label: '新建工作区', pane_count: 1, tab_count: 1 }
          const tab = { ...fixtureSnapshot.tabs[0], tab_id: 'created-tab', workspace_id: workspace.workspace_id, number: 1, label: '新标签页', pane_count: 1 }
          const root_pane = { ...fixtureSnapshot.panes[0], pane_id: 'created-pane', workspace_id: workspace.workspace_id, tab_id: tab.tab_id }
          fixtureSnapshot.workspaces.push(workspace); fixtureSnapshot.tabs.push(tab); fixtureSnapshot.panes.push(root_pane)
          fixtureSnapshot.layouts.push({ ...fixtureSnapshot.layouts[0], tab_id: tab.tab_id, workspace_id: workspace.workspace_id, focused_pane_id: root_pane.pane_id, panes: [{ pane_id: root_pane.pane_id, rect: { x: 0, y: 0, width: 80, height: 40 } }] })
          ws.send(JSON.stringify({ t: 'result', id: message.id, result: { workspace, tab, root_pane } }))
          // Exercise the real ordering: the creation result arrives before the
          // refreshed snapshot, so selection must not revert to the old pane.
          setTimeout(() => ws.send(JSON.stringify({ t: 'snapshot', snapshot: fixtureSnapshot })), 200)
          return
        }
        if (message.method === 'terminal.scroll') emitFrame(message.params.stream_id, `\x1b[2J\x1b[HNative wheel ${message.params.lines}`)
        ws.send(JSON.stringify({ t: 'result', id: message.id, result: message.method === 'pane.read' ? { read: { text: Array.from({ length: 100 }, (_, i) => `history line ${i}`).join('\n') } } : {} }))
      }
    })
  })
  await page.goto(base + '/h/display-test')
  await expect(page.locator('.xterm-rows').first()).toContainText('Terminal')
  await expect(page.getByRole('img', { name: '可输入', exact: true }).first()).toBeVisible()
  return { context, page, messages, errors, closePane: (paneID, reason) => closePane(paneID, reason) }
}

async function assertTerminalRecovery(f, paneID, index = 0) {
  const pane = f.page.locator('.terminal-pane').nth(index)
  const terminal = await pane.locator('.xterm').elementHandle()
  const viewportBefore = await pane.locator('.terminal-viewport').boundingBox()
  const before = f.messages.length
  f.closePane(paneID, '远程终端观察进程已退出，请检查 Herdr 后重试。' + '远程路径信息/'.repeat(35))
  await expect(pane.getByRole('alert')).toContainText('终端连接已关闭')
  await expect(pane.getByRole('button', { name: '聚焦终端输入' })).toHaveCount(0)
  await assertContained(f.page, ['.terminal-connection-feedback', '.terminal-connection-feedback button'])
  assert.deepEqual(await pane.locator('.terminal-viewport').boundingBox(), viewportBefore, 'terminal error changed terminal geometry')
  await pane.locator('.xterm-helper-textarea').focus()
  await f.page.keyboard.type('discarded while closed')
  assert.equal(f.messages.slice(before).filter(m => m.op === 3 || m.t === 'terminal.open' || m.method === 'pane.send_text').length, 0, 'closed stream sent input or retried automatically')
  await pane.getByRole('button', { name: '重连终端' }).click()
  await expect(pane.getByRole('alert')).toHaveCount(0)
  await expect(pane.getByRole('button', { name: '聚焦终端输入' })).toBeVisible()
  await expect(pane.locator('.xterm-rows')).toContainText('Terminal')
  assert.equal(f.messages.slice(before).filter(m => m.t === 'terminal.open').length, 1)
  assert.equal(f.messages.slice(before).filter(m => m.op === 3 || m.method === 'pane.send_text').length, 0, 'retry replayed input')
  assert.ok(await terminal.evaluate(el => el.isConnected), 'retry replaced the terminal DOM')
}

async function setSlider(page, name, value) {
  const slider = page.getByRole('slider', { name, exact: true })
  const { min, step } = await slider.evaluate((el) => ({ min: Number(el.min), step: Number(el.step) }))
  await slider.press('Home')
  for (let i = 0; i < Math.round((value - min) / step); i++) await slider.press('ArrowRight')
}

async function metrics(page) {
  return page.locator('.terminal-viewport').first().evaluate((viewport) => {
    const screen = viewport.querySelector('.xterm-screen').getBoundingClientRect()
    const row = viewport.querySelector('.xterm-rows > div').getBoundingClientRect()
    return { width: viewport.clientWidth, height: viewport.clientHeight, contentWidth: viewport.scrollWidth, contentHeight: viewport.scrollHeight, screenWidth: screen.width, screenHeight: screen.height, font: parseFloat(getComputedStyle(viewport.querySelector('.xterm-rows')).fontSize), rowHeight: row.height }
  })
}

async function assertContained(page, selectors) {
  for (const selector of selectors) {
    const boxes = await page.locator(selector).evaluateAll((elements) => elements.filter((el) => el.getClientRects().length).map((el) => { const r = el.getBoundingClientRect(); return { left: r.left, top: r.top, right: r.right, bottom: r.bottom } }))
    const viewport = page.viewportSize()
    for (const box of boxes) {
      assert.ok(box.left >= -1 && box.right <= viewport.width + 1 && box.top >= -1 && box.bottom <= viewport.height + 1, `${selector} outside viewport ${JSON.stringify({ viewport, box })}`)
    }
  }
  assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth + 1), 'page has horizontal overflow')
}

async function screenshot(page, name) {
  if (!process.env.HERDRX_DISPLAY_SCREENSHOTS) return
  await mkdir(process.env.HERDRX_DISPLAY_SCREENSHOTS, { recursive: true })
  await page.screenshot({ path: join(process.env.HERDRX_DISPLAY_SCREENSHOTS, name + '.png') })
}

async function assertMergedHeaders(page, expectedHeight) {
  await expect(page.locator('.workbench-main > .display-toolbar')).toHaveCount(0)
  for (const pane of await page.locator('.terminal-pane').all()) {
    const header = pane.locator('.terminal-titlebar')
    const box = await header.boundingBox()
    assert.equal(box.height, expectedHeight, 'terminal header grew beyond one row')
    const content = await pane.locator('.terminal-viewport').boundingBox()
    assert.ok(Math.abs(content.y - box.y - box.height) <= 1, 'extra row remains above terminal')
    const children = await header.locator('button, .terminal-title').evaluateAll((els) => els.filter((el) => el.getClientRects().length).map((el) => { const r = el.getBoundingClientRect(); return { x: r.x, y: r.y, right: r.right, bottom: r.bottom } }))
    for (const child of children) assert.ok(child.x >= box.x - 1 && child.right <= box.x + box.width + 1 && child.y >= box.y - 1 && child.bottom <= box.y + box.height + 1, `control escaped its pane header: ${JSON.stringify({ box, child })}`)
  }
}

async function touchAndKeyboardChecks(context, page) {
  const cdp = await context.newCDPSession(page)
  const viewport = page.locator('.terminal-viewport')
  const box = await viewport.boundingBox()
  await viewport.evaluate((el) => { el.scrollLeft = 0 })
  const y = box.y + Math.min(160, box.height / 2)
  await cdp.send('Input.dispatchTouchEvent', { type: 'touchStart', touchPoints: [{ x: box.x + box.width - 40, y, id: 1 }] })
  for (let i = 1; i <= 8; i++) {
    await cdp.send('Input.dispatchTouchEvent', { type: 'touchMove', touchPoints: [{ x: box.x + box.width - 40 - i * 25, y, id: 1 }] })
  }
  await cdp.send('Input.dispatchTouchEvent', { type: 'touchEnd', touchPoints: [] })
  await expect.poll(() => viewport.evaluate((el) => el.scrollLeft)).toBeGreaterThan(40)
  await page.getByRole('button', { name: '聚焦终端输入' }).click()
  await expect(page.locator('.xterm-helper-textarea')).toBeFocused()
  await page.getByRole('button', { name: 'Enter', exact: true }).click()
  await expect(page.locator('.xterm-helper-textarea')).toBeFocused()
  await expect.poll(() => viewport.evaluate((el) => el.scrollLeft)).toBeLessThan(100)

  // Exercise the browser's visual-viewport keyboard event contract separately
  // from a viewport resize; desktop automation cannot open an OS keyboard.
  await page.evaluate(() => {
    Object.defineProperty(window.visualViewport, 'height', { configurable: true, value: 360 })
    window.visualViewport.dispatchEvent(new Event('resize'))
  })
  await expect.poll(() => page.locator('.keybar').evaluate((el) => el.getBoundingClientRect().bottom)).toBe(360)
  await expect.poll(() => page.getByRole('button', { name: '发送', exact: true }).evaluate((el) => el.getBoundingClientRect().bottom)).toBeLessThanOrEqual(360)
  await expect(page.locator('.workbench')).toHaveClass(/workbench-short/)
  await page.getByRole('button', { name: '工作台设置', exact: true }).click()
  const dialog = page.getByRole('dialog', { name: '工作台设置' })
  assert.ok((await dialog.boundingBox()).height <= 328)
  await page.getByRole('button', { name: '完成', exact: true }).click()
  await page.evaluate(() => { delete window.visualViewport.height; window.visualViewport.dispatchEvent(new Event('resize')) })
  await cdp.send('Emulation.setPageScaleFactor', { pageScaleFactor: 1.5 })
  await expect.poll(() => page.evaluate(() => window.visualViewport.scale)).toBe(1.5)
  assert.equal(await page.locator('.workbench').evaluate((el) => el.getBoundingClientRect().height), page.viewportSize().height, 'native pinch unexpectedly reflowed the terminal')
  await cdp.send('Emulation.setPageScaleFactor', { pageScaleFactor: 1 })
  await cdp.detach()
}

const engines = process.env.HERDRX_TEST_ENGINES?.split(',') || ['chromium']
try {
  for (const name of engines) {
    const browser = await ({ chromium, firefox, webkit })[name].launch({ headless: true, ...(name === 'chromium' && process.env.HERDRX_TEST_CHROMIUM ? { executablePath: process.env.HERDRX_TEST_CHROMIUM } : {}) })
    try {
      const labels = structuredClone(snapshot)
      labels.workspaces[0].label = 'workspace-with-a-very-long-name-工作区名称应完整展示'
      labels.agents = [{ pane_id: 'p1', workspace_id: 'w1', tab_id: 't1', agent: 'codex', name: 'Agent-with-a-long-name-中文测试', agent_status: 'blocked' }]
      const sidebar = await fixture(browser, { viewport: { width: 1024, height: 768 } }, labels)
      await expect(sidebar.page.getByRole('button', { name: '新建工作区', exact: true })).toBeVisible()
      for (const selector of ['.workspace-row strong', '.agent-meta strong', '.agent-meta small', '.agent-state']) {
        const clipped = await sidebar.page.locator(selector).evaluateAll(els => els.some(el => {
          const box = el.getBoundingClientRect(), row = el.closest('.sidebar-row').getBoundingClientRect()
          return el.scrollWidth > el.clientWidth + 1 || el.scrollHeight > el.clientHeight + 1 || box.right > row.right + 1
        }))
        assert.equal(clipped, false, `sidebar text clipped: ${selector}`)
      }
      await screenshot(sidebar.page, `${name}-sidebar-long-labels`)
      await sidebar.page.getByRole('button', { name: '新建工作区', exact: true }).click()
      await expect.poll(() => sidebar.messages.filter(m => m.t === 'terminal.open').at(-1)?.pane_id).toBe('created-pane')
      await sidebar.context.close()
      const desktop = await fixture(browser, { viewport: { width: 1440, height: 900 }, deviceScaleFactor: 2 })
      const { page, messages } = desktop
      const initial = await metrics(page)
      assert.ok(initial.screenWidth <= initial.width && initial.screenHeight <= initial.height)
      await assertMergedHeaders(page, 32)
      await expect(page.locator('.terminal-pane-active > .terminal-titlebar > .display-toolbar')).toHaveCount(1)
      const originalTerminals = await page.locator('.terminal-host > .xterm').elementHandles()
      const originalViewports = await page.locator('.terminal-viewport').evaluateAll((els) => els.map((el) => { const r = el.getBoundingClientRect(); return { x: r.x, y: r.y, width: r.width, height: r.height } }))
      const focusStart = messages.length
      await page.locator('.terminal-pane').nth(1).locator('.terminal-title').click()
      await expect(page.locator('.terminal-pane-active .terminal-title')).toContainText('终端 2')
      await expect(page.locator('.terminal-pane-active > .terminal-titlebar > .display-toolbar')).toHaveCount(1)
      await page.locator('.terminal-pane').first().locator('.terminal-title').click()
      await expect(page.locator('.terminal-pane-active .terminal-title')).toContainText('终端 1')
      assert.deepEqual(await page.locator('.terminal-viewport').evaluateAll((els) => els.map((el) => { const r = el.getBoundingClientRect(); return { x: r.x, y: r.y, width: r.width, height: r.height } })), originalViewports, 'pane focus moved or resized terminal content')
      for (const terminal of originalTerminals) assert.ok(await terminal.evaluate((el) => el.isConnected), 'pane focus replaced the terminal DOM')
      assert.equal(messages.slice(focusStart).filter((m) => m.t === 'terminal.open' || m.t === 'terminal.close' || m.op === 3 || m.op === 4).length, 0, 'moving display controls changed terminal streams or geometry')
      // Use actual wheel events over rendered terminal rows, including repeated
      // up/down gestures. Button-based history checks do not cover this path.
      const firstPane = page.locator('.terminal-pane').first()
      const firstRows = firstPane.locator('.xterm-rows > div')
      const firstHistoryRow = () => firstRows.first().innerText()
      await firstRows.nth(5).hover()
      await page.mouse.wheel(0, -120)
      await expect(firstPane.getByRole('button', { name: '返回实时' })).toBeVisible()
      await expect(firstRows.first()).toContainText('history line')
      await expect(firstRows.first()).toHaveText(/^history line \d+$/)
      const historyAtStart = await firstHistoryRow()
      await page.mouse.wheel(0, -120)
      await expect.poll(firstHistoryRow).not.toBe(historyAtStart)
      const historyAfterUp = await firstHistoryRow()
      await page.mouse.wheel(0, 60)
      await expect.poll(firstHistoryRow).not.toBe(historyAfterUp)
      await firstPane.getByRole('button', { name: '返回实时' }).click()
      await expect(firstPane.locator('.xterm-rows')).toContainText('Terminal')
      messages.length = 0
      const opens = messages.filter((m) => m.t === 'terminal.open').length
      await page.getByRole('button', { name: '调整终端字号' }).click()
      const dialog = page.getByRole('dialog', { name: '工作台设置' })
      const terminalBackground = await firstPane.evaluate((el) => getComputedStyle(el).backgroundColor)
      await page.getByRole('button', { name: '切换为浅色', exact: true }).click()
      await expect(page.locator('html')).toHaveAttribute('data-appearance', 'light')
      assert.equal(await firstPane.evaluate((el) => getComputedStyle(el).backgroundColor), terminalBackground, 'UI theme must preserve terminal colors')
      await screenshot(page, `${name}-settings-light`)
      await page.getByRole('button', { name: '切换为深色', exact: true }).click()
      assert.equal(messages.filter((m) => m.t === 'terminal.open' || m.t === 'terminal.close' || m.op === 3).length, 0, 'UI appearance changed terminal connection or source geometry')
      await setSlider(page, '终端字号', 22)
      await setSlider(page, '终端缩放', 150)
      await page.getByRole('button', { name: '完成', exact: true }).click()
      await expect.poll(async () => (await metrics(page)).font).toBe(33)
      assert.ok((await metrics(page)).contentWidth > initial.width)
      await page.getByRole('button', { name: '缩小终端', exact: true }).click()
      await expect.poll(async () => Math.abs((await metrics(page)).font - 30.8)).toBeLessThan(0.05)
      await page.getByRole('button', { name: '重置终端缩放' }).click()
      await expect.poll(async () => (await metrics(page)).font).toBe(22)
      await page.locator('.terminal-viewport').first().evaluate((el) => { el.scrollLeft = 0; el.scrollTop = 0 })
      await page.locator('.terminal-viewport').first().hover()
      await page.mouse.wheel(200, 12)
      await expect.poll(() => page.locator('.terminal-viewport').first().evaluate((el) => el.scrollLeft)).toBeGreaterThan(40)
      assert.equal(messages.filter((m) => m.t === 'terminal.open').length, opens, 'display changes reopened terminal')
      assert.equal(messages.filter((m) => m.op === 3 || m.op === 4 || m.t === 'terminal.close').length, 0, 'display changes sent input/resize/close')
      await page.locator('.terminal-viewport').first().evaluate((el) => { el.scrollLeft = el.scrollWidth; el.scrollTop = el.scrollHeight })
      assert.ok(await page.locator('.terminal-viewport').first().evaluate((el) => el.scrollLeft > 0 && el.scrollTop > 0), 'enlarged frame cannot be panned')
      await page.reload()
      await expect.poll(async () => (await metrics(page)).font).toBe(22)
      await page.getByRole('button', { name: '适应窗口', exact: true }).click()
      await expect.poll(async () => { const m = await metrics(page); return m.screenWidth <= m.width && m.screenHeight <= m.height }).toBe(true)
      await page.getByRole('button', { name: '工作台设置', exact: true }).click()
      await page.getByRole('button', { name: '关闭设置' }).focus()
      await page.keyboard.press('Shift+Tab')
      await expect(page.getByRole('button', { name: '完成', exact: true })).toBeFocused()
      await page.keyboard.press('Escape')
      await expect(dialog).toHaveCount(0)
      await expect(page.getByRole('button', { name: '工作台设置', exact: true })).toBeFocused()
      await screenshot(page, `${name}-desktop`)
      await assertContained(page, ['.hostbar', '.display-toolbar', '.terminal-viewport'])
      assert.deepEqual(desktop.errors, [])
      await desktop.context.close()

      const singleSnapshot = { ...snapshot, panes: [{ ...snapshot.panes[0], label: '开发终端', cwd: '/workspace/example' }], layouts: [{ ...snapshot.layouts[0], panes: [{ pane_id: 'p1', rect: { x: 0, y: 0, width: 160, height: 40 } }] }] }
      const single = await fixture(browser, { viewport: { width: 1440, height: 900 } }, singleSnapshot)
      await assertMergedHeaders(single.page, 32)
      const tabbar = await single.page.locator('.tabbar').boundingBox()
      assert.ok((await single.page.locator('.terminal-viewport').boundingBox()).y - tabbar.y - tabbar.height <= 34, 'single pane still reserves a separate display row')
      await screenshot(single.page, `${name}-single-pane`)
      await assertTerminalRecovery(single, 'p1')
      assert.deepEqual(single.errors, [])
      await single.context.close()

      const longSnapshot = { ...snapshot, panes: snapshot.panes.map((pane) => ({ ...pane, label: '很长的终端名称'.repeat(12), cwd: '/workspace/example/'.repeat(12) })) }
      const narrow = await fixture(browser, { viewport: { width: 900, height: 700 } }, longSnapshot)
      await assertMergedHeaders(narrow.page, 32)
      await expect(narrow.page.getByRole('button', { name: '缩小终端', exact: true })).toBeHidden()
      await narrow.page.getByRole('button', { name: '调整终端字号' }).click()
      await expect(narrow.page.getByRole('slider', { name: '终端缩放', exact: true })).toBeVisible()
      await narrow.page.getByRole('button', { name: '完成', exact: true }).click()
      await screenshot(narrow.page, `${name}-narrow-split`)
      assert.deepEqual(narrow.errors, [])
      await narrow.context.close()

      const nativeSnapshot = { ...snapshot, panes: snapshot.panes.map((pane) => ({ ...pane, scroll: { max_offset_from_bottom: 0, offset_from_bottom: 0, viewport_rows: 40 } })) }
      const native = await fixture(browser, { viewport: { width: 1440, height: 900 } }, nativeSnapshot)
      const nativePane = native.page.locator('.terminal-pane').first()
      await nativePane.locator('.xterm-rows > div').nth(5).hover()
      await native.page.mouse.wheel(0, -120)
      await expect(nativePane.locator('.xterm-rows')).toContainText('Native wheel -')
      await native.page.mouse.wheel(0, 120)
      await expect(nativePane.locator('.xterm-rows')).toContainText(/Native wheel [1-9]/)
      assert.equal(native.messages.filter((m) => m.method === 'pane.read').length, 0, 'fullscreen wheel incorrectly entered a history snapshot')
      assert.equal(native.messages.filter((m) => m.t === 'terminal.close' || m.op === 4 || m.op === 3).length, 0, 'wheel must use native routing, not guessed raw input or browser geometry')
      assert.deepEqual(native.errors, [])
      await native.context.close()

      // Reproduce the reported 295-column desktop pane at 479px. The mock
      // responds to resize exactly as the real PTY integration fixture does.
      const wide = structuredClone(snapshot)
      wide.panes = wide.panes.slice(0, 1)
      wide.layouts[0].panes = [{ pane_id: 'p1', rect: { x: 0, y: 0, width: 295, height: 40 } }]
      const reflow = await fixture(browser, { viewport: { width: 479, height: 847 }, hasTouch: true }, wide)
      const assertReflow = async () => {
        await expect.poll(async () => { const m = await metrics(reflow.page); return m.font === 14 && m.screenWidth <= m.width && m.screenHeight <= m.height }).toBe(true)
        const lastRow = reflow.page.locator('.xterm-rows > div').last()
        await expect(lastRow).toContainText('END')
        await expect.poll(() => lastRow.evaluate(row => {
          const edge = row.lastElementChild?.getBoundingClientRect()
          const viewport = row.closest('.terminal-viewport')?.getBoundingClientRect()
          return Boolean(row.textContent.includes('END') && edge && viewport && edge.width > 0 && edge.right <= viewport.right + 1)
        })).toBe(true)
      }
      await assertReflow()
      assert.equal(reflow.messages.filter(m => m.t === 'terminal.open').at(-1).responsive, true)
      assert.ok(reflow.messages.filter(m => m.t === 'terminal.open').at(-1).cols < 70)
      await screenshot(reflow.page, `${name}-295cols-responsive-479x847`)
      const openCount = reflow.messages.filter(m => m.t === 'terminal.open').length
      for (const [width, height] of [[390, 844], [320, 720], [700, 390], [479, 847]]) {
        await reflow.page.setViewportSize({ width, height })
        await assertReflow()
      }
      await reflow.page.evaluate(() => {
        Object.defineProperty(window.visualViewport, 'height', { configurable: true, value: 360 })
        window.visualViewport.dispatchEvent(new Event('resize'))
      })
      await expect.poll(() => reflow.page.locator('.keybar').evaluate(el => el.getBoundingClientRect().bottom)).toBe(360)
      await expect.poll(() => reflow.page.getByRole('button', { name: '发送', exact: true }).evaluate(el => el.getBoundingClientRect().bottom)).toBeLessThanOrEqual(360)
      await assertReflow()
      await expect.poll(() => reflow.messages.filter(m => m.op === 4).length).toBeGreaterThan(0)
      assert.equal(reflow.messages.filter(m => m.t === 'terminal.open').length, openCount, 'rotation or keyboard reopened the responsive terminal')
      assert.deepEqual(reflow.errors, [])
      await reflow.context.close()

      for (const [width, height, touch] of [[320, 720, true], [390, 844, true], [479, 847, true], [844, 390, true], [768, 1024, true], [1024, 768, true], [720, 450, false]]) {
        const fixtureOptions = { viewport: { width, height }, deviceScaleFactor: touch ? 2 : 1, hasTouch: touch, ...(name !== 'firefox' ? { isMobile: touch } : {}) }
        const f = await fixture(browser, fixtureOptions)
        const compact = width < 768 || (touch && width < 1024 && height <= 600)
        if (compact) {
          await f.page.evaluate(() => localStorage.setItem('herdrx.sidebar-open', 'false'))
          await f.page.reload()
          await expect(f.page.locator('.xterm-rows').first()).toContainText('Terminal')
          await expect(f.page.locator('.workbench')).not.toHaveClass(/workbench-sidebar-closed/)
        }
        await expect(f.page.locator('.terminal-pane')).toHaveCount(compact ? 1 : 2)
        await assertMergedHeaders(f.page, 44)
        if (compact) {
          assert.equal((await metrics(f.page)).font, 14, 'mobile default must remain readable')
          await expect(f.page.locator('.display-toolbar')).toHaveCount(0)
          await expect(f.page.locator('.mobile-tabs')).toBeVisible()
          await expect(f.page.getByRole('button', { name: '切换工作区或终端', exact: true })).toBeVisible()
          const terminalBox = await f.page.locator('.terminal-viewport').boundingBox()
          await expect(f.page.getByRole('region', { name: '本地输入' })).toBeVisible()
          await expect(f.page.getByRole('button', { name: '发送', exact: true })).toBeVisible()
          assert.ok(terminalBox.height >= (height < 500 ? 64 : 160), `mobile terminal was collapsed: ${JSON.stringify(terminalBox)}`)
          const keybar = await f.page.locator('.keybar').boundingBox()
          const composer = await f.page.locator('.composer').boundingBox()
          const send = await f.page.getByRole('button', { name: '发送', exact: true }).boundingBox()
          assert.ok(Math.abs(keybar.y + keybar.height - height) <= 1, 'keyboard toolbar must stay at the viewport bottom')
          assert.ok(composer.y + composer.height <= keybar.y + 1, 'composer covered the auxiliary keys')
          assert.ok(send.y + send.height <= height + 1 && send.x + send.width <= width + 1, 'send button was covered or overflowed')
          if (height >= 500) {
            await expect(f.page.getByRole('button', { name: '新建工作区', exact: true })).toBeVisible()
            await expect(f.page.getByRole('button', { name: '自适应', exact: true })).toHaveAttribute('aria-pressed', 'true')
            await expect.poll(async () => { const m = await metrics(f.page); return m.screenWidth <= m.width + 1 && m.screenHeight <= m.height + 1 }).toBe(true)
            await f.page.getByRole('button', { name: '自适应', exact: true }).click()
            await f.page.getByRole('button', { name: '原始画面', exact: true }).click()
            await expect.poll(async () => (await metrics(f.page)).font).toBe(14)
          }
        }
        await assertContained(f.page, ['.hostbar', '.display-toolbar', '.terminal-titlebar', '.terminal-viewport', '.keybar', '.composer', '.hostbar button', '.display-toolbar button', '.terminal-titlebar button', '.composer-send'])
        if (touch) {
          const sizes = await f.page.locator('.display-toolbar button, .terminal-titlebar button').evaluateAll((els) => els.filter((el) => el.getClientRects().length).map((el) => ({ width: el.getBoundingClientRect().width, height: el.getBoundingClientRect().height })))
          assert.ok(sizes.every((s) => s.height >= 44 && s.width >= 44), `touch target smaller than 44px: ${JSON.stringify(sizes)}`)
        }
        await screenshot(f.page, `${name}-${width}x${height}`)
        if (name === 'chromium' && width === 390) {
          await f.page.getByRole('button', { name: '自适应', exact: true }).click()
          await touchAndKeyboardChecks(f.context, f.page)
          await f.page.getByRole('button', { name: '原始画面', exact: true }).click()
        }
        await f.page.getByRole('button', { name: '工作台设置', exact: true }).click()
        await expect(f.page.getByRole('slider', { name: '终端字号', exact: true })).toBeVisible()
        await setSlider(f.page, '终端字号', 18)
        await f.page.getByRole('button', { name: '完成', exact: true }).click()
        await expect.poll(async () => (await metrics(f.page)).font).toBe(18)
        await f.page.reload()
        await expect.poll(async () => (await metrics(f.page)).font).toBe(18)
        if (compact) {
          await f.page.getByRole('button', { name: '切换工作区或终端', exact: true }).click()
          await f.page.locator('.switcher button[aria-pressed]').filter({ hasText: '终端 2' }).click()
          await expect(f.page.locator('.terminal-title')).toContainText('终端 2')
          await f.page.getByRole('button', { name: '终端操作', exact: true }).click()
          await expect(f.page.getByRole('menu')).toBeVisible()
          await f.page.keyboard.press('Escape')
          await f.page.getByRole('button', { name: '查看终端历史' }).click()
          await expect(f.page.getByRole('toolbar', { name: '终端历史导航' })).toBeVisible()
          await expect(f.page.locator('.xterm-rows')).toContainText('history line')
          await f.page.getByRole('button', { name: '上一屏', exact: true }).click()
          await f.page.getByRole('button', { name: '返回实时', exact: true }).click()
          await expect(f.page.locator('.xterm-rows')).toContainText('Terminal')
        }
        await assertTerminalRecovery(f, compact ? 'p2' : 'p1')
        if (compact) {
          // The switcher remains available in landscape and with the keyboard.
          await f.page.getByRole('button', { name: '切换工作区或终端', exact: true }).click()
          await f.page.locator('.switcher').getByRole('button', { name: '新建工作区', exact: true }).click()
          await expect.poll(() => f.messages.filter(m => m.t === 'terminal.open').at(-1)?.pane_id).toBe('created-pane')
          await expect(f.page.locator('.mobile-tabs .mobile-tab-active')).toContainText('新标签页')
        }
        assert.deepEqual(f.errors, [])
        await f.context.close()
      }
      console.log(`${name}: merged header, single pane and narrow splits, stable terminals on pane focus, font/zoom persistence, native application wheel, repeated history wheel, LF snapshots, unchanged terminal connection/grid, panning, fit, keyboard dialog, phone portrait/landscape, tablet, 200% equivalent layout and touch controls passed`)
    } finally { await browser.close() }
  }
} finally { await new Promise((done) => server.close(done)) }
