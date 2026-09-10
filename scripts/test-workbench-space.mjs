#!/usr/bin/env node
// Real React/xterm layout with isolated HTTP/WebSocket fixtures. No Herdr connection.
// Run after `make web-build`; HERDRX_SPACE_BASELINE=1 records old-layout geometry.
import assert from 'node:assert/strict'
import { createServer } from 'node:http'
import { readFile, mkdir, writeFile } from 'node:fs/promises'
import { fileURLToPath } from 'node:url'
import { join } from 'node:path'
import { chromium, firefox, webkit, expect } from '../web/node_modules/@playwright/test/index.mjs'

const dist = fileURLToPath(new URL('../web/dist/', import.meta.url))
const artifacts = process.env.HERDRX_SPACE_ARTIFACTS
const baseline = process.env.HERDRX_SPACE_BASELINE === '1'
const hosts = [
  { id: 'space-test', name: '布局测试主机', transport: 'ssh' },
  { id: 'space-other', name: '另一台布局主机', transport: 'ssh' },
]
const snapshot = {
  protocol: 1, version: 'test', focused_workspace_id: 'w1', focused_tab_id: 't1', focused_pane_id: 'p1',
  workspaces: [
    { workspace_id: 'w1', active_tab_id: 't1', label: '开发工作区', number: 1, pane_count: 3, tab_count: 2, agent_status: 'working' },
    { workspace_id: 'w2', active_tab_id: 't3', label: '检查工作区', number: 2, pane_count: 1, tab_count: 1, agent_status: 'idle' },
  ],
  tabs: [
    { workspace_id: 'w1', tab_id: 't1', label: '开发标签', number: 1, pane_count: 2, agent_status: 'working' },
    { workspace_id: 'w1', tab_id: 't2', label: '验证标签', number: 2, pane_count: 1, agent_status: 'idle' },
    { workspace_id: 'w2', tab_id: 't3', label: '检查标签', number: 1, pane_count: 1, agent_status: 'idle' },
  ],
  panes: [
    { workspace_id: 'w1', tab_id: 't1', pane_id: 'p1', label: '上方终端' },
    { workspace_id: 'w1', tab_id: 't1', pane_id: 'p2', label: '下方终端' },
    { workspace_id: 'w1', tab_id: 't2', pane_id: 'p3', label: '验证终端' },
    { workspace_id: 'w2', tab_id: 't3', pane_id: 'p4', label: '检查终端' },
  ].map((pane) => ({ ...pane, cwd: '/workspace/example', terminal_id: `term-${pane.pane_id}`, revision: 1, agent_status: 'working' })),
  layouts: [
    { workspace_id: 'w1', tab_id: 't1', focused_pane_id: 'p1', area: { x: 0, y: 0, width: 160, height: 80 }, panes: [{ pane_id: 'p1', rect: { x: 0, y: 0, width: 160, height: 39 } }, { pane_id: 'p2', rect: { x: 0, y: 40, width: 160, height: 40 } }] },
    { workspace_id: 'w1', tab_id: 't2', focused_pane_id: 'p3', area: { x: 0, y: 0, width: 80, height: 40 }, panes: [{ pane_id: 'p3', rect: { x: 0, y: 0, width: 80, height: 40 } }] },
    { workspace_id: 'w2', tab_id: 't3', focused_pane_id: 'p4', area: { x: 0, y: 0, width: 80, height: 40 }, panes: [{ pane_id: 'p4', rect: { x: 0, y: 0, width: 80, height: 40 } }] },
  ].map((layout) => ({ ...layout, splits: [], zoomed: false })),
  agents: [],
}
const server = createServer(async (req, res) => {
  const path = new URL(req.url, 'http://localhost').pathname
  const selectedHost = hosts.find((host) => path === `/api/hosts/${host.id}/`)
  const json = path === '/api/bootstrap/status' ? { required: false }
    : path === '/api/me' ? { user: { id: 'space-user', email: 'space@example.test', display_name: 'Space', role: 'admin' }, csrf_token: 'space-fixture', session_id: 'space-session' }
    : path === '/api/hosts/' ? { hosts } : selectedHost ? { host: selectedHost } : null
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
const results = []

async function fixture(browser, options, fixtureSnapshot = snapshot) {
  const context = await browser.newContext(options)
  const page = await context.newPage()
  const messages = [], errors = []
  page.on('pageerror', (error) => errors.push(error.message))
  await page.routeWebSocket('**/api/hosts/*/ws', (ws) => {
    let streamID = 0
    const streams = new Map()
    const emit = (id) => {
      const { cols, rows, paneID } = streams.get(id)
      const text = '\x1b[2J' + Array.from({ length: rows }, (_, i) => `\x1b[${i + 1};1H${`Space ${paneID} ${String(i + 1).padStart(2)}  中文终端显示与布局 `}`.slice(0, cols)).join('') + '\x1b[H'
      const bytes = Buffer.from(text)
      const frame = Buffer.alloc(20 + bytes.length)
      frame.set([0x74, 1, 1, 1]); frame.writeUInt32LE(id, 4); frame.writeBigUInt64LE(1n, 8)
      frame.writeUInt16LE(cols, 16); frame.writeUInt16LE(rows, 18); bytes.copy(frame, 20)
      ws.send(frame)
    }
    ws.onMessage((raw) => {
      if (typeof raw !== 'string') {
        const data = Buffer.from(raw), id = data.readUInt32LE(4)
        messages.push({ op: data[2], stream_id: id, ...(data[2] === 3 ? { bytes: data.subarray(16).toString() } : {}) })
        if (data[2] === 4 && streams.has(id)) {
          streams.set(id, { ...streams.get(id), cols: data.readUInt16LE(16), rows: data.readUInt16LE(18) })
          emit(id)
        }
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
        streams.set(id, { cols: message.cols, rows: message.rows, paneID: message.pane_id })
        ws.send(JSON.stringify({ t: 'terminal.opened', id: message.id, stream_id: id }))
        emit(id)
      } else if (message.t === 'call') {
        ws.send(JSON.stringify({ t: 'result', id: message.id, result: {} }))
      }
    })
  })
  await page.goto(base + '/h/space-test')
  await expect(page.locator('.xterm-rows').first()).toContainText('Space')
  await page.waitForTimeout(150)
  return { context, page, messages, errors }
}

async function geometry(page) {
  return page.evaluate(() => {
    const box = (el) => { const r = el.getBoundingClientRect(); return { x: r.x, y: r.y, width: r.width, height: r.height, bottom: r.bottom, right: r.right } }
    const visible = (selector) => [...document.querySelectorAll(selector)].filter((el) => el.getClientRects().length).map(box)
    const bench = document.querySelector('.workbench')
    const topbar = document.querySelector('.mobile-topbar')
    const dock = document.querySelector('.workbench-dock')
    const topSafe = topbar ? parseFloat(getComputedStyle(topbar).paddingTop) || 0 : 0
    const bottomSafe = dock ? parseFloat(getComputedStyle(dock).paddingBottom) || 0 : 0
    return {
      viewport: { width: innerWidth, height: innerHeight, visualHeight: visualViewport.height },
      workbench: box(bench), hostbar: visible('.hostbar'), topbar: visible('.mobile-topbar'), mobileHeader: visible('.mobile-header'), mobileTabs: visible('.mobile-tabs'), paneChips: visible('.mobile-pane-chips'),
      titlebars: visible('.terminal-titlebar'), terminals: visible('.terminal-viewport'), panes: visible('.terminal-pane'), surface: visible('.terminal-surface'),
      composer: visible('.composer'), keybar: visible('.keybar'), dock: visible('.workbench-dock'),
      horizontalOverflow: document.documentElement.scrollWidth > innerWidth + 1,
      safeArea: { top: topSafe, bottom: bottomSafe },
    }
  })
}

async function record(page, label) {
  const value = await geometry(page)
  results.push({ label, ...value })
  if (artifacts) {
    await mkdir(artifacts, { recursive: true })
    await page.screenshot({ path: join(artifacts, label + '.png') })
  }
  return value
}

async function contained(page, selector) {
  const boxes = await page.locator(selector).evaluateAll((els) => els.filter((el) => el.getClientRects().length).map((el) => { const r = el.getBoundingClientRect(); return { x: r.x, y: r.y, right: r.right, bottom: r.bottom } }))
  const limit = await page.evaluate(() => ({ width: innerWidth, height: visualViewport.height }))
  for (const box of boxes) assert.ok(box.x >= -1 && box.right <= limit.width + 1 && box.y >= -1 && box.bottom <= limit.height + 1, `${selector} escapes viewport: ${JSON.stringify({ box, limit })}`)
}

async function assertMobileSpace(page, minimum = .8) {
  const g = await geometry(page)
  assert.equal(g.horizontalOverflow, false, 'workbench overflows horizontally')
  assert.equal(g.terminals.length, 1, 'mobile renders more than the selected pane')
  assert.equal(g.titlebars.length, 0, 'pane titlebar reserves space by default')
  assert.equal(g.mobileHeader.length + g.mobileTabs.length, 0, 'extra mobile navigation rows remain')
  assert.equal(g.keybar.length, 0, 'auxiliary keyboard is expanded by default')
  assert.equal(g.topbar.length, 1, 'missing unified mobile navigation')
  assert.ok(g.topbar[0].height - g.safeArea.top <= 45, `mobile navigation taller than one row: ${JSON.stringify(g.topbar)}`)
  assert.ok(g.composer[0].height <= 56, `empty/single-line composer grew: ${JSON.stringify(g.composer)}`)
  const chipHeight = g.paneChips[0]?.height || 0
  const available = g.workbench.height - g.safeArea.top - g.safeArea.bottom - chipHeight
  const ratio = g.terminals[0].height / available
  assert.ok(ratio >= minimum, `terminal only uses ${(100 * ratio).toFixed(1)}% of available height; minimum ${100 * minimum}%`)
  await contained(page, '.mobile-topbar button, .composer, .composer-send, .composer-input')
  return ratio
}

async function assertOverlayStable(page, toggle) {
  const before = await geometry(page)
  const terminals = await page.locator('.xterm').elementHandles()
  await toggle.click()
  await expect(page.locator('.terminal-titlebar:visible')).toHaveCount(1)
  const compact = await page.locator('.workbench-compact').count()
  if (!compact) await expect.poll(() => page.locator('.terminal-titlebar:visible').evaluate((el) => el.contains(document.activeElement))).toBe(true)
  await contained(page, '.terminal-titlebar, .terminal-titlebar button')
  assert.deepEqual((await geometry(page)).terminals, before.terminals, 'toolbar opening changed terminal geometry')
  for (let i = 0; i < terminals.length; i++) assert.ok(await terminals[i].evaluate((el) => el.isConnected), 'toolbar opening remounted xterm')
  await page.getByRole('button', { name: '收起终端工具', exact: true }).click()
  await expect(page.locator('.terminal-titlebar:visible')).toHaveCount(0)
  assert.deepEqual((await geometry(page)).terminals, before.terminals, 'toolbar closing changed terminal geometry')
  await expect(toggle).toBeFocused()
  await toggle.click()
  await page.getByRole('button', { name: '收起终端工具', exact: true }).focus()
  await page.keyboard.press('Escape')
  await expect(page.locator('.terminal-titlebar:visible')).toHaveCount(0)
  await expect(toggle).toBeFocused()
}

async function switchTo(page, title, name) {
  await page.getByRole('button', { name: '切换工作区或终端', exact: true }).click()
  const dialog = page.getByRole('dialog', { name: '切换工作区或终端', exact: true })
  await expect(dialog).toBeVisible()
  const group = dialog.locator('.switcher-group').filter({ has: page.getByRole('heading', { name: title, exact: true }) })
  await group.getByRole('button', { name: new RegExp(name) }).click()
  await expect(dialog).toHaveCount(0)
}

async function mobileNavigation(page) {
  await switchTo(page, '终端', '下方终端')
  await expect(page.locator('.xterm-rows')).toContainText('Space p2')
  await switchTo(page, '标签页', '验证标签')
  await expect(page.locator('.xterm-rows')).toContainText('Space p3')
  await switchTo(page, '工作区', '检查工作区')
  await expect(page.locator('.xterm-rows')).toContainText('Space p4')
  await switchTo(page, '工作区', '开发工作区')
  await page.getByRole('button', { name: '切换工作区或终端', exact: true }).click()
  await page.getByRole('dialog', { name: '切换工作区或终端', exact: true }).getByRole('link', { name: '另一台布局主机', exact: true }).click()
  await expect(page).toHaveURL(base + '/h/space-other')
  await page.getByRole('button', { name: '切换工作区或终端', exact: true }).click()
  await page.getByRole('dialog', { name: '切换工作区或终端', exact: true }).getByRole('link', { name: '布局测试主机', exact: true }).click()
  await expect(page).toHaveURL(base + '/h/space-test')
  await expect(page.locator('.xterm-rows')).toContainText('Space')
}

async function keyboardStable(page, engine, messages) {
  const box = page.getByRole('textbox', { name: '本地输入内容', exact: true })
  const draft = '布局变化保留中文 draft 🙂'
  await box.fill(draft)
  const terminal = await page.locator('.xterm').elementHandle()
  await page.evaluate(() => {
    Object.defineProperty(window.visualViewport, 'height', { configurable: true, value: 390 })
    window.visualViewport.dispatchEvent(new Event('resize'))
  })
  await expect.poll(() => page.locator('.workbench').evaluate((el) => el.getBoundingClientRect().height)).toBe(390)
  await assertMobileSpace(page, .65)
  await expect(box).toHaveValue(draft)
  await expect(page.locator('.terminal-pane')).toHaveClass(/terminal-pane-composer/)
  assert.ok(await terminal.evaluate((el) => el.isConnected), 'keyboard visibility remounted xterm')
  await record(page, `${engine}-keyboard-390-local-draft`)
  await page.evaluate(() => { delete window.visualViewport.height; window.visualViewport.dispatchEvent(new Event('resize')) })
  await expect.poll(() => page.locator('.workbench').evaluate((el) => el.getBoundingClientRect().height)).toBe(page.viewportSize().height)
  await expect(box).toHaveValue(draft)
  await expect(page.locator('.terminal-pane')).toHaveClass(/terminal-pane-composer/)
  assert.ok(await terminal.evaluate((el) => el.isConnected), 'keyboard dismissal remounted xterm')
  await page.getByRole('button', { name: '输入方式：本地输入', exact: true }).click()
  await page.getByRole('menuitemradio', { name: /^直接输入终端/ }).click()
  await expect(page.locator('.terminal-pane')).not.toHaveClass(/terminal-pane-composer/)
  await page.evaluate(() => {
    Object.defineProperty(window.visualViewport, 'height', { configurable: true, value: 390 })
    window.visualViewport.dispatchEvent(new Event('resize'))
  })
  await expect.poll(() => page.locator('.workbench').evaluate((el) => el.getBoundingClientRect().height)).toBe(390)
  await expect(page.locator('.terminal-pane')).not.toHaveClass(/terminal-pane-composer/)
  await expect(box).toHaveValue(draft)
  await page.evaluate(() => { delete window.visualViewport.height; window.visualViewport.dispatchEvent(new Event('resize')) })
  await expect.poll(() => page.locator('.workbench').evaluate((el) => el.getBoundingClientRect().height)).toBe(page.viewportSize().height)
  await expect(page.locator('.terminal-pane')).not.toHaveClass(/terminal-pane-composer/)
  await expect(box).toHaveValue(draft)
  await page.getByRole('button', { name: '输入方式：直接输入终端', exact: true }).click()
  await page.getByRole('menuitemradio', { name: /^直接输入终端/ }).click()
  await expect(page.locator('.xterm-helper-textarea')).toBeFocused()
  const beforeDirectInput = messages.length
  const marker = 'space-fixture-direct-input'
  await page.keyboard.insertText(marker)
  await expect.poll(() => messages.slice(beforeDirectInput).filter((m) => m.op === 3).map((m) => m.bytes).join('')).toBe(marker)
  const allowedInputFrames = messages.slice(beforeDirectInput).filter((m) => m.op === 3).length
  await expect(box).toHaveValue(draft)
  await page.getByRole('button', { name: '输入方式：直接输入终端', exact: true }).click()
  await page.getByRole('menuitemradio', { name: /^本地输入/ }).click()
  await expect(box).toHaveValue(draft)
  await expect(box).toBeFocused()
  return allowedInputFrames
}

try {
  for (const engine of process.env.HERDRX_TEST_ENGINES?.split(',') || ['chromium']) {
    const browser = await ({ chromium, firefox, webkit })[engine].launch({ headless: true, ...(engine === 'chromium' && process.env.HERDRX_TEST_CHROMIUM ? { executablePath: process.env.HERDRX_TEST_CHROMIUM } : {}) })
    try {
      for (const [width, height] of [[440, 956], [390, 844], [320, 720], [844, 390]]) {
        const f = await fixture(browser, { viewport: { width, height }, hasTouch: true, deviceScaleFactor: 2, ...(engine !== 'firefox' ? { isMobile: true } : {}) })
        await record(f.page, `${engine}-${baseline ? 'before' : 'after'}-mobile-${width}x${height}`)
        if (!baseline) {
          await assertMobileSpace(f.page, height < 480 ? .65 : .8)
          await assertOverlayStable(f.page, f.page.getByRole('button', { name: '终端工具', exact: true }))
          const keyToggle = f.page.getByRole('button', { name: '终端辅助键', exact: true })
          const input = f.page.getByRole('textbox', { name: '本地输入内容', exact: true })
          await input.focus()
          await keyToggle.click()
          await expect(f.page.getByRole('toolbar', { name: '终端辅助键', exact: true })).toBeVisible()
          if (height < 500) await expect(f.page.getByRole('region', { name: '本地输入' })).toHaveCount(0)
          else await expect(input).toBeFocused()
          await contained(f.page, '.keybar')
          await keyToggle.click()
          await expect(f.page.getByRole('toolbar', { name: '终端辅助键', exact: true })).toHaveCount(0)
          await mobileNavigation(f.page)
          const allowedInputFrames = width === 390 ? await keyboardStable(f.page, engine, f.messages) : 0
          if (engine === 'chromium' && width === 440) {
            // Browser CSS safe-area emulation, not a claim of physical PWA validation.
            const cdp = await f.context.newCDPSession(f.page)
            await cdp.send('Emulation.setSafeAreaInsetsOverride', { insets: { top: 62, bottom: 34, left: 0, right: 0 } })
            await expect.poll(async () => (await geometry(f.page)).safeArea).toEqual({ top: 62, bottom: 34 })
            await assertMobileSpace(f.page)
            await record(f.page, `${engine}-mobile-440x956-safe-area`)
            await cdp.detach()
          }
          assert.deepEqual(f.errors, [])
          assert.equal(f.messages.filter((m) => m.op === 3).length, allowedInputFrames, 'layout/navigation sent unexpected terminal input')
          assert.equal(f.messages.filter((m) => ['pane.send_input', 'pane.send_text', 'pane.send_keys'].includes(m.method)).length, 0, 'layout/navigation sent an input RPC')
        }
        await f.context.close()
      }
      for (const [width, height, direction] of [[1440, 900, 'vertical'], [2560, 1440, 'vertical'], [1440, 900, 'horizontal'], [2560, 1440, 'horizontal']]) {
        const desktopSnapshot = structuredClone(snapshot)
        if (direction === 'horizontal') Object.assign(desktopSnapshot.layouts[0], {
          area: { x: 0, y: 0, width: 161, height: 40 },
          panes: [{ pane_id: 'p1', rect: { x: 0, y: 0, width: 80, height: 40 } }, { pane_id: 'p2', rect: { x: 81, y: 0, width: 80, height: 40 } }],
        })
        const f = await fixture(browser, { viewport: { width, height } }, desktopSnapshot)
        const g = await record(f.page, `${engine}-${baseline ? 'before' : 'after'}-desktop-${width}x${height}-${direction}`)
        if (!baseline) {
          assert.equal(g.terminals.length, 2)
          assert.equal(g.titlebars.length, 0, 'desktop panes reserve titlebar rows')
          const terminals = [...g.terminals].sort((a, b) => direction === 'vertical' ? a.y - b.y : a.x - b.x)
          const gap = direction === 'vertical' ? terminals[1].y - terminals[0].bottom : terminals[1].x - terminals[0].right
          assert.ok(gap >= 0 && gap <= 1.5, `desktop divider is ${gap}px`)
          for (let i = 0; i < g.panes.length; i++) assert.ok(g.terminals[i].height >= g.panes[i].height - 2, 'desktop pane reserves non-terminal vertical space')
          await assertOverlayStable(f.page, f.page.locator('.pane-controls-toggle').first())
          assert.deepEqual(f.errors, [])
        }
        await f.context.close()
      }
    } finally { await browser.close() }
  }
  console.log(JSON.stringify(results.map((g) => {
    const availableHeight = g.workbench.height - g.safeArea.top - g.safeArea.bottom
    return {
      label: g.label, terminalHeight: g.terminals.map((t) => t.height), availableHeight,
      terminalPercent: g.terminals.length === 1 ? +(100 * g.terminals[0].height / availableHeight).toFixed(1) : null,
      topbars: [...g.hostbar, ...g.topbar, ...g.mobileHeader, ...g.mobileTabs, ...g.titlebars].map((b) => b.height),
      composerHeight: g.composer.map((c) => c.height), keybarHeight: g.keybar.map((k) => k.height),
    }
  }), null, 2))
  console.log(baseline ? 'Workbench baseline recorded' : 'Workbench space regression passed')
} finally {
  if (artifacts) { await mkdir(artifacts, { recursive: true }); await writeFile(join(artifacts, baseline ? 'baseline-metrics.json' : 'space-metrics.json'), JSON.stringify(results, null, 2)) }
  await new Promise((done) => server.close(done))
}
