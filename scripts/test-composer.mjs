#!/usr/bin/env node
// Isolated browser composer regression. Never connects to a real Herdr pane.
import assert from 'node:assert/strict'
import { createServer } from 'node:http'
import { readFile, mkdir } from 'node:fs/promises'
import { fileURLToPath } from 'node:url'
import { join } from 'node:path'
import { chromium, firefox, webkit, expect } from '../web/node_modules/@playwright/test/index.mjs'

const dist = fileURLToPath(new URL('../web/dist/', import.meta.url))
const artifacts = process.env.HERDRX_COMPOSER_ARTIFACTS
const host = { id: 'composer-test', name: '输入框测试', transport: 'ssh' }
const otherHost = { id: 'composer-other', name: '另一台主机', transport: 'ssh' }
const snapshot = {
  protocol: 1, version: 'test', focused_workspace_id: 'w1', focused_tab_id: 't1', focused_pane_id: 'p1',
  workspaces: [{ workspace_id: 'w1', active_tab_id: 't1', label: '本地输入测试', number: 1, pane_count: 2, tab_count: 1, agent_status: 'working' }],
  tabs: [{ workspace_id: 'w1', tab_id: 't1', label: '开发终端', number: 1, pane_count: 2, agent_status: 'working' }],
  panes: [1, 2].map((i) => ({ workspace_id: 'w1', tab_id: 't1', pane_id: `p${i}`, label: `终端 ${i}`, cwd: '/workspace/example', terminal_id: `term${i}`, revision: 1, agent_status: 'working' })),
  layouts: [{ workspace_id: 'w1', tab_id: 't1', area: { x: 0, y: 0, width: 160, height: 40 }, focused_pane_id: 'p1', splits: [], zoomed: false, panes: [1, 2].map((i) => ({ pane_id: `p${i}`, rect: { x: (i - 1) * 80, y: 0, width: 80, height: 40 } })) }],
  agents: [],
}
const ansi = Array.from({ length: 24 }, (_, i) => `\x1b[${i + 1};1HComposer terminal ${i}`).join('')
// 当前 fixture 的 pane 接口替身（prompt / dialog / dialog/answer）。
let paneAPI = null
const server = createServer(async (req, res) => {
  const path = new URL(req.url, 'http://localhost').pathname
  const pane = path.match(/^\/api\/hosts\/[^/]+\/panes\/([^/]+)\/(prompt|dialog|dialog\/answer)$/)
  if (pane && paneAPI) {
    let raw = ''
    for await (const chunk of req) raw += chunk
    await paneAPI(pane[2], decodeURIComponent(pane[1]), raw ? JSON.parse(raw) : {}, req, res)
    return
  }
  const json = path === '/api/bootstrap/status' ? { required: false } : path === '/api/me' ? { user: { id: 'composer-user', email: 'composer@example.test', display_name: 'Composer', role: 'admin' }, csrf_token: 'composer-fixture', session_id: 'composer-session' } : path === '/api/me/workbench-session' ? { session: null } : path === '/api/hosts/' ? { hosts: [host, otherHost] } : path === '/api/hosts/composer-test/' ? { host } : path === '/api/hosts/composer-other/' ? { host: otherHost } : null
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

const promptDialog = { fingerprint: '0123456789abcdef', title: 'Do you want to proceed?', lines: ['Bash command', 'rm -rf build'], options: [{ index: 1, label: 'Yes', selected: true }, { index: 2, label: 'No, and tell Claude what to do differently', selected: false }] }

async function fixture(browser, options, { delayMs = 500, failText = 'FORCE_FAIL', closeOnSend = false, receiptError = '', screen = 'idle' } = {}) {
  const context = await browser.newContext(options)
  const page = await context.newPage()
  const messages = [], errors = [], prompts = [], answers = []
  page.on('pageerror', (error) => errors.push(error.message))
  // 发送经服务端 POST …/prompt：服务端读屏防护后再按“正文 + 回车”两腿送入。这里按请求体模拟服务端的各种结果。
  // 由本地假服务器转交（WebKit 下 page.route 拦不到这些 fetch）；同一时刻只有一个 fixture 在用。
  paneAPI = async (kind, pane, body, req, res) => {
    const json = (status, payload) => { res.writeHead(status, { 'content-type': 'application/json' }); res.end(JSON.stringify(payload)) }
    if (kind === 'dialog') return json(200, { dialog: screen === 'dialog' ? promptDialog : null })
    if (kind === 'dialog/answer') { answers.push(body); return json(200, { ok: true }) }
    prompts.push({ pane, ...body })
    if (closeOnSend) { req.socket.destroy(); return }
    await new Promise((resolve) => setTimeout(resolve, delayMs))
    if (screen === 'dialog') return json(409, { code: 'pane_dialog', error: '终端正弹着一个选择对话框', dialog: promptDialog })
    if (screen === 'draft' && !body.force) return json(409, { code: 'pane_draft', error: '电脑上的输入框里有未发送的内容' })
    if (receiptError && String(body.text).includes('EOF_RECEIPT')) return json(502, { code: 'delivery_unknown', error: '上一次发送结果不确定，请先查看终端再决定是否重发' })
    if (String(body.text).includes(failText)) return json(502, { code: 'pane_send_failed', error: '发送失败（远端拒绝），内容没有送入终端，请检查后重试' })
    return json(200, { delivered: 'sent' })
  }
  await page.routeWebSocket('**/api/hosts/*/ws', (ws) => {
    let streamID = 0
    const emitFrame = (id) => {
      const bytes = Buffer.from(ansi)
      const frame = Buffer.alloc(20 + bytes.length)
      frame.set([0x74, 1, 1, 1]); frame.writeUInt32LE(id, 4); frame.writeBigUInt64LE(1n, 8)
      frame.writeUInt16LE(80, 16); frame.writeUInt16LE(24, 18); bytes.copy(frame, 20)
      ws.send(frame)
    }
    ws.onMessage((raw) => {
      if (typeof raw !== 'string') {
        const data = Buffer.from(raw)
        messages.push({ op: data[2], stream_id: data.readUInt32LE(4), bytes: data.subarray(16).toString() })
        return
      }
      const message = JSON.parse(raw)
      messages.push(message)
      if (message.t === 'hello') {
        ws.send(JSON.stringify({ t: 'server_info' }))
        ws.send(JSON.stringify({ t: 'conn', state: 'ready' }))
        ws.send(JSON.stringify({ t: 'snapshot', snapshot }))
      } else if (message.t === 'terminal.open') {
        const id = ++streamID
        ws.send(JSON.stringify({ t: 'terminal.opened', id: message.id, stream_id: id }))
        emitFrame(id)
      } else if (message.t === 'call' && message.method === 'pane.send_input') {
        if (closeOnSend) { ws.close(); return }
        const text = String(message.params?.text || '')
        const fail = text.includes(failText)
        const interrupted = Boolean(receiptError) && text.includes('EOF_RECEIPT')
        setTimeout(() => {
          if (interrupted) ws.send(JSON.stringify({ t: 'error', id: message.id, message: receiptError }))
          else if (fail) ws.send(JSON.stringify({ t: 'error', id: message.id, message: '远端拒绝' }))
          else ws.send(JSON.stringify({ t: 'result', id: message.id, result: { type: 'ok' } }))
        }, delayMs)
      } else if (message.t === 'call') {
        ws.send(JSON.stringify({ t: 'result', id: message.id, result: {} }))
      }
    })
  })
  await page.goto(base + '/h/composer-test')
  await expect(page.locator('.xterm-rows').first()).toContainText('Composer terminal')
  return { context, page, messages, errors, prompts, answers }
}

async function screenshot(page, name) {
  if (!artifacts) return
  await mkdir(artifacts, { recursive: true })
  await page.screenshot({ path: join(artifacts, name + '.png'), fullPage: true })
}

async function selectMobileHost(page, name) {
  await page.getByRole('button', { name: '切换工作区或终端', exact: true }).click()
  await page.locator('.switcher').getByRole('link', { name, exact: true }).click()
  await expect(page.locator('.mobile-location')).toContainText(name)
}

async function showAuxiliaryKeys(page) {
  const toggle = page.getByRole('button', { name: '终端辅助键', exact: true })
  if (await toggle.getAttribute('aria-expanded') !== 'true') await toggle.click()
  await expect(page.getByRole('toolbar', { name: '终端辅助键', exact: true })).toBeVisible()
}

// 工作台连接上的直接输入 RPC（键栏粘贴）。Composer 提交不走这里。
function sendCalls(messages) {
  return messages.filter((item) => item.t === 'call' && item.method === 'pane.send_input')
}

// Composer 的一次提交 = 一次 POST …/prompt；去掉随机的 client_msg_id 便于比较。
function promptBodies(f) {
  return f.prompts.map(({ client_msg_id: _id, ...rest }) => rest)
}

function inputFrames(messages) {
  return messages.filter((item) => item.op === 3)
}

const engines = process.env.HERDRX_TEST_ENGINES?.split(',') || ['chromium']
try {
  for (const name of engines) {
    const browser = await ({ chromium, firefox, webkit })[name].launch({ headless: true, ...(name === 'chromium' && process.env.HERDRX_TEST_CHROMIUM ? { executablePath: process.env.HERDRX_TEST_CHROMIUM } : {}) })
    try {
      const mobile = { viewport: { width: 390, height: 844 }, hasTouch: true, deviceScaleFactor: 2, ...(name !== 'firefox' ? { isMobile: true } : {}) }
      const f = await fixture(browser, mobile)
      const box = f.page.getByRole('textbox', { name: '本地输入内容' })
      await expect(box).toBeVisible()
      await box.click()
      const beforeType = f.messages.length
      await box.pressSequentially('hello中文🙂', { delay: 20 })
      await f.page.waitForTimeout(80)
      assert.equal(inputFrames(f.messages.slice(beforeType)).length, 0, 'typing leaked terminal input frames')
      assert.equal(sendCalls(f.messages.slice(beforeType)).length + f.prompts.length, 0, 'typing sent a submit')
      await expect(box).toHaveValue('hello中文🙂')

      await box.evaluate((el) => {
        el.dispatchEvent(new CompositionEvent('compositionstart', { bubbles: true }))
        el.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', isComposing: true, keyCode: 229, bubbles: true, cancelable: true }))
        el.dispatchEvent(new CompositionEvent('compositionend', { data: '你', bubbles: true }))
      })
      await box.fill('第一行')
      await box.press('Enter')
      await box.type('第二行')
      assert.equal(f.prompts.length, 0, 'Enter or IME submitted early')
      await expect(box).toHaveValue('第一行\n第二行')

      const started = Date.now()
      await f.page.getByRole('button', { name: '发送', exact: true }).click()
      // 一次提交 = 一次请求：整段正文，由服务端读屏后送入并补回车。
      await expect.poll(() => f.prompts.length).toBe(1)
      assert.deepEqual(promptBodies(f)[0], { pane: 'p1', text: '第一行\n第二行', submit: true })
      assert.match(f.prompts[0].client_msg_id, /^[0-9a-f]{32}$/)
      await expect(f.page.getByRole('status')).toContainText('发送中')
      await expect(f.page.getByRole('button', { name: '发送', exact: true })).toBeDisabled()
      await expect(f.page.getByRole('status')).toContainText('已送达', { timeout: 5000 })
      assert.ok(Date.now() - started >= 450, 'send finished before the simulated 500ms round trip')
      assert.equal(f.prompts.length, 1, 'one submit is exactly one request')
      assert.equal(sendCalls(f.messages).length, 0, 'composer typed through the workbench connection')

      await box.fill('切换前再发')
      await f.page.getByRole('button', { name: '发送', exact: true }).click()
      await expect(f.page.getByRole('status')).toContainText('发送中')
      await f.page.getByRole('button', { name: '切换工作区或终端', exact: true }).click()
      await f.page.locator('.switcher button').filter({ hasText: '终端 2' }).click()
      await box.fill('切换前再发')
      await expect.poll(() => f.prompts.filter((item) => item.text === '切换前再发').length).toBe(1)
      // 提交钉在点击时的 pane，切换终端不会把它送到别处。
      assert.deepEqual(promptBodies(f)[1], { pane: 'p1', text: '切换前再发', submit: true })
      await f.page.waitForTimeout(600)
      await expect(f.page.getByRole('textbox', { name: '本地输入内容' })).toHaveValue('切换前再发')
      await expect(f.page.getByRole('status')).toHaveCount(0)
      assert.equal(inputFrames(f.messages).length, 0, 'composer submit used keystroke frames')
      await screenshot(f.page, `${name}-mobile-390-composer`)
      assert.deepEqual(f.errors, [])
      await f.context.close()

      const fail = await fixture(browser, mobile)
      await fail.page.getByRole('textbox', { name: '本地输入内容' }).fill('keep FORCE_FAIL')
      await fail.page.getByRole('button', { name: '发送', exact: true }).click()
      await expect(fail.page.getByRole('status')).toContainText('发送失败')
      await expect(fail.page.getByRole('textbox', { name: '本地输入内容' })).toHaveValue('keep FORCE_FAIL')
      assert.equal(fail.prompts.length, 1)
      await fail.context.close()

      const drop = await fixture(browser, mobile, { closeOnSend: true })
      await drop.page.getByRole('textbox', { name: '本地输入内容' }).fill('do not replay')
      await drop.page.getByRole('button', { name: '发送', exact: true }).click()
      await expect.poll(() => drop.prompts.length).toBeGreaterThan(0)
      await drop.page.waitForTimeout(700)
      // 浏览器网络栈可能在连接被重置后自动重发同一个 POST；它们必须带同一个 client_msg_id，
      // 服务端据此只发一次。应用自己不重放。
      assert.equal(new Set(drop.prompts.map((item) => item.client_msg_id)).size, 1, 'disconnect replayed composer submit')
      await expect(drop.page.getByRole('textbox', { name: '本地输入内容' })).toHaveValue('do not replay')
      await expect(drop.page.getByRole('status')).toContainText('结果未知')
      await drop.context.close()

      const hop = await fixture(browser, mobile)
      await hop.page.getByRole('textbox', { name: '本地输入内容' }).fill('host hop draft')
      await hop.page.getByRole('button', { name: '发送', exact: true }).click()
      await expect(hop.page.getByRole('status')).toContainText('发送中')
      await selectMobileHost(hop.page, '另一台主机')
      await selectMobileHost(hop.page, '输入框测试')
      // 发送是独立的 HTTP 请求，不随工作台连接切换而中断：回来时看到它的真实结果。
      await expect(hop.page.getByRole('status')).toContainText('已送达', { timeout: 5000 })
      await expect(hop.page.getByRole('textbox', { name: '本地输入内容' })).toHaveValue('')
      assert.equal(hop.prompts.length, 1, 'returning to the host replayed composer submit')
      await hop.context.close()

      const eof = await fixture(browser, mobile, { receiptError: 'read herdr response: EOF' })
      await eof.page.getByRole('textbox', { name: '本地输入内容' }).fill('EOF_RECEIPT already written')
      await eof.page.getByRole('button', { name: '发送', exact: true }).click()
      await expect(eof.page.getByRole('status')).toContainText('结果未知')
      await expect(eof.page.getByRole('status')).not.toContainText('发送失败')
      await expect(eof.page.getByRole('textbox', { name: '本地输入内容' })).toHaveValue('EOF_RECEIPT already written')
      assert.equal(eof.prompts.length, 1)
      await selectMobileHost(eof.page, '另一台主机')
      await selectMobileHost(eof.page, '输入框测试')
      await expect(eof.page.getByRole('textbox', { name: '本地输入内容' })).toHaveValue('EOF_RECEIPT already written')
      await expect(eof.page.getByRole('status')).toContainText('结果未知')
      await expect(eof.page.getByRole('status')).not.toContainText('发送失败')
      assert.equal(eof.prompts.length, 1, 'EOF receipt navigation replayed composer submit')
      await eof.context.close()

      // 终端弹着对话框：文字不发出去，改为显示答题卡片，点选项走 answer。
      const dialog = await fixture(browser, mobile, { screen: 'dialog', delayMs: 50 })
      await dialog.page.getByRole('textbox', { name: '本地输入内容' }).fill('1')
      await dialog.page.getByRole('button', { name: '发送', exact: true }).click()
      const card = dialog.page.getByRole('group', { name: 'Do you want to proceed?' })
      await expect(card).toBeVisible()
      await expect(dialog.page.locator('.composer-status')).toContainText('未发送')
      await expect(dialog.page.getByRole('textbox', { name: '本地输入内容' })).toHaveValue('1')
      const option = card.getByRole('button', { name: 'No, and tell Claude what to do differently' })
      const optionBox = await option.boundingBox()
      assert.ok(optionBox && optionBox.height >= 44, `dialog option is not 44px on a phone: ${JSON.stringify(optionBox)}`)
      await screenshot(dialog.page, `${name}-mobile-390-dialog-card`)
      await option.click()
      await expect.poll(() => dialog.answers).toEqual([{ fingerprint: promptDialog.fingerprint, option_index: 2 }])
      assert.equal(sendCalls(dialog.messages).length, 0)
      assert.deepEqual(dialog.errors, [])
      await dialog.context.close()

      // 电脑上有未发送的输入：先用自绘确认框询问，确认后才 force 发送。
      const draft = await fixture(browser, mobile, { screen: 'draft', delayMs: 50 })
      await draft.page.getByRole('textbox', { name: '本地输入内容' }).fill('接着说')
      await draft.page.getByRole('button', { name: '发送', exact: true }).click()
      const ask = draft.page.getByRole('alertdialog', { name: '电脑上有未发送的输入，仍要发送吗？' })
      await expect(ask).toBeVisible()
      await ask.getByRole('button', { name: '仍要发送' }).click()
      await expect(draft.page.locator('.composer-status')).toContainText('已送达', { timeout: 5000 })
      assert.deepEqual(draft.prompts.map((item) => item.force), [undefined, true])
      assert.equal(new Set(draft.prompts.map((item) => item.client_msg_id)).size, 1)
      assert.deepEqual(draft.errors, [])
      await draft.context.close()

      // 手机键栏的「粘贴」：只把剪贴板整段送进当前终端，绝不隐式回车。
      // 走真实浏览器剪贴板（Chromium 授权 clipboard-read），断言的是 RPC 形状与次数，
      // 不是本地 xterm 的显示；bracketed-paste 分帧由远端 herdr 按 pane 状态决定。
      if (name === 'chromium' && browser.version) {
        const clipboard = await fixture(browser, { viewport: { width: 390, height: 844 }, hasTouch: true, isMobile: true, permissions: ['clipboard-read', 'clipboard-write'] })
        await clipboard.page.evaluate(() => navigator.clipboard.writeText('第一行\n第二行 🙂'))
        await showAuxiliaryKeys(clipboard.page)
        const before = clipboard.messages.length
        // 44px 目标：按钮必须在手机上可点，而不是只在 DOM 里存在。
        const pasteButton = clipboard.page.getByRole('button', { name: '粘贴到终端，不自动回车', exact: true })
        await expect(pasteButton).toBeVisible()
        const pasteBox = await pasteButton.boundingBox()
        assert.ok(pasteBox && pasteBox.width >= 44 && pasteBox.height >= 44, `paste target is not 44px: ${JSON.stringify(pasteBox)}`)
        await pasteButton.click()
        await expect.poll(() => sendCalls(clipboard.messages).length).toBe(1)
        // 只发正文、keys 为空：回车由用户自己按，远端才是唯一一次提交。
        assert.deepEqual(sendCalls(clipboard.messages)[0].params, { pane_id: 'p1', text: '第一行\n第二行 🙂', keys: [] })
        assert.equal(inputFrames(clipboard.messages.slice(before)).length, 0, 'keybar paste used keystroke frames')
        // 截图留证：粘贴按钮确实在键栏里可见可点。
        await screenshot(clipboard.page, `${name}-mobile-390-keybar-paste`)
        assert.deepEqual(clipboard.errors, [])
        await clipboard.context.close()
      }

      // 手机辅助键栏：按键必须真的把字节送进终端，而不只是渲染出按钮。
      // 这里断言的是 OP_INPUT 帧的内容——辅助键走本地终端输入通道，不是 RPC。
      if (name === 'chromium' && browser.version) {
        const keys = await fixture(browser, { viewport: { width: 390, height: 844 }, hasTouch: true, isMobile: true })
        await showAuxiliaryKeys(keys.page)
        const before = keys.messages.length
        const toolbar = keys.page.getByRole('toolbar', { name: '终端辅助键', exact: true })
        // 键栏只有在终端注册了发送通道后才可用；按钮禁用就说明通道没接上。
        const shiftTabButton = toolbar.getByRole('button', { name: 'Shift 加 Tab 反向切换', exact: true })
        await expect(shiftTabButton).toBeEnabled()
        await shiftTabButton.click()
        await toolbar.getByRole('button', { name: '中断 Ctrl+C', exact: true }).click()
        await toolbar.getByRole('button', { name: '回车键', exact: true }).click()
        await expect.poll(() => inputFrames(keys.messages.slice(before)).length).toBe(3)
        assert.deepEqual(inputFrames(keys.messages.slice(before)).map((item) => item.bytes), ['\x1b[Z', '\x03', '\r'])
        // 键栏不该走 RPC：那是粘贴按钮和提交的通道。
        assert.equal(sendCalls(keys.messages).length, 0, 'keybar keys were sent as composer submits')
        assert.deepEqual(keys.errors, [])
        await keys.context.close()
      }

      for (const [width, height] of [[320, 720], [390, 844], [479, 847], [844, 390]]) {
        const view = await fixture(browser, { viewport: { width, height }, hasTouch: true, ...(name !== 'firefox' ? { isMobile: true } : {}) })
        await expect(view.page.locator('.keybar')).toHaveCount(0)
        await expect(view.page.getByRole('status')).toHaveCount(0)
        const foldedComposer = await view.page.locator('.composer').boundingBox()
        assert.ok(foldedComposer && Math.abs(foldedComposer.y + foldedComposer.height - height) <= 1, `composer does not meet viewport bottom at ${width}x${height}`)
        await showAuxiliaryKeys(view.page)
        const keybar = await view.page.locator('.keybar').boundingBox()
        assert.ok(keybar, `missing keybar at ${width}x${height}`)
        if (height < 500) {
          await expect(view.page.getByRole('region', { name: '本地输入' })).toHaveCount(0)
          assert.ok(Math.abs(keybar.y + keybar.height - height) <= 1, `keybar does not meet viewport bottom at ${width}x${height}`)
        } else {
          const send = await view.page.getByRole('button', { name: '发送', exact: true }).boundingBox()
          const composer = await view.page.locator('.composer').boundingBox()
          assert.ok(send && composer, `missing composer chrome at ${width}x${height}`)
          assert.ok(send.y + send.height <= height + 1 && composer.y + composer.height <= keybar.y + 1)
          // Both inputs work side by side: a tap on the terminal types straight
          // into it (Tab completion, arrows, Ctrl), a tap on the box edits locally.
          const box = view.page.getByRole('textbox', { name: '本地输入内容' })
          await box.tap()
          await expect(box).toBeFocused()
          const beforeTap = view.messages.length
          const screenBox = await view.page.locator('.xterm-screen').first().boundingBox()
          await view.page.touchscreen.tap(screenBox.x + screenBox.width / 3, screenBox.y + screenBox.height / 2)
          await expect(view.page.locator('.xterm-helper-textarea')).toBeFocused()
          await expect(view.page.getByRole('region', { name: '本地输入' })).toBeVisible()
          await expect(view.page.getByRole('button', { name: '输入方式：直接输入终端', exact: true })).toBeVisible()
          await view.page.keyboard.type('ls')
          await view.page.getByRole('toolbar', { name: '终端辅助键' }).getByRole('button', { name: 'Tab 键', exact: true }).click()
          await expect.poll(() => inputFrames(view.messages.slice(beforeTap)).map((item) => item.bytes).join('')).toBe('ls\t')
          assert.equal(view.prompts.length, 0, 'direct typing went through the local submit path')
          await box.tap()
          await expect(box).toBeFocused()
          await expect(view.page.getByRole('button', { name: '输入方式：本地输入', exact: true })).toBeVisible()
        }
        await view.page.evaluate(() => {
          Object.defineProperty(window.visualViewport, 'height', { configurable: true, value: Math.min(360, window.innerHeight) })
          window.visualViewport.dispatchEvent(new Event('resize'))
        })
        const keyboard = Math.min(360, height)
        await expect.poll(() => view.page.locator('.keybar').evaluate((el) => el.getBoundingClientRect().bottom)).toBe(keyboard)
        if (keyboard < 500) {
          await expect(view.page.getByRole('region', { name: '本地输入' })).toHaveCount(0)
        } else {
          await expect.poll(() => view.page.getByRole('button', { name: '发送', exact: true }).evaluate((el) => el.getBoundingClientRect().bottom)).toBeLessThanOrEqual(keyboard + 1)
          await expect(view.page.getByRole('status')).toHaveCount(0)
          await view.page.getByRole('button', { name: '输入方式：本地输入', exact: true }).click()
          await view.page.getByRole('menuitemradio', { name: /^直接输入终端/ }).click()
          await expect(view.page.locator('.xterm-helper-textarea')).toBeFocused()
          await view.page.getByRole('button', { name: '输入方式：直接输入终端', exact: true }).click()
          await view.page.getByRole('menuitemradio', { name: /^本地输入/ }).click()
          await expect(view.page.getByRole('textbox', { name: '本地输入内容' })).toBeFocused()
        }
        await view.context.close()
      }

      for (const [width, kind, options] of [
        [320, 'failed', {}],
        [390, 'unknown', { closeOnSend: true }],
      ]) {
        const view = await fixture(browser, { viewport: { width, height: 844 }, hasTouch: true, ...(name !== 'firefox' ? { isMobile: true } : {}) }, options)
        await view.page.evaluate(() => {
          Object.defineProperty(window.visualViewport, 'height', { configurable: true, value: 360 })
          window.visualViewport.dispatchEvent(new Event('resize'))
        })
        await expect(view.page.locator('.workbench')).toHaveClass(/workbench-short/)
        await view.page.getByRole('textbox', { name: '本地输入内容' }).fill(kind === 'failed' ? 'keep FORCE_FAIL' : 'unknown after close')
        await view.page.getByRole('button', { name: '发送', exact: true }).click()
        await expect(view.page.getByRole('status')).toContainText(kind === 'failed' ? '失败' : '结果未知')
        const status = await view.page.getByRole('status').boundingBox()
        const send = await view.page.getByRole('button', { name: '发送', exact: true }).boundingBox()
        assert.ok(status && status.height > 0 && status.y + status.height <= 360 + 1, `${kind} status hidden under keyboard at ${width}`)
        assert.ok(send && send.y + send.height <= 360 + 1)
        await screenshot(view.page, `${name}-${width}-keyboard-${kind}`)
        await view.context.close()
      }

      const desktop = await fixture(browser, { viewport: { width: 1440, height: 900 } })
      await expect(desktop.page.getByRole('region', { name: '本地输入' })).toHaveCount(0)
      await desktop.page.getByRole('button', { name: '本地输入框' }).click()
      await desktop.page.getByRole('textbox', { name: '本地输入内容' }).fill('desktop 整段')
      await desktop.page.getByRole('textbox', { name: '本地输入内容' }).press('Control+Enter')
      await expect.poll(() => desktop.prompts.length).toBe(1)
      assert.deepEqual(promptBodies(desktop)[0], { pane: 'p1', text: 'desktop 整段', submit: true })
      await screenshot(desktop.page, `${name}-desktop-composer`)
      await desktop.page.getByRole('button', { name: '直接输入终端' }).click()
      await expect.poll(() => desktop.page.evaluate(() => document.activeElement?.classList.contains('xterm-helper-textarea'))).toBe(true)
      assert.deepEqual(desktop.errors, [])
      await desktop.context.close()
      console.log(`${name}: composer local edit, delayed one-shot guarded send, pane switch, failure, disconnect, dialog card, draft confirm and layout passed`)
    } finally { await browser.close() }
  }
} finally { await new Promise((resolve) => server.close(resolve)) }
