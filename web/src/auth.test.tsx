import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { StrictMode } from 'react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { AUTH_CHECK_TIMEOUT_MS, AuthProvider, useAuth } from './auth'
import { api, csrf, currentSessionID, invalidateAuthentication } from './lib/api'

const user = { id: 'user-a', email: 'user@example.test', display_name: 'Member', role: 'user' as const }
const login = (id = 'login-a') => ({ user, csrf_token: 'csrf-' + id, session_id: id })
const response = (data: unknown, status = 200) => new Response(JSON.stringify(data), { status, headers: { 'Content-Type': 'application/json' } })
const fetchMock = vi.fn<typeof fetch>()
function Probe() {
  const auth = useAuth()
  return <><p data-testid="user">{auth.user?.id || 'anonymous'}</p><p data-testid="loading">{auth.loading ? 'yes' : 'no'}</p><p data-testid="error">{auth.error}</p><p role="status">{auth.notice}</p><button onClick={() => void auth.refresh()}>重新连接</button><button onClick={() => void auth.signOut().catch(() => {})}>退出</button></>
}
beforeEach(() => { invalidateAuthentication(); vi.stubGlobal('fetch', fetchMock); vi.stubGlobal('BroadcastChannel', undefined) })
afterEach(() => { vi.unstubAllGlobals(); vi.restoreAllMocks(); fetchMock.mockReset() })
async function mountSignedIn() {
  fetchMock.mockResolvedValueOnce(response({ required: false, registration: 'invite' })).mockResolvedValueOnce(response(login()))
  render(<AuthProvider><Probe/></AuthProvider>)
  await waitFor(() => expect(screen.getByTestId('user')).toHaveTextContent('user-a'))
}

describe('Web authentication lifecycle', () => {
  it('clears current user and CSRF on a protected API 401', async () => {
    await mountSignedIn()
    fetchMock.mockResolvedValueOnce(response({ code: 'unauthorized' }, 401))
    await act(async () => { await expect(api.hosts()).rejects.toMatchObject({ status: 401 }) })
    expect(screen.getByTestId('user')).toHaveTextContent('anonymous')
    expect(csrf()).toBe(''); expect(currentSessionID()).toBe('')
    expect(screen.getByRole('status')).toHaveTextContent('远程任务仍在运行')
  })
  it.each([403, 503])('keeps login after HTTP %s and surfaces logout failure', async (status) => {
    await mountSignedIn()
    fetchMock.mockResolvedValueOnce(response({ error: 'temporary error' }, status))
    await act(async () => { await expect(api.hosts()).rejects.toMatchObject({ status }) })
    expect(screen.getByTestId('user')).toHaveTextContent('user-a')
    fetchMock.mockResolvedValueOnce(response({}, status))
    fireEvent.click(screen.getByRole('button', { name: '退出' }))
    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(4))
    expect(screen.getByTestId('user')).toHaveTextContent('user-a')
  })
  it.each([200, 401])('finishes local logout when the server returns %s', async (status) => {
    await mountSignedIn()
    fetchMock.mockResolvedValueOnce(response({ ok: status === 200 }, status))
    fireEvent.click(screen.getByRole('button', { name: '退出' }))
    await waitFor(() => expect(screen.getByTestId('user')).toHaveTextContent('anonymous'))
    expect(csrf()).toBe('')
  })
  it('does not let a delayed old 401 erase a newer login', async () => {
    await mountSignedIn()
    let finish!: (value: Response) => void
    fetchMock.mockImplementationOnce(() => new Promise((resolve) => { finish = resolve }))
    const old = api.hosts().catch((reason) => reason)
    fetchMock.mockResolvedValueOnce(response(login('login-b')))
    await act(async () => { await api.login({ email: user.email, password: 'test-password' }) })
    await act(async () => { finish(response({}, 401)); await old })
    expect(currentSessionID()).toBe('login-b')
    expect(screen.getByTestId('user')).toHaveTextContent('user-a')
  })
  it('ignores stale /me success and keeps invalid credentials separate from expiration', async () => {
    await mountSignedIn()
    let finish!: (value: Response) => void
    fetchMock.mockImplementationOnce(() => new Promise((resolve) => { finish = resolve }))
    const old = api.me().catch((reason) => reason)
    fetchMock.mockResolvedValueOnce(response(login('login-b')))
    await act(async () => { await api.login({ email: user.email, password: 'test-password' }) })
    await act(async () => { finish(response(login())); expect(await old).toMatchObject({ code: 'auth_changed' }) })
    fetchMock.mockResolvedValueOnce(response({ code: 'invalid_credentials' }, 401))
    await expect(api.login({ email: user.email, password: 'wrong' })).rejects.toMatchObject({ code: 'invalid_credentials' })
    expect(currentSessionID()).toBe('login-b')
  })
  it('keeps current login during a network failure', async () => {
    await mountSignedIn()
    fetchMock.mockRejectedValueOnce(new TypeError('network unavailable'))
    await expect(api.hosts()).rejects.toThrow('network unavailable')
    expect(currentSessionID()).toBe('login-a')
  })
  it('leaves the boot spinner when the login check never returns', async () => {
    vi.useFakeTimers()
    try {
      fetchMock.mockImplementation(() => new Promise(() => {}))
      render(<AuthProvider><Probe/></AuthProvider>)
      expect(screen.getByTestId('loading')).toHaveTextContent('yes')
      const started = fetchMock.mock.calls.length
      await act(async () => {
        window.dispatchEvent(new Event('online'))
        window.dispatchEvent(new Event('focus'))
        document.dispatchEvent(new Event('visibilitychange'))
      })
      expect(fetchMock.mock.calls.length).toBe(started)
      const signal = fetchMock.mock.calls[0][1]?.signal
      expect(signal?.aborted).toBe(false)
      await act(async () => { await vi.advanceTimersByTimeAsync(AUTH_CHECK_TIMEOUT_MS) })
      expect(signal?.aborted).toBe(true)
      expect(screen.getByTestId('loading')).toHaveTextContent('no')
      expect(screen.getByTestId('user')).toHaveTextContent('anonymous')
      expect(screen.getByTestId('error')).toHaveTextContent('无法连接工作台，请重试')
    } finally {
      vi.useRealTimers()
    }
  })
  it('reconnects immediately while an automatic retry is stalled and ignores its late login', async () => {
    vi.useFakeTimers()
    try {
      fetchMock.mockRejectedValueOnce(new TypeError('network unavailable'))
      render(<AuthProvider><Probe/></AuthProvider>)
      await act(async () => {})
      expect(screen.getByTestId('error')).toHaveTextContent('network unavailable')
      let finish!: (value: Response) => void
      fetchMock.mockResolvedValueOnce(response({ required: false, registration: 'closed' }))
        .mockImplementationOnce(() => new Promise((resolve) => { finish = resolve }))
      await act(async () => { await vi.advanceTimersByTimeAsync(5000) })
      const stalled = fetchMock.mock.calls[2][1]?.signal
      expect(stalled?.aborted).toBe(false)
      fetchMock.mockResolvedValueOnce(response({ required: false, registration: 'closed' }))
        .mockResolvedValueOnce(response({}, 401))
      await act(async () => { fireEvent.click(screen.getByRole('button', { name: '重新连接' })) })
      expect(stalled?.aborted).toBe(true)
      expect(fetchMock).toHaveBeenCalledTimes(5)
      expect(screen.getByTestId('error')).toBeEmptyDOMElement()
      expect(screen.getByTestId('user')).toHaveTextContent('anonymous')
      await act(async () => { finish(response(login('late-login'))) })
      expect(currentSessionID()).toBe('')
      expect(screen.getByTestId('user')).toHaveTextContent('anonymous')
    } finally { vi.useRealTimers() }
  })
  it('aborts a timed-out /me response and recovers on the next periodic check', async () => {
    vi.useFakeTimers()
    try {
      let finish!: (value: Response) => void
      fetchMock.mockResolvedValueOnce(response({ required: false, registration: 'closed' }))
        .mockImplementationOnce(() => new Promise((resolve) => { finish = resolve }))
      render(<AuthProvider><Probe/></AuthProvider>)
      await act(async () => {})
      const stalled = fetchMock.mock.calls[1][1]?.signal
      await act(async () => { await vi.advanceTimersByTimeAsync(AUTH_CHECK_TIMEOUT_MS) })
      expect(stalled?.aborted).toBe(true)
      await act(async () => { finish(response(login('late-login'))) })
      expect(currentSessionID()).toBe('')
      expect(screen.getByTestId('error')).toHaveTextContent('无法连接工作台，请重试')
      fetchMock.mockResolvedValueOnce(response({ required: false, registration: 'closed' }))
        .mockResolvedValueOnce(response(login('recovered-login')))
      await act(async () => { await vi.advanceTimersByTimeAsync(5000) })
      expect(currentSessionID()).toBe('recovered-login')
      expect(screen.getByTestId('error')).toBeEmptyDOMElement()
      expect(screen.getByTestId('user')).toHaveTextContent('user-a')
    } finally { vi.useRealTimers() }
  })
  it('cancels an abandoned mount and starts a fresh check during Strict Mode remount', async () => {
    fetchMock.mockImplementationOnce(() => new Promise(() => {}))
      .mockResolvedValueOnce(response({ required: false, registration: 'closed' }))
      .mockResolvedValueOnce(response(login()))
    const mounted = render(<StrictMode><AuthProvider><Probe/></AuthProvider></StrictMode>)
    await waitFor(() => expect(screen.getByTestId('user')).toHaveTextContent('user-a'))
    expect(fetchMock.mock.calls[0][1]?.signal?.aborted).toBe(true)
    fetchMock.mockImplementationOnce(() => new Promise(() => {}))
    fireEvent.click(screen.getByRole('button', { name: '重新连接' }))
    const pending = fetchMock.mock.calls.at(-1)?.[1]?.signal
    mounted.unmount()
    expect(pending?.aborted).toBe(true)
  })
})
