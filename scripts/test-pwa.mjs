#!/usr/bin/env node
import assert from 'node:assert/strict'
import { createServer } from 'node:http'
import { mkdir, readFile, writeFile } from 'node:fs/promises'
import { join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { chooseOption } from './browser-controls.mjs'
import { chromium, firefox, webkit, expect } from '../web/node_modules/@playwright/test/index.mjs'

const dist = fileURLToPath(new URL('../web/dist/', import.meta.url))
const diagnostics = process.env.HERDRX_PWA_DIAGNOSTICS === '1'
const forceTimeouts = process.env.HERDRX_PWA_FORCE_TIMEOUTS === '1'
assert.ok(!forceTimeouts || diagnostics, 'forced PWA timeouts require explicit diagnostics mode')
const diagnosticsDir = process.env.HERDRX_PWA_DIAGNOSTICS_DIR
const diagnosticPrefix = '__HERDRX_AUTH_DIAG__'
const diagnosticRun = `${Date.now()}-${process.pid}`
const worker = await readFile(join(dist, 'sw.js'), 'utf8')
assert.ok(!worker.includes('__BUILD_ID__') && !worker.includes('__PRECACHE__'), 'build must generate complete PWA precache')
const manifest = JSON.parse(await readFile(join(dist, 'manifest.webmanifest'), 'utf8'))
assert.equal(manifest.id, '/'); assert.equal(manifest.scope, '/'); assert.equal(manifest.display, 'standalone')
assert.equal(manifest.orientation, 'any')
assert.ok(manifest.icons.some(icon => icon.sizes === '192x192' && icon.purpose === 'maskable'))
for (const icon of manifest.icons) assert.ok((await readFile(join(dist, icon.src))).length > 0)
const html = await readFile(join(dist, 'index.html'), 'utf8')
assert.ok(html.includes('apple-mobile-web-app-title'), 'PWA title meta')
assert.ok(html.includes('interactive-widget=resizes-content'), 'viewport keyboard widget')
assert.ok(html.includes('<script>'), 'boot.js must be inlined')
assert.ok(!html.includes('src="/boot.js"'), 'boot.js must not remain an extra request')
const workerPrecache = JSON.parse(worker.match(/const PRECACHE = (\[[\s\S]*?\n?])/)?.[1] || worker.match(/const PRECACHE = (\[.*\])/)[1])
assert.ok(!workerPrecache.includes('/boot.js'), 'inlined boot.js stays out of precache')
assert.ok(!workerPrecache.some(path => /logo|icon-1024|icon-256/.test(path)), 'unused brand files stay out of precache')
assert.ok(!workerPrecache.some(path => path.endsWith('.gz') || path.endsWith('.br')))
let revision = 'one', denyAPI = false, failAsset = false, droppedNetwork = false
let phase = 'initial'
let currentEngine = '', forcedRecoveryHeld = false
const calls = []
const authTrace = []
function traceAuth(event, url, details = {}) {
  const path = new URL(url, 'http://localhost').pathname
  if (path === '/api/bootstrap/status' || path === '/api/me') authTrace.push({ at: Date.now(), phase, event, path, ...details })
}
const server = createServer(async (req, res) => {
  traceAuth('server-request', req.url, { droppedNetwork, denyAPI })
  const path = new URL(req.url, 'http://localhost').pathname
  if (forceTimeouts && currentEngine === 'webkit' && path === '/api/bootstrap/status' &&
      (phase === 'second-offline' || (phase === 'revoked-session-recovery' && !forcedRecoveryHeld))) {
    // Leave the real HTTP response pending until the application's existing
    // eight-second deadline aborts fetch. Do not patch fetch, clocks or signals.
    if (phase === 'revoked-session-recovery') forcedRecoveryHeld = true
    const heldPhase = phase, heldAt = Date.now()
    traceAuth('server-held', req.url, { heldPhase })
    res.once('close', () => traceAuth('server-held-closed', req.url, { heldPhase, elapsed: Date.now() - heldAt }))
    return
  }
  if (droppedNetwork) { req.socket.destroy(); return }
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
    } else if (path === '/api/me/workbench-session') body = { session: null }
    else if (path === '/api/hosts/') body = { hosts: [] }
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
// WebKit reports fixture-destroyed or navigation-aborted same-origin API
// fetches as pageerror "due to access control checks". Those are not CORS
// bugs; HostsPage and restore issue GET /api/hosts|/host-folders|/ssh-keys|
// /me/workbench-session when the offline path tears the socket down.
function unexpectedErrors(engine, errors) {
  if (engine !== 'webkit') return errors
  return errors.filter((message) => !/\/api\/\S+ due to access control checks\.?$/.test(message))
}

async function assertUnrelatedCache(page, phase) {
  const state = await page.evaluate(async () => {
    const names = await caches.keys()
    if (!names.includes('unrelated-app')) return { names, paths: [], marker: null }
    const cache = await caches.open('unrelated-app')
    const paths = (await cache.keys()).map(request => new URL(request.url).pathname)
    const marker = await cache.match('/unrelated-marker')
    return { names, paths, marker: marker ? await marker.text() : null }
  })
  assert.equal(state.marker, 'keep', `unrelated cache lost ${phase}: ${JSON.stringify(state)}`)
}
try {
  for (const engine of process.env.HERDRX_TEST_ENGINES?.split(',') || ['chromium', 'firefox', 'webkit']) {
    revision = 'one'; denyAPI = false; failAsset = false; droppedNetwork = false; calls.length = 0; authTrace.length = 0; phase = 'initial'
    currentEngine = engine; forcedRecoveryHeld = false
    const browser = await ({ chromium, firefox, webkit })[engine].launch()
    const context = await browser.newContext({ viewport: { width: 390, height: 844 } })
    const runtimeTrace = []
    if (diagnostics) {
      // This collector is inert with a normal build. Only the explicit Vite
      // diagnostic config emits events; no fetch/Abort methods are wrapped.
      await context.addInitScript(({ prefix }) => {
        const identities = new WeakMap()
        let nextIdentity = 0
        let count = 0
        const documentID = `${Date.now()}-${Math.random().toString(36).slice(2)}`
        globalThis.__herdrxAuthDiagnostics = {
          events: [],
          identity(value) {
            if ((typeof value !== 'object' || value === null) && typeof value !== 'function') return null
            if (!identities.has(value)) identities.set(value, ++nextIdentity)
            return identities.get(value)
          },
          record(event, details) {
            try {
              if (count++ >= 3000) return
              const entry = { at: Date.now(), documentID, path: location.pathname, event, ...details }
              this.events.push(entry)
              if (this.events.length > 400) this.events.shift()
              console.debug(prefix + JSON.stringify(entry))
            } catch { /* Diagnostics must never change authentication behavior. */ }
          },
        }
      }, { prefix: diagnosticPrefix })
      context.on('console', message => {
        const text = message.text()
        if (!text.startsWith(diagnosticPrefix) || runtimeTrace.length >= 12000) return
        try { runtimeTrace.push({ phase, ...JSON.parse(text.slice(diagnosticPrefix.length)) }) } catch { /* Keep the test's original result. */ }
      })
    }
    const page = await context.newPage(), errors = []
    let diagnosticResult = 'failed'
    page.on('pageerror', error => errors.push(error.message))
    page.on('request', request => traceAuth('request', request.url()))
    page.on('response', response => traceAuth('response', response.url(), { status: response.status() }))
    page.on('requestfailed', request => traceAuth('failed', request.url(), { error: request.failure()?.errorText }))
    if (diagnostics) page.on('requestfinished', request => traceAuth('finished', request.url()))
    page.on('dialog', dialog => { errors.push(`Unexpected dialog: ${dialog.type()}`); void dialog.dismiss() })
    try {
      await page.goto(base)
      await expect(page.getByRole('heading', { name: '主机', exact: true })).toBeVisible()
      if (diagnostics) assert.ok(await page.evaluate(() => globalThis.__herdrxAuthDiagnostics.events.some(event => event.event === 'auth.render')), 'PWA diagnostics require vite.pwa-diagnostics.config.ts')
      await controllerReady(page)
      // A repeated controller notification for the same worker is not an update.
      await page.evaluate(() => navigator.serviceWorker.dispatchEvent(new Event('controllerchange')))
      await page.evaluate(() => new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve))))
      await expect(page.getByRole('complementary', { name: '应用更新' })).toHaveCount(0)
      // Capture beforeinstallprompt so the page can show a CTA; the actual install confirmation is still the browser prompt().
      assert.equal(await page.evaluate(() => window.dispatchEvent(new Event('beforeinstallprompt', { cancelable: true }))), false)
      await expect(page.getByRole('complementary', { name: '添加到主屏幕' })).toBeVisible()
      assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth))
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
      // Verify the seed before tearing down its document, and isolate the phase
      // of any storage loss without recreating a missing cache during assertions.
      await assertUnrelatedCache(page, 'after seed')
      await page.reload(); await controllerReady(page)
      await assertUnrelatedCache(page, 'after initial reload')
      phase = 'first-offline'
      droppedNetwork = true; if (engine !== 'webkit') await context.setOffline(true)
      await page.goto(base + '/h/original-pane')
      // WebKit uses real socket failures here; hung /api/me is released by the
      // 8s auth-check timeout, then the error heading can render.
      const offlineTimeout = engine === 'webkit' ? 30000 : 5000
      await expect(page.getByRole('heading', { name: engine === 'webkit' ? '无法读取登录状态' : '当前处于离线状态' })).toBeVisible({ timeout: offlineTimeout })
      assert.equal(new URL(page.url()).pathname, '/h/original-pane')
      phase = 'first-recovery'
      droppedNetwork = false; if (engine !== 'webkit') await context.setOffline(false)
      // Return to the host list after automatic auth recovery (fixture has no terminal).
      await page.goto(base)
      await expect(page.getByRole('heading', { name: '主机', exact: true })).toBeVisible()
      await assertUnrelatedCache(page, 'after offline recovery')
      const other = await context.newPage()
      await other.goto(base)
      await controllerReady(other)
      await other.getByRole('button', { name: '添加主机', exact: true }).click()
      await chooseOption(other.getByRole('combobox', { name: '连接方式' }), 'ssh')
      await other.getByRole('textbox', { name: '名称', exact: true }).fill('保留未提交内容')
      // Install two while both pages are still on one; never silently refresh.
      phase = 'successful-update'
      revision = 'two'; await checkUpdate(page)
      await expect(page.getByRole('complementary', { name: '应用更新' })).toBeVisible()
      assert.equal(await page.locator('html').getAttribute('data-test-build'), 'one')
      assert.equal(await other.locator('html').getAttribute('data-test-build'), 'one')
      await page.getByRole('button', { name: '刷新更新', exact: true }).click()
      await expect(page.locator('html')).toHaveAttribute('data-test-build', 'two')
      assert.equal(await other.locator('html').getAttribute('data-test-build'), 'one')
      await expect(other.getByRole('textbox', { name: '名称', exact: true })).toHaveValue('保留未提交内容')
      await assertUnrelatedCache(page, 'after successful update')
      await other.close()
      // A broken deployment leaves the previous working shell active and usable.
      // Observe the new worker before starting the update: an idle registration
      // can briefly have no installing/waiting worker before installation begins.
      await page.evaluate(async () => {
        const registration = await navigator.serviceWorker.getRegistration()
        globalThis.__failedUpdateWorker = null
        registration.addEventListener('updatefound', () => {
          globalThis.__failedUpdateWorker = registration.installing
        }, { once: true })
      })
      phase = 'failed-update'
      revision = 'three'; failAsset = true; await checkUpdate(page)
      await page.waitForFunction(() => globalThis.__failedUpdateWorker?.state === 'redundant')
      assert.equal(await page.evaluate(async () => (await caches.keys()).includes('herdrx-shell-test-three')), false)
      await assertUnrelatedCache(page, 'after failed precache')
      phase = 'second-offline'
      droppedNetwork = true; if (engine !== 'webkit') await context.setOffline(true); await page.reload()
      await expect(page.getByRole('heading', { name: engine === 'webkit' ? '无法读取登录状态' : '当前处于离线状态' })).toBeVisible({ timeout: offlineTimeout })
      await expect(page.locator('html')).toHaveAttribute('data-test-build', 'two')
      phase = 'revoked-session-recovery'
      failAsset = false; revision = 'two'; denyAPI = true
      droppedNetwork = false; if (engine !== 'webkit') await context.setOffline(false)
      // Destroying fixture sockets does not send WebKit an online event.
      // Exercise the error page's manual reconnect without reloading the shell.
      if (engine === 'webkit') {
        const reconnect = page.getByRole('button', { name: '重新连接', exact: true })
        if (await reconnect.isVisible()) await reconnect.click()
      }
      await expect(page.getByRole('heading', { name: '欢迎回来' })).toBeVisible({ timeout: engine === 'webkit' ? offlineTimeout : 12000 })
      if (forceTimeouts && engine === 'webkit') {
        for (const expectedPhase of ['second-offline', 'revoked-session-recovery']) {
          assert.ok(runtimeTrace.some(event => event.phase === expectedPhase && event.event === 'timeout.fire'), `${expectedPhase} must exercise the native authentication timeout`)
        }
      }
      await assertUnrelatedCache(page, 'after session revocation')
      assert.ok(!calls.some(call => call.method !== 'GET'), 'offline and update paths must never replay mutations')
      assert.deepEqual(unexpectedErrors(engine, errors), [])
      diagnosticResult = 'passed'
      console.log(`${engine}: PWA offline shell, uncached auth, network recovery, deferred update, two tabs, failed precache, revoked session PASS`)
    } catch (error) {
      const state = await page.evaluate(() => ({ hidden: document.hidden, visibility: document.visibilityState, online: navigator.onLine })).catch(() => null)
      console.error(`${engine}: PWA failure diagnostics`, JSON.stringify({ state, authTrace: authTrace.slice(-80), errors, ...(diagnostics ? { runtimeTrace } : {}) }, null, 2))
      throw error
    } finally {
      try {
        if (diagnostics && diagnosticsDir) {
          const state = await page.evaluate(() => ({ hidden: document.hidden, visibility: document.visibilityState, online: navigator.onLine, heading: document.querySelector('h1')?.textContent, alert: document.querySelector('[role="alert"]')?.textContent })).catch(() => null)
          await mkdir(diagnosticsDir, { recursive: true })
          const output = join(diagnosticsDir, `${diagnosticRun}-${engine}-${Date.now()}.json`)
          await writeFile(output, JSON.stringify({ result: diagnosticResult, engine, browserVersion: browser.version(), node: process.version, forceTimeouts, phase, state, authTrace, runtimeTrace, errors }, null, 2) + '\n')
          console.log(`${engine}: PWA auth diagnostics saved: ${output}`)
        }
      } finally { await context.close(); await browser.close() }
    }
  }
} finally { await new Promise(done => server.close(done)) }
