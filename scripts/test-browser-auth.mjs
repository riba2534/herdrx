#!/usr/bin/env node
import { chooseOption, acceptConfirmation } from './browser-controls.mjs'
// Real-browser authentication checks against an isolated website process.
// Usage: node scripts/test-browser-auth.mjs /absolute/path/to/herdrx-server
import assert from 'node:assert/strict'
import { spawn } from 'node:child_process'
import { mkdtemp, readFile, rm } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'
import { createServer } from 'node:net'
import { createServer as createHTTPServer } from 'node:http'
import { chromium, expect } from '../web/node_modules/@playwright/test/index.mjs'

const binary = resolve(process.argv[2] || 'bin/herdrx-server')
const data = await mkdtemp(join(tmpdir(), 'herdrx-browser-auth-'))
const port = await new Promise((done) => { const probe = createServer(); probe.listen(0, '127.0.0.1', () => { const port = probe.address().port; probe.close(() => done(port)) }) })
const base = `http://127.0.0.1:${port}`
// Even a leftover legacy environment setting must not open registration.
const start = () => spawn(binary, [], { env: { ...process.env, HERDRX_REGISTRATION: 'invite', HERDRX_DATA_DIR: data, HERDRX_ADDR: `127.0.0.1:${port}`, HERDRX_PUBLIC_URL: base, HERDRX_COOKIE_SECURE: 'false', HERDRX_BOOTSTRAP_TOKEN: '', HERDRX_SESSION_TTL: '1h', HERDRX_ALLOWED_ORIGINS: '', HERDRX_TRUSTED_PROXIES: '' }, stdio: 'ignore' })
let child = start()
let browser
let attacker
const password = 'browser-test-password-only'
async function login(page, email) {
  await page.getByLabel('邮箱', { exact: true }).fill(email)
  await page.getByLabel('密码', { exact: true }).fill(password)
  await page.getByRole('button', { name: '登录', exact: true }).click()
  await expect(page.getByRole('heading', { name: '主机', exact: true })).toBeVisible()
}
async function ready() {
  for (let i = 0; i < 100; i++) {
    if (child.exitCode !== null) throw new Error('Isolated website exited before becoming ready')
    if (await fetch(base + '/healthz').then((r) => r.ok).catch(() => false)) break
    if (i === 99) throw new Error('Isolated website did not become ready')
    await new Promise((done) => setTimeout(done, 100))
  }
}
async function stop() {
  child.kill('SIGTERM')
  if (child.exitCode === null) await new Promise((done) => { const timer = setTimeout(() => child.kill('SIGKILL'), 3000); child.once('exit', () => { clearTimeout(timer); done() }) })
}
try {
  await ready()
  assert.deepEqual(await fetch(base + '/api/bootstrap/status').then((r) => r.json()), { required: true, registration: 'closed' })
  browser = await chromium.launch({ headless: true, executablePath: process.env.HERDRX_TEST_CHROMIUM || undefined })
  const context = await browser.newContext()
  const admin = await context.newPage()
  const errors = []
  admin.on('pageerror', (err) => errors.push(err.message))
  await admin.goto(base)
  await admin.getByLabel('显示名称').fill('Admin')
  await admin.getByLabel('邮箱', { exact: true }).fill('admin@example.test')
  await admin.getByLabel('密码', { exact: true }).fill(password)
  await admin.getByLabel('初始化令牌').fill((await readFile(join(data, 'bootstrap-token'), 'utf8')).trim())
  await admin.getByRole('button', { name: '创建管理员' }).click()
  await admin.getByRole('button', { name: '添加主机', exact: true }).click()
  await admin.getByRole('combobox', { name: '连接方式' }).click()
  await expect(admin.getByRole('option', { name: '本机 Herdr' })).toBeAttached()
  await admin.keyboard.press('Escape')
  await admin.getByRole('button', { name: '关闭', exact: true }).click()
  const guestContext = await browser.newContext()
  const guest = await guestContext.newPage()
  await guest.goto(base)
  await expect(guest.getByRole('heading', { name: '欢迎回来' })).toBeVisible()
  await expect(guest.getByRole('button', { name: '有邀请码？创建账号' })).toHaveCount(0)
  await admin.getByRole('button', { name: '管理', exact: true }).click()
  await expect(admin.getByRole('heading', { name: '访问管理' })).toBeVisible()
  await expect(admin.getByRole('button', { name: '禁用账号' })).toBeDisabled()
  await expect(admin.getByRole('button', { name: '开启注册', exact: true })).toBeEnabled()
  await admin.getByRole('button', { name: '开启注册', exact: true }).click()
  await expect(admin.getByRole('button', { name: '关闭注册', exact: true })).toBeEnabled()
  await guest.evaluate(() => window.dispatchEvent(new Event('focus')))
  await expect(guest.getByRole('button', { name: '有邀请码？创建账号' })).toBeVisible()
  await stop(); child = start(); await ready()
  assert.deepEqual(await fetch(base + '/api/bootstrap/status').then((r) => r.json()), { required: false, registration: 'invite' })
  await admin.reload()
  await expect(admin.getByRole('button', { name: '关闭注册', exact: true })).toBeEnabled()
  await admin.getByRole('button', { name: '邀请', exact: true }).click()
  await admin.getByRole('button', { name: '创建邀请' }).click()
  const code = await admin.getByRole('region', { name: '新邀请码' }).locator('pre').innerText()
  const memberContext = await browser.newContext()
  const member = await memberContext.newPage()
  await member.goto(base)
  await member.getByRole('button', { name: '有邀请码？创建账号' }).click()
  await member.getByLabel('显示名称').fill('Member')
  await member.getByLabel('邮箱', { exact: true }).fill('member@example.test')
  await member.getByLabel('密码', { exact: true }).fill(password)
  await member.getByLabel('邀请码').fill(code)
  await member.getByRole('button', { name: '创建账号' }).click()
  await expect(member.getByRole('heading', { name: '主机', exact: true })).toBeVisible()
  assert.equal(await member.getByRole('button', { name: '管理', exact: true }).count(), 0)
  await member.getByRole('button', { name: '添加主机', exact: true }).click()
  await member.getByRole('combobox', { name: '连接方式' }).click()
  await expect(member.getByRole('option', { name: '本机 Herdr' })).toHaveCount(0)
  await expect(member.getByRole('option', { name: 'SSH', exact: true })).toBeAttached()
  await expect(member.getByRole('option', { name: /Tailcat/ })).toBeAttached()
  await member.keyboard.press('Escape')
  await member.getByRole('button', { name: '关闭', exact: true }).click()
  await member.goto(base + '/admin')
  await expect(member.getByRole('heading', { name: '需要管理员权限' })).toBeVisible()
  await guest.getByRole('button', { name: '有邀请码？创建账号' }).click()
  await expect(guest.getByLabel('邀请码')).toBeVisible()
  await admin.getByRole('button', { name: '关闭注册', exact: true }).click()
  await expect(admin.getByRole('button', { name: '开启注册', exact: true })).toBeEnabled()
  await expect(admin.getByRole('button', { name: '创建邀请' })).toBeDisabled()
  await guest.evaluate(() => window.dispatchEvent(new Event('focus')))
  await expect(guest.getByLabel('邀请码')).toHaveCount(0)
  await expect(guest.getByRole('heading', { name: '欢迎回来' })).toBeVisible()
  await stop(); child = start(); await ready()
  assert.deepEqual(await fetch(base + '/api/bootstrap/status').then((r) => r.json()), { required: false, registration: 'closed' })
  await admin.reload()

  // Same-browser tabs share only a notification; they independently revalidate.
  const otherTab = await context.newPage()
  await otherTab.goto(base)
  await admin.getByRole('button', { name: '返回主机' }).click()
  await admin.getByRole('button', { name: '退出', exact: true }).click()
  await expect(otherTab.getByRole('heading', { name: '欢迎回来' })).toBeVisible()
  await login(admin, 'admin@example.test')
  await expect(otherTab.getByRole('heading', { name: '主机', exact: true })).toBeVisible()
  await admin.getByRole('button', { name: '管理', exact: true }).click()
  await admin.getByLabel('搜索用户').fill('member')
  await chooseOption(admin.getByLabel('角色'), 'user')
  await chooseOption(admin.getByLabel('账号状态'), 'enabled')
  await admin.getByRole('button', { name: '筛选用户' }).click()
  await expect(admin.locator('article')).toHaveCount(1)
  await chooseOption(admin.getByLabel('账号状态'), '')
  await admin.getByRole('button', { name: '筛选用户' }).click()
  const memberRow = admin.locator('article').filter({ has: admin.getByRole('heading', { name: 'Member', exact: true }) })
  await memberRow.getByRole('button', { name: '禁用账号' }).click()
  await acceptConfirmation(admin)
  await expect(memberRow.getByRole('button', { name: '启用账号' })).toBeVisible()
  await member.reload()
  await expect(member.getByRole('heading', { name: '欢迎回来' })).toBeVisible()
  await memberRow.getByRole('button', { name: '启用账号' }).click()
  await acceptConfirmation(admin)
  await member.reload()
  await expect(member.getByRole('heading', { name: '欢迎回来' })).toBeVisible()
  await member.goto(base)
  await login(member, 'member@example.test')
  await admin.setViewportSize({ width: 320, height: 720 })
  assert.equal(await admin.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth), true, 'Admin page overflows at 320px')
  await admin.getByRole('button', { name: '审计记录' }).click()
  await expect(admin.getByRole('heading', { name: '禁用账号', exact: true })).toBeVisible()
  await expect(admin.getByRole('heading', { name: '启用账号', exact: true })).toBeVisible()

  // A cross-origin simple form must not overwrite the browser's current login.
  const before = await context.cookies()
  attacker = createHTTPServer((_req, res) => {
    const name = JSON.stringify({ email: 'member@example.test', password, display_name: '' }).slice(0, -2).replaceAll('"', '&quot;')
    res.setHeader('Content-Type', 'text/html')
    res.end(`<form method="post" enctype="text/plain" action="${base}/api/login"><input name="${name}" value="&quot;}"><button>Submit form</button></form>`)
  })
  await new Promise((done) => attacker.listen(0, '127.0.0.1', done))
  const attackPage = await context.newPage()
  await attackPage.goto(`http://localhost:${attacker.address().port}`)
  const [attack] = await Promise.all([attackPage.waitForResponse(base + '/api/login'), attackPage.getByRole('button', { name: 'Submit form' }).click()])
  assert.equal(attack.status(), 403)
  assert.deepEqual(await context.cookies(), before)
  assert.deepEqual(errors, [])
  console.log('Browser: default closed despite legacy env, first admin, explicit open/close, restart persistence, existing registration form refresh, local host role boundary, member registration, user filters, cross-tab logout/login, disable/enable, fresh login while closed, audit, 320px layout and cross-origin rejection passed')
} finally {
  await browser?.close()
  if (attacker) await new Promise((done) => attacker.close(done))
  await stop()
  await rm(data, { recursive: true, force: true })
}
