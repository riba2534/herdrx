#!/usr/bin/env node
// Isolated browser regression for the workbench Web UI mode (the default) and
// its switch to Web Shell. A fake website answers the HTTP API and the
// workbench WebSocket the way the Go server does; it never connects to a real
// Herdr host, pane, PTY or Claude.
//
// Pins: the workbench opens in Web UI mode, a Claude pane shows the console
// chat view and another pane the read-only screen with a command line and
// "在 Shell 中打开"; switching modes keeps the selected pane, sends no Herdr
// call and survives a reload; the new-session dialog lists launchers with
// their flags (or falls back to an editable `claude`), starts the session once
// and the first message is sent exactly once after Claude turns idle; desktop
// 1280 and phone 390 widths have no horizontal overflow and single-line buttons.
//
//   HERDRX_UI_MODE_ARTIFACTS=/tmp/ui-mode node scripts/test-web-ui-mode.mjs   # with screenshots
import assert from 'node:assert/strict'
import { createServer } from 'node:http'
import { readFile, mkdir } from 'node:fs/promises'
import { fileURLToPath } from 'node:url'
import { join } from 'node:path'
import { chromium, firefox, webkit, expect } from '../web/node_modules/@playwright/test/index.mjs'
import { servePanePrompt } from './browser-controls.mjs'

const dist = fileURLToPath(new URL('../web/dist/', import.meta.url))
const artifacts = process.env.HERDRX_UI_MODE_ARTIFACTS
const HOST = 'ui-test'
const host = { id: HOST, name: 'UI 测试', transport: 'local' }
const SESSION = 'aaaa1111-2222-4333-8444-555555555555'
const QA = [
  { id: 'u1', role: 'user', at: '2026-11-02T05:00:00.000Z', blocks: [{ type: 'text', text: '把 header 高度改成 44' }] },
  { id: 'a1', role: 'assistant', at: '2026-11-02T05:00:01.000Z', blocks: [{ type: 'text', text: '已改成 44，并补了一条回归测试：\n\n```sh\npnpm --dir web exec vitest run\n```\n' }] },
]
const LAUNCHERS = { supported: true, launchers: [
  { name: 'cc', bypass: false, fullscreen: false, teammate: false },
  { name: 'xcc', bypass: true, fullscreen: true, teammate: true },
] }
const pane = (id, tab, patch = {}) => ({ workspace_id: 'w1', tab_id: tab, pane_id: id, terminal_id: `term-${id}`, revision: 1, agent_status: 'idle', cwd: '/workspace/example', ...patch })
const layout = (workspace, tab, ids) => ({ workspace_id: workspace, tab_id: tab, area: { x: 0, y: 0, width: 160, height: 40 }, focused_pane_id: ids[0], splits: [], zoomed: false, panes: ids.map((id, index) => ({ pane_id: id, rect: { x: index * 80, y: 0, width: 80, height: 40 } })) })
const initialSnapshot = () => ({
  protocol: 1, version: 'test', focused_workspace_id: 'w1', focused_tab_id: 't1', focused_pane_id: 'p1',
  workspaces: [{ workspace_id: 'w1', active_tab_id: 't1', label: 'example', number: 1, pane_count: 2, tab_count: 1, agent_status: 'working' }],
  tabs: [{ workspace_id: 'w1', tab_id: 't1', label: '开发', number: 1, pane_count: 2, agent_status: 'working' }],
  panes: [pane('p1', 't1', { agent: 'claude', label: 'Claude' }), pane('p2', 't1', { label: '构建', agent_status: 'working' })],
  layouts: [layout('w1', 't1', ['p1', 'p2'])],
  agents: [{ workspace_id: 'w1', tab_id: 't1', pane_id: 'p1', name: 'claude', agent: 'claude', agent_status: 'idle' }],
})
const screenLines = ['\x1b[0m\x1b[38;5;2m✓\x1b[0m built in 2.1s', '\x1b[0m\x1b[1m\x1b[38;5;1merror\x1b[0m src/app.ts:12 type mismatch', '', '\x1b[0m\x1b[1m\x1b[38;5;4m$ \x1b[0m']
const ansi = Array.from({ length: 24 }, (_, i) => `\x1b[${i + 1};1HShell terminal ${i}`).join('')

// 每个 fixture 一份状态；同一时刻只有一个 fixture 在用。
let state = null
const sendJSON = (res, status, body) => { res.writeHead(status, { 'content-type': 'application/json', 'cache-control': 'no-store' }); res.end(JSON.stringify(body)) }
async function readBody(req) {
  let raw = ''
  for await (const chunk of req) raw += chunk
  return raw ? JSON.parse(raw) : {}
}
const server = createServer(async (req, res) => {
  const url = new URL(req.url, 'http://localhost')
  const path = url.pathname
  if (await servePanePrompt(req, res, path, state?.onPrompt)) return
  if (path === `/api/hosts/${HOST}/launchers`) return sendJSON(res, 200, state.launchers)
  if (req.method === 'POST' && path === `/api/hosts/${HOST}/sessions`) return sendJSON(res, 200, await state.onSession(await readBody(req)))
  if (/^\/api\/hosts\/[^/]+\/panes\/[^/]+\/dialog$/.test(path)) return sendJSON(res, 200, { dialog: null })
  if (/^\/api\/hosts\/[^/]+\/panes\/[^/]+\/transcript$/.test(path)) {
    return sendJSON(res, 200, { supported: true, agent: 'claude', session_id: SESSION, binding: 'verified', session: 'candidate-p1', messages: url.searchParams.get('cursor') ? [] : QA, next_cursor: 'c1', has_more: false })
  }
  const json = path === '/api/bootstrap/status' ? { required: false } : path === '/api/me' ? { user: { id: 'ui-user', email: 'ui@example.test', display_name: 'UI', role: 'admin' }, csrf_token: 'ui-fixture', session_id: 'ui-session' } : path === '/api/me/workbench-session' ? { session: null } : path === '/api/hosts/' ? { hosts: [host] } : path === `/api/hosts/${HOST}/` ? { host } : null
  if (json) return sendJSON(res, 200, json)
  if (path.startsWith('/api/')) { res.writeHead(404); res.end(); return }
  try {
    const file = (path.startsWith('/assets/') || path.startsWith('/brand/')) || ['/boot.js', '/sw.js', '/manifest.webmanifest', '/favicon.ico'].includes(path) ? path.slice(1) : 'index.html'
    res.setHeader('content-type', file.endsWith('.js') ? 'application/javascript' : file.endsWith('.css') ? 'text/css' : file.endsWith('.png') ? 'image/png' : file.endsWith('.ico') ? 'image/x-icon' : file.endsWith('.webmanifest') ? 'application/manifest+json' : 'text/html')
    res.end(await readFile(join(dist, file)))
  } catch { res.writeHead(404); res.end() }
})
await new Promise((done) => server.listen(0, '127.0.0.1', done))
const base = `http://127.0.0.1:${server.address().port}`

async function fixture(browser, options, { appearance, launchers = LAUNCHERS } = {}) {
  const context = await browser.newContext(options)
  if (appearance) await context.addInitScript((value) => localStorage.setItem('herdrx.workbench-appearance.v1', value), appearance)
  const page = await context.newPage()
  page.setDefaultTimeout(10_000)
  const messages = [], errors = [], prompts = [], sessions = [], sockets = []
  page.on('pageerror', (error) => errors.push(error.message))
  state = {
    snapshot: initialSnapshot(),
    launchers,
    onPrompt: async (paneID, body) => { prompts.push({ pane: paneID, ...body }); return { delivered: 'sent' } },
    onSession: async (body) => {
      sessions.push(body)
      return { workspace_id: 'w2', tab_id: 't2', pane_id: 'p9', session_id: SESSION, launcher: body.launcher }
    },
  }
  const push = (next) => {
    state.snapshot = next
    for (const ws of sockets) ws.send(JSON.stringify({ t: 'snapshot', snapshot: next }))
  }
  await page.routeWebSocket('**/api/hosts/*/ws', (ws) => {
    sockets.push(ws)
    let streams = 0, gen = 0
    const send = (message) => ws.send(JSON.stringify(message))
    ws.onMessage((raw) => {
      if (typeof raw !== 'string') return
      const message = JSON.parse(raw)
      messages.push(message)
      if (message.t === 'hello') {
        send({ t: 'server_info', features: { terminal_control: 1, terminal_resize_v2: 1 } })
        send({ t: 'conn', state: 'ready' })
        send({ t: 'snapshot', snapshot: state.snapshot })
      } else if (message.t === 'terminal.open') {
        const bytes = Buffer.from(ansi)
        const frame = Buffer.alloc(20 + bytes.length)
        frame.set([0x74, 1, 1, 1])
        const streamID = ++streams
        frame.writeUInt32LE(streamID, 4)
        frame.writeBigUInt64LE(1n, 8)
        frame.writeUInt16LE(80, 16)
        frame.writeUInt16LE(40, 18)
        bytes.copy(frame, 20)
        send({ t: 'terminal.opened', id: message.id, stream_id: streamID })
        ws.send(frame)
      } else if (message.t === 'screen.watch') {
        gen++
        send({ t: 'screen.watching', id: message.id, pane_id: message.pane_id, lines: message.lines, gen })
        send({ t: 'screen', pane_id: message.pane_id, gen, seq: 1, full: true, lines: screenLines, total: screenLines.length, requested: message.lines })
      } else if (message.t === 'call') {
        send({ t: 'result', id: message.id, result: {} })
      }
    })
  })
  await page.goto(`${base}/h/${HOST}`)
  return { context, page, messages, errors, prompts, sessions, push }
}

async function withFixture(browser, options, settings, body) {
  const f = await fixture(browser, options, settings)
  try {
    await body(f)
    assert.deepEqual(f.errors, [], 'page errors')
  } finally { await f.context.close() }
}

async function screenshot(page, name) {
  if (!artifacts) return
  await mkdir(artifacts, { recursive: true })
  await page.evaluate(() => Promise.all(document.getAnimations().map((animation) => animation.finished.catch(() => {}))))
  await page.screenshot({ path: join(artifacts, `${name}.png`) })
}

// 页面和控制台区域都不能横向溢出；按钮文字保持单行。
async function expectFits(page, label) {
  const report = await page.evaluate(() => {
    const overflow = []
    if (document.documentElement.scrollWidth > window.innerWidth + 1) overflow.push(`document ${document.documentElement.scrollWidth}>${window.innerWidth}`)
    for (const element of document.querySelectorAll('.console-surface, .console-main, .console-screen, .console-command, .ui-modal.console-dialog, .console-form, .mobile-topbar, .hostbar')) {
      const rect = element.getBoundingClientRect()
      if (!rect.width) continue
      if (rect.right > window.innerWidth + 1 || rect.left < -1) overflow.push(`${element.className} sticks out (${Math.round(rect.left)}..${Math.round(rect.right)})`)
      if (element.scrollWidth > element.clientWidth + 1 && getComputedStyle(element).overflowX !== 'auto') overflow.push(`${element.className} scrolls ${element.scrollWidth}>${element.clientWidth}`)
    }
    const wrapped = []
    for (const button of document.querySelectorAll('.console-surface button, .console-dialog button, .mode-switch button, .tabbar-new-session')) {
      if (!button.getClientRects().length) continue
      // 每段文字自己的行框只能有一行（图标、状态点与文字基线不同，不放在一起比）。
      const walker = document.createTreeWalker(button, NodeFilter.SHOW_TEXT)
      for (let node = walker.nextNode(); node; node = walker.nextNode()) {
        if (!node.textContent.trim()) continue
        const range = document.createRange()
        range.selectNodeContents(node)
        const tops = new Set([...range.getClientRects()].filter((rect) => rect.width).map((rect) => Math.round(rect.top)))
        if (tops.size > 1) { wrapped.push(button.textContent.trim()); break }
      }
    }
    return { overflow, wrapped }
  })
  assert.deepEqual(report.overflow, [], `${label}: horizontal overflow`)
  assert.deepEqual(report.wrapped, [], `${label}: button text wrapped`)
}

const modeRadio = (page, mode) => page.getByRole('radio', { name: mode === 'ui' ? /Web UI 模式/ : /Web Shell 模式/ }).first()
const calls = (messages) => messages.filter((message) => message.t === 'call')

async function desktop(browser, name) {
  const viewport = { width: 1280, height: 800 }
  await withFixture(browser, { viewport }, {}, async ({ page, messages, prompts, sessions, push }) => {
    // 默认 Web UI：Claude pane 是对话视图，没有 xterm。
    await expect(modeRadio(page, 'ui')).toHaveAttribute('aria-checked', 'true')
    await expect(page.getByRole('region', { name: '对话视图' })).toBeVisible()
    await expect(page.getByText('已改成 44，并补了一条回归测试')).toBeVisible()
    await expect(page.getByRole('navigation', { name: '选择终端' }).getByRole('button')).toHaveCount(2)
    assert.equal(await page.locator('.xterm').count(), 0, 'Web UI mode must not open terminals')
    assert.equal(messages.filter((message) => message.t === 'terminal.open').length, 0)
    await expect(page.locator('html')).toHaveAttribute('data-appearance', 'light')
    await expectFits(page, `${name} desktop chat`)
    await screenshot(page, `${name}-desktop-1280-ui-light-chat`)

    // 非 Claude pane：只读画面 + 一行命令 +「在 Shell 中打开」。
    await page.getByRole('navigation', { name: '选择终端' }).getByRole('button', { name: /构建/ }).click()
    await expect(page.getByRole('region', { name: '画面视图' })).toContainText('type mismatch')
    await expect(page.getByRole('button', { name: '在 Shell 中打开' })).toBeVisible()
    const command = page.getByRole('textbox', { name: '发送到终端的命令' })
    await command.fill('pnpm build')
    await command.press('Enter')
    await expect.poll(() => prompts.length).toBe(1)
    assert.equal(prompts[0].pane, 'p2')
    assert.equal(prompts[0].text, 'pnpm build')
    assert.equal(prompts[0].submit, true)
    assert.match(prompts[0].client_msg_id, /^[A-Za-z0-9_-]{1,64}$/)
    await expect(command).toHaveValue('')
    await expectFits(page, `${name} desktop screen`)
    await screenshot(page, `${name}-desktop-1280-ui-light-screen`)

    // 切到 Shell：同一个 pane，不调用 Herdr；刷新后保持。
    const before = calls(messages).length
    await modeRadio(page, 'shell').click()
    await expect(page.locator('.xterm-rows').first()).toContainText('Shell terminal')
    await expect(page.locator('.terminal-pane-active .terminal-title')).toContainText('构建')
    assert.equal(calls(messages).length, before, 'switching modes must not call Herdr')
    assert.equal(await page.evaluate(() => localStorage.getItem('herdrx.workbench-mode.v1')), 'shell')
    await page.reload()
    await expect(modeRadio(page, 'shell')).toHaveAttribute('aria-checked', 'true')
    await expect(page.locator('.xterm-rows').first()).toContainText('Shell terminal')
    await modeRadio(page, 'ui').click()
    await page.reload()
    await expect(modeRadio(page, 'ui')).toHaveAttribute('aria-checked', 'true')
    await expect(page.getByRole('region', { name: '对话视图' })).toBeVisible()

    // 新建 Claude 会话：列出启动器与开关，提交一次，就绪后首条消息只发一次。
    await page.getByRole('button', { name: '新建 Claude 会话' }).first().click()
    const dialog = page.getByRole('dialog', { name: '新建 Claude 会话' })
    const xcc = dialog.getByRole('radio', { name: /xcc/ })
    await expect(xcc).toHaveAttribute('aria-checked', 'true')
    for (const tag of ['免授权', '全屏', '队友']) await expect(xcc).toContainText(tag)
    await expect(dialog.getByRole('combobox')).toHaveValue('/workspace/example')
    await dialog.getByLabel('首条消息').fill('先读一遍 README')
    await expectFits(page, `${name} desktop dialog`)
    await screenshot(page, `${name}-desktop-1280-new-session-light`)
    await dialog.getByRole('button', { name: '新建会话', exact: true }).click()
    await expect(dialog).toBeHidden()
    assert.equal(sessions.length, 1)
    assert.deepEqual({ ...sessions[0], request_id: 'id' }, { request_id: 'id', cwd: '/workspace/example', launcher: 'xcc', target: { kind: 'workspace' } })
    const created = initialSnapshot()
    created.workspaces.push({ workspace_id: 'w2', active_tab_id: 't2', label: 'example', number: 2, pane_count: 1, tab_count: 1, agent_status: 'unknown' })
    created.tabs.push({ workspace_id: 'w2', tab_id: 't2', label: '1', number: 1, pane_count: 1, agent_status: 'unknown' })
    created.panes.push(pane('p9', 't2', { workspace_id: 'w2', agent_status: 'unknown' }))
    created.layouts.push(layout('w2', 't2', ['p9']))
    push(created)
    await expect(page.getByText(/首条消息会在 Claude 就绪/)).toBeVisible()
    assert.equal(prompts.length, 1, 'the first message must wait for Claude')
    push({ ...created, panes: created.panes.map((item) => item.pane_id === 'p9' ? { ...item, agent: 'claude', agent_status: 'idle' } : item) })
    await expect.poll(() => prompts.length).toBe(2)
    assert.deepEqual(prompts[1], { pane: 'p9', text: '先读一遍 README', submit: true, client_msg_id: `first-${sessions[0].request_id}` })
    // 横幅消失说明这条已从等待列表移除，之后不会再发。
    await expect(page.getByText(/首条消息会在 Claude 就绪/)).toBeHidden()
    assert.equal(prompts.length, 2, 'the first message must be sent exactly once')
  })

  await withFixture(browser, { viewport }, { appearance: 'dark' }, async ({ page }) => {
    await expect(page.locator('html')).toHaveAttribute('data-appearance', 'dark')
    await expect(page.getByText('已改成 44，并补了一条回归测试')).toBeVisible()
    await screenshot(page, `${name}-desktop-1280-ui-dark-chat`)
    await page.getByRole('navigation', { name: '选择终端' }).getByRole('button', { name: /构建/ }).click()
    await expect(page.getByRole('region', { name: '画面视图' })).toContainText('type mismatch')
    await screenshot(page, `${name}-desktop-1280-ui-dark-screen`)
    await page.getByRole('button', { name: '新建 Claude 会话' }).first().click()
    await expect(page.getByRole('dialog', { name: '新建 Claude 会话' }).getByRole('radio', { name: /xcc/ })).toBeVisible()
    await expectFits(page, `${name} desktop dark dialog`)
    await screenshot(page, `${name}-desktop-1280-new-session-dark`)
  })
}

async function phone(browser, name) {
  const options = { viewport: { width: 390, height: 844 }, hasTouch: true, ...(name !== 'firefox' ? { isMobile: true } : {}) }
  for (const appearance of ['light', 'dark']) {
    await withFixture(browser, options, { appearance }, async ({ page }) => {
      const topbar = page.getByRole('banner', { name: '工作台导航' })
      await expect(topbar.getByRole('radiogroup', { name: '工作台模式' })).toBeVisible()
      await expect(topbar.getByRole('button', { name: '终端工具' })).toHaveCount(0)
      await expect(page.getByRole('region', { name: '对话视图' })).toBeVisible()
      await expect(page.getByRole('navigation', { name: '切换终端' })).toBeVisible()
      await expectFits(page, `${name} phone ${appearance} chat`)
      await screenshot(page, `${name}-phone-390-ui-${appearance}-chat`)
      await page.getByRole('navigation', { name: '切换终端' }).getByRole('button', { name: /构建/ }).click()
      await expect(page.getByRole('region', { name: '画面视图' })).toContainText('type mismatch')
      await expect(page.getByRole('button', { name: '在 Shell 中打开' })).toBeVisible()
      await expectFits(page, `${name} phone ${appearance} screen`)
      await screenshot(page, `${name}-phone-390-ui-${appearance}-screen`)
      await topbar.getByRole('button', { name: '新建 Claude 会话' }).click()
      await expect(page.getByRole('dialog', { name: '新建 Claude 会话' }).getByRole('radio', { name: /xcc/ })).toBeVisible()
      await expectFits(page, `${name} phone ${appearance} dialog`)
      await screenshot(page, `${name}-phone-390-new-session-${appearance}`)
    })
  }
  // 旧受控端列不出启动器：退回可编辑的 claude，并说明怎么升级。
  await withFixture(browser, options, { launchers: { supported: false, reason: 'unsupported_agent', launchers: [] } }, async ({ page, sessions }) => {
    await page.getByRole('banner', { name: '工作台导航' }).getByRole('button', { name: '新建 Claude 会话' }).click()
    const dialog = page.getByRole('dialog', { name: '新建 Claude 会话' })
    await expect(dialog.getByRole('textbox', { name: '启动器' })).toHaveValue('claude')
    await expect(dialog.getByText(/herdrx update/)).toBeVisible()
    await expectFits(page, `${name} phone fallback dialog`)
    await screenshot(page, `${name}-phone-390-new-session-fallback`)
    await dialog.getByRole('button', { name: '新建会话', exact: true }).click()
    await expect(dialog).toBeHidden()
    assert.equal(sessions[0].launcher, 'claude')
  })
}

try {
  for (const name of process.env.HERDRX_TEST_ENGINES?.split(',') || ['chromium']) {
    const browser = await ({ chromium, firefox, webkit })[name].launch()
    try {
      await desktop(browser, name)
      await phone(browser, name)
      console.log(`${name}: Web UI mode ok`)
    } finally { await browser.close() }
  }
} finally {
  server.close()
}
