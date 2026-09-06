#!/usr/bin/env node
import assert from 'node:assert/strict'
import { createServer } from 'node:http'
import { readFile } from 'node:fs/promises'
import { join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { chooseOption } from './browser-controls.mjs'
import { chromium, firefox, webkit, expect } from '../web/node_modules/@playwright/test/index.mjs'

const dist = fileURLToPath(new URL('../web/dist/', import.meta.url))
const worker = await readFile(join(dist, 'sw.js'), 'utf8')
assert.ok(!worker.includes('__BUILD_ID__') && !worker.includes('__PRECACHE__'), 'build must generate complete PWA precache')
const manifest = JSON.parse(await readFile(join(dist, 'manifest.webmanifest'), 'utf8'))
assert.equal(manifest.id, '/'); assert.equal(manifest.scope, '/'); assert.equal(manifest.display, 'standalone')
for (const icon of manifest.icons) assert.ok((await readFile(join(dist, icon.src))).length > 0)
let revision = 'one', denyAPI = false, failAsset = false, droppedNetwork = false
const calls = []
const server = createServer(async (req, res) => {
  if (droppedNetwork) { req.socket.destroy(); return }
  const path = new URL(req.url, 'http://localhost').pathname
  res.setHeader('cache-control', 'no-store')
  // Match Go http.FileServer, including the redirected precache response.
  if (path === '/index.html') { res.writeHead(301, { location: '/' }); res.end(); return }
  if (path.startsWith('/api/')) {
    calls.push({ path, method: req.method })
    let body = { ok: true }, status = 200
    if (path === '/api/bootstrap/status') body = { required: false, registration: 'closed' }
    else if (path === '/api/me') {
      if (denyAPI) { status = 401; body = { error: 'unauthorized' } }
      else body = { user: { id: 'pwa-user', email: 'pwa@example.test', display_name: 'PWA 验收', role: 'admin' }, csrf_token: 'test', session_id: 'pwa-session' }
    } else if (path === '/api/hosts/') body = { hosts: [] }
    else if (path === '/api/host-folders/') body = { folders: [] }
    else if (path === '/api/ssh-keys/') body = { keys: [] }
    else if (path === '/api/tailcat/enrollments') body = { tasks: [] }
    res.writeHead(status, { 'content-type': 'application/json' }); res.end(JSON.stringify(body)); return
  }
  const file = path.startsWith('/assets/') || path.startsWith('/brand/') || ['/boot.js', '/sw.js', '/manifest.webmanifest', '/favicon.ico', '/index.html'].includes(path) ? path.slice(1) : 'index.html'
  try {
    if (failAsset && file.endsWith('.css')) { res.writeHead(503); res.end('test failed download'); return }
    let bytes = await readFile(join(dist, file))
    if (file === 'sw.js') bytes = Buffer.from(worker.replace(/herdrx-shell-[a-f0-9]+/, `herdrx-shell-test-${revision}`))
    if (file === 'index.html') bytes = Buffer.from(bytes.toString().replace('<html ', `<html data-test-build="${revision}" `))
    res.setHeader('content-type', file.endsWith('.js') ? 'application/javascript' : file.endsWith('.css') ? 'text/css' : file.endsWith('.png') ? 'image/png' : file.endsWith('.ico') ? 'image/x-icon' : file.endsWith('.webmanifest') ? 'application/manifest+json' : 'text/html')
    res.end(bytes)
  } catch { res.writeHead(404); res.end() }
})
await new Promise(done => server.listen(0, '127.0.0.1', done))
const base = `http://127.0.0.1:${server.address().port}`
const checkUpdate = page => page.evaluate(async () => { await (await navigator.serviceWorker.getRegistration()).update() })
const controllerReady = page => page.waitForFunction(() => Boolean(navigator.serviceWorker.controller))
try {
  for (const engine of process.env.HERDRX_TEST_ENGINES?.split(',') || ['chromium', 'firefox', 'webkit']) {
    revision = 'one'; denyAPI = false; failAsset = false; droppedNetwork = false; calls.length = 0
    const browser = await ({ chromium, firefox, webkit })[engine].launch()
    const context = await browser.newContext({ viewport: { width: 390, height: 844 } })
    const page = await context.newPage(), errors = []
    page.on('pageerror', error => errors.push(error.message))
    page.on('dialog', dialog => { errors.push(`Unexpected dialog: ${dialog.type()}`); void dialog.dismiss() })
    try {
      await page.goto(base)
      await expect(page.getByRole('heading', { name: '主机', exact: true })).toBeVisible()
      await controllerReady(page)
      await page.getByRole('button', { name: '安装应用', exact: true }).click()
      await expect(page.getByRole('dialog')).toContainText('添加到程序坞')
      assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth))
      await page.keyboard.press('Escape')
      // API results are never available through CacheStorage, even after login.
      const cachedPaths = await page.evaluate(async () => {
        const paths = []
        for (const name of await caches.keys()) for (const req of await (await caches.open(name)).keys()) paths.push(new URL(req.url).pathname)
        return paths
      })
      assert.ok(cachedPaths.includes('/index.html') && cachedPaths.some(path => /WorkbenchPage.*\.js$/.test(path)))
      assert.ok(!cachedPaths.some(path => path.startsWith('/api/')))
      assert.equal(await page.evaluate(async () => (await (await caches.open('herdrx-shell-test-one')).match('/index.html')).redirected), true)
      await page.evaluate(() => caches.open('unrelated-app').then(cache => cache.put('/unrelated-marker', new Response('keep'))))
      await page.reload(); await controllerReady(page)
      droppedNetwork = true; if (engine !== 'webkit') await context.setOffline(true)
      await page.goto(base + '/h/original-pane')
      await expect(page.getByRole('heading', { name: engine === 'webkit' ? '无法读取登录状态' : '当前处于离线状态' })).toBeVisible()
      assert.equal(new URL(page.url()).pathname, '/h/original-pane')
      droppedNetwork = false; if (engine !== 'webkit') await context.setOffline(false)
      // Return to the host list after automatic auth recovery (fixture has no terminal).
      await page.goto(base)
      await expect(page.getByRole('heading', { name: '主机', exact: true })).toBeVisible()
      const other = await context.newPage()
      await other.goto(base)
      await controllerReady(other)
      await other.getByRole('button', { name: '添加主机', exact: true }).click()
      await chooseOption(other.getByRole('combobox', { name: '连接方式' }), 'ssh')
      await other.getByRole('textbox', { name: '名称', exact: true }).fill('保留未提交内容')
      // Install two while both pages are still on one; never silently refresh.
      revision = 'two'; await checkUpdate(page)
      await expect(page.getByRole('complementary', { name: '应用更新' })).toBeVisible()
      assert.equal(await page.locator('html').getAttribute('data-test-build'), 'one')
      assert.equal(await other.locator('html').getAttribute('data-test-build'), 'one')
      await page.getByRole('button', { name: '刷新更新', exact: true }).click()
      await expect(page.locator('html')).toHaveAttribute('data-test-build', 'two')
      assert.equal(await other.locator('html').getAttribute('data-test-build'), 'one')
      await expect(other.getByRole('textbox', { name: '名称', exact: true })).toHaveValue('保留未提交内容')
      await other.close()
      // A broken deployment leaves the previous working shell active and usable.
      revision = 'three'; failAsset = true; await checkUpdate(page)
      await page.waitForFunction(async () => { const reg = await navigator.serviceWorker.getRegistration(); return !reg.installing && !reg.waiting })
      assert.equal(await page.evaluate(async () => (await caches.keys()).includes('herdrx-shell-test-three')), false)
      droppedNetwork = true; if (engine !== 'webkit') await context.setOffline(true); await page.reload()
      await expect(page.getByRole('heading', { name: engine === 'webkit' ? '无法读取登录状态' : '当前处于离线状态' })).toBeVisible()
      await expect(page.locator('html')).toHaveAttribute('data-test-build', 'two')
      failAsset = false; revision = 'two'; denyAPI = true
      droppedNetwork = false; if (engine !== 'webkit') await context.setOffline(false)
      await expect(page.getByRole('heading', { name: '欢迎回来' })).toBeVisible({ timeout: 12000 })
      assert.ok(await page.evaluate(async () => Boolean(await (await caches.open('unrelated-app')).match('/unrelated-marker'))))
      assert.ok(!calls.some(call => call.method !== 'GET'), 'offline and update paths must never replay mutations')
      assert.deepEqual(errors, [])
      console.log(`${engine}: PWA offline shell, uncached auth, network recovery, deferred update, two tabs, failed precache, revoked session PASS`)
    } finally { await context.close(); await browser.close() }
  }
} finally { await new Promise(done => server.close(done)) }
