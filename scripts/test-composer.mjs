#!/usr/bin/env node
// Isolated browser composer regression. Never connects to a real Herdr pane.
import assert from 'node:assert/strict'
import { createServer } from 'node:http'
import { readFile, mkdir } from 'node:fs/promises'
import { fileURLToPath } from 'node:url'
import { join } from 'node:path'
import { chromium, firefox, webkit, expect } from '../web/node_modules/@playwright/test/index.mjs'

const dist = fileURLToPath(new URL('../web/dist/', import.meta.url))
const artifacts = process.env.HERDRX_COMPOSER_ARTIFACTS
const host = { id: 'composer-test', name: '输入框测试', transport: 'ssh' }
const otherHost = { id: 'composer-other', name: '另一台主机', transport: 'ssh' }
const snapshot = {
  protocol: 1, version: 'test', focused_workspace_id: 'w1', focused_tab_id: 't1', focused_pane_id: 'p1',
  workspaces: [{ workspace_id: 'w1', active_tab_id: 't1', label: '本地输入测试', number: 1, pane_count: 2, tab_count: 1, agent_status: 'working' }],
  tabs: [{ workspace_id: 'w1', tab_id: 't1', label: '开发终端', number: 1, pane_count: 2, agent_status: 'working' }],
  panes: [1, 2].map((i) => ({ workspace_id: 'w1', tab_id: 't1', pane_id: `p${i}`, label: `终端 ${i}`, cwd: '/workspace/example', terminal_id: `term${i}`, revision: 1, agent_status: 'working' })),
  layouts: [{ workspace_id: 'w1', tab_id: 't1', area: { x: 0, y: 0, width: 160, height: 40 }, focused_pane_id: 'p1', splits: [], zoomed: false, panes: [1, 2].map((i) => ({ pane_id: `p${i}`, rect: { x: (i - 1) * 80, y: 0, width: 80, height: 40 } })) }],
  agents: [],
}
const ansi = Array.from({ length: 24 }, (_, i) => `\x1b[${i + 1};1HComposer terminal ${i}`).join('')
const server = createServer(async (req, res) => {
  const path = new URL(req.url, 'http://localhost').pathname
  const json = path === '/api/bootstrap/status' ? { required: false } : path === '/api/me' ? { user: { id: 'composer-user', email: 'composer@example.test', display_name: 'Composer', role: 'admin' }, csrf_token: 'composer-fixture', session_id: 'composer-session' } : path === '/api/hosts/' ? { hosts: [host, otherHost] } : path === '/api/hosts/composer-test/' ? { host } : path === '/api/hosts/composer-other/' ? { host: otherHost } : null
  if (json) { res.setHeader('content-type', 'application/json'); res.end(JSON.stringify(json)); return }
  if (path.startsWith('/api/')) { res.writeHead(404); res.end(); return }
  try {
    const file = (path.startsWith('/assets/') || path.startsWith('/brand/')) || ['/boot.js', '/sw.js', '/manifest.webmanifest', '/favicon.ico'].includes(path) ? path.slice(1) : 'index.html'
    res.setHeader('content-type', file.endsWith('.js') ? 'application/javascript' : file.endsWith('.css') ? 'text/css' : file.endsWith('.png') ? 'image/png' : file.endsWith('.ico') ? 'image/x-icon' : file.endsWith('.webmanifest') ? 'application/manifest+json' : 'text/html')
    res.end(await readFile(join(dist, file)))
  } catch { res.writeHead(404); res.end() }
})
await new Promise((done) => server.listen(0, '127.0.0.1', done))
const base = `http://127.0.0.1:${server.address().port}`

async function fixture(browser, options, { delayMs = 500, failText = 'FORCE_FAIL', closeOnSend = false, receiptError = '' } = {}) {
  const context = await browser.newContext(options)
  const page = await context.newPage()
  const messages = [], errors = []
  page.on('pageerror', (error) => errors.push(error.message))
  await page.routeWebSocket('**/api/hosts/*/ws', (ws) => {
    let streamID = 0
    const emitFrame = (id) => {
      const bytes = Buffer.from(ansi)
      const frame = Buffer.alloc(20 + bytes.length)
      frame.set([0x74, 1, 1, 1]); frame.writeUInt32LE(id, 4); frame.writeBigUInt64LE(1n, 8)
      frame.writeUInt16LE(80, 16); frame.writeUInt16LE(24, 18); bytes.copy(frame, 20)
      ws.send(frame)
    }
    ws.onMessage((raw) => {
      if (typeof raw !== 'string') {
        const data = Buffer.from(raw)
        messages.push({ op: data[2], stream_id: data.readUInt32LE(4), bytes: data.subarray(16).toString() })
        return
      }
      const message = JSON.parse(raw)
      messages.push(message)
      if (message.t === 'hello') {
        ws.send(JSON.stringify({ t: 'server_info' }))
        ws.send(JSON.stringify({ t: 'conn', state: 'ready' }))
        ws.send(JSON.stringify({ t: 'snapshot', snapshot }))
      } else if (message.t === 'terminal.open') {
        const id = ++streamID
        ws.send(JSON.stringify({ t: 'terminal.opened', id: message.id, stream_id: id }))
        emitFrame(id)
      } else if (message.t === 'call' && message.method === 'pane.send_input') {
        if (closeOnSend) { ws.close(); return }
        const text = String(message.params?.text || '')
        const fail = text.includes(failText)
        const interrupted = Boolean(receiptError) && text.includes('EOF_RECEIPT')
        setTimeout(() => {
          if (interrupted) ws.send(JSON.stringify({ t: 'error', id: message.id, message: receiptError }))
          else if (fail) ws.send(JSON.stringify({ t: 'error', id: message.id, message: '远端拒绝' }))
          else ws.send(JSON.stringify({ t: 'result', id: message.id, result: { type: 'ok' } }))
        }, delayMs)
      } else if (message.t === 'call') {
        ws.send(JSON.stringify({ t: 'result', id: message.id, result: {} }))
      }
    })
  })
  await page.goto(base + '/h/composer-test')
  await expect(page.locator('.xterm-rows').first()).toContainText('Composer terminal')
  return { context, page, messages, errors }
}

async function screenshot(page, name) {
  if (!artifacts) return
  await mkdir(artifacts, { recursive: true })
  await page.screenshot({ path: join(artifacts, name + '.png'), fullPage: true })
}

async function selectMobileHost(page, name) {
  await page.getByRole('button', { name: '切换工作区或终端', exact: true }).click()
  await page.locator('.switcher').getByRole('link', { name, exact: true }).click()
  await expect(page.locator('.mobile-location')).toContainText(name)
}

async function showAuxiliaryKeys(page) {
  const toggle = page.getByRole('button', { name: '终端辅助键', exact: true })
  if (await toggle.getAttribute('aria-expanded') !== 'true') await toggle.click()
  await expect(page.getByRole('toolbar', { name: '终端辅助键', exact: true })).toBeVisible()
}

function sendCalls(messages) {
  return messages.filter((item) => item.t === 'call' && item.method === 'pane.send_input')
}

function inputFrames(messages) {
  return messages.filter((item) => item.op === 3)
}

const engines = process.env.HERDRX_TEST_ENGINES?.split(',') || ['chromium']
try {
  for (const name of engines) {
    const browser = await ({ chromium, firefox, webkit })[name].launch({ headless: true, ...(name === 'chromium' && process.env.HERDRX_TEST_CHROMIUM ? { executablePath: process.env.HERDRX_TEST_CHROMIUM } : {}) })
    try {
      const mobile = { viewport: { width: 390, height: 844 }, hasTouch: true, deviceScaleFactor: 2, ...(name !== 'firefox' ? { isMobile: true } : {}) }
      const f = await fixture(browser, mobile)
      const box = f.page.getByRole('textbox', { name: '本地输入内容' })
      await expect(box).toBeVisible()
      await box.click()
      const beforeType = f.messages.length
      await box.pressSequentially('hello中文🙂', { delay: 20 })
      await f.page.waitForTimeout(80)
      assert.equal(inputFrames(f.messages.slice(beforeType)).length, 0, 'typing leaked terminal input frames')
      assert.equal(sendCalls(f.messages.slice(beforeType)).length, 0, 'typing sent a submit RPC')
      await expect(box).toHaveValue('hello中文🙂')

      await box.evaluate((el) => {
        el.dispatchEvent(new CompositionEvent('compositionstart', { bubbles: true }))
        el.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', isComposing: true, keyCode: 229, bubbles: true, cancelable: true }))
        el.dispatchEvent(new CompositionEvent('compositionend', { data: '你', bubbles: true }))
      })
      await box.fill('第一行')
      await box.press('Enter')
      await box.type('第二行')
      assert.equal(sendCalls(f.messages).length, 0, 'Enter or IME submitted early')
      await expect(box).toHaveValue('第一行\n第二行')

      const started = Date.now()
      await f.page.getByRole('button', { name: '发送', exact: true }).click()
      await expect.poll(() => sendCalls(f.messages).length).toBe(1)
      await expect(f.page.getByRole('status')).toContainText('发送中')
      await expect(f.page.getByRole('button', { name: '发送', exact: true })).toBeDisabled()
      await expect(f.page.getByRole('status')).toContainText('已送达', { timeout: 5000 })
      assert.ok(Date.now() - started >= 450, 'send finished before the simulated 500ms round trip')
      assert.deepEqual(sendCalls(f.messages)[0].params, { pane_id: 'p1', text: '第一行\n第二行', keys: ['Enter'] })

      await box.fill('切换前再发')
      await f.page.getByRole('button', { name: '发送', exact: true }).click()
      await expect(f.page.getByRole('status')).toContainText('发送中')
      await f.page.getByRole('button', { name: '切换工作区或终端', exact: true }).click()
      await f.page.locator('.switcher button').filter({ hasText: '终端 2' }).click()
      await box.fill('切换前再发')
      await expect.poll(() => sendCalls(f.messages).length).toBe(2)
      assert.deepEqual(sendCalls(f.messages)[1].params, { pane_id: 'p1', text: '切换前再发', keys: ['Enter'] })
      await f.page.waitForTimeout(600)
      await expect(f.page.getByRole('textbox', { name: '本地输入内容' })).toHaveValue('切换前再发')
      await expect(f.page.getByRole('status')).toHaveCount(0)
      assert.equal(inputFrames(f.messages).length, 0, 'composer submit used keystroke frames')
      await screenshot(f.page, `${name}-mobile-390-composer`)
      assert.deepEqual(f.errors, [])
      await f.context.close()

      const fail = await fixture(browser, mobile)
      await fail.page.getByRole('textbox', { name: '本地输入内容' }).fill('keep FORCE_FAIL')
      await fail.page.getByRole('button', { name: '发送', exact: true }).click()
      await expect(fail.page.getByRole('status')).toContainText('发送失败')
      await expect(fail.page.getByRole('textbox', { name: '本地输入内容' })).toHaveValue('keep FORCE_FAIL')
      assert.equal(sendCalls(fail.messages).length, 1)
      await fail.context.close()

      const drop = await fixture(browser, mobile, { closeOnSend: true })
      await drop.page.getByRole('textbox', { name: '本地输入内容' }).fill('do not replay')
      await drop.page.getByRole('button', { name: '发送', exact: true }).click()
      await expect.poll(() => drop.messages.some((item) => item.t === 'call' && item.method === 'pane.send_input')).toBe(true)
      await drop.page.waitForTimeout(700)
      assert.equal(sendCalls(drop.messages).length, 1, 'disconnect replayed composer submit')
      await expect(drop.page.getByRole('textbox', { name: '本地输入内容' })).toHaveValue('do not replay')
      await expect(drop.page.getByRole('status')).toContainText('结果未知')
      await drop.context.close()

      const hop = await fixture(browser, mobile)
      await hop.page.getByRole('textbox', { name: '本地输入内容' }).fill('host hop draft')
      await hop.page.getByRole('button', { name: '发送', exact: true }).click()
      await expect(hop.page.getByRole('status')).toContainText('发送中')
      await selectMobileHost(hop.page, '另一台主机')
      await selectMobileHost(hop.page, '输入框测试')
      await expect(hop.page.getByRole('textbox', { name: '本地输入内容' })).toHaveValue('host hop draft')
      await expect(hop.page.getByRole('status')).toContainText('结果未知')
      assert.equal(sendCalls(hop.messages).length, 1, 'returning to the host replayed composer submit')
      await hop.context.close()

      const eof = await fixture(browser, mobile, { receiptError: 'read herdr response: EOF' })
      await eof.page.getByRole('textbox', { name: '本地输入内容' }).fill('EOF_RECEIPT already written')
      await eof.page.getByRole('button', { name: '发送', exact: true }).click()
      await expect(eof.page.getByRole('status')).toContainText('结果未知')
      await expect(eof.page.getByRole('status')).not.toContainText('发送失败')
      await expect(eof.page.getByRole('textbox', { name: '本地输入内容' })).toHaveValue('EOF_RECEIPT already written')
      assert.equal(sendCalls(eof.messages).length, 1)
      await selectMobileHost(eof.page, '另一台主机')
      await selectMobileHost(eof.page, '输入框测试')
      await expect(eof.page.getByRole('textbox', { name: '本地输入内容' })).toHaveValue('EOF_RECEIPT already written')
      await expect(eof.page.getByRole('status')).toContainText('结果未知')
      await expect(eof.page.getByRole('status')).not.toContainText('发送失败')
      assert.equal(sendCalls(eof.messages).length, 1, 'EOF receipt navigation replayed composer submit')
      await eof.context.close()

      for (const [width, height] of [[320, 720], [390, 844], [479, 847], [844, 390]]) {
        const view = await fixture(browser, { viewport: { width, height }, hasTouch: true, ...(name !== 'firefox' ? { isMobile: true } : {}) })
        await expect(view.page.locator('.keybar')).toHaveCount(0)
        await expect(view.page.getByRole('status')).toHaveCount(0)
        const foldedComposer = await view.page.locator('.composer').boundingBox()
        assert.ok(foldedComposer && Math.abs(foldedComposer.y + foldedComposer.height - height) <= 1, `composer does not meet viewport bottom at ${width}x${height}`)
        await showAuxiliaryKeys(view.page)
        const keybar = await view.page.locator('.keybar').boundingBox()
        assert.ok(keybar, `missing keybar at ${width}x${height}`)
        if (height < 500) {
          await expect(view.page.getByRole('region', { name: '本地输入' })).toHaveCount(0)
          assert.ok(Math.abs(keybar.y + keybar.height - height) <= 1, `keybar does not meet viewport bottom at ${width}x${height}`)
        } else {
          const send = await view.page.getByRole('button', { name: '发送', exact: true }).boundingBox()
          const composer = await view.page.locator('.composer').boundingBox()
          assert.ok(send && composer, `missing composer chrome at ${width}x${height}`)
          assert.ok(send.y + send.height <= height + 1 && composer.y + composer.height <= keybar.y + 1)
        }
        await view.page.evaluate(() => {
          Object.defineProperty(window.visualViewport, 'height', { configurable: true, value: Math.min(360, window.innerHeight) })
          window.visualViewport.dispatchEvent(new Event('resize'))
        })
        const keyboard = Math.min(360, height)
        await expect.poll(() => view.page.locator('.keybar').evaluate((el) => el.getBoundingClientRect().bottom)).toBe(keyboard)
        if (keyboard < 500) {
          await expect(view.page.getByRole('region', { name: '本地输入' })).toHaveCount(0)
        } else {
          await expect.poll(() => view.page.getByRole('button', { name: '发送', exact: true }).evaluate((el) => el.getBoundingClientRect().bottom)).toBeLessThanOrEqual(keyboard + 1)
          await expect(view.page.getByRole('status')).toHaveCount(0)
          await view.page.getByRole('button', { name: '输入方式：本地输入', exact: true }).click()
          await view.page.getByRole('menuitemradio', { name: /^直接输入终端/ }).click()
          await expect(view.page.locator('.xterm-helper-textarea')).toBeFocused()
          await view.page.getByRole('button', { name: '输入方式：直接输入终端', exact: true }).click()
          await view.page.getByRole('menuitemradio', { name: /^本地输入/ }).click()
          await expect(view.page.getByRole('textbox', { name: '本地输入内容' })).toBeFocused()
        }
        await view.context.close()
      }

      for (const [width, kind, options] of [
        [320, 'failed', {}],
        [390, 'unknown', { closeOnSend: true }],
      ]) {
        const view = await fixture(browser, { viewport: { width, height: 844 }, hasTouch: true, ...(name !== 'firefox' ? { isMobile: true } : {}) }, options)
        await view.page.evaluate(() => {
          Object.defineProperty(window.visualViewport, 'height', { configurable: true, value: 360 })
          window.visualViewport.dispatchEvent(new Event('resize'))
        })
        await expect(view.page.locator('.workbench')).toHaveClass(/workbench-short/)
        await view.page.getByRole('textbox', { name: '本地输入内容' }).fill(kind === 'failed' ? 'keep FORCE_FAIL' : 'unknown after close')
        await view.page.getByRole('button', { name: '发送', exact: true }).click()
        await expect(view.page.getByRole('status')).toContainText(kind === 'failed' ? '失败' : '结果未知')
        const status = await view.page.getByRole('status').boundingBox()
        const send = await view.page.getByRole('button', { name: '发送', exact: true }).boundingBox()
        assert.ok(status && status.height > 0 && status.y + status.height <= 360 + 1, `${kind} status hidden under keyboard at ${width}`)
        assert.ok(send && send.y + send.height <= 360 + 1)
        await screenshot(view.page, `${name}-${width}-keyboard-${kind}`)
        await view.context.close()
      }

      const desktop = await fixture(browser, { viewport: { width: 1440, height: 900 } })
      await expect(desktop.page.getByRole('region', { name: '本地输入' })).toHaveCount(0)
      await desktop.page.getByRole('button', { name: '本地输入框' }).click()
      await desktop.page.getByRole('textbox', { name: '本地输入内容' }).fill('desktop 整段')
      await desktop.page.getByRole('textbox', { name: '本地输入内容' }).press('Control+Enter')
      await expect.poll(() => sendCalls(desktop.messages).length).toBe(1)
      assert.equal(desktop.messages.find((item) => item.t === 'call' && item.method === 'pane.send_input').params.pane_id, 'p1')
      await screenshot(desktop.page, `${name}-desktop-composer`)
      await desktop.page.getByRole('button', { name: '直接输入终端' }).click()
      await expect.poll(() => desktop.page.evaluate(() => document.activeElement?.classList.contains('xterm-helper-textarea'))).toBe(true)
      assert.deepEqual(desktop.errors, [])
      await desktop.context.close()
      console.log(`${name}: composer local edit, delayed one-shot send, pane switch, failure, disconnect and layout passed`)
    } finally { await browser.close() }
  }
} finally { await new Promise((resolve) => server.close(resolve)) }
