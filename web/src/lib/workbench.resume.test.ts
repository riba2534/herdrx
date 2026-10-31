import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { api, invalidateAuthentication } from './api'
import { WorkbenchClient } from './workbench'

class FakeSocket {
  static OPEN = 1
  static instances: FakeSocket[] = []
  readyState = 1
  onopen?: () => void
  onclose?: (event: { code: number; reason: string }) => void
  onmessage?: (event: { data: string | ArrayBuffer }) => void
  send = vi.fn()
  close = vi.fn()
  constructor() { FakeSocket.instances.push(this) }
  end(code: number) { this.onclose?.({ code, reason: '' }) }
  receive(message: object) { this.onmessage?.({ data: JSON.stringify(message) }) }
  pings() { return this.send.mock.calls.filter(([data]) => typeof data === 'string' && JSON.parse(data).t === 'ping').length }
}
const user = { id: 'user', email: 'user@example.test', role: 'user', display_name: 'User' }
// 模拟手机/PWA 从后台切回前台。
function comeBack() {
  const visibility = vi.spyOn(document, 'visibilityState', 'get')
  visibility.mockReturnValue('hidden')
  document.dispatchEvent(new Event('visibilitychange'))
  visibility.mockReturnValue('visible')
  document.dispatchEvent(new Event('visibilitychange'))
}
let client: WorkbenchClient
beforeEach(async () => {
  invalidateAuthentication(); FakeSocket.instances = []
  vi.stubGlobal('WebSocket', FakeSocket)
  vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response(JSON.stringify({ user, csrf_token: 'csrf', session_id: 'login' }))))
  await api.me(); vi.useFakeTimers()
  vi.spyOn(Math, 'random').mockReturnValue(0.5)
  client = new WorkbenchClient('host')
  client.connect()
})
afterEach(() => { client.dispose(); vi.useRealTimers(); vi.unstubAllGlobals(); vi.restoreAllMocks() })

it('reconnects immediately when the app returns while waiting for a backoff retry', async () => {
  // 退到 30s 退避档位，确认前台恢复能打断等待。
  for (let attempt = 0; attempt < 6; attempt++) {
    FakeSocket.instances.at(-1)!.end(1006)
    await vi.advanceTimersByTimeAsync(attempt === 5 ? 0 : 10_000)
  }
  const waiting = FakeSocket.instances.length
  await vi.advanceTimersByTimeAsync(2000)
  expect(FakeSocket.instances).toHaveLength(waiting)
  comeBack()
  expect(FakeSocket.instances).toHaveLength(waiting + 1)
  await vi.advanceTimersByTimeAsync(60_000)
  expect(FakeSocket.instances).toHaveLength(waiting + 1)
})

it('keeps a healthy open connection when the probe is answered', async () => {
  const socket = FakeSocket.instances[0]
  socket.onopen?.()
  comeBack()
  expect(socket.pings()).toBe(1)
  await vi.advanceTimersByTimeAsync(1000)
  socket.receive({ t: 'pong', at: Date.now() })
  await vi.advanceTimersByTimeAsync(30_000)
  expect(socket.close).not.toHaveBeenCalled()
  expect(FakeSocket.instances).toHaveLength(1)
})

it('treats any incoming message during the probe as proof of life', async () => {
  const socket = FakeSocket.instances[0]
  socket.onopen?.()
  comeBack()
  socket.onmessage?.({ data: new ArrayBuffer(0) })
  await vi.advanceTimersByTimeAsync(30_000)
  expect(socket.close).not.toHaveBeenCalled()
  expect(FakeSocket.instances).toHaveLength(1)
})

it('rebuilds a half-open connection whose probe times out, running one probe at a time', async () => {
  const socket = FakeSocket.instances[0]
  socket.onopen?.()
  comeBack()
  await vi.advanceTimersByTimeAsync(2000)
  window.dispatchEvent(new Event('online'))
  expect(socket.pings()).toBe(1)
  await vi.advanceTimersByTimeAsync(1999)
  expect(FakeSocket.instances).toHaveLength(1)
  await vi.advanceTimersByTimeAsync(1)
  expect(socket.close).toHaveBeenCalledWith(1000, 'retry')
  expect(FakeSocket.instances).toHaveLength(2)
})

it('honours a configured probe timeout', async () => {
  client.dispose()
  client = new WorkbenchClient('host', { probeTimeoutMs: 500 })
  client.connect()
  FakeSocket.instances.at(-1)!.onopen?.()
  const count = FakeSocket.instances.length
  comeBack()
  await vi.advanceTimersByTimeAsync(500)
  expect(FakeSocket.instances).toHaveLength(count + 1)
})

it('does not reconnect on resume after the login was revoked or the host refused', async () => {
  FakeSocket.instances[0].end(4401)
  await vi.advanceTimersByTimeAsync(0)
  comeBack()
  await vi.advanceTimersByTimeAsync(60_000)
  expect(FakeSocket.instances).toHaveLength(1)

  const other = new WorkbenchClient('other')
  other.connect()
  const socket = FakeSocket.instances[1]
  socket.onopen?.()
  socket.receive({ t: 'error', code: 'host_auth_failed', message: '请检查 SSH 密钥', retryable: false })
  socket.end(1006)
  await vi.advanceTimersByTimeAsync(2000)
  window.dispatchEvent(new Event('online'))
  await vi.advanceTimersByTimeAsync(60_000)
  expect(FakeSocket.instances).toHaveLength(2)
  other.dispose()
})

it('wakes every host client and stops listening after dispose', async () => {
  const other = new WorkbenchClient('other')
  other.connect()
  FakeSocket.instances[0].end(1006)
  FakeSocket.instances[1].end(1006)
  await vi.advanceTimersByTimeAsync(0)
  comeBack()
  expect(FakeSocket.instances).toHaveLength(4)
  other.dispose()
  client.dispose()
  vi.advanceTimersByTime(2000)
  comeBack()
  await vi.advanceTimersByTimeAsync(60_000)
  expect(FakeSocket.instances).toHaveLength(4)
})

it('reports whether terminal input was actually written to the connection', async () => {
  const socket = FakeSocket.instances[0]
  socket.onopen?.()
  const opening = client.openTerminal('p1', 80, 24)
  const request = JSON.parse(socket.send.mock.lastCall![0])
  socket.receive({ t: 'terminal.opened', id: request.id, pane_id: 'p1', stream_id: 7, stream_epoch: 'e1' })
  expect(await opening).toBe(7)
  expect(client.sendInput(7, 'live')).toBe(true)
  expect(client.sendInput(8, 'unknown stream')).toBe(false)
  socket.readyState = 3
  expect(client.sendInput(7, 'closing socket')).toBe(false)
  socket.end(1006)
  expect(client.sendInput(7, 'after close')).toBe(false)
  const frames = socket.send.mock.calls.filter(([data]) => data instanceof Uint8Array)
  expect(frames).toHaveLength(1)
})

it('waits longer for a pong while requests are still in flight, up to the total limit', async () => {
  const socket = FakeSocket.instances[0]
  socket.onopen?.()
  // 慢主机还在处理 terminal.open/call，pong 排在它们之后。
  void client.call('pane.read', {}).catch(() => {})
  comeBack()
  await vi.advanceTimersByTimeAsync(4000)
  expect(FakeSocket.instances).toHaveLength(1)
  expect(socket.pings()).toBe(2)
  // 第一个请求 15s 超时前再发一个，保持始终有请求未完成。
  await vi.advanceTimersByTimeAsync(10_000)
  void client.call('pane.read', {}).catch(() => {})
  await vi.advanceTimersByTimeAsync(15_999)
  expect(FakeSocket.instances).toHaveLength(1)
  await vi.advanceTimersByTimeAsync(1)
  expect(socket.close).toHaveBeenCalledWith(1000, 'retry')
  expect(FakeSocket.instances).toHaveLength(2)
})

it('drops the old connection state when retryNow replaces the socket', async () => {
  const socket = FakeSocket.instances[0]
  socket.onopen?.()
  const opening = client.openTerminal('p1', 80, 24)
  const request = JSON.parse(socket.send.mock.lastCall![0])
  socket.receive({ t: 'terminal.opened', id: request.id, pane_id: 'p1', stream_id: 1, stream_epoch: 'e1' })
  expect(await opening).toBe(1)
  const closed = vi.fn()
  client.onTerminal(1, vi.fn(), closed)
  const pending = client.call('pane.read', {}).catch((reason: Error) => reason.message)
  client.retryNow()
  expect(await pending).toBe('连接已断开')
  expect(client.hasOpenTerminals()).toBe(false)
  // 旧连接迟到的 close 不再触发任何东西。
  socket.end(1000)
  expect(closed).not.toHaveBeenCalled()
  // 新连接的 stream ID 从 1 重新编号：新流打开前，旧流 ID 不能把输入送进去。
  const next = FakeSocket.instances[1]
  next.onopen?.()
  expect(client.sendInput(1, 'stale stream')).toBe(false)
  expect(next.send.mock.calls.some(([data]) => data instanceof Uint8Array)).toBe(false)
})
