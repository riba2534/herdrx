import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { api, invalidateAuthentication } from './api'
import { clearPaneViewModes, readPaneViewMode, retainPaneViewModes, subscribePaneViewModes, writePaneViewMode } from './paneViewMode'

const user = { id: 'user', email: 'user@example.test', role: 'user', display_name: 'User' }

async function login(sessionID: string) {
  vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response(JSON.stringify({ user, csrf_token: `csrf-${sessionID}`, session_id: sessionID }))))
  await api.login({ email: 'user@example.test', password: 'test' })
}

beforeEach(async () => {
  invalidateAuthentication()
  clearPaneViewModes()
  localStorage.clear()
  sessionStorage.clear()
  await login('sess-a')
})
afterEach(() => {
  clearPaneViewModes()
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
})

describe('pane view mode isolation', () => {
  it('defaults every pane to the terminal view', () => {
    expect(readPaneViewMode('host-1', 'pane-1')).toBe('terminal')
  })

  it('keeps chat mode per host and per pane', () => {
    writePaneViewMode('host-1', 'pane-1', 'chat')
    expect(readPaneViewMode('host-1', 'pane-1')).toBe('chat')
    expect(readPaneViewMode('host-1', 'pane-2')).toBe('terminal')
    expect(readPaneViewMode('host-2', 'pane-1')).toBe('terminal')
  })

  it('does not leak chat mode into another login session', async () => {
    writePaneViewMode('host-1', 'pane-1', 'chat')
    expect(localStorage.getItem(`herdrx.pane-view.v1.${encodeURIComponent('sess-a')}/${encodeURIComponent('host-1')}/${encodeURIComponent('pane-1')}`)).toBe('chat')
    invalidateAuthentication()
    expect(readPaneViewMode('host-1', 'pane-1')).toBe('terminal')
    await login('sess-b')
    expect(readPaneViewMode('host-1', 'pane-1')).toBe('terminal')
    writePaneViewMode('host-2', 'pane-9', 'chat')
    retainPaneViewModes('sess-b')
    const remaining = [...Array(localStorage.length)].map((_, index) => localStorage.key(index)).filter((key) => key?.startsWith('herdrx.pane-view.v1.'))
    expect(remaining).toEqual([`herdrx.pane-view.v1.${encodeURIComponent('sess-b')}/${encodeURIComponent('host-2')}/${encodeURIComponent('pane-9')}`])
  })

  it('persists chat mode under the login-scoped key and removes it on switch back', () => {
    const key = `herdrx.pane-view.v1.${encodeURIComponent('sess-a')}/${encodeURIComponent('host-1')}/${encodeURIComponent('pane-1')}`
    writePaneViewMode('host-1', 'pane-1', 'chat')
    expect(localStorage.getItem(key)).toBe('chat')
    writePaneViewMode('host-1', 'pane-1', 'terminal')
    expect(localStorage.getItem(key)).toBeNull()
    clearPaneViewModes()
    expect(localStorage.length).toBe(0)
  })

  it('ignores writes without a pane and notifies subscribers on change', () => {
    const listener = vi.fn()
    const unsubscribe = subscribePaneViewModes(listener)
    writePaneViewMode('host-1', '', 'chat')
    expect(readPaneViewMode('host-1', '')).toBe('terminal')
    expect(listener).not.toHaveBeenCalled()
    writePaneViewMode('host-1', 'pane-1', 'chat')
    expect(listener).toHaveBeenCalled()
    unsubscribe()
  })
})
