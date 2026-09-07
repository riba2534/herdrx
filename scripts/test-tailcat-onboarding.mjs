#!/usr/bin/env node
// Built UI guide and binding-state checks. All host/release/network data are fixtures.
import assert from 'node:assert/strict'
import { createServer } from 'node:http'
import { readFile, mkdir } from 'node:fs/promises'
import { join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { chromium, firefox, webkit, expect } from '../web/node_modules/@playwright/test/index.mjs'

const dist = fileURLToPath(new URL('../web/dist/', import.meta.url))
const releaseURL = 'https://github.com/riba2534/herdrx/releases'
const fixtures = new Map()
const server = createServer(async (req, res) => {
  const path = new URL(req.url, 'http://localhost').pathname
  let body
  const fixture = fixtures.get(req.headers['x-fixture'])
  if (path === '/api/bootstrap/status') body = { required: false, registration: 'closed' }
  else if (path === '/api/me') body = { user: { id: 'member', email: 'member@example.test', display_name: 'Lin', role: 'user' }, csrf_token: 'fixture', session_id: 'session' }
  else if (path === '/api/cli-release') body = fixture.release
  else if (path === '/api/tailcat/enrollments' && req.method === 'POST') {
    let content = ''; for await (const chunk of req) content += chunk
    fixture.posted = JSON.parse(content)
    body = { task_id: 'task-fixture', status: 'connecting', agent_id: 'agent-fixture' }
  } else if (path === '/api/tailcat/enrollments/task-fixture') body = { id: 'task-fixture', status: fixture.taskStatus, error: '测试：凭据过期，请重新生成', host_id: fixture.taskStatus === 'active' ? 'bound' : undefined }
  else if (path === '/api/hosts/') body = { hosts: [] }
  else if (path === '/api/ssh-keys/') body = { keys: [] }
  else if (path === '/api/host-folders/') body = { folders: [] }
  else if (path === '/api/hosts/bound/') body = { host: { id: 'bound', name: '新远程主机', transport: 'tailcat' } }
  if (body) { res.writeHead(200, { 'content-type': 'application/json' }); res.end(JSON.stringify(body)); return }
  if (path.startsWith('/api/')) { res.writeHead(404, { 'content-type': 'application/json' }); res.end(JSON.stringify({ error: 'fixture route missing' })); return }
  try {
    const file = path.startsWith('/assets/') || path.startsWith('/brand/') || ['/boot.js', '/sw.js', '/manifest.webmanifest', '/favicon.ico'].includes(path) ? path.slice(1) : 'index.html'
    res.setHeader('content-type', file.endsWith('.js') ? 'application/javascript' : file.endsWith('.css') ? 'text/css' : file.endsWith('.png') ? 'image/png' : file.endsWith('.ico') ? 'image/x-icon' : file.endsWith('.webmanifest') ? 'application/manifest+json' : 'text/html')
    res.end(await readFile(join(dist, file)))
  } catch { res.writeHead(404); res.end() }
})
await new Promise(done => server.listen(0, '127.0.0.1', done))
const base = `http://127.0.0.1:${server.address().port}`
async function fit(page) {
  assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth + 1), 'horizontal page overflow')
  assert.ok(await page.getByRole('dialog').evaluate(el => el.scrollWidth <= el.clientWidth + 1), 'horizontal dialog overflow')
}
async function capture(page, name) {
  if (!process.env.HERDRX_UI_SCREENSHOTS) return
  await mkdir(process.env.HERDRX_UI_SCREENSHOTS, { recursive: true })
  await page.screenshot({ path: join(process.env.HERDRX_UI_SCREENSHOTS, name + '.png'), fullPage: true })
}
try {
  for (const engine of process.env.HERDRX_TEST_ENGINES?.split(',') || ['chromium']) {
    const browser = await ({ chromium, firefox, webkit })[engine].launch()
    try {
      for (const width of [1440, 768, 390, 320]) {
        const fixtureID = `${engine}-${width}`
        const fixture = { release: { status: 'available', version: 'v0.1.0-rc.1', prerelease: true }, posted: null, taskStatus: 'connecting' }
        fixtures.set(fixtureID, fixture)
        const context = await browser.newContext({ viewport: { width, height: 900 }, hasTouch: width < 1024, extraHTTPHeaders: { 'x-fixture': fixtureID } })
        const page = await context.newPage()
        const errors = []
        page.on('pageerror', e => errors.push(e.message))
        await page.goto(base)
        await page.getByRole('button', { name: '添加主机', exact: true }).click()
        await expect(page.getByRole('heading', { name: '安装 CLI', exact: true })).toBeVisible()
        await expect(page.getByRole('textbox', { name: /绑定凭据/ })).toHaveCount(0)
        await expect(page.getByRole('button', { name: '保存主机' })).toHaveCount(0)
        await expect(page.getByRole('link', { name: 'GitHub Releases' })).toHaveAttribute('href', releaseURL + '/tag/v0.1.0-rc.1')
        await expect(page.getByLabel('下载安装命令', { exact: true })).toContainText('/releases/download/v0.1.0-rc.1/install-herdrx.sh')
        await fit(page)
        if (engine === 'chromium' && [1440, 390].includes(width)) await capture(page, `install-${width}-light`)
        // Validate clipboard API and the real HTTP fallback independently.
        await page.evaluate(() => Object.defineProperty(navigator, 'clipboard', { configurable: true, value: { writeText: value => { window.__copiedCommand = value; return Promise.resolve() } } }))
        await page.getByRole('button', { name: '复制下载安装命令', exact: true }).click()
        assert.equal(await page.evaluate(() => window.__copiedCommand), await page.getByLabel('下载安装命令', { exact: true }).innerText())
        await page.evaluate(() => Object.defineProperty(navigator, 'clipboard', { configurable: true, value: undefined }))
        await page.getByRole('button', { name: '复制下载安装命令', exact: true }).click()
        await expect(page.getByText('命令已选中，请使用系统菜单或 Ctrl/Cmd+C 复制。', { exact: true })).toBeVisible()
        assert.ok((await page.evaluate(() => getSelection().toString())).includes('sh "$installer"'))
        await page.getByText('手动下载与安装', { exact: true }).click()
        await expect(page.getByRole('link', { name: 'Linux ARM64', exact: true })).toHaveAttribute('href', releaseURL + '/download/v0.1.0-rc.1/herdrx-linux-arm64.tar.gz')
        await page.getByRole('button', { name: '已安装，下一步' }).click()
        await expect(page.getByRole('heading', { name: '后台运行', exact: true })).toBeFocused()
        await expect(page.getByLabel('启动后台服务', { exact: true })).toContainText('herdrx setup && herdrx status')
        await expect(page.getByLabel('检查开机与登出保活', { exact: true })).toContainText('loginctl show-user')
        await fit(page)
        if (engine === 'chromium' && [1440, 390].includes(width)) await capture(page, `service-${width}-light`)
        await page.getByRole('button', { name: '服务已就绪，下一步' }).click()
        await expect(page.getByLabel('生成绑定凭据', { exact: true })).toHaveText('herdrx connect --plain')
        const credential = 'herdrx://v1/fixture-once-only'
        await page.getByRole('textbox', { name: /绑定凭据/ }).fill(credential)
        await page.getByRole('textbox', { name: '主机名称（可选）', exact: true }).fill('家里的主机')
        await page.getByRole('textbox', { name: 'Herdr 命名会话（可选）', exact: true }).fill('coding')
        await page.getByRole('button', { name: '上一步', exact: true }).click()
        await page.getByRole('button', { name: '服务已就绪，下一步' }).click()
        await expect(page.getByRole('textbox', { name: /绑定凭据/ })).toHaveValue(credential)
        assert.equal(fixture.posted, null, 'step navigation must never submit a binding')
        await fit(page)
        if (engine === 'chromium' && [1440, 390].includes(width)) await capture(page, `binding-${width}-light`)
        await page.getByRole('button', { name: '绑定并打开主机' }).click()
        await expect.poll(() => fixture.posted).toEqual({ connection_string: credential, name: '家里的主机', session_name: 'coding' })
        await expect(page.getByRole('textbox', { name: /绑定凭据/ })).toHaveValue('')
        await expect(page.getByRole('button', { name: '绑定并打开主机' })).toBeDisabled()
        const saved = await page.evaluate(() => sessionStorage.getItem('herdrx.enrollmentTask.member'))
        assert.ok(saved.includes('task-fixture') && !saved.includes(credential))
        await page.reload()
        await expect(page.getByRole('heading', { name: '绑定主机', exact: true })).toBeVisible()
        await expect(page.getByRole('button', { name: '绑定并打开主机' })).toBeDisabled()
        fixture.taskStatus = 'failed'
        await expect(page.getByRole('alert')).toContainText('测试：凭据过期')
        await expect(page.getByRole('button', { name: '绑定并打开主机' })).toBeEnabled()
        await page.getByRole('textbox', { name: /绑定凭据/ }).fill(credential)
        fixture.taskStatus = 'active'
        await page.getByRole('button', { name: '绑定并打开主机' }).click()
        await expect(page).toHaveURL(base + '/h/bound')
        assert.equal(await page.evaluate(() => sessionStorage.getItem('herdrx.enrollmentTask.member')), null)
        // Missing/unavailable Release must offer an honest next step, never a fake URL.
        await page.goto(base)
        for (const status of ['unpublished', 'unavailable']) {
          fixture.release = { status }
          await page.getByRole('button', { name: '添加主机', exact: true }).click()
          await expect(page.getByText(status === 'unpublished' ? /CLI 安装包尚未发布/ : /暂时无法获取版本/)).toBeVisible()
          await expect(page.getByLabel('下载安装命令', { exact: true })).toHaveCount(0)
          await expect(page.getByRole('link', { name: 'GitHub Releases' })).toBeVisible()
          await fit(page)
          await page.getByRole('button', { name: '关闭', exact: true }).click()
          await expect(page.getByRole('button', { name: '添加主机', exact: true })).toBeFocused()
        }
        fixture.release = { status: 'unpublished' }
        await page.getByRole('button', { name: '切换为深色', exact: true }).click()
        await page.getByRole('button', { name: '添加主机', exact: true }).click()
        await expect(page.getByText(/CLI 安装包尚未发布/)).toBeVisible()
        if (engine === 'chromium' && [1440, 390].includes(width)) await capture(page, `unpublished-${width}-dark`)
        fixture.release = { status: 'available', version: 'v0.1.0' }
        await page.getByRole('button', { name: '重新检查', exact: true }).click()
        await expect(page.getByLabel('下载安装命令', { exact: true })).toContainText("--version 'v0.1.0'")
        await page.keyboard.press('Escape')
        await expect(page.getByRole('dialog')).toHaveCount(0)
        assert.deepEqual(errors, [])
        await context.close()
      }
      console.log(`${engine}: Tailcat guide, Release states, clipboard, binding/resume/error, themes and 320–1440 px passed`)
    } finally { await browser.close() }
  }
} finally { server.closeAllConnections(); await new Promise(done => server.close(done)) }
