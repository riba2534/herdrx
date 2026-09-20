#!/usr/bin/env node
// Linux native WebKit regression: only terminate the Networking child of the
// browser launched here. No host services, Herdr sessions or existing browsers.
import assert from 'node:assert/strict'
import { createServer } from 'node:http'
import { readFileSync } from 'node:fs'
import { readFile } from 'node:fs/promises'
import { basename, join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { webkit, expect } from '../web/node_modules/@playwright/test/index.mjs'
import { chooseOption } from './browser-controls.mjs'

assert.equal(process.platform, 'linux', 'native WebKit recovery requires Linux /proc ownership checks')
const dist = process.env.HERDRX_TEST_WEB_DIST || fileURLToPath(new URL('../web/dist/', import.meta.url))
let bootstrapUnavailable = true, authorized = true, logins = 0
const calls = []
const user = { id: 'pwa-network-user', email: 'pwa@example.test', display_name: '网络恢复验收', role: 'admin' }
const session = () => ({ user, csrf_token: 'fixture-csrf', session_id: `fixture-session-${logins}` })
const server = createServer(async (req, res) => {
  const path = new URL(req.url, 'http://localhost').pathname
  res.setHeader('cache-control', 'no-store')
  if (path.startsWith('/api/')) {
    calls.push({ method: req.method, path })
    let status = 200, body
    if (path === '/api/bootstrap/status') {
      status = bootstrapUnavailable ? 503 : 200
      body = bootstrapUnavailable ? { error: '网络暂时不可用，请重试' } : { required: false, registration: 'closed' }
    } else if (path === '/api/me') {
      status = authorized ? 200 : 401
      body = authorized ? session() : { code: 'unauthorized', error: '请重新登录' }
    } else if (path === '/api/login' && req.method === 'POST') {
      authorized = true; logins++; body = session()
    } else if (path === '/api/hosts/') body = { hosts: [] }
    else if (path === '/api/host-folders/') body = { folders: [] }
    else if (path === '/api/ssh-keys/') body = { keys: [] }
    else if (path === '/api/tailcat/enrollments') body = { tasks: [] }
    else if (path === '/api/me/workbench-session') body = { session: null }
    else if (path === '/api/cli-release') body = { status: 'unpublished' }
    else if (path === '/api/tailcat/relay-offer') body = { available: false }
    else { status = 404; body = { error: 'unknown fixture endpoint' } }
    res.writeHead(status, { 'content-type': 'application/json' }); res.end(JSON.stringify(body)); return
  }
  if (path === '/index.html') { res.writeHead(301, { location: '/' }); res.end(); return }
  const file = path.startsWith('/assets/') || path.startsWith('/brand/') || ['/sw.js', '/manifest.webmanifest', '/favicon.ico'].includes(path) ? path.slice(1) : 'index.html'
  try {
    const bytes = await readFile(join(dist, file))
    res.setHeader('content-type', file.endsWith('.js') ? 'application/javascript' : file.endsWith('.css') ? 'text/css' : file.endsWith('.png') ? 'image/png' : file.endsWith('.ico') ? 'image/x-icon' : file.endsWith('.webmanifest') ? 'application/manifest+json' : 'text/html')
    res.end(bytes)
  } catch { res.writeHead(404); res.end() }
})

function processInfo(pid) {
  try {
    const stat = readFileSync(`/proc/${pid}/stat`, 'utf8')
    const fields = stat.slice(stat.lastIndexOf(') ') + 2).split(' ')
    const executable = readFileSync(`/proc/${pid}/cmdline`, 'utf8').split('\0')[0]
    return { pid, ppid: Number(fields[1]), start: fields[19], state: fields[0], name: basename(executable) }
  } catch (error) {
    if (error.code === 'ENOENT' || error.code === 'ESRCH') return null
    throw error
  }
}

function sameProcess(left, right) {
  return Boolean(left && right && left.pid === right.pid && left.ppid === right.ppid && left.start === right.start && left.name === right.name)
}

function ownedDescendants(root) {
  assert.ok(sameProcess(processInfo(root.pid), root), 'owned browser launcher identity must remain stable')
  const result = [], pending = [root]
  while (pending.length) {
    const parent = pending.pop()
    let children
    try { children = readFileSync(`/proc/${parent.pid}/task/${parent.pid}/children`, 'utf8').trim() }
    catch (error) { if (error.code === 'ENOENT') continue; throw error }
    for (const pid of children.split(/\s+/).filter(Boolean).map(Number)) {
      const child = processInfo(pid)
      if (child?.ppid === parent.pid) { result.push(child); pending.push(child) }
    }
  }
  return result
}

function terminateOwnedNetworkProcess(root) {
  const children = ownedDescendants(root)
  const candidates = children.filter(child => ['WPENetworkProcess', 'WebKitNetworkProcess'].includes(child.name))
  assert.equal(candidates.length, 1, 'must identify exactly one owned WebKit Networking process')
  const target = candidates[0]
  // Re-read ancestry and Linux start time immediately before signalling this
  // exact PID. A name match alone never authorizes a signal.
  let current = processInfo(target.pid)
  assert.ok(sameProcess(current, target), 'Networking process identity must remain stable')
  for (let depth = 0; current?.pid !== root.pid && depth < 32; depth++) current = processInfo(current.ppid)
  assert.ok(sameProcess(current, root), 'Networking process must descend from our launcher')
  assert.ok(sameProcess(processInfo(target.pid), target), 'Networking PID must not have been reused')
  process.kill(target.pid, 'SIGKILL')
  return target
}

await new Promise(resolve => server.listen(0, '127.0.0.1', resolve))
let launcher, browser, page
try {
  launcher = await webkit.launchServer()
  const owner = processInfo(launcher.process().pid)
  assert.ok(owner, 'launchServer must expose its owned Linux process')
  browser = await webkit.connect(launcher.wsEndpoint())
  const context = await browser.newContext({ viewport: { width: 390, height: 844 } })
  await context.addInitScript(() => { globalThis.__pwaNetworkDocument = crypto.randomUUID() })
  page = await context.newPage()
  const errors = []
  page.on('pageerror', error => errors.push(error.message))
  await page.goto(`http://127.0.0.1:${server.address().port}`)
  await expect(page.getByRole('heading', { name: '无法读取登录状态' })).toBeVisible()
  await expect(page.getByRole('alert')).toHaveText('网络暂时不可用，请重试')
  await page.waitForFunction(() => Boolean(navigator.serviceWorker.controller))
  const documentID = await page.evaluate(() => globalThis.__pwaNetworkDocument)
  let navigations = 0
  page.on('framenavigated', frame => { if (frame === page.mainFrame()) navigations++ })

  await page.evaluate(async () => {
    const channel = new MessageChannel()
    globalThis.__pwaNetworkOldChannel = channel
    await new Promise(resolve => { channel.port1.onmessage = resolve; channel.port2.postMessage('before') })
  })
  bootstrapUnavailable = false
  const terminated = terminateOwnedNetworkProcess(owner)
  // Await actual network recovery before asking the application to recover;
  // this probe does not update React or retry an application mutation.
  await expect.poll(async () => page.evaluate(async () => {
    try { const response = await fetch('/api/bootstrap/status', { signal: AbortSignal.timeout(2000) }); return response.status === 200 && (await response.json()).required === false }
    catch { return false }
  }), { timeout: 10000 }).toBe(true)
  const replacement = ownedDescendants(owner).find(child => ['WPENetworkProcess', 'WebKitNetworkProcess'].includes(child.name))
  assert.ok(replacement && replacement.pid !== terminated.pid, 'a fresh owned Networking process must service HTTP')
  const ports = await page.evaluate(async () => {
    const probe = channel => new Promise(resolve => {
      const timeout = setTimeout(() => resolve(false), 500)
      channel.port1.onmessage = () => { clearTimeout(timeout); resolve(true) }
      channel.port2.postMessage('after')
    })
    const fresh = new MessageChannel()
    try { return { old: await probe(globalThis.__pwaNetworkOldChannel), fresh: await probe(fresh) } }
    finally { fresh.port1.close(); fresh.port2.close(); globalThis.__pwaNetworkOldChannel.port1.close(); globalThis.__pwaNetworkOldChannel.port2.close() }
  })
  assert.deepEqual(ports, { old: false, fresh: true }, 'native NetworkProcess replacement must invalidate old MessagePorts')
  console.log(`webkit: owned Networking process ${terminated.pid} → ${replacement.pid}; old port stopped, fresh port and HTTP recovered`)

  await page.getByRole('button', { name: '重新连接', exact: true }).click()
  await expect(page.getByRole('heading', { name: '主机', exact: true })).toBeVisible({ timeout: 8000 })
  assert.equal(await page.evaluate(() => globalThis.__pwaNetworkDocument), documentID, '200 recovery must keep the existing document')
  // A later 401 must still update the same React tree after the fallback has
  // drained its first pending task; a one-off synchronous render is insufficient.
  authorized = false
  await page.evaluate(() => window.dispatchEvent(new Event('focus')))
  await expect(page.getByRole('heading', { name: '欢迎回来', exact: true })).toBeVisible({ timeout: 8000 })
  await page.getByRole('textbox', { name: '邮箱', exact: true }).fill('pwa@example.test')
  await page.getByLabel('密码', { exact: true }).fill('fixture-password')
  await page.getByRole('button', { name: '登录', exact: true }).click()
  await expect(page.getByRole('heading', { name: '主机', exact: true })).toBeVisible({ timeout: 8000 })
  await page.getByRole('button', { name: '添加主机', exact: true }).click()
  await chooseOption(page.getByRole('combobox', { name: '连接方式' }), 'ssh')
  await page.getByRole('textbox', { name: '名称', exact: true }).fill('恢复后的主机')
  await expect(page.getByRole('textbox', { name: '名称', exact: true })).toHaveValue('恢复后的主机')
  await page.getByRole('button', { name: '取消', exact: true }).click()
  assert.equal(await page.evaluate(() => globalThis.__pwaNetworkDocument), documentID)
  assert.equal(navigations, 0, 'network recovery must not reload or navigate the page')
  assert.equal(logins, 1, 'a login submission must not be replayed')
  // Opening the host form may query the fixture's relay offer with POST.
  assert.deepEqual(calls.filter(call => call.method !== 'GET' && call.path !== '/api/tailcat/relay-offer'), [{ method: 'POST', path: '/api/login' }])
  assert.deepEqual(errors, [])
  console.log('webkit: native network-process restart, same-document 200/401/login recovery and subsequent UI interaction PASS')
} catch (error) {
  const state = await page?.evaluate(() => ({ heading: document.querySelector('h1')?.textContent, alert: document.querySelector('[role="alert"]')?.textContent, online: navigator.onLine, hidden: document.hidden })).catch(() => null)
  console.error('Native PWA recovery failure', JSON.stringify({ state, calls }))
  throw error
} finally {
  try { await browser?.close() }
  finally {
    try { await launcher?.close() }
    finally { server.closeAllConnections(); await new Promise(resolve => server.close(resolve)) }
  }
}
