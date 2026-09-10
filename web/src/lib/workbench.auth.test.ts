import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { api, currentSessionID, invalidateAuthentication } from './api'
import { WorkbenchClient } from './workbench'

class FakeSocket {
  static OPEN = 1
  static instances: FakeSocket[] = []
  readyState = 1
  onopen?: () => void
  onclose?: (event: { code: number; reason: string }) => void
  onmessage?: (event: { data: string }) => void
  send = vi.fn()
  close = vi.fn()
  constructor() { FakeSocket.instances.push(this) }
  end(code: number) { this.onclose?.({ code, reason: '' }) }
}
const user = { id: 'user', email: 'user@example.test', role: 'user', display_name: 'User' }
beforeEach(async () => {
  invalidateAuthentication(); FakeSocket.instances = []
  vi.stubGlobal('WebSocket', FakeSocket)
  vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response(JSON.stringify({ user, csrf_token: 'csrf', session_id: 'login' }))))
  await api.me(); vi.useFakeTimers()
})
afterEach(() => { vi.useRealTimers(); vi.unstubAllGlobals(); vi.restoreAllMocks() })

it('stops reconnecting on login close code 4401', async () => {
  const client = new WorkbenchClient('host'); client.connect()
  FakeSocket.instances[0].end(4401)
  await vi.advanceTimersByTimeAsync(60000)
  expect(currentSessionID()).toBe('')
  expect(FakeSocket.instances).toHaveLength(1)
  client.dispose()
})
it('checks an opaque upgrade failure and stops when /me is 401', async () => {
  vi.mocked(fetch).mockResolvedValueOnce(new Response('{}', { status: 401 }))
  const client = new WorkbenchClient('host'); client.connect()
  FakeSocket.instances[0].end(1006)
  await vi.advanceTimersByTimeAsync(60000)
  expect(currentSessionID()).toBe('')
  expect(FakeSocket.instances).toHaveLength(1)
  client.dispose()
})
it('reconnects after a network outage without replaying input or unresolved calls', async () => {
  vi.mocked(fetch).mockRejectedValueOnce(new TypeError('offline'))
  const client = new WorkbenchClient('host'); client.connect()
  FakeSocket.instances[0].onopen?.()
  client.sendInput(1, 'once only')
  const pending = client.call('pane.send_text', { text: 'once only' }).catch((reason) => reason)
  FakeSocket.instances[0].end(1006)
  expect(await pending).toBeInstanceOf(Error)
  await vi.advanceTimersByTimeAsync(1200)
  expect(FakeSocket.instances).toHaveLength(2)
  FakeSocket.instances[1].onopen?.()
  expect(FakeSocket.instances[1].send).toHaveBeenCalledTimes(1)
  expect(JSON.parse(FakeSocket.instances[1].send.mock.calls[0][0]).t).toBe('hello')
  expect(currentSessionID()).toBe('login')
  client.dispose()
})
it('ignores a late revoked socket after a new login', async () => {
  const client = new WorkbenchClient('host'); client.connect()
  vi.mocked(fetch).mockResolvedValueOnce(new Response(JSON.stringify({ user, csrf_token: 'new', session_id: 'new-login' })))
  await api.login({ email: 'user@example.test', password: 'test' })
  FakeSocket.instances[0].end(4401)
  expect(currentSessionID()).toBe('new-login')
  client.dispose()
})

it('preserves an actionable host error and waits for a manual retry', async () => {
  const client = new WorkbenchClient('host')
  const state = vi.fn()
  client.onState(state)
  client.connect()
  const socket = FakeSocket.instances[0]
  socket.onopen?.()
  socket.onmessage?.({ data: JSON.stringify({ t: 'error', code: 'host_auth_failed', message: '请检查 SSH 密钥', retryable: false }) })
  socket.end(4405)
  await vi.advanceTimersByTimeAsync(120_000)
  expect(FakeSocket.instances).toHaveLength(1)
  expect(state).toHaveBeenLastCalledWith('offline', '请检查 SSH 密钥', undefined)
  expect(currentSessionID()).toBe('login')
  client.connect()
  expect(FakeSocket.instances).toHaveLength(2)
  client.dispose()
})

it('retryNow reconnects immediately and reports the scheduled retry time', async () => {
  vi.spyOn(Math, 'random').mockReturnValue(0.5)
  const client = new WorkbenchClient('host')
  const state = vi.fn()
  client.onState(state)
  client.connect()
  FakeSocket.instances[0].end(1006)
  await vi.advanceTimersByTimeAsync(0)
  expect(FakeSocket.instances).toHaveLength(1)
  const scheduled = state.mock.calls.filter((call) => call[0] === 'offline' && typeof call[2] === 'number').at(-1)
  expect(scheduled?.[1]).toBe('连接已断开')
  expect(scheduled?.[2]).toBe(Date.now() + 1000)
  client.retryNow()
  expect(FakeSocket.instances).toHaveLength(2)
  expect(state).toHaveBeenLastCalledWith('connecting', undefined, undefined)
  await vi.advanceTimersByTimeAsync(30_000)
  expect(FakeSocket.instances).toHaveLength(2)
  client.dispose()
})

it('increases backoff when WebSocket opens but the remote host still fails', async () => {
  vi.spyOn(Math, 'random').mockReturnValue(0.5)
  const client = new WorkbenchClient('host')
  client.connect()
  FakeSocket.instances[0].onopen?.()
  FakeSocket.instances[0].end(4404)
  await vi.advanceTimersByTimeAsync(1000)
  expect(FakeSocket.instances).toHaveLength(2)
  FakeSocket.instances[1].onopen?.()
  FakeSocket.instances[1].end(4404)
  await vi.advanceTimersByTimeAsync(1000)
  expect(FakeSocket.instances).toHaveLength(2)
  await vi.advanceTimersByTimeAsync(1000)
  expect(FakeSocket.instances).toHaveLength(3)
  // Only a usable Herdr snapshot resets recovery backoff.
  FakeSocket.instances[2].onmessage?.({ data: JSON.stringify({ t: 'snapshot', snapshot: {} }) })
  FakeSocket.instances[2].end(4404)
  await vi.advanceTimersByTimeAsync(1000)
  expect(FakeSocket.instances).toHaveLength(4)
  client.dispose()
})

it('delivers an early terminal exit and isolates it from other panes and later streams', async () => {
  const client = new WorkbenchClient('host'); client.connect()
  const socket = FakeSocket.instances[0]
  const message = (value: unknown) => socket.onmessage?.({ data: JSON.stringify(value) })
  socket.onopen?.()
  const opening = client.openTerminal('w1:p1', 80, 24)
  const request = JSON.parse(socket.send.mock.lastCall![0])
  message({ t: 'terminal.opened', id: request.id, stream_id: 7 })
  message({ t: 'terminal.closed', stream_id: 7, reason: 'herdr: command not found' })
  const failed = vi.fn(), other = vi.fn()
  expect(await opening).toBe(7)
  message({ t: 'terminal.opened', stream_id: 8 })
  client.onTerminal(8, vi.fn(), other)
  client.onTerminal(7, vi.fn(), failed)
  expect(failed).toHaveBeenCalledExactlyOnceWith('herdr: command not found')
  expect(other).not.toHaveBeenCalled()
  expect(socket.close).not.toHaveBeenCalled()
  message({ t: 'terminal.closed', stream_id: 7, reason: 'late duplicate' })
  expect(failed).toHaveBeenCalledTimes(1)
  client.closeTerminal(8)
  message({ t: 'terminal.closed', stream_id: 8, reason: 'user already detached' })
  expect(other).not.toHaveBeenCalled()
  message({ t: 'terminal.opened', stream_id: 9 })
  client.onTerminal(9, vi.fn(), other)
  message({ t: 'terminal.closed', stream_id: 9 })
  expect(other).toHaveBeenCalledExactlyOnceWith('终端观察连接已关闭')
  client.dispose()
})
