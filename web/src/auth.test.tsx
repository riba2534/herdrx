import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { AuthProvider, useAuth } from './auth'
import { api, csrf, currentSessionID, invalidateAuthentication } from './lib/api'

const user = { id: 'user-a', email: 'user@example.test', display_name: 'Member', role: 'user' as const }
const login = (id = 'login-a') => ({ user, csrf_token: 'csrf-' + id, session_id: id })
const response = (data: unknown, status = 200) => new Response(JSON.stringify(data), { status, headers: { 'Content-Type': 'application/json' } })
const fetchMock = vi.fn<typeof fetch>()
function Probe() {
  const auth = useAuth()
  return <><p data-testid="user">{auth.user?.id || 'anonymous'}</p><p role="status">{auth.notice}</p><button onClick={() => void auth.signOut().catch(() => {})}>退出</button></>
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
})
