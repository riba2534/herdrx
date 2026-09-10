import { afterEach, describe, expect, it, vi } from 'vitest'
import { isStandalone, needsHomeScreenForNotifications } from './pwa'

afterEach(() => vi.unstubAllGlobals())

describe('PWA standalone and iOS notification copy', () => {
  it('detects standalone display mode and iOS navigator.standalone', () => {
    vi.stubGlobal('matchMedia', (query: string) => ({ matches: query.includes('standalone'), addEventListener() {}, removeEventListener() {} }))
    expect(isStandalone()).toBe(true)
    vi.stubGlobal('matchMedia', () => ({ matches: false, addEventListener() {}, removeEventListener() {} }))
    vi.stubGlobal('navigator', { ...navigator, standalone: true })
    expect(isStandalone()).toBe(true)
    vi.stubGlobal('navigator', { ...navigator, standalone: false })
    expect(isStandalone()).toBe(false)
  })

  it('asks iOS Safari without Notification to add the app to the home screen', () => {
    vi.stubGlobal('navigator', { userAgent: 'Mozilla/5.0 (iPhone; CPU iPhone OS 16_0 like Mac OS X)', platform: 'iPhone', maxTouchPoints: 5 })
    const original = window.Notification
    // eslint-disable-next-line @typescript-eslint/no-dynamic-delete
    delete (window as Window & { Notification?: unknown }).Notification
    expect(needsHomeScreenForNotifications()).toBe(true)
    Object.defineProperty(window, 'Notification', { configurable: true, value: original })
    vi.stubGlobal('navigator', { userAgent: 'Mozilla/5.0 (X11; Linux x86_64)', platform: 'Linux', maxTouchPoints: 0 })
    expect(needsHomeScreenForNotifications()).toBe(false)
  })
})
