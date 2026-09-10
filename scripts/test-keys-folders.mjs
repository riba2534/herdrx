#!/usr/bin/env node
import { chooseOption, acceptConfirmation, openSiteNav } from './browser-controls.mjs'
// Real UI/API/database flow in a disposable website. Never contacts a real SSH host.
import assert from 'node:assert/strict'
import { spawn } from 'node:child_process'
import { generateKeyPairSync } from 'node:crypto'
import { mkdtemp, readFile, mkdir, rm } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'
import { createServer } from 'node:net'
import { chromium, firefox, webkit, expect } from '../web/node_modules/@playwright/test/index.mjs'

const binary = resolve(process.argv[2] || 'bin/herdrx-server')
const data = await mkdtemp(join(tmpdir(), 'herdrx-keys-folders-'))
const port = await new Promise((done) => { const probe = createServer(); probe.listen(0, '127.0.0.1', () => { const port = probe.address().port; probe.close(() => done(port)) }) })
const base = `http://127.0.0.1:${port}`
const password = 'keys-folders-fixture-password'
const keyPair = generateKeyPairSync('ed25519')
const privateKey = keyPair.privateKey.export({ type: 'pkcs8', format: 'pem' }).toString()
const start = () => spawn(binary, [], { env: { ...process.env, HERDRX_DATA_DIR: data, HERDRX_ADDR: `127.0.0.1:${port}`, HERDRX_PUBLIC_URL: base, HERDRX_COOKIE_SECURE: 'false', HERDRX_BOOTSTRAP_TOKEN: '', HERDRX_ALLOWED_ORIGINS: '', HERDRX_TRUSTED_PROXIES: '' }, stdio: 'ignore' })
let child = start()
async function ready() {
  for (let i = 0; i < 100; i++) {
    if (child.exitCode !== null) throw new Error('Fixture website exited')
    if (await fetch(base + '/healthz').then((r) => r.ok).catch(() => false)) return
    await new Promise((done) => setTimeout(done, 100))
  }
  throw new Error('Fixture website did not start')
}
async function stop() {
  child.kill('SIGTERM')
  if (child.exitCode === null) await new Promise((done) => { const timer = setTimeout(() => child.kill('SIGKILL'), 3000); child.once('exit', () => { clearTimeout(timer); done() }) })
}
async function fit(page) {
  const outside = await page.locator('button, input:not([type=file]), select, textarea, .key-card, .host-card').evaluateAll((els) => els.filter((el) => el.getClientRects().length).map((el) => ({ name: el.getAttribute('aria-label') || el.textContent?.slice(0, 30), left: el.getBoundingClientRect().left, right: el.getBoundingClientRect().right })).filter((r) => r.left < -1 || r.right > innerWidth + 1))
  assert.deepEqual(outside, [], 'controls outside viewport')
  assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth + 1), 'page overflow')
}
async function capture(page, name) {
  if (!process.env.HERDRX_KEYS_SCREENSHOTS) return
  await mkdir(process.env.HERDRX_KEYS_SCREENSHOTS, { recursive: true })
  await page.screenshot({ path: join(process.env.HERDRX_KEYS_SCREENSHOTS, name + '.png'), fullPage: true, animations: 'disabled' })
}
async function closeDialog(page) { await page.getByRole('dialog').getByRole('button', { name: '关闭', exact: true }).click() }
try {
  await ready()
  const bootstrap = await fetch(base + '/api/bootstrap', { method: 'POST', headers: { 'content-type': 'application/json', origin: base }, body: JSON.stringify({ email: 'keys@example.test', display_name: 'Test', password, token: (await readFile(join(data, 'bootstrap-token'), 'utf8')).trim() }) })
  assert.equal(bootstrap.status, 200)
  for (const name of process.env.HERDRX_TEST_ENGINES?.split(',') || ['chromium']) {
    const browser = await ({ chromium, firefox, webkit })[name].launch()
    try {
      const context = await browser.newContext({ viewport: { width: 1440, height: 1000 } })
      const page = await context.newPage()
      const errors = []
      page.on('pageerror', (error) => errors.push(error.message))
      await page.goto(base)
      await page.getByLabel('邮箱', { exact: true }).fill('keys@example.test')
      await page.getByLabel('密码', { exact: true }).fill(password)
      await page.getByRole('button', { name: '登录', exact: true }).click()
      await openSiteNav(page, '密钥')
      await page.getByRole('button', { name: '添加密钥', exact: true }).click()
      const keyDialog = page.getByRole('dialog', { name: '添加密钥', exact: true })
      await expect(keyDialog.getByLabel('密钥名称')).toBeFocused()
      await keyDialog.getByLabel('密钥名称').fill('共享运维密钥')
      await page.getByLabel('导入私钥文件', { exact: true }).setInputFiles({ name: 'fixture-key.pem', mimeType: 'text/plain', buffer: Buffer.from(privateKey) })
      await expect(page.getByRole('textbox', { name: 'OpenSSH/PEM 私钥', exact: true })).toHaveValue(privateKey)
      await capture(page, `${name}-key-import`)
      await page.getByRole('button', { name: '保存密钥', exact: true }).click()
      await expect(page.getByRole('heading', { name: '共享运维密钥', exact: true })).toBeVisible()
      await expect(page.locator('.key-card')).toContainText('0 台主机')
      await page.getByRole('button', { name: '编辑 共享运维密钥', exact: true }).click()
      await expect(page.getByRole('textbox', { name: 'OpenSSH/PEM 私钥', exact: true })).toHaveValue('')
      await page.getByLabel('密钥名称').fill('共享开发密钥')
      await page.getByRole('button', { name: '保存密钥', exact: true }).click()
      await expect(page.getByRole('heading', { name: '共享开发密钥', exact: true })).toBeVisible()
      await page.getByLabel('搜索密钥').fill('不存在')
      await expect(page.getByRole('heading', { name: '没有匹配的密钥' })).toBeVisible()
      await page.getByRole('button', { name: '清除搜索', exact: true }).click()
      await page.locator('.key-details summary').click()
      assert.ok((await page.locator('.key-block').innerText()).startsWith('ssh-ed25519 '))
      await page.getByRole('button', { name: '返回主机', exact: true }).click()
      const newFolder = async (name, parent) => {
        await page.getByRole('button', { name: '新建文件夹', exact: true }).click()
        await page.getByLabel('文件夹名称').fill(name)
        await chooseOption(page.getByLabel('上级文件夹'), { label: parent })
        await page.getByRole('button', { name: '保存文件夹', exact: true }).click()
        await expect(page.getByRole('dialog')).toHaveCount(0)
      }
      await newFolder('工作主机', '根目录')
      await newFolder('生产环境', '工作主机')
      for (const label of ['应用主机', '数据库主机']) {
        await page.getByRole('button', { name: '添加主机', exact: true }).click()
        await chooseOption(page.getByLabel('连接方式'), 'ssh')
        await page.getByLabel('名称', { exact: true }).fill(label)
        await page.getByLabel('主机名或 IP').fill('ssh.example.test')
        await page.getByLabel('端口', { exact: true }).fill('2222')
        await page.getByLabel('SSH 用户', { exact: true }).fill('deploy')
        await chooseOption(page.getByRole('combobox', { name: '认证密钥', exact: true }), { label: '共享开发密钥 · ed25519' })
        await chooseOption(page.getByLabel('所属文件夹'), { label: '工作主机 / 生产环境' })
        await fit(page)
        await capture(page, `${name}-ssh-key-choice`)
        await page.getByRole('button', { name: '保存主机', exact: true }).click()
        await expect(page.getByRole('dialog')).toHaveCount(0)
      }
      await expect(page.locator('.host-card')).toHaveCount(2)
      if (name === 'chromium') { await stop(); child = start(); await ready(); await page.reload(); await expect(page.locator('.host-card')).toHaveCount(2) }
      await openSiteNav(page, '密钥')
      await expect(page.locator('.key-card')).toContainText('2 台主机')
      await page.getByRole('button', { name: '删除 共享开发密钥', exact: true }).click()
      await expect(page.getByRole('dialog').getByRole('button', { name: '删除密钥', exact: true })).toBeDisabled()
      await closeDialog(page)
      await capture(page, `${name}-keys`)
      for (const width of [1024, 736, 390, 320]) {
        await page.setViewportSize({ width, height: 900 })
        await fit(page)
        await page.getByRole('button', { name: '编辑 共享开发密钥', exact: true }).click()
        await fit(page)
        await capture(page, `${name}-key-edit-${width}`)
        await page.getByRole('button', { name: '取消', exact: true }).click()
      }
      await page.setViewportSize({ width: 1440, height: 1000 })
      await page.getByRole('button', { name: '返回主机', exact: true }).click()
      await page.getByRole('button', { name: '连接设置 应用主机', exact: true }).click()
      await expect(page.getByLabel('SSH 用户', { exact: true })).toHaveValue('deploy')
      await expect(page.getByLabel('端口', { exact: true })).toHaveValue('2222')
      await chooseOption(page.getByRole('combobox', { name: '认证', exact: true }), 'password')
      await page.getByLabel('SSH 密码', { exact: true }).fill('fixture-ssh-password')
      await page.getByLabel('SSH 用户', { exact: true }).fill('custom-user')
      await page.getByLabel('端口', { exact: true }).fill('2200')
      await page.getByRole('button', { name: '保存主机', exact: true }).click()
      await expect(page.getByRole('dialog')).toHaveCount(0)
      await expect(page.getByText('custom-user@ssh.example.test:2200', { exact: false })).toBeVisible()
      await page.getByRole('button', { name: '连接设置 应用主机', exact: true }).click()
      await expect(page.getByLabel('SSH 密码', { exact: true })).toHaveValue('')
      await page.getByLabel('端口', { exact: true }).fill('2201')
      await page.getByRole('button', { name: '保存主机', exact: true }).click()
      await expect(page.getByRole('dialog')).toHaveCount(0)
      await page.getByRole('button', { name: '移动 数据库主机', exact: true }).click()
      await chooseOption(page.getByLabel('目标文件夹'), { label: '未分组' })
      await page.getByRole('button', { name: '移动主机', exact: true }).click()
      await expect(page.getByRole('dialog')).toHaveCount(0)
      const nav = page.getByRole('navigation', { name: '主机文件夹', exact: true })
      await nav.getByRole('button', { name: /^未分组/ }).click()
      await expect(page.locator('.host-card')).toHaveCount(1)
      await expect(page.getByRole('heading', { name: '数据库主机', exact: true })).toBeVisible()
      await nav.getByRole('button', { name: /^全部主机/ }).click()
      await page.getByRole('button', { name: '编辑文件夹 工作主机', exact: true }).click()
      await expect(page.getByLabel('上级文件夹')).toHaveAttribute('data-value', '')
      await page.getByLabel('上级文件夹').click()
      await expect(page.getByRole('listbox').getByRole('option')).toHaveCount(1)
      await page.keyboard.press('Escape')
      await page.getByLabel('文件夹名称').fill('我的主机')
      await page.getByRole('button', { name: '保存文件夹', exact: true }).click()
      await expect(page.getByRole('dialog')).toHaveCount(0)
      for (const width of [1440, 1024, 900, 736, 390, 320]) {
        await page.setViewportSize({ width, height: 900 })
        await fit(page)
        await capture(page, `${name}-folders-${width}`)
        await page.getByRole('button', { name: '连接设置 应用主机', exact: true }).click()
        await fit(page)
        await closeDialog(page)
      }
      await page.getByRole('button', { name: '删除文件夹 生产环境', exact: true }).click()
      await page.getByRole('dialog').getByRole('button', { name: '删除文件夹', exact: true }).click()
      await expect(page.getByRole('dialog')).toHaveCount(0)
      await expect(page.locator('.host-card')).toHaveCount(2)
      await page.getByRole('button', { name: '删除文件夹 我的主机', exact: true }).click()
      await page.getByRole('dialog').getByRole('button', { name: '删除文件夹', exact: true }).click()
      await expect(page.getByRole('dialog')).toHaveCount(0)
      for (const label of ['数据库主机', '应用主机']) { await page.getByRole('button', { name: `删除 ${label}`, exact: true }).click(); await acceptConfirmation(page); await expect(page.getByRole('heading', { name: label, exact: true })).toHaveCount(0) }
      await openSiteNav(page, '密钥')
      await expect(page.locator('.key-card')).toContainText('0 台主机')
      await page.getByRole('button', { name: '删除 共享开发密钥', exact: true }).click()
      await page.getByRole('dialog').getByRole('button', { name: '删除密钥', exact: true }).click()
      await expect(page.getByRole('heading', { name: '还没有密钥', exact: true })).toBeVisible()
      await page.getByRole('button', { name: '添加密钥', exact: true }).click()
      await page.getByLabel('密钥名称').fill('生成测试')
      await chooseOption(page.getByLabel('创建方式'), 'generate')
      await page.getByRole('button', { name: '保存密钥', exact: true }).click()
      await expect(page.locator('.key-card')).toContainText('ed25519')
      await page.getByRole('button', { name: '删除 生成测试', exact: true }).click()
      await page.getByRole('dialog').getByRole('button', { name: '删除密钥', exact: true }).click()
      await expect(page.getByRole('heading', { name: '还没有密钥', exact: true })).toBeVisible()
      assert.deepEqual(errors, [])
      const storage = await page.evaluate(() => JSON.stringify({ ...localStorage, ...sessionStorage }))
      assert.ok(!storage.includes('PRIVATE KEY') && !storage.includes('fixture-ssh-password'), 'secrets persisted in browser storage')
      console.log(`${name}: import/generate/edit keys, shared references, in-use deletion guard, custom SSH password/key settings, blank-secret preservation, folder hierarchy/moves/delete preservation, responsive dialogs and no browser secret storage passed`)
      await context.close()
    } finally { await browser.close() }
  }
} finally { await stop(); await rm(data, { recursive: true, force: true }) }
