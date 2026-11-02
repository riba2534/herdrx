import { describe, expect, it, vi } from 'vitest'
import { api, apiErrorMessage } from './api'

describe('apiErrorMessage', () => {
  it('keeps Chinese API errors', () => {
    expect(apiErrorMessage({ error: '未登录', code: 'unauthorized' })).toBe('未登录')
  })

  it('adds a Chinese fallback for English-only errors', () => {
    expect(apiErrorMessage({ error: 'boom' })).toBe('boom。请求失败，请检查网络后重试')
  })

  it('falls back when the payload has no message', () => {
    expect(apiErrorMessage({})).toBe('请求失败，请检查网络后重试')
    expect(apiErrorMessage({ code: 'request_failed' })).toBe('请求失败，请检查网络后重试')
  })
})

describe('api.launchers', () => {
  it('sends the CSRF header, since listing launchers runs a script on the remote host', async () => {
    const fetchMock = vi.fn(async (path: string) => new Response(JSON.stringify(path === '/api/me'
      ? { user: { id: 'u' }, csrf_token: 'csrf-launchers', session_id: 'launchers-session' }
      : { supported: true, launchers: [] }), { status: 200 }))
    vi.stubGlobal('fetch', fetchMock)
    try {
      await api.me()
      await api.launchers('host one', { headers: { 'X-Extra': '1' } })
      const [path, init] = fetchMock.mock.calls[1] as unknown as [string, RequestInit]
      expect(path).toBe('/api/hosts/host%20one/launchers')
      const headers = new Headers(init.headers)
      expect(headers.get('X-CSRF-Token')).toBe('csrf-launchers')
      expect(headers.get('X-Extra')).toBe('1')
      expect(init.method).toBeUndefined()
    } finally {
      vi.unstubAllGlobals()
    }
  })
})
