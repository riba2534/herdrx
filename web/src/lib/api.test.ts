import { describe, expect, it } from 'vitest'
import { apiErrorMessage } from './api'

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
