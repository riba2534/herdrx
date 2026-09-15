import { act, render, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import App from './App'
import { api } from './lib/api'
import { navigate } from './lib/navigation'
import { resetWorkbenchSessionCache } from './lib/workbenchSession'

vi.mock('./auth', () => ({ useAuth: () => ({ user: { id: 'user', role: 'admin' }, sessionID: 'login', loading: false, bootstrapRequired: false, error: '' }) }))
vi.mock('./lib/pwa', () => ({ usePWA: () => ({ online: true }) }))
vi.mock('./lib/appearance', () => ({ setAppearanceScope: vi.fn() }))
vi.mock('./lib/api', () => ({
  api: { workbenchSession: vi.fn(), hosts: vi.fn() },
  currentSessionID: () => 'login',
}))
vi.mock('./pages/HostsPage', () => ({ HostsPage: () => <main>主机列表测试</main> }))
vi.mock('./pages/WorkbenchPage', () => ({ WorkbenchPage: () => <main>工作台测试</main> }))
vi.mock('./pages/AuthPage', () => ({ AuthPage: () => null }))
vi.mock('./pages/PairPage', () => ({ PairPage: () => null }))

beforeEach(() => {
  localStorage.clear()
  sessionStorage.clear()
  resetWorkbenchSessionCache()
  vi.mocked(api.workbenchSession).mockResolvedValue({ session: { host_id: 'host', workspace_id: 'w2', device_id: 'other-phone', client_class: 'mobile' } })
  vi.mocked(api.hosts).mockResolvedValue({ hosts: [{ id: 'host' }] as never })
})
afterEach(() => { vi.clearAllMocks(); window.history.replaceState({}, '', '/') })

describe('root workbench restore navigation', () => {
  it('keeps an explicit return to the host list when another device wrote last', async () => {
    window.history.replaceState({}, '', '/h/host')
    render(<App/>)
    await screen.findByText('工作台测试')
    act(() => navigate('/'))
    await screen.findByText('主机列表测试')
    expect(window.location.pathname).toBe('/')
    expect(api.workbenchSession).not.toHaveBeenCalled()
  })

  it('still restores the last workbench when opening the root on a new device', async () => {
    window.history.replaceState({}, '', '/')
    render(<App/>)
    await screen.findByText('工作台测试')
    await waitFor(() => expect(window.location.pathname).toBe('/h/host'))
    expect(api.workbenchSession).toHaveBeenCalledOnce()
  })
})
