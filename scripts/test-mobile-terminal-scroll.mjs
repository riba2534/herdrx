#!/usr/bin/env node
import { chooseOption, acceptConfirmation } from './browser-controls.mjs'
// Isolated browser data only; never connects to Herdr or an existing user pane.
// Chromium uses trusted CDP touch input. WebKit uses TouchEvent dispatch to
// check routing/cancellation; native iOS scrolling still needs a device check.
import assert from 'node:assert/strict'
import { createServer } from 'node:http'
import { readFile } from 'node:fs/promises'
import { fileURLToPath } from 'node:url'
import { join } from 'node:path'
import ts from '../web/node_modules/typescript/lib/typescript.js'
import { chromium, webkit, expect } from '../web/node_modules/@playwright/test/index.mjs'

const root = fileURLToPath(new URL('../', import.meta.url))
const module = ts.transpileModule(await readFile(join(root, 'web/src/lib/terminalTouch.ts'), 'utf8'), { compilerOptions: { target: ts.ScriptTarget.ES2022, module: ts.ModuleKind.ES2022 } }).outputText
const harness = `<!doctype html><meta name="viewport" content="width=device-width, initial-scale=1"><style>
body{margin:0}#viewport{margin:80px 10px;width:340px;height:500px;overflow:auto;overscroll-behavior:contain;background:#ddd}#content{width:700px;height:900px;background:repeating-linear-gradient(#ddd 0 19px,#aaa 20px 21px)}
</style><div id="viewport"><div id="content">Touch scroll fixture</div></div><script type="module">
import { attachTerminalTouch } from '/terminalTouch.js';
window.events=[];window.generation=1;window.selected=false;
attachTerminalTouch(document.querySelector('#viewport'),{onScrollPixels:(...args)=>window.events.push(args),getGeneration:()=>window.generation,hasSelection:()=>window.selected});window.ready=true;
</script>`
const host = { id: 'mobile-test', name: '触摸测试', transport: 'ssh' }
const snapshot = (history) => ({
  protocol: 1, version: 'test', focused_workspace_id: 'w1', focused_tab_id: 't1', focused_pane_id: 'p1',
  workspaces: [{ workspace_id: 'w1', active_tab_id: 't1', label: '隔离触摸测试', number: 1, pane_count: 1, tab_count: 1 }],
  tabs: [{ workspace_id: 'w1', tab_id: 't1', label: '终端', number: 1, pane_count: 1 }],
  panes: [{ workspace_id: 'w1', tab_id: 't1', pane_id: 'p1', label: '终端', cwd: '/workspace/test', terminal_id: 'term1', revision: 1, scroll: { offset_from_bottom: 0, max_offset_from_bottom: history ? 400 : 0, viewport_rows: 40 } }],
  layouts: [{ workspace_id: 'w1', tab_id: 't1', area: { x: 0, y: 0, width: 80, height: 40 }, focused_pane_id: 'p1', splits: [], zoomed: false, panes: [{ pane_id: 'p1', rect: { x: 0, y: 0, width: 80, height: 40 } }] }],
  agents: [],
})
const ansi = Array.from({ length: 40 }, (_, i) => `\x1b[${i + 1};1HMobile live ${i} 中文触摸测试`).join('')
const server = createServer(async (req, res) => {
  const path = new URL(req.url, 'http://localhost').pathname
  if (path === '/harness') { res.setHeader('content-type', 'text/html'); res.end(harness); return }
  if (path === '/terminalTouch.js') { res.setHeader('content-type', 'application/javascript'); res.end(module); return }
  const json = path === '/api/bootstrap/status' ? { required: false } : path === '/api/me' ? { user: { id: 'test-user', email: 'test@example.test', display_name: 'Test', role: 'admin' }, csrf_token: 'fixture', session_id: 'test-session' } : path === '/api/hosts/' ? { hosts: [host] } : path === '/api/hosts/mobile-test/' ? { host } : null
  if (json) { res.setHeader('content-type', 'application/json'); res.end(JSON.stringify(json)); return }
  if (path.startsWith('/api/')) { res.writeHead(404); res.end(); return }
  try {
    const file = path.startsWith('/assets/') || ['/boot.js', '/sw.js', '/manifest.webmanifest'].includes(path) ? path.slice(1) : 'index.html'
    res.setHeader('content-type', file.endsWith('.js') ? 'application/javascript' : file.endsWith('.css') ? 'text/css' : 'text/html')
    res.end(await readFile(join(root, 'web/dist', file)))
  } catch { res.writeHead(404); res.end() }
})
await new Promise((resolve) => server.listen(0, '127.0.0.1', resolve))
const base = `http://127.0.0.1:${server.address().port}`

async function touchInput(context, page, engine, selector) {
  const cdp = engine === 'chromium' ? await context.newCDPSession(page) : null
  const send = async (type, points) => {
    if (cdp) {
      await cdp.send('Input.dispatchTouchEvent', { type: { touchstart: 'touchStart', touchmove: 'touchMove', touchend: 'touchEnd', touchcancel: 'touchCancel' }[type], touchPoints: points.map(([x, y, id = 1]) => ({ x, y, id })) })
    } else {
      await page.locator(selector).evaluate((target, { type, points }) => {
        const touches = document.createTouchList(...points.map(([clientX, clientY, identifier = 1]) => document.createTouch(window, target, identifier, clientX, clientY, clientX, clientY)))
        target.dispatchEvent(new TouchEvent(type, { touches, targetTouches: touches, changedTouches: touches, bubbles: true, cancelable: true }))
      }, { type, points })
    }
  }
  const swipe = async (deltaY, deltaX = 0) => {
    const box = await page.locator(selector).boundingBox()
    const x = box.x + Math.min(box.width - 40, Math.max(40, box.width / 2 - deltaX / 2))
    const y = box.y + box.height / 2 - deltaY / 2
    await send('touchstart', [[x, y]])
    for (let i = 1; i <= 8; i++) {
      await send('touchmove', [[x + deltaX * i / 8, y + deltaY * i / 8]])
      await page.evaluate(() => new Promise(requestAnimationFrame))
    }
    await send('touchend', [])
  }
  return { send, swipe, cdp }
}

async function controllerChecks(browser, engine) {
  const context = await browser.newContext({ viewport: { width: 390, height: 844 }, hasTouch: true, isMobile: true })
  const page = await context.newPage()
  const errors = []
  page.on('pageerror', (error) => errors.push(error.message))
  try {
    await page.goto(base + '/harness')
    await page.waitForFunction(() => window.ready)
    const input = await touchInput(context, page, engine, '#viewport')
    await page.evaluate(() => { document.querySelector('#viewport').scrollTop = 40 })
    await input.swipe(100)
    await expect.poll(() => page.evaluate(() => Math.abs(window.events.reduce((sum, [pixels]) => sum + pixels, 0) + 54))).toBeLessThanOrEqual(1)
    await expect.poll(() => page.locator('#viewport').evaluate((el) => el.scrollTop)).toBe(0)
    await page.evaluate(() => { window.events = [] })
    await input.swipe(-100)
    await expect.poll(() => page.locator('#viewport').evaluate((el) => el.scrollTop)).toBe(94)
    assert.equal(await page.evaluate(() => window.events.length), 0, 'frame panning also scrolled remote content')
    if (engine === 'chromium') {
      await input.swipe(0, -150)
      await expect.poll(() => page.locator('#viewport').evaluate((el) => el.scrollLeft)).toBeGreaterThan(40)
    }
    await page.evaluate(() => { window.selected = true; document.querySelector('#viewport').scrollTop = 0 })
    await input.swipe(100)
    assert.equal(await page.evaluate(() => window.events.length), 0, 'selection gesture reached terminal')
    await page.evaluate(() => { window.selected = false })
    await input.send('touchstart', [[100, 200], [180, 200, 2]])
    for (let i = 1; i <= 8; i++) await input.send('touchmove', [[100 - i * 6, 200], [180 + i * 6, 200, 2]])
    await input.send('touchend', [])
    assert.equal(await page.evaluate(() => window.events.length), 0, 'pinch produced terminal scrolling')
    if (engine === 'chromium') {
      await expect.poll(() => page.evaluate(() => window.visualViewport.scale)).toBeGreaterThan(1.05)
      await expect.poll(() => page.locator('#viewport').evaluate((el) => el.style.touchAction)).toBe('auto')
      await input.cdp.send('Emulation.setPageScaleFactor', { pageScaleFactor: 1 })
      await input.send('touchstart', [[100, 230]])
      await input.send('touchmove', [[100, 250]])
      const beforePinch = await page.evaluate(() => window.events.length)
      await input.send('touchstart', [[100, 250], [180, 250, 2]])
      for (let i = 1; i <= 8; i++) await input.send('touchmove', [[100 - i * 6, 250], [180 + i * 6, 250, 2]])
      await input.send('touchend', [])
      assert.equal(await page.evaluate(() => window.events.length), beforePinch, 'a pinch following single-finger scrolling also scrolled remote content')
      await expect.poll(() => page.evaluate(() => window.visualViewport.scale)).toBeGreaterThan(1.05)
      await input.cdp.send('Emulation.setPageScaleFactor', { pageScaleFactor: 1 })
    }
    assert.deepEqual(errors, [])
    console.log(`${engine}: controller vertical edge handoff, finger directions, selection and pinch passed${engine === 'chromium' ? ' (trusted input, native horizontal pan and actual pinch zoom)' : ' (TouchEvent routing only)'}`)
  } finally { await context.close() }
}

async function workbenchChecks(browser, engine, history) {
  const context = await browser.newContext({ viewport: { width: 390, height: 844 }, hasTouch: true, isMobile: true })
  const page = await context.newPage()
  // Fixed source geometry is intentional here: this suite tests viewport-edge handoff.
  await page.addInitScript(() => localStorage.setItem('herdrx.terminal-display.v2', JSON.stringify({ mobile: { mode: 'fixed', fontSize: 14, zoom: 100 } })))
  const messages = [], errors = []
  page.on('pageerror', (error) => errors.push(error.message))
  await page.routeWebSocket('**/api/hosts/*/ws', (ws) => {
    let streamID = 0
    let sequence = 0n
    const frame = (id, text) => {
      const bytes = Buffer.from(text), data = Buffer.alloc(20 + bytes.length)
      data.set([0x74, 1, 1, 1]); data.writeUInt32LE(id, 4); data.writeBigUInt64LE(++sequence, 8)
      data.writeUInt16LE(80, 16); data.writeUInt16LE(40, 18); bytes.copy(data, 20); ws.send(data)
    }
    ws.onMessage((raw) => {
      if (typeof raw !== 'string') { messages.push({ op: raw[2] }); return }
      const message = JSON.parse(raw); messages.push(message)
      if (message.t === 'hello') {
        ws.send(JSON.stringify({ t: 'server_info' })); ws.send(JSON.stringify({ t: 'conn', state: 'ready' }))
        ws.send(JSON.stringify({ t: 'snapshot', snapshot: snapshot(history) }))
      } else if (message.t === 'terminal.open') {
        const id = ++streamID
        ws.send(JSON.stringify({ t: 'terminal.opened', id: message.id, stream_id: id })); frame(id, ansi)
      } else if (message.t === 'call') {
        if (message.method === 'terminal.scroll') frame(message.params.stream_id, `\x1b[2J\x1b[HMobile native ${message.params.lines}`)
        ws.send(JSON.stringify({ t: 'result', id: message.id, result: message.method === 'pane.read' ? { read: { text: Array.from({ length: 400 }, (_, i) => `mobile history ${String(i).padStart(3)}`).join('\n') } } : {} }))
      }
    })
  })
  try {
    await page.goto(base + '/h/mobile-test')
    await expect(page.locator('.xterm-rows')).toContainText('Mobile live')
    await expect(page.locator('.terminal-pane')).toHaveAttribute('data-terminal-status', '可输入')
    await page.getByRole('button', { name: '工作台设置', exact: true }).click()
    await chooseOption(page.getByRole('combobox', { name: /显示方式/ }), 'fit')
    await page.getByRole('button', { name: '完成', exact: true }).click()
    const input = await touchInput(context, page, engine, '.terminal-viewport')
    const first = () => page.locator('.xterm-rows > div').first().innerText()
    const viewport = page.locator('.terminal-viewport')
    // Composer chrome can leave a locally scrollable frame. Pan to the edge
    // first so remaining finger movement still reaches Herdr.
    await viewport.evaluate((el) => { el.scrollTop = 0 })
    await input.swipe(110)
    if (history) {
      await expect(page.locator('.xterm-rows')).toContainText('mobile history')
      const before = await first()
      await input.swipe(70)
      await expect.poll(first).not.toBe(before)
      const older = await first()
      await input.swipe(-35)
      await expect.poll(first).not.toBe(older)
      assert.equal(messages.filter((m) => m.method === 'pane.read').length, 1, 'continuous touch repeatedly reloaded history')
      assert.equal(messages.filter((m) => m.method === 'terminal.scroll').length, 0, 'history gesture reached fullscreen app')
    } else {
      await expect(page.locator('.xterm-rows')).toContainText('Mobile native -')
      await viewport.evaluate((el) => { el.scrollTop = Math.max(0, el.scrollHeight - el.clientHeight) })
      await input.swipe(-80)
      await expect(page.locator('.xterm-rows')).toContainText(/Mobile native [1-9]/)
      const calls = messages.filter((m) => m.method === 'terminal.scroll')
      assert.ok(calls.length > 1)
      assert.ok(calls.every((m) => m.params.column >= 0 && m.params.column < 80 && m.params.row >= 0 && m.params.row < 40), 'touch sent invalid cell coordinates')
      assert.equal(messages.filter((m) => m.method === 'pane.read').length, 0, 'fullscreen touch froze a history snapshot')
    }
    assert.equal(messages.filter((m) => m.t === 'terminal.open').length, 1, 'continuous touch reopened the terminal')
    assert.equal(messages.filter((m) => m.op === 4 || m.op === 3 || m.t === 'terminal.close').length, 0, 'touch sent raw input, resize or close')
    // Dispatch in one task so the second finger arrives before the pending RAF.
    // This specifically checks the component's unsent queue, which trusted CDP
    // calls cannot reliably keep in the same browser animation frame.
    // First let the previous, normally completed gesture finish xterm's render.
    await page.evaluate(() => new Promise((resolve) => requestAnimationFrame(() => requestAnimationFrame(resolve))))
    const scrollCalls = messages.filter((m) => m.method === 'terminal.scroll').length
    const beforePinch = await first()
    await page.locator('.terminal-viewport').evaluate((target) => {
      const box = target.getBoundingClientRect(), x = box.left + 80, y = box.top + 80
      const send = (type, points) => {
        const touchList = typeof document.createTouch === 'function'
          ? document.createTouchList(...points.map(([identifier, clientY]) => document.createTouch(window, target, identifier, x, clientY, x, clientY)))
          : points.map(([identifier, clientY]) => new Touch({ identifier, target, clientX: x, clientY }))
        target.dispatchEvent(new TouchEvent(type, { touches: touchList, targetTouches: touchList, changedTouches: touchList, bubbles: true, cancelable: true }))
      }
      send('touchstart', [[1, y]])
      send('touchmove', [[1, y + 100]])
      send('touchstart', [[1, y + 100], [2, y + 110]])
      send('touchend', [])
    })
    await page.evaluate(() => new Promise((resolve) => requestAnimationFrame(() => requestAnimationFrame(resolve))))
    assert.equal(messages.filter((m) => m.method === 'terminal.scroll').length, scrollCalls, 'pinch replayed an unsent one-finger scroll')
    assert.equal(await first(), beforePinch, 'pinch replayed unsent history movement')
    await page.getByRole('button', { name: '终端工具', exact: true }).click()
    await page.getByRole('button', { name: '聚焦终端输入' }).click()
    await expect(page.locator('.xterm-helper-textarea')).toBeFocused()
    assert.deepEqual(errors, [])
    console.log(`${engine}: React/xterm ${history ? 'history' : 'fullscreen native'} vertical finger scrolling, repeated gestures, queued-pinch cancellation and keyboard focus passed`)
  } finally { await context.close() }
}

try {
  for (const engine of (process.env.HERDRX_TEST_ENGINES?.split(',') || ['chromium', 'webkit'])) {
    const browser = await ({ chromium, webkit })[engine].launch({ headless: true })
    try {
      await controllerChecks(browser, engine)
      if (process.env.HERDRX_MOBILE_CONTROLLER_ONLY !== '1') {
        await workbenchChecks(browser, engine, false)
        await workbenchChecks(browser, engine, true)
      }
    } finally { await browser.close() }
  }
} finally { await new Promise((resolve) => server.close(resolve)) }
