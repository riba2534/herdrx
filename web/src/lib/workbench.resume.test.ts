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
