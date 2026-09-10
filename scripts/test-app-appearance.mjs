#!/usr/bin/env node
import { chooseOption, acceptConfirmation } from './browser-controls.mjs'
// Exercise the built UI with isolated account/host fixtures; no Herdr connection.
import assert from 'node:assert/strict'
import { createServer } from 'node:http'
import { readFile, mkdir } from 'node:fs/promises'
import { join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { chromium, firefox, webkit, expect } from '../web/node_modules/@playwright/test/index.mjs'

const dist = fileURLToPath(new URL('../web/dist/', import.meta.url))
const hosts = [
  { id: 'dev', name: '开发主机', transport: 'ssh', username: 'dev', hostname: 'dev.example.test', port: 22 },
  { id: 'portable', name: '随身工作站', transport: 'tailcat' },
  { id: 'local', name: '本机工作区', transport: 'local' },
]
const users = ['admin', 'user'].map((role) => ({ id: role, display_name: role === 'admin' ? 'Alex' : 'Lin', email: role + '@example.test', role, disabled: false, active_sessions: 1, created_at: '2026-01-01T00:00:00Z' }))
const pagination = { offset: 0, limit: 50, has_more: false, next_offset: 2 }
const expiredContexts = new Set()
const server = createServer(async (req, res) => {
  const url = new URL(req.url, 'http://localhost')
  const role = req.headers['x-ui-role'] || 'guest'
  const path = url.pathname
  res.setHeader('cache-control', 'no-store')
  let body
  if (path === '/api/bootstrap/status') body = { required: false, registration: 'closed' }
  else if (path === '/api/me') {
    if (role === 'guest' || expiredContexts.has(req.headers['x-ui-context'])) { res.writeHead(401, { 'content-type': 'application/json' }); res.end(JSON.stringify({ error: 'unauthorized' })); return }
    body = { user: { ...users.find((user) => user.role === role), ...(req.headers['x-ui-long-name'] ? { display_name: '很长的用户名用于检查导航是否保持单行'.repeat(5) } : {}) }, csrf_token: 'appearance-fixture', session_id: role }
  } else if (path === '/api/hosts/') body = { hosts: hosts.filter((host) => role === 'admin' || host.transport !== 'local') }
  else if (path === '/api/ssh-keys/') body = { keys: [] }
  else if (path === '/api/host-folders/') body = { folders: [] }
  else if (path === '/api/hosts/dev/') body = { host: hosts[0] }
  else if (path === '/api/admin/settings') body = { settings: { registration: 'closed', revision: 0, updated_at: '' } }
  else if (path === '/api/admin/users') body = { ...pagination, users }
  else if (path === '/api/admin/invites') body = { ...pagination, invites: [] }
  else if (path === '/api/admin/audit') body = { ...pagination, events: [] }
  else if (path.endsWith('/sessions')) body = { ...pagination, sessions: [] }
  if (body) { res.writeHead(200, { 'content-type': 'application/json' }); res.end(JSON.stringify(body)); return }
  if (path.startsWith('/api/')) { res.writeHead(404); res.end(); return }
  try {
    const file = (path.startsWith('/assets/') || path.startsWith('/brand/')) || ['/boot.js', '/sw.js', '/manifest.webmanifest', '/favicon.ico'].includes(path) ? path.slice(1) : 'index.html'
    res.setHeader('content-type', file.endsWith('.js') ? 'application/javascript' : file.endsWith('.css') ? 'text/css' : file.endsWith('.png') ? 'image/png' : file.endsWith('.ico') ? 'image/x-icon' : file.endsWith('.webmanifest') ? 'application/manifest+json' : 'text/html')
    res.end(await readFile(join(dist, file)))
  } catch { res.writeHead(404); res.end() }
})
await new Promise((done) => server.listen(0, '127.0.0.1', done))
const base = `http://127.0.0.1:${server.address().port}`
async function fit(page) {
  const overflow = await page.evaluate(() => Array.from(document.querySelectorAll('button, input, select, h1, h2, .host-card, .admin-row, .modal')).filter((el) => el.getClientRects().length).map((el) => ({ name: el.textContent?.slice(0, 50), left: el.getBoundingClientRect().left, right: el.getBoundingClientRect().right })).filter((r) => r.left < -1 || r.right > innerWidth + 1))
  assert.deepEqual(overflow, [], 'UI content outside viewport')
  assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth + 1), 'horizontal page overflow')
}
async function expectTheme(page, theme) {
  await expect(page.locator('html')).toHaveAttribute('data-appearance', theme)
  await expect(page.getByRole('button', { name: theme === 'light' ? '切换为深色' : '切换为浅色', exact: true })).toBeVisible()
  await expect(page.getByRole('combobox', { name: '界面主题' })).toHaveCount(0)
}
async function changeTheme(page, theme) {
  await expectTheme(page, theme === 'light' ? 'dark' : 'light')
  await page.getByRole('button', { name: theme === 'light' ? '切换为浅色' : '切换为深色', exact: true }).click()
  await expectTheme(page, theme)
}
async function compactHeader(page) {
  await expect(page.getByRole('img', { name: 'herdrx', exact: true })).toBeVisible()
  await expect.poll(() => page.locator('.brand-logo .brand-icon').evaluate(image => image.complete && image.naturalWidth > 0)).toBe(true)
  const maskLoaded = await page.locator('.brand-wordmark').evaluate(async (wordmark) => {
    const url = getComputedStyle(wordmark).maskImage.match(/url\(["']?(.*?)["']?\)/)?.[1]
    if (!url) return false
    const image = new Image(); image.src = url
    try { await image.decode(); return image.naturalWidth > 0 } catch { return false }
  })
  assert.ok(maskLoaded, 'generated wordmark asset failed to load')
  const layout = await page.locator('.topbar, .auth-topbar').evaluate((header) => {
    const rect = header.getBoundingClientRect()
    const touch = matchMedia('(max-width: 767px), (pointer: coarse)').matches
    const controls = Array.from(header.querySelectorAll('button, a.brand, .user-chip')).filter(el => el.getClientRects().length).map(el => {
      const r = el.getBoundingClientRect()
      return { name: el.getAttribute('aria-label') || el.textContent, x: r.x, y: r.y, right: r.right, width: r.width, height: r.height }
    })
    return { height: rect.height, width: rect.width, top: rect.top, touch, controls }
  })
  assert.equal(layout.height, layout.touch ? 52 : 44, 'compact navigation height')
  let previousRight = 0
  for (const control of layout.controls) {
    assert.ok(Math.abs(control.y + control.height / 2 - (layout.top + layout.height / 2)) <= 1, `navigation wrapped: ${control.name}`)
    assert.ok(control.x >= previousRight - 1 && control.right <= layout.width + 1, `navigation overlaps or overflows: ${control.name}`)
    if (layout.touch && control.name !== null && control.height >= 32) assert.ok(control.height >= 44 && control.width >= 44, `touch target too small: ${control.name}`)
    previousRight = control.right
  }
}
async function capture(page, name) {
  if (!process.env.HERDRX_UI_SCREENSHOTS) return
  await mkdir(process.env.HERDRX_UI_SCREENSHOTS, { recursive: true })
  await page.evaluate(() => Promise.all(document.getAnimations().filter((animation) => animation.effect?.getTiming().iterations !== Infinity).map((animation) => animation.finished.catch(() => {}))))
  await page.screenshot({ path: join(process.env.HERDRX_UI_SCREENSHOTS, name + '.png'), fullPage: true })
}
try {
  for (const name of process.env.HERDRX_TEST_ENGINES?.split(',') || ['chromium']) {
    const browser = await ({ chromium, firefox, webkit })[name].launch()
    try {
      for (const width of [1440, 768, 736, 390, 320]) {
        const contextID = `${name}-${width}`
        const context = await browser.newContext({ viewport: { width, height: 900 }, hasTouch: width < 1024, extraHTTPHeaders: { 'x-ui-role': 'admin', 'x-ui-context': contextID } })
        await context.addInitScript(() => localStorage.setItem('herdrx.appearance.v1', 'dark'))
        const page = await context.newPage()
        const errors = []
        page.on('pageerror', (error) => errors.push(error.message))
        await page.goto(base)
        await expect(page.locator('.host-card')).toHaveCount(3)
        await expectTheme(page, 'light')
        await expect(page.locator('html')).toHaveAttribute('data-appearance-scope', 'site')
        await expect(page.locator('html')).toHaveAttribute('data-appearance', 'light')
        await expect(page.locator('.host-card').nth(1)).toContainText('Tailcat 加密连接')
        await expect(page.locator('.host-card').nth(1)).not.toContainText('undefined')
        for (const theme of ['dark', 'light']) {
          await changeTheme(page, theme)
          await expect(page.locator('html')).toHaveAttribute('data-appearance', theme)
          await fit(page)
          await compactHeader(page)
          if (width === 1440 || width === 390) await capture(page, `${name}-hosts-${theme}-${width}`)
        }
        const toggle = page.getByRole('button', { name: '切换为深色', exact: true })
        await toggle.focus()
        await toggle.press('Enter')
        await expectTheme(page, 'dark')
        await page.getByRole('button', { name: '切换为浅色', exact: true }).press('Space')
        await expectTheme(page, 'light')
        await expect(toggle).toBeFocused()
        await page.getByRole('button', { name: '密钥', exact: true }).click()
        await expect(page.getByRole('heading', { name: '密钥', exact: true })).toBeVisible()
        for (const theme of ['dark', 'light']) { await changeTheme(page, theme); await compactHeader(page); await fit(page) }
        await page.getByRole('button', { name: '返回主机', exact: true }).click()
        await page.getByLabel('搜索主机').fill('TAILCAT')
        await expect(page.locator('.host-card')).toHaveCount(1)
        await page.getByLabel('搜索主机').fill('absent')
        await expect(page.getByRole('heading', { name: '没有匹配的主机' })).toBeVisible()
        await page.getByRole('button', { name: '清除搜索' }).click()
        await page.getByRole('button', { name: '重命名 开发主机', exact: true }).click()
        await expect(page.getByRole('textbox', { name: '主机名称', exact: true })).toBeFocused()
        await fit(page)
        await page.getByRole('textbox', { name: '主机名称', exact: true }).press('Escape')
        await expect(page.getByRole('button', { name: '重命名 开发主机', exact: true })).toBeFocused()
        await page.getByRole('button', { name: '添加主机', exact: true }).click()
        await expect(page.getByRole('dialog')).toBeVisible()
        await fit(page)
        await chooseOption(page.getByLabel('连接方式'), 'ssh')
        await fit(page)
        await page.getByRole('button', { name: '关闭', exact: true }).click()
        await page.getByRole('button', { name: '管理', exact: true }).click()
        await expect(page.locator('.admin-user-row')).toHaveCount(2)
        for (const theme of ['dark', 'light']) {
          await changeTheme(page, theme)
          await fit(page)
          await compactHeader(page)
          if (width === 1440 || width === 390) await capture(page, `${name}-admin-${theme}-${width}`)
        }
        await page.locator('.admin-user-details summary').first().click()
        await fit(page)
        await page.getByRole('button', { name: '查看登录', exact: true }).first().click()
        await expect(page.getByText('没有有效的 Web 登录。')).toBeVisible()
        await fit(page)
        for (const tab of ['邀请', '审计记录']) { await page.getByRole('button', { name: tab, exact: true }).click(); await fit(page) }
        await page.reload()
        await expectTheme(page, 'light')
        if (width === 1440) {
          const other = await context.newPage()
          await other.goto(base)
          await changeTheme(page, 'dark')
          await expect(other.locator('html')).toHaveAttribute('data-appearance', 'dark')
          await expectTheme(other, 'dark')
          await page.emulateMedia({ colorScheme: 'light' })
          await expectTheme(page, 'dark')
          await page.emulateMedia({ colorScheme: 'dark' })
          await expectTheme(page, 'dark')
          await changeTheme(page, 'light')
          await page.routeWebSocket('**/api/hosts/*/ws', (ws) => {
            ws.onMessage((raw) => {
              if (typeof raw === 'string' && JSON.parse(raw).t === 'hello') {
                ws.send(JSON.stringify({ t: 'conn', state: 'ready' }))
                ws.send(JSON.stringify({ t: 'snapshot', snapshot: { protocol: 1, version: 'test', workspaces: [], tabs: [], panes: [], layouts: [], agents: [] } }))
              }
            })
          })
          await page.getByRole('button', { name: '返回主机', exact: true }).click()
          await page.locator('.host-card').first().getByRole('button', { name: '打开', exact: true }).click()
          await expect(page.locator('.workbench')).toBeVisible()
          await expect(page.locator('html')).toHaveAttribute('data-appearance-scope', 'workbench')
          await expect(page.locator('html')).toHaveAttribute('data-appearance', 'dark')
          await page.getByRole('button', { name: '工作台设置', exact: true }).click()
          await expectTheme(page, 'dark')
          await changeTheme(page, 'light')
          await expect(other.locator('html')).toHaveAttribute('data-appearance', 'light')
          await changeTheme(other, 'dark')
          await expect(page.locator('html')).toHaveAttribute('data-appearance', 'light')
          await changeTheme(other, 'light')
          await changeTheme(page, 'dark')
          await page.getByRole('button', { name: '完成', exact: true }).click()
          await page.getByRole('button', { name: '返回主机列表', exact: true }).click()
          await expect(page.locator('.host-card')).toHaveCount(3)
          await expect(page.locator('html')).toHaveAttribute('data-appearance', 'light')
          await page.goto(base + '/h/dev')
          await expect(page.locator('.workbench')).toBeVisible()
          await expect(page.locator('html')).toHaveAttribute('data-appearance', 'dark')
          expiredContexts.add(contextID)
          await page.bringToFront()
          await expect.poll(() => page.evaluate(() => document.hidden)).toBe(false)
          await page.evaluate(() => window.dispatchEvent(new Event('focus')))
          await expect(page.getByRole('heading', { name: '欢迎回来' })).toBeVisible()
          await expect(page.locator('html')).toHaveAttribute('data-appearance-scope', 'site')
          await expect(page.locator('html')).toHaveAttribute('data-appearance', 'light')
        }
        assert.deepEqual(errors, [])
        await context.close()

        const guest = await browser.newContext({ viewport: { width, height: 900 } })
        const login = await guest.newPage()
        await login.goto(base)
        await expect(login.getByRole('heading', { name: '欢迎回来' })).toBeVisible()
        await expectTheme(login, 'light')
        for (const theme of ['dark', 'light']) {
          await changeTheme(login, theme)
          await fit(login)
          await compactHeader(login)
          if (width === 1440 || width === 390) await capture(login, `${name}-login-${theme}-${width}`)
        }
        await guest.close()
      }
      // Legacy 'system' preferences resolve to the section defaults in both boot and React.
      const legacy = await browser.newContext({ viewport: { width: 768, height: 900 }, hasTouch: true, colorScheme: 'dark', extraHTTPHeaders: { 'x-ui-role': 'admin', 'x-ui-long-name': '1' } })
      await legacy.addInitScript(() => {
        if (!sessionStorage.getItem('appearance-seeded')) {
          localStorage.setItem('herdrx.site-appearance.v1', 'system')
          localStorage.setItem('herdrx.workbench-appearance.v1', 'system')
          sessionStorage.setItem('appearance-seeded', 'yes')
        }
      })
      const legacyPage = await legacy.newPage()
      const html = await readFile(join(dist, 'index.html'), 'utf8')
      const boot = html.match(/<script>([\s\S]*?)<\/script>/)?.[1] || await readFile(join(dist, 'boot.js'), 'utf8')
      await legacyPage.goto(base)
      await expectTheme(legacyPage, 'light')
      await compactHeader(legacyPage)
      await fit(legacyPage)
      await legacyPage.evaluate(boot)
      await expectTheme(legacyPage, 'light')
      await changeTheme(legacyPage, 'dark')
      await legacyPage.reload()
      await expectTheme(legacyPage, 'dark')
      await legacyPage.emulateMedia({ colorScheme: 'light' })
      await expectTheme(legacyPage, 'dark')
      await legacyPage.evaluate(() => history.replaceState(null, '', '/h/dev'))
      await legacyPage.evaluate(boot)
      await expect(legacyPage.locator('html')).toHaveAttribute('data-appearance', 'dark')
      await legacy.close()
      // Storage may be blocked: the in-memory choice must still toggle immediately.
      const blocked = await browser.newContext({ extraHTTPHeaders: { 'x-ui-role': 'admin' } })
      await blocked.addInitScript(() => Object.defineProperty(window, 'localStorage', { get() { throw new DOMException('Unavailable', 'SecurityError') } }))
      const blockedPage = await blocked.newPage()
      await blockedPage.goto(base)
      await expectTheme(blockedPage, 'light')
      await changeTheme(blockedPage, 'dark')
      await changeTheme(blockedPage, 'light')
      await blocked.close()
      const memberContext = await browser.newContext({ viewport: { width: 390, height: 844 }, extraHTTPHeaders: { 'x-ui-role': 'user' } })
      const member = await memberContext.newPage()
      await member.goto(base)
      await expect(member.locator('.host-card')).toHaveCount(2)
      await expect(member.getByRole('button', { name: '管理', exact: true })).toHaveCount(0)
      await member.getByRole('button', { name: '添加主机', exact: true }).click()
      await member.getByRole('combobox', { name: '连接方式' }).click()
      await expect(member.getByRole('option', { name: 'SSH', exact: true })).toBeVisible()
      await expect(member.getByRole('option', { name: '本机 Herdr' })).toHaveCount(0)
      await fit(member)
      await memberContext.close()
      console.log(`${name}: compact login/hosts/keys/admin headers, 44px touch controls, one-click and keyboard themes, persistence/legacy defaults/cross-tab/blocked storage, host search and rename focus, SSH/Tailcat forms, role-specific controls passed`)
    } finally { await browser.close() }
  }
} finally { await new Promise((done) => server.close(done)) }
