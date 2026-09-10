import { act, render, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import App from './App'

vi.mock('./auth', () => ({
  useAuth: () => ({
    loading: false,
    bootstrapRequired: false,
    registration: 'closed',
    user: { id: 'u1', email: 'a@example.test', display_name: 'A', role: 'user' },
    sessionID: 's1',
    error: '',
    notice: '',
    refresh: vi.fn(),
    setAuthenticated: vi.fn(),
    signOut: vi.fn(),
  }),
}))
vi.mock('./lib/pwa', () => ({ usePWA: () => ({ online: true, updateReady: false, error: '' }) }))
vi.mock('./lib/appearance', () => ({ setAppearanceScope: vi.fn() }))
vi.mock('./lib/api', () => ({
  api: {
    pairTailcat: vi.fn(),
    hosts: vi.fn().mockResolvedValue({ hosts: [] }),
    hostFolders: vi.fn().mockResolvedValue({ folders: [] }),
    sshKeys: vi.fn().mockResolvedValue({ keys: [] }),
    cliRelease: vi.fn().mockResolvedValue({ status: 'unpublished' }),
  },
}))

function encodePair(payload: object) {
  return btoa(JSON.stringify(payload)).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '')
}

beforeEach(() => {
  sessionStorage.clear()
  window.history.replaceState({}, '', '/')
})
afterEach(() => {
  sessionStorage.clear()
  window.history.replaceState({}, '', '/')
})

it('keeps a #pair= deep link on the pairing page after the hash is replaced', async () => {
  window.history.replaceState({}, '', `/#pair=${encodePair({ host: 'box', os: 'linux', arch: 'amd64', agent_ver: '1' })}`)
  render(<App/>)
  expect(screen.getByRole('heading', { name: '连接 Tailcat 主机' })).toBeInTheDocument()
  await waitFor(() => expect(window.location.pathname).toBe('/pair'))
  await act(async () => { window.dispatchEvent(new PopStateEvent('popstate')) })
  expect(screen.getByRole('heading', { name: '连接 Tailcat 主机' })).toBeInTheDocument()
  expect(screen.queryByRole('heading', { name: '主机' })).not.toBeInTheDocument()
})
