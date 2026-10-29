/// <reference types="node" />
import { readFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { describe, expect, it, vi } from 'vitest'

type Listener = (event: Record<string, unknown>) => void

// Run public/sw.js against a minimal worker global so notification routing is
// checked without a browser. Only the push and click handlers are exercised.
function loadWorker(clients: { url: string; focus: () => unknown; postMessage: (message: unknown) => void }[] = [], setAppBadge?: () => Promise<void>) {
  const listeners = new Map<string, Listener>()
  const shown: { title: string; options: { body: string; tag: string; data: { url: string } } }[] = []
  const opened: string[] = []
  const self = {
    location: { origin: 'https://herdrx.example.test' },
    navigator: setAppBadge ? { setAppBadge } : {},
    addEventListener: (type: string, listener: Listener) => listeners.set(type, listener),
    registration: { showNotification: async (title: string, options: (typeof shown)[number]['options']) => { shown.push({ title, options }) } },
    clients: { matchAll: async () => clients, openWindow: async (url: string) => { opened.push(url) } },
  }
  const source = readFileSync(join(dirname(fileURLToPath(import.meta.url)), '../../public/sw.js'), 'utf8')
  new Function('self', 'caches', source)(self, {})
  const run = async (type: string, event: Record<string, unknown>) => {
    let pending: Promise<unknown> = Promise.resolve()
    listeners.get(type)!({ ...event, waitUntil: (promise: Promise<unknown>) => { pending = promise } })
    await pending
  }
  return {
    shown, opened,
    push: (payload: unknown) => run('push', { data: { json: () => payload } }),
    click: (url: unknown) => run('notificationclick', { notification: { close() {}, data: { url } } }),
  }
}

describe('service worker notifications', () => {
  it('keeps only the workbench path and a bounded pane parameter', async () => {
    const worker = loadWorker()
    const cases: [unknown, string][] = [
      ['/h/host-1?pane=w1%3Ap1', '/h/host-1?pane=w1%3Ap1'],
      ['/h/host-1?pane=w1%3Ap1&next=https://evil.example&x=1#frag', '/h/host-1?pane=w1%3Ap1'],
      ['/h/host-1?other=1', '/h/host-1'],
      [`/h/host-1?pane=${'p'.repeat(257)}`, '/h/host-1'],
      ['https://evil.example/h/host-1?pane=p1', '/'],
      ['/h/host-1/extra?pane=p1', '/'],
      ['/admin?pane=p1', '/'],
      [42, '/'],
    ]
    for (const [url, expected] of cases) {
      await worker.push({ title: 't', body: 'b', tag: 'x', data: { url } })
      expect(worker.shown.at(-1)!.options.data.url).toBe(expected)
    }
  })

  it('opens the deep link when no window shows that host', async () => {
    const other = { url: 'https://herdrx.example.test/h/host-2', focus: vi.fn(), postMessage: vi.fn() }
    const worker = loadWorker([other])
    await worker.click('/h/host-1?pane=w1%3Ap1')
    expect(worker.opened).toEqual(['/h/host-1?pane=w1%3Ap1'])
    expect(other.focus).not.toHaveBeenCalled()
  })

  it('focuses an open window of the host and asks it to switch panes instead of navigating', async () => {
    const existing = { url: 'https://herdrx.example.test/h/host-1', focus: vi.fn(), postMessage: vi.fn() }
    const worker = loadWorker([existing])
    await worker.click('/h/host-1?pane=w1%3Ap1')
    expect(existing.postMessage).toHaveBeenCalledWith({ type: 'FOCUS_PANE', pane: 'w1:p1' })
    expect(existing.focus).toHaveBeenCalled()
    expect(worker.opened).toEqual([])
    await worker.click('/h/host-1')
    expect(existing.postMessage).toHaveBeenCalledTimes(1)
  })

  it('marks the app badge only for an Agent waiting for input', async () => {
    const setAppBadge = vi.fn(async () => {})
    const worker = loadWorker([], setAppBadge)
    await worker.push({ title: 'claude 已完成', body: 'b', tag: 'x', data: { url: '/h/h1?pane=p1', status: 'done' } })
    expect(setAppBadge).not.toHaveBeenCalled()
    await worker.push({ title: 'claude 需要你', body: 'b', tag: 'x', data: { url: '/h/h1?pane=p1', status: 'blocked' } })
    expect(setAppBadge).toHaveBeenCalledWith()
    // Browsers without the Badging API still show the notification.
    const plain = loadWorker()
    await plain.push({ title: 'claude 需要你', body: 'b', tag: 'x', data: { url: '/', status: 'blocked' } })
    expect(plain.shown).toHaveLength(1)
  })
})
