#!/usr/bin/env node
// 结构化对话（逐轮 Q/A）闭环验收：**真实服务二进制 + 真实 HTTP handler + 真实受限读取器**，
// 只有 Herdr 本身被替换成隔离的 mock（unix socket JSON API + `terminal session observe` 帧），
// 会话记录来自合成 JSONL。不连接任何真实 Herdr、pane、PTY、用户会话或真实用户日志。
//
// 与 scripts/test-chat-mode.mjs 的分工：那个脚本在浏览器里伪造 WebSocket 与 pane.read 响应，
// 验收的是「pane 视图开关 + 覆盖层」的几何与交互；本脚本不伪造任何前端数据源，走完整链路：
//
//   合成 JSONL（$HOME/.claude/projects/…）→ Go 受限读取器（openat + O_NOFOLLOW）
//     → GET /api/hosts/{id}/panes/{pane}/transcript（owner 认证）→ ChatView 渲染
//
// 用法：node scripts/test-structured-chat.mjs [/absolute/path/to/herdrx-server]
// 环境：HERDRX_TEST_CHROMIUM（浏览器可执行文件）、HERDRX_CHAT_ARTIFACTS（截图目录）、
//       HERDRX_TEST_ENGINES（默认 chromium）

import assert from 'node:assert/strict'
import { spawn } from 'node:child_process'
import { createServer } from 'node:net'
import { existsSync } from 'node:fs'
import { appendFile, chmod, mkdir, mkdtemp, readFile, rm, writeFile } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { chromium, expect } from '../web/node_modules/@playwright/test/index.mjs'

const repoRoot = fileURLToPath(new URL('..', import.meta.url))
const binary = resolve(process.argv[2] || join(repoRoot, 'bin/herdrx-server'))
const artifacts = process.env.HERDRX_CHAT_ARTIFACTS || join(repoRoot, 'artifacts/structured-chat')
const engines = process.env.HERDRX_TEST_ENGINES?.split(',').filter(Boolean) || ['chromium']
// 低内存机器上限制渲染进程与堆大小，避免浏览器自身被 OOM 掉。
const launchArgs = ['--disable-gpu', '--disable-software-rasterizer', '--renderer-process-limit=2', '--js-flags=--max-old-space-size=384']

// ── 合成事实 ──
//
// `CWD` 编码后是 `-srv-app`；`FOREIGN_CWD`（`/srv-app`）编码后**同样是** `-srv-app`。
// 目录名编码是有损的，所以这个碰撞正是「编码前缀不能证明 cwd 归属」的现场：
// 同目录下两个会话文件，只有记录内 cwd 精确等于 `/srv/app` 的那个才是本 pane 的候选。
const CWD = '/srv/app'
const FOREIGN_CWD = '/srv-app'
const SESSION_ID = 'aaaaaaaa-1111-4111-8111-111111111111'
const FOREIGN_SESSION_ID = 'cccccccc-3333-4333-8333-333333333333'
const PAGING_SESSION_ID = 'dddddddd-4444-4444-8444-444444444444'
const GOAL_TEXT = '把 README 里的构建命令列出来。'
const DUPLICATE_PROMPT = '再列一次刚才的构建命令。'
const TOOL_CALL_ID = 'toolu_01structuredchat'
const TOOL_OUTPUT = '# herdrx\n构建：make web-build\ngo test ./...'
const CODE_BLOCK = 'make web-build\nGOMAXPROCS=2 go test -count=1 ./...'
const TERMINAL_MARKER = 'TERMINAL-SCREEN-TEXT-终端屏幕文本'
const SKIPPED_LINE_TEXT = '这条上游漂移记录不该显示'

const uuid = (n) => `${String(n).padStart(8, '0')}-0000-4000-8000-000000000000`
const line = (value) => `${JSON.stringify(value)}\n`

function userRecord(id, text) {
  return line({ type: 'user', uuid: id, timestamp: '2026-09-13T05:00:00.000Z', cwd: CWD, message: { role: 'user', content: [{ type: 'text', text }] } })
}
function assistantRecord(id, blocks) {
  return line({ type: 'assistant', uuid: id, timestamp: '2026-09-13T05:00:01.000Z', cwd: CWD, message: { role: 'assistant', content: blocks } })
}
function toolResultRecord(id, callID, output, isError = false) {
  return line({ type: 'user', uuid: id, timestamp: '2026-09-13T05:00:02.000Z', cwd: CWD, message: { role: 'user', content: [{ type: 'tool_result', tool_use_id: callID, content: output, is_error: isError }] } })
}

const MAIN_ASSISTANT_1 = [
  { type: 'thinking', thinking: '这段思想链永远不该出现在界面上（THINKING-LEAK-MARKER）' },
  { type: 'text', text: `## 构建\n\n用 **pnpm** 加 \`make\`：\n\n\`\`\`sh\n${CODE_BLOCK}\n\`\`\`\n\n详见 [README](README.md) 与 [外链](https://example.test/docs)。` },
  { type: 'tool_use', id: TOOL_CALL_ID, name: 'Read', input: { file_path: 'README.md', limit: 40 } },
]
const MAIN_ASSISTANT_2 = [{ type: 'text', text: '构建命令是 `make web-build`，测试是 `go test ./...`。' }]
const MAIN_ASSISTANT_3 = [{ type: 'text', text: '第二次回答：命令同上，没有变化。' }]

/** 主会话：两轮问答 + 同源重复 prompt（两条都必须在）+ 工具调用与结果 + 一条被省略的 thinking。 */
function mainSession() {
  return line({ type: 'summary', summary: '会话摘要（元数据，不渲染）', cwd: CWD })
    + line({ type: 'system', uuid: uuid(900), cwd: CWD, subtype: 'meta' })
    + userRecord(uuid(1), GOAL_TEXT)
    + assistantRecord(uuid(2), MAIN_ASSISTANT_1)
    + toolResultRecord(uuid(3), TOOL_CALL_ID, TOOL_OUTPUT)
    + userRecord(uuid(4), DUPLICATE_PROMPT)
    + assistantRecord(uuid(5), MAIN_ASSISTANT_2)
    // 逐字相同的 prompt 再问一次：不同 uuid，两条都必须保留（禁止按文本 hash 去重）。
    + userRecord(uuid(6), DUPLICATE_PROMPT)
    + assistantRecord(uuid(7), MAIN_ASSISTANT_3)
    // 注入轮：isMeta 的 user 记录是系统注入，不是用户提问。
    + line({ type: 'user', uuid: uuid(8), cwd: CWD, isMeta: true, message: { role: 'user', content: [{ type: 'text', text: 'META-INJECTED-MARKER' }] } })
}

/** 上游字段漂移：一行是合法 JSON、也是消息形状，但内容块无法识别 → 计入 skipped。 */
function skippedLine() {
  return line({ type: 'assistant', uuid: uuid(999), timestamp: '2026-09-13T05:00:03.000Z', cwd: CWD, message: { role: 'assistant', content: [{ type: 'brand_new_block', text: SKIPPED_LINE_TEXT }] } })
}

/** 分页会话：260 条记录，超过一页默认上限（200 条），用来验证「加载更早的记录」。 */
function pagingSession() {
  const text = []
  for (let index = 0; index < 130; index++) {
    text.push(userRecord(uuid(1000 + index * 2), `分页问题 ${index}`))
    text.push(assistantRecord(uuid(1001 + index * 2), [{ type: 'text', text: `分页回答 ${index}` }]))
  }
  return text.join('')
}

// 编码规则与 Go 侧一致：非字母数字一律替换成 '-'（按字符，不按字节）。
const projectDir = (cwd) => cwd.split('').map((character) => /[A-Za-z0-9]/.test(character) ? character : '-').join('')

const MOCK_HERDR_DAEMON = `#!/usr/bin/env node
// 隔离的 Herdr JSON API socket mock：只回答 session.snapshot，其余方法记一条调用后回 ok。
// 每条调用都追加到 callsFile，供验收脚本断言「pane.read 从未被对话视图调用」。
import { createServer } from 'node:net'
import { appendFileSync, readFileSync } from 'node:fs'
const [socketPath, callsFile, snapshotPath] = process.argv.slice(2)
const snapshot = JSON.parse(readFileSync(snapshotPath, 'utf8'))
const record = (entry) => appendFileSync(callsFile, JSON.stringify(entry) + '\\n')
const server = createServer((conn) => {
  let buffer = ''
  conn.on('error', () => {})
  conn.on('data', (chunk) => {
    buffer += chunk.toString('utf8')
    let index
    while ((index = buffer.indexOf('\\n')) >= 0) {
      const raw = buffer.slice(0, index)
      buffer = buffer.slice(index + 1)
      let request = null
      try { request = JSON.parse(raw) } catch { continue }
      const method = String(request?.method ?? '')
      record({ kind: 'call', method, params: request?.params ?? null })
      let result = { type: 'ok' }
      if (method === 'session.snapshot') result = { type: 'ok', snapshot }
      else if (method === 'pane.read') result = { type: 'pane_read', read: { text: '${TERMINAL_MARKER}\\\\n' } }
      conn.write(JSON.stringify({ id: request?.id ?? 'herdrx', result }) + '\\n')
    }
  })
})
server.listen(socketPath)
`

// 注意：这个文件没有扩展名，Node 会按 CommonJS 解析，所以必须用 require 而不是 import。
// 第一个参数由验收脚本注入（HERDRX_FAKE_HERDR_SPAWNS），其余参数就是 Go 侧真实传来的 argv。
const FAKE_HERDR_CLI = `#!/usr/bin/env node
// 隔离的 herdr CLI mock：只实现 'terminal session observe <pane> --cols N --rows M'。
// 每次启动都追加一行 spawn 记录，验收脚本据此断言「切视图不会重开终端流」。
const { appendFileSync } = require('node:fs')
const args = process.argv.slice(2)
appendFileSync(process.env.HERDRX_FAKE_HERDR_SPAWNS, JSON.stringify({ kind: 'spawn', args }) + '\\n')
if (args[0] !== 'terminal') process.exit(1)
const pane = args[3] || 'unknown'
const frame = (seq, full, text) => JSON.stringify({
  type: 'terminal.frame', seq, encoding: 'ansi', width: 80, height: 24, full,
  bytes: Buffer.from(text, 'utf8').toString('base64'),
})
process.stdout.write(frame(1, true, '\\u001b[2J\\u001b[H${TERMINAL_MARKER} ' + pane + '\\r\\n') + '\\n')
process.stdin.resume()
process.stdin.on('data', () => {})
setInterval(function () {}, 1 << 30)
`

function snapshotFixture({ width }) {
  const half = Math.floor(width / 2)
  return {
    version: 'mock-0.8.2', protocol: 1,
    focused_workspace_id: 'w1', focused_tab_id: 't1', focused_pane_id: 'p1',
    workspaces: [{ workspace_id: 'w1', label: '结构化对话验收', number: 1, active_tab_id: 't1', agent_status: 'idle', focused: true, pane_count: 2, tab_count: 1 }],
    tabs: [{ workspace_id: 'w1', tab_id: 't1', label: '1', number: 1, pane_count: 2, agent_status: 'idle', focused: true }],
    panes: [1, 2].map((index) => ({
      workspace_id: 'w1', tab_id: 't1', pane_id: `p${index}`, terminal_id: `term${index}`,
      label: `终端 ${index}`, agent: 'claude', agent_status: 'idle',
      cwd: CWD, foreground_cwd: CWD, focused: index === 1, revision: 1,
    })),
    layouts: [{
      workspace_id: 'w1', tab_id: 't1', area: { x: 0, y: 0, width: width, height: 40 }, focused_pane_id: 'p1',
      splits: [{ id: 's1', direction: 'right', ratio: 0.5, rect: { x: 0, y: 0, width: width, height: 40 } }],
      zoomed: false,
      panes: [1, 2].map((index) => ({ pane_id: `p${index}`, rect: { x: (index - 1) * half, y: 0, width: half, height: 40 } })),
    }],
    agents: [],
  }
}

// ── 进程与浏览器清理台账：任何失败路径都必须走 finally 全部释放 ──
const resources = { server: null, daemon: null, browser: null, contexts: [], temp: null }

async function freePort() {
  return new Promise((done) => { const probe = createServer(); probe.listen(0, '127.0.0.1', () => { const port = probe.address().port; probe.close(() => done(port)) }) })
}

async function screenshot(page, name, fullPage = true) {
  await mkdir(artifacts, { recursive: true })
  await page.screenshot({ path: join(artifacts, `${name}.png`), fullPage }).catch(() => {})
}

async function stopChild(child) {
  if (!child || child.exitCode !== null) return
  child.kill('SIGTERM')
  await new Promise((done) => {
    const timer = setTimeout(() => { child.kill('SIGKILL'); done() }, 3000)
    child.once('exit', () => { clearTimeout(timer); done() })
  })
}

/** 等一个文件出现并追加了至少 count 行。 */
async function callLines(path) {
  try {
    return (await readFile(path, 'utf8')).split('\n').filter(Boolean).map((raw) => JSON.parse(raw))
  } catch { return [] }
}

async function main() {
  const root = await mkdtemp(join(tmpdir(), 'herdrx-structured-chat-'))
  resources.temp = root
  const home = join(root, 'home')
  const configDir = join(root, 'config')
  const dataDir = join(root, 'data')
  const binDir = join(root, 'bin')
  const callsFile = join(root, 'herdr-calls.ndjson')
  const spawnFile = join(root, 'herdr-spawns.ndjson')
  const snapshotPath = join(root, 'snapshot.json')
  for (const dir of [home, configDir, dataDir, binDir, join(configDir, 'herdr'), join(home, '.claude', 'projects', projectDir(CWD))]) {
    await mkdir(dir, { recursive: true })
  }

  // 合成会话日志。全部落在临时 HOME 里，不读任何真实用户日志。
  const sessionDir = join(home, '.claude', 'projects', projectDir(CWD))
  const sessionFile = join(sessionDir, `${SESSION_ID}.jsonl`)
  await writeFile(sessionFile, mainSession())
  await writeFile(join(sessionDir, `${FOREIGN_SESSION_ID}.jsonl`), line({ type: 'assistant', uuid: uuid(50), cwd: FOREIGN_CWD, message: { role: 'assistant', content: [{ type: 'text', text: 'FOREIGN-CWD-MARKER 这份记录属于另一个工作目录。' }] } }))
  assert.equal(projectDir(CWD), projectDir(FOREIGN_CWD), 'fixture must collide on the encoded directory name')

  // mock Herdr：socket 守护 + CLI 双子进程。
  const daemonPath = join(root, 'mock-herdr-daemon.mjs')
  const cliPath = join(binDir, 'herdr')
  await writeFile(daemonPath, MOCK_HERDR_DAEMON)
  await writeFile(cliPath, FAKE_HERDR_CLI)
  await chmod(cliPath, 0o755)

  const port = await freePort()
  const base = `http://127.0.0.1:${port}`
  await writeFile(snapshotPath, JSON.stringify(snapshotFixture({ width: 160 })))

  const socketPath = join(configDir, 'herdr', 'herdr.sock')
  const daemonLog = []
  // socket 就绪探测：等监听文件真正出现，而不是假设 spawn 成功就等于 listen 成功。
  const launchDaemon = async () => {
    await rm(socketPath, { force: true }).catch(() => {})
    const daemon = spawn(process.execPath, [daemonPath, socketPath, callsFile, snapshotPath], { stdio: ['ignore', 'pipe', 'pipe'] })
    resources.daemon = daemon
    daemon.stdout.on('data', (chunk) => daemonLog.push(String(chunk)))
    daemon.stderr.on('data', (chunk) => daemonLog.push(String(chunk)))
    daemon.on('error', (error) => daemonLog.push(String(error)))
    for (let attempt = 0; attempt < 200; attempt++) {
      if (daemon.exitCode !== null) throw new Error(`mock herdr daemon exited early: ${daemonLog.join('')}`)
      if (existsSync(socketPath)) return daemon
      await new Promise((done) => setTimeout(done, 50))
    }
    throw new Error(`mock herdr daemon did not listen on ${socketPath}: ${daemonLog.join('')}`)
  }
  await launchDaemon()

  const server = spawn(binary, [], {
    env: {
      ...process.env,
      HOME: home,
      XDG_CONFIG_HOME: configDir,
      XDG_CACHE_HOME: join(root, 'cache'),
      PATH: `${binDir}:${process.env.PATH ?? ''}`,
      HERDRX_FAKE_HERDR_SPAWNS: spawnFile,
      HERDRX_DATA_DIR: dataDir,
      HERDRX_ADDR: `127.0.0.1:${port}`,
      HERDRX_PUBLIC_URL: base,
      HERDRX_COOKIE_SECURE: 'false',
      HERDRX_HERDR_BIN: cliPath,
      HERDRX_BOOTSTRAP_TOKEN: '',
      HERDRX_REGISTRATION: 'closed',
      HERDRX_ALLOWED_ORIGINS: '',
      HERDRX_TRUSTED_PROXIES: '',
      HERDRX_SESSION_TTL: '1h',
    },
    stdio: ['ignore', 'pipe', 'pipe'],
  })
  resources.server = server
  const serverLog = []
  server.stdout.on('data', (chunk) => serverLog.push(String(chunk)))
  server.stderr.on('data', (chunk) => serverLog.push(String(chunk)))

  const ready = async () => {
    for (let attempt = 0; attempt < 150; attempt++) {
      if (server.exitCode !== null) throw new Error(`isolated website exited early: ${serverLog.join('')}`)
      if (await fetch(`${base}/healthz`).then((response) => response.ok).catch(() => false)) return
      await new Promise((done) => setTimeout(done, 100))
    }
    throw new Error('isolated website did not become ready')
  }
  await ready()

  const password = 'structured-chat-acceptance-password'
  const browser = await chromium.launch({ headless: true, args: launchArgs, executablePath: process.env.HERDRX_TEST_CHROMIUM || undefined })
  resources.browser = browser
  const context = await browser.newContext({ viewport: { width: 1280, height: 800 } })
  resources.contexts.push(context)
  await context.grantPermissions(['clipboard-read', 'clipboard-write'], { origin: base })
  const page = await context.newPage()
  const pageErrors = []
  page.on('pageerror', (error) => pageErrors.push(error.message))
  page.setDefaultTimeout(20_000)

  // 1) 初始化管理员并登录。
  await page.goto(base)
  await page.getByLabel('显示名称').fill('Acceptance')
  await page.getByLabel('邮箱', { exact: true }).fill('acceptance@example.test')
  await page.getByLabel('密码', { exact: true }).fill(password)
  await page.getByLabel('初始化令牌').fill((await readFile(join(dataDir, 'bootstrap-token'), 'utf8')).trim())
  await page.getByRole('button', { name: '创建管理员' }).click()
  await expect(page.getByRole('heading', { name: '主机', exact: true })).toBeVisible()

  // 2) 通过真实 HTTP 端点创建本机主机（管理员专属接入）。
  const host = await page.evaluate(async () => {
    const me = await fetch('/api/me', { credentials: 'same-origin' }).then((response) => response.json())
    const response = await fetch('/api/hosts/', {
      method: 'POST',
      credentials: 'same-origin',
      headers: { 'content-type': 'application/json', 'x-csrf-token': me.csrf_token },
      body: JSON.stringify({ name: '结构化对话验收', transport: 'local' }),
    })
    if (!response.ok) throw new Error(`create host failed: ${response.status} ${await response.text()}`)
    return (await response.json()).host
  })
  assert.ok(host?.id, 'host was not created')

  await page.goto(`${base}/h/${host.id}`)
  const paneSwitch = page.getByRole('switch', { name: '对话视图' }).first()
  await expect(paneSwitch).toBeVisible()
  // 开关是**单个** role="switch"，不是两个按钮拼出的分段控件。
  await expect(paneSwitch).toHaveAttribute('aria-checked', 'false')
  assert.equal(await page.getByRole('switch', { name: '对话视图' }).count(), 2, 'expected exactly one switch per pane')
  assert.equal(await page.locator('.pane-view-toggle').first().getByRole('tab').count(), 0, 'the toggle must not be a segmented control')
  await page.locator('.xterm-rows').first().filter({ hasText: TERMINAL_MARKER }).waitFor()

  const spawnCount = async () => (await callLines(spawnFile)).filter((entry) => entry.kind === 'spawn').length
  const callMethods = async () => (await callLines(callsFile)).map((entry) => entry.method)
  await expect.poll(spawnCount, { timeout: 10_000 }).toBeGreaterThanOrEqual(1)
  const spawnsOnTerminal = await spawnCount()
  const readsOnTerminal = (await callMethods()).filter((method) => method === 'pane.read').length
  await screenshot(page, '01-terminal')

  // 3) 切到对话视图：终端流不重开，视图区域换成结构化对话。
  await paneSwitch.click()
  await expect(paneSwitch).toHaveAttribute('aria-checked', 'true')
  const chat = page.getByRole('region', { name: '对话视图' }).first()
  await expect(chat).toBeVisible()
  await expect(page.locator('.terminal-viewport').first()).toHaveAttribute('aria-hidden', 'true')
  assert.equal(await spawnCount(), spawnsOnTerminal, 'entering the chat view reopened the terminal stream')
  await screenshot(page, '02-chat-candidates')

  // 4) 候选：必须先显式选择，绝不自动认领；跨 cwd 的同名编码目录不算候选。
  await expect(chat.getByRole('heading', { name: '选择此终端的会话记录' })).toBeVisible()
  await expect(chat.getByRole('article')).toHaveCount(0)
  const candidates = chat.locator('.chat-candidate')
  assert.equal(await candidates.count(), 1, 'exactly one candidate belongs to this pane cwd')
  await expect(candidates.first()).toContainText('claude')
  await expect(candidates.first()).toContainText('aaaaaaaa')

  // 5) 选择会话 → 真实逐轮 Q/A 从 JSONL 渲染出来。
  await candidates.first().click()
  await expect(chat.locator('.chat-turn')).toHaveCount(3)
  const userBubbles = chat.locator('.chat-message-user')
  await expect(userBubbles.first()).toContainText(GOAL_TEXT)
  // 同源重复 prompt 的两条都必须在（同文不同 id，禁止按文本去重）。
  assert.equal(await chat.locator('.chat-bubble', { hasText: DUPLICATE_PROMPT }).count(), 2, 'a repeated prompt must keep both records')
  // 注入轮与 thinking 都不渲染。
  await expect(chat.getByText('META-INJECTED-MARKER')).toHaveCount(0)
  await expect(chat.getByText(/THINKING-LEAK-MARKER/)).toHaveCount(0)
  // 工具结果记录是派生角色 tool，不能变成用户气泡。
  const toolMessages = chat.locator('.chat-message[data-role="tool"]')
  assert.equal(await toolMessages.count(), 1, 'the tool-result record must be re-roled to tool')
  await expect(toolMessages.first()).toContainText('Read')
  assert.equal(await chat.locator('.chat-message-user', { hasText: TOOL_OUTPUT.split('\n')[0] }).count(), 0, 'tool output leaked into a user bubble')
  // 工具调用与结果都是可折叠分区，带真实工具名与 call id。
  const toolCall = chat.locator('.chat-tool-call').first()
  await expect(toolCall).toContainText('工具调用')
  await expect(toolCall).toContainText(TOOL_CALL_ID)
  const toolResult = chat.locator('.chat-tool-result').first()
  await expect(toolResult).toContainText('工具结果')
  await toolResult.locator('summary').click()
  await expect(toolResult).toContainText('make web-build')
  // 助手正文是 Markdown 文档流。
  await expect(chat.locator('.chat-markdown h2').first()).toContainText('构建')
  await expect(chat.locator('.chat-markdown strong').first()).toContainText('pnpm')
  // 对话视图完全没有读终端屏幕文本。
  assert.equal((await callMethods()).filter((method) => method === 'pane.read').length, readsOnTerminal, 'the chat view read terminal screen text')
  await screenshot(page, '03-chat-qa')

  // 6) Markdown / 代码块复制。
  const codeCopy = chat.locator('.chat-code .chat-copy').first()
  await codeCopy.click()
  // 代码块内容带尾随换行，比较时按行归一化，避免把无关空白当成缺陷。
  await expect.poll(() => page.evaluate(() => navigator.clipboard.readText())).toBe(`${CODE_BLOCK}\n`)
  await expect(chat.locator('.chat-code-lang').first()).toContainText('sh')
  const assistantCopy = chat.locator('.chat-message-assistant .chat-message-copy').first()
  await assistantCopy.click()
  await expect.poll(() => page.evaluate(() => navigator.clipboard.readText())).toContain('make web-build')

  // 7) 增量：追加一条新记录，轮询自动出现（不刷新、不点任何按钮）。
  await appendFile(sessionFile, userRecord(uuid(20), '增量问题：刚刚新增的一条。') + assistantRecord(uuid(21), [{ type: 'text', text: '增量回答：来自磁盘的新记录。' }]))
  await expect(chat.getByText('增量回答：来自磁盘的新记录。')).toBeVisible({ timeout: 15_000 })
  assert.equal(await chat.locator('.chat-turn').count(), 4)
  await screenshot(page, '04-chat-incremental')

  // 8) 同 id 原地更新 + 追加：改写助手 a2（同 uuid、内容更长）并追加一条，再点击刷新。
  const rewritten = mainSession().replace(assistantRecord(uuid(5), MAIN_ASSISTANT_2), assistantRecord(uuid(5), [{ type: 'text', text: '更新后的第二轮回答：同一条记录被原地替换。' }]))
  await writeFile(sessionFile, rewritten + userRecord(uuid(20), '增量问题：刚刚新增的一条。') + assistantRecord(uuid(21), [{ type: 'text', text: '增量回答：来自磁盘的新记录。' }]) + userRecord(uuid(22), '刷新后追加的新问题。') + assistantRecord(uuid(23), [{ type: 'text', text: '刷新后追加的新回答。' }]) + skippedLine())
  await chat.getByRole('button', { name: '刷新会话记录' }).click()
  await expect(chat.getByText('更新后的第二轮回答：同一条记录被原地替换。')).toBeVisible()
  assert.equal(await chat.getByText('构建命令是 `make web-build`，测试是 `go test ./...`。').count(), 0, 'the superseded record was not replaced in place')
  await expect(chat.getByText('刷新后追加的新回答。')).toBeVisible()
  // skipped 只统计真正无法识别的**消息**行，元数据与省略的 thinking 不计入。
  await expect(chat.locator('.chat-skipped')).toContainText('1')
  await screenshot(page, '05-chat-upsert')

  // 9) 反向分页：260 条记录超过一页默认上限，必须给出「加载更早的记录」。
  await writeFile(join(sessionDir, `${PAGING_SESSION_ID}.jsonl`), pagingSession())
  await chat.getByRole('button', { name: '重新选择会话记录' }).click()
  const allCandidates = chat.locator('.chat-candidate')
  await expect(allCandidates).toHaveCount(2)
  await expect(allCandidates.filter({ hasText: 'dddddddd' })).toHaveCount(1)
  await allCandidates.filter({ hasText: 'dddddddd' }).click()
  // 260 条记录 / 一页 200 条上限 = 首页 200 条（100 轮），剩下的必须能反向取回。
  await expect(chat.locator('.chat-message')).toHaveCount(200)
  await expect(chat.locator('.chat-turn')).toHaveCount(100)
  const earlier = chat.getByRole('button', { name: '加载更早的记录' })
  await expect(earlier).toBeVisible()
  await screenshot(page, '06-chat-paging-tail')
  // 单次点击必须生效：轮询在途时用户点「加载更早」不能被静默吞掉。
  await earlier.click()
  await expect.poll(async () => chat.locator('.chat-message').count(), { timeout: 15_000 }).toBeGreaterThan(200)
  await expect(chat.getByText('分页问题 0')).toBeAttached()
  // 走到文件开头后，按钮必须消失（服务端不再给 previous_cursor）。
  await expect(earlier).toHaveCount(0, { timeout: 15_000 })
  await screenshot(page, '07-chat-paging-earlier')

  // 10) 断线：主机不可达 → 读取暂停并给出可执行提示；主机恢复后自动继续。
  //     这条链路走的是真实 WS 状态机（服务端 3 次快照失败 → conn degraded/offline），
  //     不是浏览器层的假离线。
  const offlineNotice = chat.getByText('主机未连接，已暂停读取会话记录，也不能发送；重新连接后会自动继续。')
  await stopChild(resources.daemon)
  resources.daemon = null
  await expect(offlineNotice).toBeVisible({ timeout: 25_000 })
  await screenshot(page, '08-chat-disconnected')
  await launchDaemon()
  await expect(offlineNotice).toBeHidden({ timeout: 60_000 })

  // 11) 切回终端：xterm 仍在，终端流没有被重开。
  await paneSwitch.click()
  await expect(paneSwitch).toHaveAttribute('aria-checked', 'false')
  await expect(page.getByRole('region', { name: '对话视图' })).toHaveCount(0)
  await page.locator('.xterm-rows').first().filter({ hasText: TERMINAL_MARKER }).waitFor()
  await page.waitForTimeout(800)
  assert.equal(await spawnCount(), spawnsOnTerminal, 'switching back reopened the terminal stream')
  await screenshot(page, '09-terminal-restored')

  // 12) 窄 pane：280 / 320 / 496px，对话视图不越界。
  await page.setViewportSize({ width: 1280, height: 800 })
  await paneSwitch.click()
  await expect(page.getByRole('region', { name: '对话视图' }).first()).toBeVisible()
  // 重新进入对话视图会新建一个只读会话（pane 视图切换会卸载 ChatView），
  // 先显式重选一次，让窄宽度量的是**真实消息**（Markdown、代码块、工具卡片）而不是候选列表。
  await chat.locator('.chat-candidate').filter({ hasText: 'aaaaaaaa' }).click()
  await expect(chat.locator('.chat-tool-call').first()).toBeVisible()
  for (const target of [496, 320, 280]) {
    const measured = await aimPaneWidth(page, target)
    assert.ok(Math.abs(measured.actual - target) <= 6, `could not size the pane to ${target}px (got ${measured.actual})`)
    const geometry = await chatGeometry(page)
    assert.ok(geometry.pane.width > 0, `pane collapsed at ${target}px`)
    assert.ok(geometry.chat.right <= geometry.pane.right + 1 && geometry.chat.left >= geometry.pane.left - 1, `chat view escapes the pane at ${measured.actual}px: ${JSON.stringify(geometry)}`)
    assert.ok(geometry.log.scrollWidth <= geometry.log.clientWidth + 1, `chat log overflows horizontally at ${measured.actual}px: ${geometry.log.scrollWidth} > ${geometry.log.clientWidth}`)
    assert.ok(geometry.toggle.width >= 24 && geometry.toggle.top >= geometry.pane.top - 1 && geometry.toggle.bottom <= geometry.pane.bottom + 1, `view toggle collapsed or escaped at ${measured.actual}px`)
    const composer = await page.getByRole('textbox', { name: '对话输入内容' }).first().boundingBox()
    assert.ok(composer && composer.width > 60, `chat composer collapsed at ${measured.actual}px`)
    assert.ok(composer.x >= geometry.pane.left - 1 && composer.x + composer.width <= geometry.pane.right + 1, `chat composer escapes the pane at ${measured.actual}px`)
    const documentOverflow = await page.evaluate(() => ({ scroll: document.documentElement.scrollWidth, client: document.documentElement.clientWidth }))
    assert.ok(documentOverflow.scroll <= documentOverflow.client + 1, `page overflows horizontally at pane ${measured.actual}px`)
    await screenshot(page, `10-pane-${target}`, false)
  }

  // 13) REST 契约与安全：降级一律 HTTP 200；伪造的不透明令牌拿不到任何记录；
  //     非 owner 的主机是 404；路径与游标只由服务端派生。
  const api = await transcriptAPI(page, host.id)
  const forged = await api.get(`/api/hosts/${host.id}/panes/p1/transcript?session=${encodeURIComponent('not-a-real-token')}`)
  assert.equal(forged.status, 200, 'a degraded transcript read must stay HTTP 200')
  assert.equal(forged.body.supported, true)
  assert.equal(forged.body.reason, 'session_unavailable')
  assert.equal(forged.body.reset, true)
  assert.deepEqual(forged.body.messages, [])
  assert.ok(Array.isArray(forged.body.candidates) && forged.body.candidates.length === 2, 'session loss must re-offer the candidates')
  const badCursor = await api.get(`/api/hosts/${host.id}/panes/p1/transcript?session=${encodeURIComponent(forged.body.candidates[0].id)}&cursor=${encodeURIComponent('../../../../etc/passwd')}`)
  assert.equal(badCursor.status, 400, 'a malformed cursor must be rejected before any decryption')
  const badBefore = await api.get(`/api/hosts/${host.id}/panes/p1/transcript?session=abc&before=${encodeURIComponent('..%2f..%2fetc%2fpasswd')}`)
  assert.equal(badBefore.status, 400)
  const unknownPane = await api.get(`/api/hosts/${host.id}/panes/nope/transcript`)
  assert.equal(unknownPane.status, 404)
  const candidate = forged.body.candidates.find((item) => item.session_id === PAGING_SESSION_ID) || forged.body.candidates[0]
  assert.ok(!JSON.stringify(forged.body).includes(CWD), 'the candidate list must not leak any filesystem path')
  assert.ok(!JSON.stringify(forged.body.candidates).includes('分页问题'), 'the candidate list must not leak any prompt text')
  const page1 = await api.get(`/api/hosts/${host.id}/panes/p1/transcript?session=${encodeURIComponent(candidate.id)}`)
  assert.equal(page1.status, 200)
  assert.equal(page1.body.binding, 'selected')
  assert.ok(page1.body.messages.length > 0 && page1.body.messages.length <= 200)
  assert.ok(page1.body.next_cursor)
  const page1Ids = new Set(page1.body.messages.map((record) => record.id))
  const page2 = await api.get(`/api/hosts/${host.id}/panes/p1/transcript?session=${encodeURIComponent(candidate.id)}&cursor=${encodeURIComponent(page1.body.next_cursor)}`)
  assert.equal(page2.status, 200)
  const overlap = page2.body.messages.filter((record) => page1Ids.has(record.id))
  assert.equal(overlap.length, 0, 'the forward cursor re-delivered records that were already returned')
  const forged2 = await api.get(`/api/hosts/${host.id}/panes/p1/transcript?session=${encodeURIComponent(forged.body.candidates[0].id + 'x')}`)
  assert.equal(forged2.status, 200)
  assert.equal(forged2.body.reason, 'session_unavailable')
  assert.deepEqual(forged2.body.messages, [], 'a tampered token must not open a read')

  // 14) 第二个登录身份（不是同一份 cookie）读不到这台主机：owner 边界必须落在服务端。
  const invite = await page.evaluate(async () => {
    const me = await fetch('/api/me', { credentials: 'same-origin' }).then((response) => response.json())
    const settings = await fetch('/api/admin/settings', { credentials: 'same-origin' }).then((response) => response.json())
    const patched = await fetch('/api/admin/settings', {
      method: 'PATCH', credentials: 'same-origin',
      headers: { 'content-type': 'application/json', 'x-csrf-token': me.csrf_token },
      body: JSON.stringify({ registration: 'invite', revision: settings.settings?.revision ?? settings.revision }),
    })
    if (!patched.ok) throw new Error(`open registration failed: ${patched.status} ${await patched.text()}`)
    const created = await fetch('/api/admin/invites', { method: 'POST', credentials: 'same-origin', headers: { 'content-type': 'application/json', 'x-csrf-token': me.csrf_token }, body: '{}' })
    if (!created.ok) throw new Error(`create invite failed: ${created.status} ${await created.text()}`)
    return (await created.json()).code
  })
  const other = await browser.newContext({ viewport: { width: 1280, height: 800 } })
  resources.contexts.push(other)
  const otherPage = await other.newPage()
  await otherPage.goto(base)
  const registered = await otherPage.evaluate(async ({ code, password: secret }) => {
    const response = await fetch('/api/register', {
      method: 'POST',
      credentials: 'same-origin',
      headers: { 'content-type': 'application/json' },
      body: JSON.stringify({ email: 'stranger@example.test', display_name: 'Stranger', password: secret, invite_code: code }),
    })
    return response.status
  }, { code: invite, password })
  assert.equal(registered, 200, 'the second identity could not register')
  const strangerHosts = await otherPage.evaluate(async () => (await fetch('/api/hosts/', { credentials: 'same-origin' }).then((response) => response.json())).hosts.length)
  assert.equal(strangerHosts, 0, 'the second identity must not see the first identity host')
  // Playwright 的 Page.goto 返回的是 Playwright Response，`status` 在那里是**方法**，
  // 不是属性（只有 fetch 的 Response 才是属性）。
  const foreign = await otherPage.goto(`${base}/h/${host.id}`).then((response) => response.status())
  assert.equal(foreign, 200, 'deep link should still render the SPA shell')
  const probe = await otherPage.evaluate(async (hostID) => (await fetch(`/api/hosts/${hostID}/panes/p1/transcript`, { credentials: 'same-origin' })).status, host.id)
  assert.equal(probe, 404, 'another login must not read this host transcript')
  // 匿名请求必须在认证层就被挡住，而不是掉进降级分支。
  const anonymous = await fetch(`${base}/api/hosts/${host.id}/panes/p1/transcript`)
  assert.equal(anonymous.status, 401, 'an anonymous transcript read must be rejected')

  assert.deepEqual(pageErrors, [], 'the page logged uncaught errors')
  console.log(`structured chat end-to-end passed: candidates=2 rounds=3 paging=${page1.body.messages.length}+ messages, screenshots in ${artifacts}`)

  /** 走真实 HTTP（浏览器 cookie + CSRF 由同源请求携带）读一页结构化记录。 */
  async function transcriptAPI(targetPage, hostID) {
    return {
      get: async (path) => targetPage.evaluate(async (requestPath) => {
        const response = await fetch(requestPath, { credentials: 'same-origin', headers: { accept: 'application/json' } })
        return { status: response.status, body: await response.json().catch(() => ({})) }
      }, path),
      hostID,
    }
  }

  async function chatGeometry(targetPage) {
    return targetPage.evaluate(() => {
      const rect = (element) => {
        const box = element?.getBoundingClientRect()
        return box ? { left: box.left, top: box.top, right: box.right, bottom: box.bottom, width: box.width, height: box.height } : null
      }
      const pane = document.querySelector('.terminal-pane')
      const log = document.querySelector('.chat-log')
      return {
        pane: rect(pane),
        chat: rect(document.querySelector('.chat-view')),
        toggle: rect(pane?.querySelector('.pane-view-toggle')),
        log: { scrollWidth: log?.scrollWidth ?? 0, clientWidth: log?.clientWidth ?? 0 },
      }
    })
  }

  /** 用割线法把第一个 pane 调到目标像素宽度（pane 宽度与视口宽度局部线性）。 */
  async function aimPaneWidth(targetPage, target) {
    const paneWidthAt = async (width) => {
      await targetPage.setViewportSize({ width, height: 800 })
      await targetPage.waitForTimeout(200)
      return targetPage.evaluate(() => document.querySelector('.terminal-pane')?.getBoundingClientRect().width || 0)
    }
    let previousWidth = 1100
    let previousPane = await paneWidthAt(previousWidth)
    let width = 1300
    let pane = await paneWidthAt(width)
    for (let attempt = 0; attempt < 6 && Math.abs(pane - target) > 2; attempt++) {
      const slope = (pane - previousPane) / (width - previousWidth)
      if (!(Math.abs(slope) > 0.05)) break
      const next = Math.min(Math.max(Math.round(width + (target - pane) / slope), 768), 2400)
      if (next === width) break
      previousWidth = width
      previousPane = pane
      width = next
      pane = await paneWidthAt(width)
    }
    return { width, actual: pane }
  }
}

try {
  await main()
} finally {
  for (const context of resources.contexts) await context.close().catch(() => {})
  if (resources.browser) await resources.browser.close().catch(() => {})
  await stopChild(resources.server)
  await stopChild(resources.daemon)
  if (resources.temp) await rm(resources.temp, { recursive: true, force: true }).catch(() => {})
}
