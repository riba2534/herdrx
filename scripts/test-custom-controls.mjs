#!/usr/bin/env node
import assert from 'node:assert/strict'
import { createServer } from 'node:http'
import { readFile, mkdir, readdir } from 'node:fs/promises'
import { join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { chromium, firefox, webkit, expect } from '../web/node_modules/@playwright/test/index.mjs'
import ts from '../web/node_modules/typescript/lib/typescript.js'
import { chooseOption, acceptConfirmation } from './browser-controls.mjs'

const dist = fileURLToPath(new URL('../web/dist/', import.meta.url))
// Prevent newly added app widgets from silently restoring browser-native UI.
async function checkSources(directory) {
  for (const entry of await readdir(directory, { withFileTypes: true })) {
    const file = join(directory, entry.name)
    if (entry.isDirectory()) { await checkSources(file); continue }
    if (!file.endsWith('.tsx') || file.endsWith('.test.tsx')) continue
    const source = await readFile(file, 'utf8')
    assert.ok(!/window\.(?:alert|confirm|prompt)\s*\(/.test(source), `native dialog in ${file}`)
    const ast = ts.createSourceFile(file, source, ts.ScriptTarget.Latest, true, ts.ScriptKind.TSX)
    function visit(node) {
      if (ts.isJsxOpeningElement(node) || ts.isJsxSelfClosingElement(node)) {
        const tag = node.tagName.getText(ast)
        assert.ok(!['select', 'option'].includes(tag), `native select in ${file}`)
        assert.ok(tag !== 'form' || file.endsWith('/Form.tsx'), `form bypasses custom validation in ${file}`)
        if (tag === tag.toLowerCase()) for (const prop of node.attributes.properties) {
          assert.ok(!ts.isJsxAttribute(prop) || prop.name.getText(ast) !== 'title', `native tooltip in ${file}`)
        }
      }
      ts.forEachChild(node, visit)
    }
    visit(ast)
  }
}
await checkSources(fileURLToPath(new URL('../web/src/', import.meta.url)))
const calls = [], removed = new Set()
const host = { id: 'fixture-host', name: '开发主机', transport: 'ssh', hostname: 'dev.example.test', username: 'dev', port: 22 }
const admin = { id: 'admin', display_name: '管理员', email: 'admin@example.test', role: 'admin', active_sessions: 1 }
const member = { ...admin, id: 'member', display_name: '成员', role: 'user', disabled: false }
const pager = { offset: 0, limit: 50, has_more: false, next_offset: 0 }
const server = createServer(async (req, res) => {
  const url = new URL(req.url, 'http://localhost'), path = url.pathname, scope = req.headers['x-test-scope'], guest = scope === 'guest'
  res.setHeader('cache-control', 'no-store')
  if (path.startsWith('/api/')) {
    let raw = ''; for await (const chunk of req) raw += chunk
    calls.push({ path, method: req.method, scope, query: Object.fromEntries(url.searchParams), body: raw && JSON.parse(raw) })
    let body = { ok: true }, status = 200
    if (path === '/api/bootstrap/status') body = { required: false, registration: 'closed' }
    else if (path === '/api/me') { body = { user: admin, csrf_token: 'isolated-ui-fixture', session_id: 'admin' }; if (guest) { status = 401; body = { error: 'unauthorized' } } }
    else if (path === '/api/hosts/' && req.method === 'GET') body = { hosts: removed.has(scope) ? [] : [host] }
    else if (path === '/api/hosts/fixture-host/' && req.method === 'DELETE') removed.add(scope)
    else if (path === '/api/host-folders/') body = { folders: Array.from({ length: 48 }, (_, i) => ({ id: `folder-${i}`, name: `文件夹 ${String(i).padStart(2, '0')} ${'较长的文件夹名称'.repeat(3)}`, parent_id: '', position: i })) }
    else if (path === '/api/ssh-keys/') body = { keys: [{ id: 'key', name: '开发密钥', algorithm: 'ssh-ed25519', fingerprint: 'SHA256:fixture' }] }
    else if (path === '/api/cli-release') body = { status: 'unpublished' }
    else if (path === '/api/admin/settings') body = { settings: { registration: 'closed', revision: 0 } }
    else if (path === '/api/admin/users') body = { ...pager, users: [admin, member] }
    else if (path === '/api/admin/audit') body = { ...pager, events: [] }
    else if (path === '/api/admin/invites') body = { ...pager, invites: [{ id: 'invite', status: 'active', created_by: 'admin' }] }
    else if (path.endsWith('/sessions')) body = { ...pager, sessions: [] }
    res.writeHead(status, { 'content-type': 'application/json' }); res.end(JSON.stringify(body)); return
  }
  try {
    const file = path.startsWith('/assets/') || path.startsWith('/brand/') || ['/boot.js', '/sw.js', '/manifest.webmanifest', '/favicon.ico'].includes(path) ? path.slice(1) : 'index.html'
    res.setHeader('content-type', file.endsWith('.js') ? 'application/javascript' : file.endsWith('.css') ? 'text/css' : file.endsWith('.png') ? 'image/png' : file.endsWith('.ico') ? 'image/x-icon' : 'text/html')
    res.end(await readFile(join(dist, file)))
  } catch { res.writeHead(404); res.end() }
})
await new Promise(done => server.listen(0, '127.0.0.1', done))
const base = `http://127.0.0.1:${server.address().port}`
async function noNativeUI(page) {
  assert.deepEqual(await page.evaluate(() => ({
    native: [...document.querySelectorAll('select:not([aria-hidden="true"]), [title], input[type=date], input[type=time], input[type=datetime-local], input[type=color]')].map(node => node.outerHTML.slice(0, 150)),
    validation: [...document.forms].filter(form => !form.noValidate).map(form => form.outerHTML.slice(0, 100)),
  })), { native: [], validation: [] })
}
async function fits(locator, page) {
  const box = await locator.boundingBox(), viewport = page.viewportSize()
  assert.ok(box && box.x >= 0 && box.y >= 0 && box.x + box.width <= viewport.width + 1 && box.y + box.height <= viewport.height + 1, `overlay outside viewport: ${JSON.stringify(box)}`)
}
async function screenshot(page, file) {
  if (!process.env.HERDRX_UI_SCREENSHOTS) return
  await mkdir(process.env.HERDRX_UI_SCREENSHOTS, { recursive: true })
  await page.screenshot({ path: join(process.env.HERDRX_UI_SCREENSHOTS, file + '.png') })
}
try {
  for (const engine of process.env.HERDRX_TEST_ENGINES?.split(',') || ['chromium']) {
    const browser = await ({ chromium, firefox, webkit })[engine].launch()
    try {
      for (const width of [1440, 390, 320]) {
        const scope = `${engine}-${width}`
        const context = await browser.newContext({ viewport: { width, height: width === 320 ? 560 : 900 }, hasTouch: width < 768, extraHTTPHeaders: { 'x-test-scope': scope } })
        const page = await context.newPage(), errors = [], native = []
        page.on('pageerror', error => errors.push(error.message)); page.on('dialog', dialog => { native.push(dialog.type()); void dialog.dismiss() })
        await page.goto(base)
        const add = page.getByRole('button', { name: '添加主机', exact: true })
        await add.click()
        const trigger = page.getByRole('combobox', { name: '连接方式', exact: true })
        await expect(trigger).toContainText('Tailcat')
        await trigger.press('ArrowDown')
        await expect(page.getByRole('listbox')).toBeVisible()
        await page.keyboard.press('Home'); await expect(page.getByRole('option', { name: /Tailcat/ })).toBeFocused(); await page.keyboard.press('ArrowDown'); await expect(page.getByRole('option', { name: 'SSH', exact: true })).toBeFocused(); await page.keyboard.press('Enter')
        await expect(trigger).toHaveAttribute('data-value', 'ssh'); await expect(trigger).toBeFocused()
        await trigger.click(); await page.keyboard.press('Escape')
        await expect(page.getByRole('listbox')).toHaveCount(0); await expect(page.getByRole('dialog', { name: '添加主机' })).toBeVisible()
        await chooseOption(page.getByRole('combobox', { name: '认证密钥', exact: true }), '')
        await page.getByRole('button', { name: '保存主机' }).click()
        await expect(page.getByLabel('主机名或 IP')).toHaveAttribute('aria-invalid', 'true')
        await expect(page.getByLabel('认证密钥', { exact: true })).toHaveAttribute('aria-invalid', 'true')
        assert.equal(calls.filter(call => call.scope === scope && call.path === '/api/hosts/' && call.method === 'POST').length, 0)
        await page.getByLabel('主机名或 IP').fill('example.test'); await page.getByLabel('SSH 用户', { exact: true }).fill('dev')
        await page.getByLabel('端口', { exact: true }).fill('65536'); await page.getByRole('button', { name: '保存主机' }).click()
        await expect(page.getByText('请输入不大于 65535 的数值。')).toBeVisible()
        await chooseOption(page.getByRole('combobox', { name: '认证密钥', exact: true }), 'key')
        await expect(page.getByRole('combobox', { name: '认证密钥', exact: true })).not.toHaveAttribute('aria-invalid', 'true')
        const folders = page.getByRole('combobox', { name: '所属文件夹', exact: true })
        await folders.click(); await fits(page.getByRole('listbox'), page)
        await page.keyboard.press('End'); await expect(page.getByRole('option').last()).toBeFocused(); await page.keyboard.press('Enter'); await expect(folders).toHaveAttribute('data-value', 'folder-47')
        await folders.click(); await screenshot(page, `${engine}-custom-select-${width}`); await noNativeUI(page)
        await page.keyboard.press('Escape'); await page.keyboard.press('Escape'); await expect(add).toBeFocused()
        const remove = page.getByRole('button', { name: '删除 开发主机', exact: true })
        if (width === 1440) { await remove.hover(); await expect(page.getByRole('tooltip')).toHaveText('删除主机'); await noNativeUI(page) }
        await remove.click(); const dialog = page.getByRole('alertdialog')
        await expect(dialog.getByRole('button', { name: '取消', exact: true })).toBeFocused(); await fits(dialog, page)
        await page.keyboard.press('Escape'); await expect(remove).toBeFocused()
        assert.equal(calls.filter(call => call.scope === scope && call.method === 'DELETE').length, 0)
        await remove.click(); await screenshot(page, `${engine}-custom-confirm-${width}`); await acceptConfirmation(page)
        await expect(page.getByRole('heading', { name: '开发主机', exact: true })).toHaveCount(0)
        assert.equal(calls.filter(call => call.scope === scope && call.method === 'DELETE').length, 1)
        await page.getByRole('button', { name: '管理', exact: true }).click()
        await chooseOption(page.getByRole('combobox', { name: '角色', exact: true }), 'user')
        await chooseOption(page.getByRole('combobox', { name: '账号状态', exact: true }), 'disabled')
        await page.getByRole('button', { name: '筛选用户', exact: true }).click()
        await expect.poll(() => calls.filter(call => call.scope === scope && call.path === '/api/admin/users').at(-1)?.query.role).toBe('user')
        await chooseOption(page.getByRole('combobox', { name: '角色', exact: true }), '')
        await expect(page.getByRole('combobox', { name: '角色', exact: true })).toContainText('全部角色')
        await page.getByRole('button', { name: '审计记录' }).click()
        for (const theme of ['light', 'dark']) {
          if (theme === 'dark') await page.getByRole('button', { name: '切换为深色', exact: true }).click()
          await page.getByRole('button', { name: '开始时间', exact: true }).click()
          const calendar = page.getByRole('dialog', { name: '开始时间选择器' })
          await fits(calendar, page)
          await chooseOption(page.getByRole('combobox', { name: '年份' }), '2026')
          await chooseOption(page.getByRole('combobox', { name: '月份' }), '1')
          await calendar.getByRole('button', { name: '2026-02-28' }).click()
          await calendar.getByRole('textbox', { name: '小时' }).fill('24'); await expect(calendar.getByRole('button', { name: '应用时间' })).toBeDisabled()
          await calendar.getByRole('textbox', { name: '小时' }).fill('09'); await calendar.getByRole('textbox', { name: '分钟' }).fill('07')
          await screenshot(page, `${engine}-custom-calendar-${width}-${theme}`); await noNativeUI(page)
          await calendar.getByRole('button', { name: '应用时间' }).click()
          await expect(page.getByRole('button', { name: '开始时间', exact: true })).toHaveAttribute('data-value', '2026-02-28T09:07')
          await page.getByRole('button', { name: '筛选', exact: true }).click()
          const expected = await page.evaluate(() => new Date('2026-02-28T09:07').toISOString())
          await expect.poll(() => calls.filter(call => call.scope === scope && call.path === '/api/admin/audit').at(-1)?.query.since).toBe(expected)
          await page.getByRole('button', { name: '开始时间', exact: true }).click(); await page.getByRole('button', { name: '清空', exact: true }).click()
        }
        assert.deepEqual(errors, []); assert.deepEqual(native, []); await context.close()
      }
      const guest = await browser.newContext({ extraHTTPHeaders: { 'x-test-scope': 'guest' } }), page = await guest.newPage()
      await page.goto(base); await page.getByRole('button', { name: '登录', exact: true }).click()
      await expect(page.getByLabel('邮箱', { exact: true })).toHaveAttribute('aria-invalid', 'true'); await expect(page.getByLabel('邮箱', { exact: true })).toBeFocused()
      await page.getByLabel('邮箱', { exact: true }).fill('invalid'); await page.getByRole('button', { name: '登录', exact: true }).click()
      await expect(page.getByText('请输入完整的邮箱地址，例如 name@example.com。')).toBeVisible(); await noNativeUI(page)
      assert.equal(calls.filter(call => call.scope === 'guest' && call.method === 'POST').length, 0); await guest.close()
      console.log(`${engine}: custom selects, nested Escape/focus, long lists, empty values, form validation, cancel/confirm exactly once, tooltips, date/time filters, light/dark and 1440/390/320px passed`)
    } finally { await browser.close() }
  }
} finally { await new Promise(done => server.close(done)) }
