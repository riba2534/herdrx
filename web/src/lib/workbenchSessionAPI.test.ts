import { afterEach, expect, it, vi } from 'vitest'
import { api } from './api'

afterEach(() => { vi.unstubAllGlobals(); vi.useRealTimers() })

it.each(['read', 'save'] as const)('aborts a hung position %s after five seconds', async (operation) => {
  vi.useFakeTimers()
  let signal: AbortSignal | undefined
  vi.stubGlobal('fetch', vi.fn((_url: string, init: RequestInit) => {
    signal = init.signal as AbortSignal
    return new Promise((_resolve, reject) => signal!.addEventListener('abort', () => reject(new DOMException('aborted', 'AbortError'))))
  }))
  const request = operation === 'read' ? api.workbenchSession() : api.saveWorkbenchSession({ host_id: 'host' })
  const rejected = expect(request).rejects.toMatchObject({ name: 'AbortError' })
  await vi.advanceTimersByTimeAsync(5000)
  await rejected
  expect(signal?.aborted).toBe(true)
})
