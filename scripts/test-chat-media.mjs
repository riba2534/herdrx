#!/usr/bin/env node
// 对话视图「图片附件 + 语音输入」的浏览器回归（真实构建产物 web/dist，纯假后端）。
//
// 全部是假的后端，任何一步都不碰真实 Herdr 主机 / pane / PTY / 用户会话：
// - 假工作台 WebSocket（`**/api/hosts/*/ws`）：终端观察流 + `pane.send_input`，与
//   scripts/test-chat-mode.mjs 同构，但只保留本回归要用的部分。
// - 假图片上传 `POST /api/hosts/media-test/panes/{pane}/paste-image?inject=<bool>`：
//   记录 `inject`、文件名与字节数，按模式返回 staged 路径 / 挂起 / 失败。
// - 假语音中继 `**/api/voice/ws**`：按冻结的 §3.4 帧协议回 `ready`，其余帧由用例
//   显式推送。**没有上游语音网关、没有付费额度、没有音频出站**：麦克风是 Chromium 的
//   `--use-fake-device-for-media-stream` 假设备，音频只到本机假中继。
//
// 契约：docs/design/chat-media-contract.md；类型唯一正文：web/src/lib/chatMediaTypes.ts。
// 语音用例需要 Chromium（假麦克风开关是 Chromium 专属）。
import assert from 'node:assert/strict'
import { createServer } from 'node:http'
import { readFile, mkdir } from 'node:fs/promises'
import { fileURLToPath } from 'node:url'
import { join } from 'node:path'
import { chromium, firefox, webkit, expect } from '../web/node_modules/@playwright/test/index.mjs'

const dist = fileURLToPath(new URL('../web/dist/', import.meta.url))
const artifacts = process.env.HERDRX_CHAT_MEDIA_ARTIFACTS
const host = { id: 'media-test', name: '媒体测试', transport: 'ssh' }
const AGENT = 'claude'
const SESSION = 'cccc3333-4444-4555-8666-777777777777'
const VOICE_CAPS = {
  enabled: true, protocol: 'openai-realtime', model: 'gpt-realtime',
  inputSampleRate: 24000, outputSampleRate: 24000, audioReply: 'off', maxSessionSeconds: 600,
}
const snapshot = {
  protocol: 1, version: 'test', focused_workspace_id: 'w1', focused_tab_id: 't1', focused_pane_id: 'p1',
  workspaces: [{ workspace_id: 'w1', active_tab_id: 't1', label: '媒体测试', number: 1, pane_count: 2, tab_count: 1, agent_status: 'idle' }],
  tabs: [{ workspace_id: 'w1', tab_id: 't1', label: '开发终端', number: 1, pane_count: 2, agent_status: 'idle' }],
  panes: [1, 2].map((i) => ({ workspace_id: 'w1', tab_id: 't1', pane_id: `p${i}`, label: `终端 ${i}`, cwd: '/workspace/example', terminal_id: `term${i}`, revision: 1, agent_status: 'idle', agent: AGENT })),
  layouts: [{ workspace_id: 'w1', tab_id: 't1', area: { x: 0, y: 0, width: 160, height: 40 }, focused_pane_id: 'p1', splits: [], zoomed: false, panes: [1, 2].map((i) => ({ pane_id: `p${i}`, rect: { x: (i - 1) * 80, y: 0, width: 80, height: 40 } })) }],
  agents: [],
}
const ansi = Array.from({ length: 24 }, (_, i) => `\x1b[${i + 1};1HMedia terminal ${i}`).join('')

// ── 假后端状态（每个 fixture 重置） ────────────────────────────────────────────
let imageMode = 'ok'          // ok | slow | fail
let slowReleased = false      // slow 模式是否已放行（放行后按 ok 应答）
let pendingUpload = null      // 挂起中的 slow 上传的 resolve
let voiceEnabled = true
const imageLog = []           // { pane, inject, name, bytes }
const voiceLog = { capabilities: 0, sessions: 0 }

function releaseUpload() {
  slowReleased = true
  const resolve = pendingUpload
  pendingUpload = null
  resolve?.()
}
/** 只有 slow 模式会挂起，用来观察「先占位、后上传」与上传中的发送拦截。 */
async function holdUpload() {
  if (imageMode !== 'slow' || slowReleased) return
  await new Promise((resolve) => { pendingUpload = resolve })
}

const sendJSON = (res, status, body) => { res.writeHead(status, { 'content-type': 'application/json' }); res.end(JSON.stringify(body)) }

const server = createServer(async (req, res) => {
  const url = new URL(req.url, 'http://localhost')
  const path = url.pathname

  // 图片 stage-only 上传。真实服务端按魔数嗅探；假端点只记录 `inject` 与文件名。
  const upload = path.match(/^\/api\/hosts\/media-test\/panes\/(p\d+)\/paste-image$/)
  if (upload) {
    let bytes = 0
    let body = ''
    for await (const chunk of req) { bytes += chunk.length; body += chunk.toString('latin1') }
    const name = /filename="([^"]*)"/.exec(body)?.[1] || ''
    imageLog.push({ pane: upload[1], inject: url.searchParams.get('inject'), name, bytes })
    await holdUpload()
    if (imageMode === 'fail') return sendJSON(res, 500, { code: 'internal_error', error: '图片上传失败，请重试' })
    return sendJSON(res, 200, { ok: true, path: `/remote/staged/${name}`, injected: false })
  }

  const transcript = path.match(/^\/api\/hosts\/media-test\/panes\/(p\d+)\/transcript$/)
  if (transcript) {
    const session = url.searchParams.get('session')
    if (!session) return sendJSON(res, 200, { supported: true, agent: AGENT, candidates: [{ id: `candidate-${transcript[1]}`, agent: AGENT, session_id: SESSION, updated_at: '2026-09-13T05:00:00.000Z' }], messages: [] })
    return sendJSON(res, 200, { supported: true, agent: AGENT, session_id: SESSION, binding: 'selected', messages: [], skipped: 0, has_more: false })
  }

  if (path === '/api/voice/capabilities') {
    voiceLog.capabilities += 1
    if (!voiceEnabled) return sendJSON(res, 200, { ...VOICE_CAPS, enabled: false, model: '' })
    return sendJSON(res, 200, VOICE_CAPS)
  }
  if (path === '/api/voice/sessions' && req.method === 'POST') {
    voiceLog.sessions += 1
    for await (const _chunk of req) { /* 丢弃请求体：只允许 {output}，这里不校验 */ }
    return sendJSON(res, 200, { ticket: `ticket-${voiceLog.sessions}`, expiresAt: '2026-09-13T06:00:00.000Z', capabilities: VOICE_CAPS })
  }

  const json = path === '/api/bootstrap/status' ? { required: false } : path === '/api/me' ? { user: { id: 'media-user', email: 'media@example.test', display_name: 'Media', role: 'admin' }, csrf_token: 'media-fixture', session_id: 'media-session' } : path === '/api/hosts/' ? { hosts: [host] } : path === '/api/hosts/media-test/' ? { host } : null
  if (json) { sendJSON(res, 200, json); return }
  if (path.startsWith('/api/')) { res.writeHead(404); res.end(); return }
  try {
    const file = (path.startsWith('/assets/') || path.startsWith('/brand/')) || ['/boot.js', '/sw.js', '/manifest.webmanifest', '/favicon.ico'].includes(path) ? path.slice(1) : 'index.html'
    res.setHeader('content-type', file.endsWith('.js') ? 'application/javascript' : file.endsWith('.css') ? 'text/css' : file.endsWith('.png') ? 'image/png' : file.endsWith('.ico') ? 'image/x-icon' : file.endsWith('.webmanifest') ? 'application/manifest+json' : 'text/html')
    // 与契约 §8.1 要求接线后的 server.go 一致：`microphone=(self)` 才允许本源的麦克风。
    res.setHeader('permissions-policy', 'camera=(), microphone=(self), geolocation=()')
    res.end(await readFile(join(dist, file)))
  } catch { res.writeHead(404); res.end() }
})
await new Promise((done) => server.listen(0, '127.0.0.1', done))
const base = `http://127.0.0.1:${server.address().port}`

// Valid synthetic 24×24 PNG: thumbnail decoding is part of the browser check.
function pngBytes() {
  return Array.from(Buffer.from('iVBORw0KGgoAAAANSUhEUgAAABgAAAAYCAIAAABvFaqvAAAAH0lEQVR4nGP4sM+NKohh1KBRg0YNGjVo1KBRgwbeIAADDGU9xCnH9wAAAABJRU5ErkJggg==', 'base64'))
}

async function fixture(browser, options, { mode = 'ok', voiceOn = true, sendDelayMs = 200 } = {}) {
  imageMode = mode
  slowReleased = false
  pendingUpload = null
  voiceEnabled = voiceOn
  imageLog.length = 0
  voiceLog.capabilities = 0
  voiceLog.sessions = 0

  const context = await browser.newContext(options)
  await context.grantPermissions(['microphone', 'clipboard-read', 'clipboard-write'], { origin: base }).catch(() => {})
  const page = await context.newPage()
  page.setDefaultTimeout(10_000)
  const messages = [], errors = [], relay = { frames: [], audio: 0, audioBytes: [], closed: [], socket: null }
  page.on('pageerror', (error) => errors.push(error.message))

  // 麦克风证据：计数 getUserMedia 与 MediaStreamTrack.stop，不改动任何应用代码。
  await page.addInitScript(() => {
    window.__herdrxMic = { getUserMedia: 0, stops: 0 }
    const media = navigator.mediaDevices
    if (media && typeof media.getUserMedia === 'function') {
      const original = media.getUserMedia.bind(media)
      media.getUserMedia = (...args) => { window.__herdrxMic.getUserMedia += 1; return original(...args) }
    }
    const stop = MediaStreamTrack.prototype.stop
    MediaStreamTrack.prototype.stop = function (...args) { window.__herdrxMic.stops += 1; return stop.apply(this, args) }
  })

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
        ws.send(JSON.stringify({ t: 'snapshot', snapshot }))
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
      } else if (message.t === 'call' && message.method === 'pane.send_input') {
        setTimeout(() => ws.send(JSON.stringify({ t: 'result', id: message.id, result: { type: 'ok' } })), sendDelayMs)
      } else if (message.t === 'call') {
        ws.send(JSON.stringify({ t: 'result', id: message.id, result: {} }))
      }
    })
  })

  // 假语音中继：收 `start` 后回 `ready`，其余帧由用例显式推送（ready/partial/transcript/assistantText）。
  await page.routeWebSocket('**/api/voice/ws**', (socket) => {
    relay.socket = socket
    socket.onMessage((raw) => {
      if (typeof raw !== 'string') { relay.audio += 1; relay.audioBytes.push(Buffer.from(raw).length); return }
      const frame = JSON.parse(raw)
      relay.frames.push(frame.t)
      if (frame.t === 'start') {
        socket.send(JSON.stringify({ t: 'ready', model: 'gpt-realtime', inputSampleRate: 24000, outputSampleRate: 24000, audioReply: false }))
      }
    })
    socket.onClose((code, reason) => { relay.closed.push({ code, reason }) })
  })

  const api = {
    context, page, messages, errors, imageLog, voice: relay,
    async push(frame) { relay.socket?.send(JSON.stringify(frame)) },
    async mic() { return page.evaluate(() => window.__herdrxMic) },
  }
  await page.goto(base + '/h/media-test')
  await expect(page.locator('.xterm-rows').first()).toContainText('Media terminal')
  return api
}

async function withFixture(browser, options, fixtureOptions, body) {
  const f = await fixture(browser, options, fixtureOptions)
  try { return await body(f) } finally { await f.context.close() }
}

async function screenshot(page, name) {
  if (!artifacts) return
  await mkdir(artifacts, { recursive: true })
  await page.screenshot({ path: join(artifacts, name + '.png'), fullPage: true })
}

const calls = (messages, method) => messages.filter((item) => item.t === 'call' && item.method === method)
const binaryFrames = (messages) => messages.filter((item) => typeof item.op === 'number')
const chatRegion = (page, index = 0) => page.getByRole('region', { name: '对话视图' }).nth(index)
const pickButton = (chat) => chat.getByRole('button', { name: '添加图片' })
const tray = (chat) => chat.locator('.chat-media-tray')
const input = (chat) => chat.getByRole('textbox', { name: '对话输入内容' })
const sendButton = (chat) => chat.getByRole('button', { name: '发送', exact: true })

/** 进对话视图：pane header 的开关优先，其次窄屏的移动端入口，最后是面板右键菜单。 */
async function enterChatView(page, index = 0) {
  const toggle = page.getByRole('switch', { name: '对话视图' })
  if (await toggle.count() > index && await toggle.nth(index).isVisible()) { await toggle.nth(index).click(); return }
  const switcher = page.getByRole('button', { name: '切换工作区或终端', exact: true })
  if (await switcher.count() > 0 && await switcher.first().isVisible()) {
    await switcher.first().click()
    await page.getByRole('button', { name: /切换到对话视图/ }).click()
    return
  }
  await page.locator('.pane-position').nth(index).click({ button: 'right' })
  await page.getByRole('menuitem', { name: '显示对话视图' }).click()
}

/** 选图入口（真实文件选择器），走「添加图片」按钮。 */
async function pickImage(page, chat, name) {
  const [chooser] = await Promise.all([page.waitForEvent('filechooser'), pickButton(chat).click()])
  await chooser.setFiles([{ name, mimeType: 'image/png', buffer: Buffer.from(pngBytes()) }])
}

/** 拖拽入口：dragover 与 drop 共用一个真实 DataTransfer，先给用例断言拖拽态的机会。 */
async function dragOver(page, target, files) {
  const dataTransfer = await page.evaluateHandle((items) => {
    const dt = new DataTransfer()
    for (const item of items) dt.items.add(new File([new Uint8Array(item.bytes)], item.name, { type: item.mime }))
    return dt
  }, files)
  await target.dispatchEvent('dragover', { dataTransfer })
  return dataTransfer
}
async function drop(page, target, dataTransfer) {
  await target.dispatchEvent('drop', { dataTransfer })
  await dataTransfer.dispose()
}
const imageFiles = (...names) => names.map((name) => ({ name, mime: 'image/png', bytes: pngBytes() }))

/** 粘贴入口：真实 ClipboardEvent（回退到带 clipboardData 的普通事件）。 */
async function pasteImages(target, files) {
  return target.evaluate((element, items) => {
    const dt = new DataTransfer()
    for (const item of items) dt.items.add(new File([new Uint8Array(item.bytes)], item.name, { type: item.mime }))
    let event = null
    try { event = new ClipboardEvent('paste', { clipboardData: dt, bubbles: true, cancelable: true }) } catch { /* 该引擎不支持 init 里的 clipboardData */ }
    if (!event || event.clipboardData !== dt) {
      event = new Event('paste', { bubbles: true, cancelable: true })
      Object.defineProperty(event, 'clipboardData', { value: dt })
    }
    element.dispatchEvent(event)
    return { prevented: event.defaultPrevented }
  }, files)
}

/** 等一次附件真正 staged：占位是先在 `staging` 出现的，这里等上传结果落地。 */
async function waitStaged(chat, count, status = 'staged') {
  const items = tray(chat).locator('.chat-media-item')
  await expect(items).toHaveCount(count)
  for (let index = 0; index < count; index++) await expect(items.nth(index)).toHaveAttribute('data-status', status)
}

const intersects = (a, b) => Boolean(a && b) && a.left < b.right - 0.5 && b.left < a.right - 0.5 && a.top < b.bottom - 0.5 && b.top < a.bottom - 0.5
const inside = (inner, outer, tolerance = 1) => Boolean(inner && outer) && inner.left >= outer.left - tolerance && inner.right <= outer.right + tolerance

function assertNoOverlap(geometry, label) {
  const { pick, voice, tray: trayBox, input: inputBox, send } = geometry
  for (const [name, box] of [['图片按钮', pick], ['语音区', voice], ['占位区', trayBox]]) {
    if (!box) continue
    assert.ok(!intersects(box, inputBox), `${label}: ${name}与输入框重叠 ${JSON.stringify(box)} ${JSON.stringify(inputBox)}`)
    assert.ok(!intersects(box, send), `${label}: ${name}与发送按钮重叠 ${JSON.stringify(box)} ${JSON.stringify(send)}`)
    assert.ok(inside(box, geometry.region), `${label}: ${name}超出对话区 ${JSON.stringify(box)}`)
  }
  assert.ok(!intersects(trayBox, pick), `${label}: 占位区与图片按钮重叠`)
  assert.ok(!intersects(trayBox, voice), `${label}: 占位区与语音区重叠`)
  if (trayBox && inputBox) assert.ok(trayBox.bottom <= inputBox.top + 1, `${label}: 占位区压住了输入框`)
  if (pick && inputBox) assert.ok(pick.bottom <= inputBox.top + 1, `${label}: 工具栏压住了输入框`)
}

const mobile = { viewport: { width: 390, height: 844 }, hasTouch: true, deviceScaleFactor: 2 }
const narrow = { viewport: { width: 320, height: 720 }, hasTouch: true }
const desktop = { viewport: { width: 1280, height: 800 } }

// 假麦克风 + 假 UI：Chromium 专属开关。其余引擎跳过语音用例（并如实打印）。
const launchArgs = [
  '--disable-gpu', '--disable-software-rasterizer', '--renderer-process-limit=2', '--js-flags=--max-old-space-size=384',
  '--use-fake-ui-for-media-stream', '--use-fake-device-for-media-stream', '--autoplay-policy=no-user-gesture-required',
]
const engines = process.env.HERDRX_TEST_ENGINES?.split(',') || ['chromium']
try {
  for (const name of engines) {
    const voiceEngine = name === 'chromium'
    if (!voiceEngine) console.log(`skip voice cases on ${name}: 假麦克风需要 Chromium 的 --use-fake-device-for-media-stream`)
    const browser = await ({ chromium, firefox, webkit })[name].launch({ headless: true, args: launchArgs })
    try {
      // 1. 选图 / 拖拽 / 粘贴：只落占位 + `inject=false` 上传，绝不向终端打字。
      await withFixture(browser, desktop, {}, async (f) => {
        await enterChatView(f.page)
        const chat = chatRegion(f.page)
        // 接线前置检查：ChatView 必须把 Composer 换成 ChatMediaComposer（契约 §8.4），
        // 否则整份回归没有任何意义，这里给出比超时更明确的原因。
        assert.ok(await pickButton(chat).count() > 0, '对话视图里没有图片附件入口：ChatView 尚未接上 ChatMediaComposer（契约 §8.4）')
        const before = f.messages.length

        // 选图
        await pickImage(f.page, chat, 'picked.png')
        await expect(tray(chat).locator('.chat-media-item')).toHaveCount(1)
        // 拖拽：先出现拖拽态与提示，drop 后再落一个占位。
        const dataTransfer = await dragOver(f.page, chat.locator('.chat-media-composer'), imageFiles('dropped.png'))
        await expect(chat.locator('.chat-media-composer')).toHaveAttribute('data-drag', 'true')
        await expect(chat.getByText('松开以添加图片')).toBeVisible()
        await drop(f.page, chat.locator('.chat-media-composer'), dataTransfer)
        await expect(chat.locator('.chat-media-composer')).not.toHaveAttribute('data-drag')
        // 粘贴
        const paste = await pasteImages(input(chat), imageFiles('pasted.png'))
        assert.equal(paste.prevented, true, '粘贴图片没有被媒体层接管（默认粘贴会同时把内容塞进输入框）')

        await waitStaged(chat, 3)
        await expect.poll(() => tray(chat).locator('img').evaluateAll((images) => images.length === 3 && images.every((image) => image.complete && image.naturalWidth === 24))).toBe(true)
        assert.deepEqual(imageLog.map((item) => ({ pane: item.pane, inject: item.inject, name: item.name })), [
          { pane: 'p1', inject: 'false', name: 'picked.png' },
          { pane: 'p1', inject: 'false', name: 'dropped.png' },
          { pane: 'p1', inject: 'false', name: 'pasted.png' },
        ], '图片上传没有固定 inject=false 或打到了别的 pane')
        for (const item of imageLog) assert.ok(item.bytes > 0, '图片上传没有带上文件内容')
        // 三条入口都不向终端打字：没有 send_input、没有 send_keys、没有原始输入帧。
        assert.equal(calls(f.messages, 'pane.send_input').length, 0, '加图片时向终端发送了输入')
        assert.equal(calls(f.messages, 'pane.send_keys').length, 0, '加图片时向终端注入了按键')
        assert.equal(binaryFrames(f.messages.slice(before)).length, 0, '加图片时向终端写了原始输入帧')
        assert.equal(await input(chat).inputValue(), '', '加图片时改动了草稿')
        await screenshot(f.page, `${name}-media-placeholders`)

        // 删除缩略图：占位消失，不补发上传，也不发送。
        await chat.getByRole('button', { name: '移除图片 dropped.png' }).click()
        await expect(tray(chat).locator('.chat-media-item')).toHaveCount(2)
        await expect(chat.getByText('dropped.png')).toHaveCount(0)
        assert.equal(imageLog.length, 3, '删除占位触发了重新上传')
        assert.equal(calls(f.messages, 'pane.send_input').length, 0, '删除占位触发了发送')

        // 图片 + 文本：Enter 只提交一次，文本与引用合成同一段，附件随后清空。
        await input(chat).fill('看这两张图')
        await input(chat).press('Enter')
        await expect.poll(() => calls(f.messages, 'pane.send_input').length).toBe(1)
        const sent = calls(f.messages, 'pane.send_input')[0].params
        assert.deepEqual(sent, { pane_id: 'p1', text: '看这两张图\n/remote/staged/picked.png\n/remote/staged/pasted.png', keys: ['Enter'] }, '文本与图片引用没有合成一次提交')
        await expect(tray(chat)).toHaveCount(0)
        await expect(input(chat)).toHaveValue('')

        // 仅图片：空正文也必须能发，提交文本就是引用行本身。
        await pickImage(f.page, chat, 'only.png')
        await waitStaged(chat, 1)
        await expect(sendButton(chat)).toBeEnabled()
        await sendButton(chat).click()
        await expect.poll(() => calls(f.messages, 'pane.send_input').length).toBe(2)
        assert.deepEqual(calls(f.messages, 'pane.send_input')[1].params, { pane_id: 'p1', text: '/remote/staged/only.png', keys: ['Enter'] }, '仅图片（空正文）没有发送提交文本')
        await expect(tray(chat)).toHaveCount(0)
        await screenshot(f.page, `${name}-media-sent`)
        assert.deepEqual(f.errors, [])
      })

      // 2. 上传中禁用发送；上传失败阻止发送，失败占位必须保留。
      await withFixture(browser, desktop, { mode: 'slow' }, async (f) => {
        await enterChatView(f.page)
        const chat = chatRegion(f.page)
        await input(chat).fill('上传中不能发')
        const dataTransfer = await dragOver(f.page, chat.locator('.chat-media-composer'), imageFiles('slow.png'))
        await drop(f.page, chat.locator('.chat-media-composer'), dataTransfer)
        await expect(tray(chat).locator('.chat-media-item')).toHaveCount(1)
        await expect(tray(chat).locator('.chat-media-item-staging')).toHaveCount(1)
        // 图片还在飞：按钮禁用，Enter 也不发。
        await expect(sendButton(chat)).toBeDisabled()
        await input(chat).press('Enter')
        await f.page.waitForTimeout(150)
        assert.equal(calls(f.messages, 'pane.send_input').length, 0, '上传中仍然把消息发出去了')
        releaseUpload()
        await waitStaged(chat, 1)
        await expect(sendButton(chat)).toBeEnabled()
        // 清掉这张已就绪的图：下一段只面对失败占位，空正文是否可发才能单独判定。
        await chat.getByRole('button', { name: '移除图片 slow.png' }).click()
        await expect(tray(chat)).toHaveCount(0)

        // 失败：占位变成 failed + 中文原因，空正文时发送仍然被阻止。
        imageMode = 'fail'
        await pickImage(f.page, chat, 'broken.png')
        await expect(tray(chat).locator('.chat-media-item-failed')).toHaveCount(1)
        await expect(chat.getByRole('alert')).toContainText('图片上传失败')
        assert.equal(calls(f.messages, 'pane.send_input').length, 0, '上传失败后仍然发送了消息')
        await input(chat).fill('')
        await expect(sendButton(chat)).toBeDisabled()
        await input(chat).press('Enter')
        await f.page.waitForTimeout(150)
        assert.equal(calls(f.messages, 'pane.send_input').length, 0, '空正文 + 上传失败仍然发送了消息')
        // 手动补文字后发送：只发文本，失败的图片绝不作为路径混进去，也不会自动重放。
        await input(chat).fill('失败的图不算')
        await input(chat).press('Enter')
        await expect.poll(() => calls(f.messages, 'pane.send_input').length).toBe(1)
        const sent = calls(f.messages, 'pane.send_input')[0].params
        assert.deepEqual(sent, { pane_id: 'p1', text: '失败的图不算', keys: ['Enter'] }, '失败的图片被当成引用发进了终端')
        assert.equal(imageLog.length, 2, '上传失败后自动重放了上传')
        await expect(tray(chat).locator('.chat-media-item-failed')).toHaveCount(1)
        assert.deepEqual(f.errors, [])
      })

      // 3. 作用域隔离：一个 pane 的附件不会出现、也不会被提交到另一个 pane。
      await withFixture(browser, desktop, {}, async (f) => {
        await enterChatView(f.page, 0)
        await enterChatView(f.page, 1)
        const first = chatRegion(f.page, 0)
        const second = chatRegion(f.page, 1)
        await pickImage(f.page, first, 'iso.png')
        await waitStaged(first, 1)
        assert.deepEqual(imageLog.map((item) => item.pane), ['p1'], '附件上传没有落在它所属的 pane')

        // 第二个 pane 看不到第一个 pane 的附件。
        await expect(tray(second)).toHaveCount(0)
        await expect(second.getByText('iso.png')).toHaveCount(0)
        await input(second).fill('第二个 pane 的消息')
        await input(second).press('Enter')
        await expect.poll(() => calls(f.messages, 'pane.send_input').length).toBe(1)
        assert.deepEqual(calls(f.messages, 'pane.send_input')[0].params, { pane_id: 'p2', text: '第二个 pane 的消息', keys: ['Enter'] }, '另一个 pane 的图片引用串进了提交文本')
        // 第一个 pane 的附件仍然完好，且没有被这次发送清掉。
        await expect(tray(first).locator('.chat-media-item')).toHaveCount(1)
        await expect(tray(first).locator('.chat-media-item-staged')).toHaveCount(1)
        assert.deepEqual(f.errors, [])
      })

      // 4. 语音：能力探测 → 假麦克风 → ready/partial/transcript 写草稿但不发送。
      if (voiceEngine) await withFixture(browser, desktop, {}, async (f) => {
        await enterChatView(f.page)
        const chat = chatRegion(f.page)
        const micButton = chat.getByRole('button', { name: '开始语音输入' })
        await expect(micButton).toBeVisible()
        assert.ok(voiceLog.capabilities > 0, '语音入口没有先探测服务端能力')

        await micButton.click()
        await expect(chat.getByRole('button', { name: '停止语音输入' })).toBeVisible()
        await expect(chat.getByText('正在聆听，识别结果会写入草稿')).toBeVisible()
        assert.equal((await f.mic()).getUserMedia, 1, '点击麦克风后没有取用麦克风')
        await expect.poll(() => voiceLog.sessions).toBe(1)
        await expect.poll(() => f.voice.frames.includes('start')).toBe(true)
        await expect.poll(() => f.voice.audio).toBeGreaterThan(0)
        assert.ok(f.voice.audioBytes.every((size) => size > 0 && size % 2 === 0 && size <= 65536), `上行音频帧不合规：${f.voice.audioBytes.join(',')}`)

        // ready 之后才是 partial：partial 只在按钮内联显示，不进草稿。
        await f.push({ t: 'partial', text: '帮我看一下这张' })
        await expect(chat.locator('.voice-input-partial')).toHaveText('帮我看一下这张')
        assert.equal(await input(chat).inputValue(), '', 'partial 识别结果被写进了草稿')
        // 最终 transcript 才是唯一写草稿的内容，且绝不触发发送。
        await f.push({ t: 'transcript', text: '帮我看一下这张图', final: true })
        await expect(input(chat)).toHaveValue('帮我看一下这张图')
        // 语音模型的回答不是终端 Agent 的话：不进草稿。
        await f.push({ t: 'assistantText', text: '我是语音助手，不是终端 Agent', final: true })
        await f.page.waitForTimeout(150)
        assert.equal(await input(chat).inputValue(), '帮我看一下这张图', '语音助手的回答被写进了草稿')
        assert.equal(calls(f.messages, 'pane.send_input').length, 0, '语音识别结果被自动发送了')
        assert.equal(calls(f.messages, 'pane.send_keys').length, 0, '语音识别结果被注入成按键')
        await screenshot(f.page, `${name}-voice-listening`)

        // 停止：先停麦克风再断连接，界面回到 idle。
        await chat.getByRole('button', { name: '停止语音输入' }).click()
        await expect(chat.getByRole('button', { name: '开始语音输入' })).toBeVisible()
        await expect.poll(() => f.voice.frames.includes('stop')).toBe(true)
        await expect.poll(() => f.voice.closed.length).toBe(1)
        assert.equal(f.voice.closed[0].code, 1000, `语音连接不是正常关闭：${JSON.stringify(f.voice.closed)}`)
        assert.ok((await f.mic()).stops >= 1, '停止语音后麦克风音轨没有被释放')
        assert.equal(await input(chat).inputValue(), '帮我看一下这张图', '停止语音时改动了草稿')

        // 再开一次，然后切回终端视图：连接与麦克风同样必须释放。
        await chat.getByRole('button', { name: '开始语音输入' }).click()
        await expect.poll(() => f.voice.frames.filter((frame) => frame === 'start').length).toBe(2)
        await expect(chat.getByText('正在聆听，识别结果会写入草稿')).toBeVisible()
        const stopsBefore = (await f.mic()).stops
        f.page.getByRole('switch', { name: '对话视图' }).first().click()
        await expect(f.page.getByRole('region', { name: '对话视图' })).toHaveCount(0)
        await expect.poll(() => f.voice.frames.filter((frame) => frame === 'stop').length).toBe(2)
        await expect.poll(() => f.voice.closed.length).toBe(2)
        await expect.poll(async () => (await f.mic()).stops).toBeGreaterThan(stopsBefore)
        assert.deepEqual(f.errors, [])
      })

      // 5. 未启用语音：隐藏入口，绝不触碰麦克风（不弹授权）。
      if (voiceEngine) await withFixture(browser, desktop, { voiceOn: false }, async (f) => {
        await enterChatView(f.page)
        const chat = chatRegion(f.page)
        await expect(pickButton(chat)).toBeVisible()
        assert.ok(voiceLog.capabilities > 0, '未启用语音时也应该探测一次能力')
        await f.page.waitForTimeout(200)
        await expect(chat.getByRole('button', { name: '开始语音输入' })).toHaveCount(0)
        assert.equal((await f.mic()).getUserMedia, 0, '未启用语音时仍然请求了麦克风')
        assert.deepEqual(f.errors, [])
      })

      // 6. 窄宽度：占位区、工具栏、输入框与发送按钮互不重叠，也不横向溢出。
      for (const [label, options] of [['390', mobile], ['320', narrow]]) {
        await withFixture(browser, options, {}, async (f) => {
          await enterChatView(f.page)
          const chat = chatRegion(f.page)
          await pickImage(f.page, chat, `wide${label}.png`)
          await waitStaged(chat, 1)
          const geometry = await chat.evaluate((root) => {
            const box = (selector) => {
              const element = root.querySelector(selector)
              if (!element || !element.getClientRects().length) return null
              const rect = element.getBoundingClientRect()
              return { left: rect.left, right: rect.right, top: rect.top, bottom: rect.bottom, width: rect.width, height: rect.height }
            }
            return {
              region: box('.chat-compose') || (() => { const rect = root.getBoundingClientRect(); return { left: rect.left, right: rect.right, top: rect.top, bottom: rect.bottom } })(),
              pick: box('.chat-media-pick'), voice: box('.voice-input'), tray: box('.chat-media-tray'),
              input: box('.composer-input'), send: box('.composer-send'),
              scroll: document.documentElement.scrollWidth, client: document.documentElement.clientWidth,
            }
          })
          assert.ok(geometry.input && geometry.send && geometry.pick, `${label}px：媒体工具栏或输入框没有渲染`)
          assert.ok(geometry.pick.width >= 44 && geometry.pick.height >= 44, `${label}px：图片按钮小于 44px 触控目标`)
          assertNoOverlap(geometry, `${name} ${label}px`)
          assert.ok(geometry.scroll <= geometry.client + 1, `${label}px：页面横向溢出 ${geometry.scroll} > ${geometry.client}`)
          if (voiceEngine) {
            await chat.getByRole('button', { name: '开始语音输入' }).click()
            // 紧凑布局按设计隐藏状态文案，这里用状态类与 aria 判定「已进入聆听」。
            await expect(chat.locator('.voice-input-listening')).toHaveCount(1)
            await expect(chat.getByRole('button', { name: '停止语音输入' })).toHaveAttribute('aria-pressed', 'true')
            const withVoice = await chat.evaluate((root) => {
              const box = (selector) => { const element = root.querySelector(selector); if (!element || !element.getClientRects().length) return null; const rect = element.getBoundingClientRect(); return { left: rect.left, right: rect.right, top: rect.top, bottom: rect.bottom } }
              return { region: box('.chat-compose'), pick: box('.chat-media-pick'), voice: box('.voice-input'), tray: box('.chat-media-tray'), input: box('.composer-input'), send: box('.composer-send'), scroll: document.documentElement.scrollWidth, client: document.documentElement.clientWidth }
            })
            assertNoOverlap(withVoice, `${name} ${label}px（聆听中）`)
            assert.ok(withVoice.scroll <= withVoice.client + 1, `${label}px：聆听时页面横向溢出`)
            await chat.getByRole('button', { name: '停止语音输入' }).click()
          }
          await screenshot(f.page, `${name}-media-${label}`)
          assert.deepEqual(f.errors, [])
        })
      }
    } finally {
      await browser.close()
    }
  }
  console.log('chat media browser regression passed')
} finally {
  await new Promise((done) => server.close(done))
}
