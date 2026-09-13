#!/usr/bin/env node
// Isolated browser regression for the pane Chat view. Uses a fake workbench
// WebSocket (terminal stream + `pane.send_input`) and a fake owner-authenticated
// `.../transcript` HTTP endpoint; never connects to a real Herdr host, pane, PTY,
// user session, or any real Agent session log.
//
// The chat view's only data source is the structured transcript endpoint, **not**
// `pane.read` terminal text, so this script pins that invariant: entering the chat
// view must issue zero `pane.read` calls, must not auto-bind the single candidate,
// and the answers on screen must come from the transcript fixture.
//
// The view toggle is a single `role="switch"` (aria-label 对话视图) that lives in
// the pane header. This script also pins the header geometry: the switch must
// stay inside the pane at narrow widths and must never intersect the agent name.
import assert from 'node:assert/strict'
import { createServer } from 'node:http'
import { readFile, mkdir } from 'node:fs/promises'
import { fileURLToPath } from 'node:url'
import { join } from 'node:path'
import { chromium, firefox, webkit, expect } from '../web/node_modules/@playwright/test/index.mjs'

const dist = fileURLToPath(new URL('../web/dist/', import.meta.url))
const artifacts = process.env.HERDRX_CHAT_ARTIFACTS
const host = { id: 'chat-test', name: '对话测试', transport: 'ssh' }
// ── transcript 假数据（冻结契约的字段形状，见 web/src/lib/structuredChatTypes.ts） ──
const AGENTS = { p1: 'claude', p2: 'codex' }
const SESSIONS = { p1: 'aaaa1111-2222-4333-8444-555555555555', p2: 'bbbb2222-3333-4444-8555-666666666666' }
const candidatesFor = (pane) => [{ id: `candidate-${pane}`, agent: AGENTS[pane], session_id: SESSIONS[pane], updated_at: '2026-09-13T05:00:00.000Z' }]
// 一轮真实问答：用户提问、助手 Markdown 正文 + 工具调用、工具结果、助手收尾。
const QA = [
  { id: 'u1', role: 'user', at: '2026-09-13T05:00:00.000Z', blocks: [{ type: 'text', text: '把 header 高度改成 44' }] },
  { id: 'a1', role: 'assistant', at: '2026-09-13T05:00:01.000Z', blocks: [
    { type: 'text', text: '先跑一遍构建：\n\n```sh\nmake web-build\ngo test ./...\n```\n' },
    { type: 'tool-call', call_id: 'call-1', name: 'Edit', input: { file_path: '/workspace/example/web/src/styles.css', old_string: 'height: 40px', new_string: 'height: 44px' } },
  ] },
  { id: 't1', role: 'tool', at: '2026-09-13T05:00:02.000Z', blocks: [{ type: 'tool-result', call_id: 'call-1', output: 'updated', is_error: false }] },
  { id: 'a2', role: 'assistant', at: '2026-09-13T05:00:03.000Z', blocks: [{ type: 'text', text: '已改成 44 并补了测试。' }] },
]
// 每次 fixture 重置：transcript 请求记录 + 终端发送过的文本（模拟 Agent 写盘后再被增量读到）。
let transcriptMode = 'ok'
const transcriptLog = []
const sentTexts = []
const snapshot = {
  protocol: 1, version: 'test', focused_workspace_id: 'w1', focused_tab_id: 't1', focused_pane_id: 'p1',
  workspaces: [{ workspace_id: 'w1', active_tab_id: 't1', label: '对话测试', number: 1, pane_count: 2, tab_count: 1, agent_status: 'idle' }],
  tabs: [{ workspace_id: 'w1', tab_id: 't1', label: '开发终端', number: 1, pane_count: 2, agent_status: 'idle' }],
  panes: [1, 2].map((i) => ({ workspace_id: 'w1', tab_id: 't1', pane_id: `p${i}`, label: `终端 ${i}`, cwd: '/workspace/example', terminal_id: `term${i}`, revision: 1, agent_status: 'idle', agent: AGENTS[`p${i}`] })),
  layouts: [{ workspace_id: 'w1', tab_id: 't1', area: { x: 0, y: 0, width: 160, height: 40 }, focused_pane_id: 'p1', splits: [], zoomed: false, panes: [1, 2].map((i) => ({ pane_id: `p${i}`, rect: { x: (i - 1) * 80, y: 0, width: 80, height: 40 } })) }],
  agents: [],
}
const ansi = Array.from({ length: 24 }, (_, i) => `\x1b[${i + 1};1HChat terminal ${i}`).join('')
const sendJSON = (res, status, body) => { res.writeHead(status, { 'content-type': 'application/json' }); res.end(JSON.stringify(body)) }
const server = createServer(async (req, res) => {
  const url = new URL(req.url, 'http://localhost')
  const path = url.pathname
  const transcript = path.match(/^\/api\/hosts\/chat-test\/panes\/(p\d+)\/transcript$/)
  if (transcript) {
    const pane = transcript[1]
    const query = Object.fromEntries(url.searchParams)
    transcriptLog.push({ pane, session: query.session, before: query.before, cursor: query.cursor })
    if (transcriptMode === 'error') return sendJSON(res, 500, { code: 'internal_error', error: '读取会话记录失败' })
    if (transcriptMode === 'unsupported') return sendJSON(res, 200, { supported: false, reason: 'unsupported_transport', messages: [] })
    if (!query.session) return sendJSON(res, 200, { supported: true, agent: AGENTS[pane], candidates: candidatesFor(pane), messages: [] })
    // 终端里刚发出去的文本，模拟成 Agent 下一轮写盘后被增量游标读到的新记录。
    const echoed = sentTexts.map((item, index) => ({ id: `sent-${index + 1}`, role: 'user', at: '2026-09-13T05:00:09.000Z', blocks: [{ type: 'text', text: item.text }] }))
    return sendJSON(res, 200, { supported: true, agent: AGENTS[pane], session_id: SESSIONS[pane], binding: 'selected', messages: QA.concat(echoed), skipped: 0, has_more: false })
  }
  const json = path === '/api/bootstrap/status' ? { required: false } : path === '/api/me' ? { user: { id: 'chat-user', email: 'chat@example.test', display_name: 'Chat', role: 'admin' }, csrf_token: 'chat-fixture', session_id: 'chat-session' } : path === '/api/hosts/' ? { hosts: [host] } : path === '/api/hosts/chat-test/' ? { host } : null
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

async function fixture(browser, options, { mode = 'ok', sendDelayMs = 300, fixtureSnapshot = snapshot } = {}) {
  transcriptMode = mode
  transcriptLog.length = 0
  sentTexts.length = 0
  const context = await browser.newContext(options)
  await context.grantPermissions(['clipboard-read', 'clipboard-write'], { origin: base })
  const page = await context.newPage()
  page.setDefaultTimeout(10_000)
  const messages = [], errors = []
  page.on('pageerror', (error) => errors.push(error.message))
  await page.routeWebSocket('**/api/hosts/*/ws', (ws) => {
    let nextStreamID = 0
    ws.onMessage((raw) => {
      if (typeof raw !== 'string') {
        const data = Buffer.from(raw)
        messages.push({ op: data[2], stream_id: data.readUInt32LE(4) })
        return
      }
      const message = JSON.parse(raw)
      messages.push(message)
      if (message.t === 'hello') {
        ws.send(JSON.stringify({ t: 'server_info' }))
        ws.send(JSON.stringify({ t: 'conn', state: 'ready' }))
        ws.send(JSON.stringify({ t: 'snapshot', snapshot: fixtureSnapshot }))
      } else if (message.t === 'terminal.open') {
        const bytes = Buffer.from(ansi)
        const frame = Buffer.alloc(20 + bytes.length)
        frame.set([0x74, 1, 1, 1])
        const streamID = ++nextStreamID
        frame.writeUInt32LE(streamID, 4)
        frame.writeBigUInt64LE(1n, 8)
        frame.writeUInt16LE(80, 16)
        frame.writeUInt16LE(24, 18)
        bytes.copy(frame, 20)
        ws.send(JSON.stringify({ t: 'terminal.opened', id: message.id, stream_id: streamID }))
        ws.send(frame)
      } else if (message.t === 'call' && message.method === 'pane.read') {
        // 对话视图不该走这条路；这里仍给出应答，好让「有没有发生」能被断言出来。
        ws.send(JSON.stringify({ t: 'result', id: message.id, result: { read: { text: 'chat-read-line-1\nchat-read-line-2\n构建完成' } } }))
      } else if (message.t === 'call' && message.method === 'pane.send_input') {
        sentTexts.push({ pane: message.params?.pane_id, text: message.params?.text })
        setTimeout(() => ws.send(JSON.stringify({ t: 'result', id: message.id, result: { type: 'ok' } })), sendDelayMs)
      } else if (message.t === 'call') {
        ws.send(JSON.stringify({ t: 'result', id: message.id, result: {} }))
      }
    })
  })
  await page.goto(base + '/h/chat-test')
  await expect(page.locator('.xterm-rows').first()).toContainText('Chat terminal')
  return { context, page, messages, errors, transcript: transcriptLog, sent: sentTexts }
}

// Every fixture closes its own browser context on success and on failure so a
// failed assertion can never leave a browser holding the event loop open.
async function withFixture(browser, options, fixtureOptions, body) {
  const f = await fixture(browser, options, fixtureOptions)
  try { return await body(f) } finally { await f.context.close() }
}

async function screenshot(page, name, fullPage = true) {
  if (!artifacts) return
  await mkdir(artifacts, { recursive: true })
  await page.screenshot({ path: join(artifacts, name + '.png'), fullPage })
}

const calls = (messages, method) => messages.filter((item) => item.t === 'call' && item.method === method)
const opens = (messages) => messages.filter((item) => item.t === 'terminal.open').length

// The pane header is the fix: the switch lives in the flow next to the agent
// name instead of being absolutely positioned over it.
async function paneHeaderGeometry(page, index = 0) {
  return page.evaluate((i) => {
    const pane = document.querySelectorAll('.terminal-pane')[i]
    if (!pane) return null
    const box = (el) => { if (!el || !el.getClientRects().length) return null; const r = el.getBoundingClientRect(); return { left: r.left, right: r.right, top: r.top, bottom: r.bottom, width: r.width, height: r.height } }
    const toggle = pane.querySelector('.pane-view-toggle')
    return { pane: box(pane), header: box(pane.querySelector('.pane-header')), toggle: box(toggle), name: box(pane.querySelector('.pane-header-name')), viewport: box(pane.querySelector('.terminal-viewport')), checked: toggle ? toggle.getAttribute('aria-checked') : null }
  }, index)
}

const intersects = (a, b) => Boolean(a && b) && a.left < b.right - 0.5 && b.left < a.right - 0.5 && a.top < b.bottom - 0.5 && b.top < a.bottom - 0.5
const inside = (inner, outer, tolerance = 0.6) => Boolean(inner && outer) && inner.left >= outer.left - tolerance && inner.right <= outer.right + tolerance && inner.top >= outer.top - tolerance && inner.bottom <= outer.bottom + tolerance

function assertHeaderGeometry(geometry, label) {
  assert.ok(geometry && geometry.pane && geometry.header && geometry.toggle, `${label}: pane header or view toggle is missing`)
  assert.ok(inside(geometry.header, geometry.pane), `${label}: pane header escapes the pane`)
  assert.ok(inside(geometry.toggle, geometry.header), `${label}: view toggle escapes the pane header`)
  assert.ok(inside(geometry.toggle, geometry.pane), `${label}: view toggle escapes the pane`)
  assert.ok(geometry.toggle.width >= 24 && geometry.toggle.height >= 16, `${label}: view toggle collapsed`)
  assert.ok(!intersects(geometry.name, geometry.toggle), `${label}: agent name overlaps the view toggle ${JSON.stringify(geometry)}`)
  assert.ok(!geometry.viewport || geometry.viewport.top >= geometry.header.bottom - 1, `${label}: terminal viewport overlaps the pane header`)
}

// Pane pixel width is linear in viewport width, so two probes give the slope and
// the intercept (the sidebar) and we can aim a split pane at an exact width.
async function paneWidthAt(page, width, height) {
  await page.setViewportSize({ width, height })
  await page.waitForTimeout(150)
  return page.evaluate(() => document.querySelector('.terminal-pane')?.getBoundingClientRect().width || 0)
}

async function aimPaneWidth(page, target, height) {
  // Secant iteration: the first two probes bracket the (locally linear) relation
  // between viewport width and pane width, then each step re-measures.
  let previousWidth = 1100, previousPane = await paneWidthAt(page, previousWidth, height)
  let width = 1300, pane = await paneWidthAt(page, width, height)
  for (let attempt = 0; attempt < 5 && Math.abs(pane - target) > 1; attempt++) {
    const slope = (pane - previousPane) / (width - previousWidth)
    if (!(Math.abs(slope) > 0.05)) break
    const next = Math.min(Math.max(Math.round(width + (target - pane) / slope), 768), 2400)
    if (next === width) break
    previousWidth = width; previousPane = pane
    width = next; pane = await paneWidthAt(page, width, height)
  }
  return { width, actual: pane }
}

const mobile = { viewport: { width: 390, height: 844 }, hasTouch: true, deviceScaleFactor: 2 }
const narrow = { viewport: { width: 400, height: 720 }, hasTouch: true }
const desktop = { viewport: { width: 1280, height: 800 } }

// 低内存机器上限制渲染进程与堆大小，避免浏览器自身被 OOM 掉。
const launchArgs = ['--disable-gpu', '--disable-software-rasterizer', '--renderer-process-limit=2', '--js-flags=--max-old-space-size=384']
const engines = process.env.HERDRX_TEST_ENGINES?.split(',') || ['chromium']
try {
  for (const name of engines) {
    const browser = await ({ chromium, firefox, webkit })[name].launch({ headless: true, args: launchArgs })
    try {
      // 桌面：pane 级别的终端/对话切换、真实终端文本、发送与 Ctrl+C。
      await withFixture(browser, desktop, {}, async (desk) => {
        // 视图开关常驻在 pane header 里，不再需要先打开“终端工具”抽屉。
        const paneSwitch = desk.page.getByRole('switch', { name: '对话视图' }).first()
        await expect(paneSwitch).toBeVisible()
        await expect(paneSwitch).toHaveAttribute('aria-checked', 'false')
        await expect(paneSwitch).toHaveAttribute('data-mode', 'terminal')
        // 终端工具抽屉入口保留，开关不在抽屉里。
        await desk.page.getByRole('button', { name: '终端工具', exact: true }).first().click()
        await expect(desk.page.locator('.terminal-titlebar').first()).toBeVisible()
        await expect(desk.page.locator('.terminal-titlebar').getByRole('switch')).toHaveCount(0)
        await desk.page.getByRole('button', { name: '收起终端工具', exact: true }).first().click()
        await expect(desk.page.locator('.terminal-titlebar').first()).toBeHidden()

        const opensBeforeChat = opens(desk.messages)
        await paneSwitch.click()
        await expect(paneSwitch).toHaveAttribute('aria-checked', 'true')
        await expect(paneSwitch).toHaveAttribute('data-mode', 'chat')

        const chat = desk.page.getByRole('region', { name: '对话视图' })
        await expect(chat).toBeVisible()
        await expect(chat.getByRole('note')).toContainText('这里的回答来自 Agent 写下的会话记录')
        await expect(chat.getByRole('note')).toContainText('请选择此终端的会话记录')
        // 数据源是 owner 认证的会话记录端点，不是终端屏幕文本。
        await expect(chat.getByRole('heading', { name: '选择此终端的会话记录' })).toBeVisible()
        await expect.poll(() => desk.transcript.length).toBeGreaterThan(0)
        assert.equal(calls(desk.messages, 'pane.read').length, 0, 'chat view read terminal text through pane.read')
        assert.equal(opens(desk.messages), opensBeforeChat, 'entering the chat view reopened the terminal stream')
        // 绝不自动绑定唯一候选：没点之前一条记录都不能出现。
        await expect(chat.locator('.chat-turn')).toHaveCount(0)
        await expect(chat.locator('.chat-message')).toHaveCount(0)

        // 显式选择后才显示逐轮问答。
        await chat.getByRole('button', { name: /aaaa1111/ }).click()
        await expect(chat.locator('.chat-turn')).toHaveCount(1)
        await expect(chat.locator('.chat-message-user .chat-bubble')).toHaveText('把 header 高度改成 44')
        await expect(chat.getByText('已改成 44 并补了测试。')).toBeVisible()
        // 真实工具调用与结果：折叠分区里显示真实工具名和 call id。
        const toolCall = chat.locator('.chat-tool-call').first()
        await expect(toolCall.locator('summary')).toContainText('Edit')
        await expect(toolCall.locator('summary')).toContainText('call-1')
        await expect(toolCall.locator('.chat-tool-pre')).toBeHidden()
        await toolCall.locator('summary').click()
        await expect(toolCall.locator('.chat-tool-pre')).toContainText('height: 44px')
        const toolResult = chat.locator('.chat-tool-result').first()
        await expect(toolResult.locator('summary')).toContainText('Edit')
        await toolResult.locator('summary').click()
        await expect(toolResult.locator('.chat-tool-pre')).toContainText('updated')
        // 助手正文按 Markdown 渲染，代码块可复制。
        await expect(chat.locator('.chat-assistant-text code').first()).toContainText('make web-build')
        await chat.locator('.chat-code .chat-copy').first().click()
        await expect.poll(() => desk.page.evaluate(() => navigator.clipboard.readText())).toContain('make web-build')
        await expect(chat.getByRole('button', { name: '重新选择会话记录' })).toBeVisible()
        await screenshot(desk.page, `${name}-desktop-chat`)

        // 工作台底部的本地输入框在对话视图下隐藏，避免两个输入框。
        await expect(desk.page.locator('.workbench-dock')).toHaveCount(0)
        // 对话里的输入框仍可发送，一次一段；发送只走 pane.send_input，不注入按键。
        const box = chat.getByRole('textbox', { name: '对话输入内容' })
        await box.fill('请运行测试')
        await box.press('Enter')
        await expect.poll(() => calls(desk.messages, 'pane.send_input').length).toBe(1)
        assert.deepEqual(calls(desk.messages, 'pane.send_input')[0].params, { pane_id: 'p1', text: '请运行测试', keys: ['Enter'] })
        assert.equal(calls(desk.messages, 'pane.send_keys').length, 0, 'chat view injected raw keys into the terminal')
        // 送达 ≠ 远端已执行：状态文案必须说清楚。
        await expect(chat.getByText(/不代表远端程序已执行成功/).first()).toBeVisible()
        // 发出去的文本随后由 Agent 写盘，增量轮询把它读回来（不本地伪造助手回答）。
        await expect(chat.locator('.chat-bubble').filter({ hasText: '请运行测试' })).toBeVisible()
        await expect(chat.locator('.chat-message-user')).toHaveCount(2)

        // 切回终端：xterm 仍在，终端流没有被重开。
        await paneSwitch.click()
        await expect(paneSwitch).toHaveAttribute('aria-checked', 'false')
        await expect(desk.page.getByRole('region', { name: '对话视图' })).toHaveCount(0)
        await expect(desk.page.locator('.xterm-rows').first()).toContainText('Chat terminal')
        await desk.page.waitForTimeout(500)
        assert.equal(opens(desk.messages), opensBeforeChat, 'switching back reopened the terminal stream')
        assert.deepEqual(desk.errors, [])
      })

      // 另一个 pane 保持终端视图，模式按 pane 隔离（走面板右键菜单这条入口）。
      await withFixture(browser, desktop, {}, async (iso) => {
        await iso.page.locator('.pane-position').nth(1).click({ button: 'right' })
        await iso.page.getByRole('menuitem', { name: '显示对话视图' }).click()
        const second = iso.page.getByRole('region', { name: '对话视图' })
        await expect(second).toBeVisible()
        await expect(iso.page.getByRole('switch', { name: '对话视图' }).nth(1)).toHaveAttribute('aria-checked', 'true')
        await expect(iso.page.getByRole('switch', { name: '对话视图' }).nth(0)).toHaveAttribute('aria-checked', 'false')
        await expect.poll(() => iso.transcript.length).toBeGreaterThan(0)
        assert.ok(iso.transcript.every((item) => item.pane === 'p2'), 'chat read a pane that is not in the chat view')
        assert.equal(calls(iso.messages, 'pane.read').length, 0, 'chat view read terminal text through pane.read')
        // 候选列表按 pane 隔离：第二个 pane 只看到自己的会话。
        await expect(second.getByRole('button', { name: /bbbb2222/ })).toBeVisible()
        await expect(second.getByRole('button', { name: /aaaa1111/ })).toHaveCount(0)
        await expect(iso.page.getByRole('heading', { name: '选择此终端的会话记录' })).toHaveCount(1)
        await expect(iso.page.getByRole('region', { name: '对话视图' })).toHaveCount(1)
        await iso.page.locator('.pane-position').nth(1).click({ button: 'right' })
        await iso.page.getByRole('menuitem', { name: '显示终端视图' }).click()
        await expect(iso.page.getByRole('region', { name: '对话视图' })).toHaveCount(0)
        await expect(iso.page.locator('.xterm-rows').nth(1)).toContainText('Chat terminal')
        assert.deepEqual(iso.errors, [])
      })

      // 读取失败时给出错误状态而不是空白外壳，也不回退到终端文本。
      await withFixture(browser, desktop, { mode: 'error' }, async (broken) => {
        await broken.page.getByRole('switch', { name: '对话视图' }).first().click()
        const brokenChat = broken.page.getByRole('region', { name: '对话视图' })
        await expect(brokenChat.getByLabel('读取会话记录失败')).toContainText('读取会话记录失败')
        await expect(brokenChat.getByRole('button', { name: '重试' })).toBeVisible()
        await expect(brokenChat.locator('.chat-message')).toHaveCount(0)
        assert.equal(calls(broken.messages, 'pane.read').length, 0, 'chat view fell back to terminal text after a read failure')
        assert.deepEqual(broken.errors, [])
      })

      // 接入方式拿不到受限读取能力时，只显示可执行的降级说明。
      await withFixture(browser, desktop, { mode: 'unsupported' }, async (off) => {
        await off.page.getByRole('switch', { name: '对话视图' }).first().click()
        const offChat = off.page.getByRole('region', { name: '对话视图' })
        await expect(offChat.getByLabel('结构化记录不可用')).toContainText('暂不支持结构化会话记录')
        await expect(offChat.getByLabel('结构化记录不可用')).toContainText('当前接入方式或远端环境暂不支持受限读取')
        await expect(offChat.locator('.chat-message')).toHaveCount(0)
        await offChat.getByRole('button', { name: '切回终端' }).click()
        await expect(off.page.getByRole('region', { name: '对话视图' })).toHaveCount(0)
        await expect(off.page.locator('.xterm-rows').first()).toContainText('Chat terminal')
        assert.deepEqual(off.errors, [])
      })

      // 窄 pane 几何：496 / 320 / 280px 的 pane 里，header 中的视图开关
      // 必须留在 pane 内，并且不能与 Agent 名称相交。
      await withFixture(browser, desktop, {}, async (geo) => {
        for (const [target, height] of [[496, 506], [320, 506], [280, 506]]) {
          const { width, actual } = await aimPaneWidth(geo.page, target, height)
          assert.ok(Math.abs(actual - target) <= 4, `could not size a pane to ${target}px (got ${actual}px at viewport ${width}px)`)
          const geometry = await paneHeaderGeometry(geo.page, 0)
          assertHeaderGeometry(geometry, `${name} pane ${target}px (actual ${Math.round(actual)}px)`)
          // 496px 仍宽于 480px 容器查询阈值，名称必须还在，否则这个断言会变成空转。
          if (target >= 496) assert.ok(geometry.name && geometry.name.width > 0, `${name}: agent name missing at ${Math.round(actual)}px, overlap cannot be verified`)
          await screenshot(geo.page, `${name}-pane-${target}x${height}`, false)
        }
        assert.deepEqual(geo.errors, [])
      })

      // 视口级别的窄窗口：496x506 以及 320 / 280px 宽，开关同样不出界。
      for (const [width, height] of [[496, 506], [320, 720], [280, 720]]) {
        await withFixture(browser, { viewport: { width, height }, hasTouch: true }, {}, async (tight) => {
          const toggle = tight.page.getByRole('switch', { name: '对话视图' }).first()
          await expect(toggle).toBeVisible()
          await expect(toggle).toHaveAttribute('aria-checked', 'false')
          assertHeaderGeometry(await paneHeaderGeometry(tight.page, 0), `${name} viewport ${width}x${height}`)
          const overflow = await tight.page.evaluate(() => ({ scroll: document.documentElement.scrollWidth, client: document.documentElement.clientWidth }))
          assert.ok(overflow.scroll <= overflow.client + 1, `viewport ${width}x${height} overflows horizontally: ${overflow.scroll} > ${overflow.client}`)
          await screenshot(tight.page, `${name}-viewport-${width}x${height}-terminal`, false)
          await toggle.click()
          await expect(toggle).toHaveAttribute('aria-checked', 'true')
          const chat = tight.page.getByRole('region', { name: '对话视图' })
          await chat.getByRole('button', { name: /aaaa1111/ }).click()
          await expect(chat.getByText('已改成 44 并补了测试。')).toBeVisible()
          assertHeaderGeometry(await paneHeaderGeometry(tight.page, 0), `${name} viewport ${width}x${height} (chat)`)
          const boxRect = await chat.getByRole('textbox', { name: '对话输入内容' }).boundingBox()
          assert.ok(boxRect && boxRect.width > 120, `chat composer collapsed at ${width}px`)
          assert.ok(boxRect.x >= -1 && boxRect.x + boxRect.width <= width + 1, `chat composer escapes the ${width}px viewport`)
          await screenshot(tight.page, `${name}-viewport-${width}x${height}-chat`, false)
          assert.deepEqual(tight.errors, [])
        })
      }

      // 400px 窄屏：无横向溢出，输入框和安全区都在。
      await withFixture(browser, narrow, {}, async (tight) => {
        await tight.page.getByRole('switch', { name: '对话视图' }).first().click()
        const tightChat = tight.page.getByRole('region', { name: '对话视图' })
        await tightChat.getByRole('button', { name: /aaaa1111/ }).click()
        await expect(tightChat.getByText('已改成 44 并补了测试。')).toBeVisible()
        const overflow = await tight.page.evaluate(() => ({ scroll: document.documentElement.scrollWidth, client: document.documentElement.clientWidth }))
        assert.ok(overflow.scroll <= overflow.client + 1, `chat view overflows horizontally at 400px: ${overflow.scroll} > ${overflow.client}`)
        const boxRect = await tightChat.getByRole('textbox', { name: '对话输入内容' }).boundingBox()
        assert.ok(boxRect && boxRect.width > 120, 'chat composer collapsed at 400px')
        assert.ok(boxRect.x >= 0 && boxRect.x + boxRect.width <= 401, 'chat composer escapes the 400px viewport')
        await screenshot(tight.page, `${name}-400-chat`)
        assert.deepEqual(tight.errors, [])
      })

      // 手机：对话入口出现在快捷切换里，输入框和切换都可用。
      await withFixture(browser, mobile, {}, async (phone) => {
        await phone.page.getByRole('button', { name: '切换工作区或终端', exact: true }).click()
        await phone.page.getByRole('button', { name: /切换到对话视图/ }).click()
        const phoneChat = phone.page.getByRole('region', { name: '对话视图' })
        await phoneChat.getByRole('button', { name: /aaaa1111/ }).click()
        await expect(phoneChat.locator('.chat-message-user .chat-bubble')).toHaveText('把 header 高度改成 44')
        await expect(phoneChat.getByText('已改成 44 并补了测试。')).toBeVisible()
        await expect(phone.page.getByRole('switch', { name: '对话视图' }).first()).toHaveAttribute('aria-checked', 'true')
        await phoneChat.getByRole('textbox', { name: '对话输入内容' }).fill('手机发送')
        await phoneChat.getByRole('button', { name: '发送', exact: true }).click()
        await expect.poll(() => calls(phone.messages, 'pane.send_input').length).toBe(1)
        const phoneOverflow = await phone.page.evaluate(() => ({ scroll: document.documentElement.scrollWidth, client: document.documentElement.clientWidth }))
        assert.ok(phoneOverflow.scroll <= phoneOverflow.client + 1, `mobile chat view overflows horizontally: ${phoneOverflow.scroll} > ${phoneOverflow.client}`)
        await screenshot(phone.page, `${name}-mobile-chat`)
        assert.deepEqual(phone.errors, [])
      })
    } finally {
      await browser.close()
    }
  }
  console.log('chat mode browser smoke passed')
} finally {
  await new Promise((done) => server.close(done))
}
