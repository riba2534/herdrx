import { render, screen } from '@testing-library/react'
import { afterEach, expect, it, vi } from 'vitest'
import App from './App'
import { api } from './lib/api'

const auth = {
  loading: false,
  bootstrapRequired: false,
  registration: 'closed',
  user: { id: 'u1', email: 'a@example.test', display_name: 'A', role: 'user' as const, must_change_password: true },
  sessionID: 's1',
  error: '',
  notice: '',
  refresh: vi.fn(),
  setAuthenticated: vi.fn(),
  signOut: vi.fn(),
}
vi.mock('./auth', () => ({ useAuth: () => auth }))
vi.mock('./lib/pwa', () => ({ usePWA: () => ({ online: true, updateReady: false, error: '' }) }))
vi.mock('./lib/appearance', async (importOriginal) => ({ ...await importOriginal<typeof import('./lib/appearance')>(), setAppearanceScope: vi.fn() }))
vi.mock('./lib/workbenchSessionPersistence', () => ({ readWorkbenchSession: vi.fn().mockResolvedValue({ session: null }) }))
vi.mock('./lib/api', () => ({ api: { changePassword: vi.fn(), hosts: vi.fn() } }))

afterEach(() => { window.history.replaceState({}, '', '/') })

it.each(['/', '/h/hst_fixture', '/admin', '/keys'])('routes %s to the forced password change after an administrator reset', (path) => {
  window.history.replaceState({}, '', path)
  render(<App/>)
  expect(screen.getByRole('heading', { name: '请先修改密码' })).toBeInTheDocument()
  expect(screen.getByRole('button', { name: /退出登录/ })).toBeInTheDocument()
  expect(api.hosts).not.toHaveBeenCalled()
})

it('opens the self-service page from /account without forcing it', () => {
  auth.user = { ...auth.user, must_change_password: false }
  window.history.replaceState({}, '', '/account')
  render(<App/>)
  expect(screen.getByRole('heading', { name: '修改密码' })).toBeInTheDocument()
  expect(screen.getByRole('button', { name: /返回主机/ })).toBeInTheDocument()
})
