import { act, renderHook } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { DEFAULT_DISPLAY, DISPLAY_MIGRATION_KEY, DISPLAY_MOBILE_MIGRATION_KEY, DISPLAY_STORAGE_KEY, readDisplayProfiles, useTerminalDisplay, useWorkbenchViewport } from './displayPreferences'

beforeEach(() => localStorage.clear())
afterEach(() => vi.unstubAllGlobals())

describe('display preferences', () => {
  it('keeps desktop and phone preferences separate across reloads', () => {
    const { result, rerender, unmount } = renderHook(({ mobile }) => useTerminalDisplay(mobile), { initialProps: { mobile: false } })
    act(() => result.current.update({ fontSize: 20, zoom: 150, mode: 'fixed' }))
    rerender({ mobile: true })
    expect(result.current.display).toEqual(DEFAULT_DISPLAY.mobile)
    act(() => result.current.update({ fontSize: 16, zoom: 120 }))
    rerender({ mobile: false })
    expect(result.current.display).toEqual({ fontSize: 20, zoom: 150, mode: 'fixed' })
    unmount()
    const reloaded = renderHook(() => useTerminalDisplay(true))
    expect(reloaded.result.current.display).toEqual({ fontSize: 16, zoom: 120, mode: 'auto' })
    act(() => reloaded.result.current.reset())
    expect(readDisplayProfiles().mobile).toEqual(DEFAULT_DISPLAY.mobile)
    expect(readDisplayProfiles().desktop.fontSize).toBe(20)
  })

  it('preserves legacy mobile local display preferences without enabling remote resize', () => {
    localStorage.setItem('herdrx.terminal-display.v1', JSON.stringify({ desktop: { fontSize: 20, zoom: 140, mode: 'fixed' }, mobile: { fontSize: 16, zoom: 170, mode: 'fixed' } }))
    expect(readDisplayProfiles()).toEqual({ desktop: { fontSize: 20, zoom: 140, mode: 'fixed' }, mobile: { fontSize: 16, zoom: 170, mode: 'fixed' }, mobileLandscape: { fontSize: 16, zoom: 170, mode: 'fixed' } })
    const { result, unmount } = renderHook(() => useTerminalDisplay(true))
    act(() => result.current.update({ mode: 'fixed' }))
    unmount()
    expect(readDisplayProfiles().mobile.mode).toBe('fixed')
  })

  it.each(['herdrx.terminal-display.v1', 'herdrx.terminal-display.v2', DISPLAY_STORAGE_KEY])('never restores remote resize from %s', (key) => {
    localStorage.setItem(key, JSON.stringify({ desktop: { fontSize: 18, zoom: 130, mode: 'responsive' }, mobile: { fontSize: 16, zoom: 120, mode: 'responsive' } }))
    expect(readDisplayProfiles()).toEqual({ desktop: { fontSize: 18, zoom: 130, mode: 'auto' }, mobile: { fontSize: 16, zoom: 120, mode: 'auto' }, mobileLandscape: { fontSize: 16, zoom: 120, mode: 'auto' } })
  })

  it('persists only local display choices and keeps font settings across visits', () => {
    const { result, unmount } = renderHook(() => useTerminalDisplay(true))
    act(() => result.current.update({ mode: 'fixed', fontSize: 18, zoom: 120 }))
    expect(JSON.parse(localStorage.getItem(DISPLAY_STORAGE_KEY)!).mobile).toEqual({ mode: 'fixed', fontSize: 18, zoom: 120 })
    unmount()
    const next = renderHook(() => useTerminalDisplay(true))
    expect(next.result.current.display).toEqual({ mode: 'fixed', fontSize: 18, zoom: 120 })
  })

  it('recovers corrupt settings and clamps invalid persisted dimensions', () => {
    localStorage.setItem(DISPLAY_STORAGE_KEY, '{broken')
    expect(readDisplayProfiles()).toEqual(DEFAULT_DISPLAY)
    localStorage.setItem(DISPLAY_STORAGE_KEY, JSON.stringify({ desktop: { mode: 'unsupported', fontSize: -50, zoom: 900 }, mobile: { fontSize: '12', zoom: null } }))
    expect(readDisplayProfiles()).toEqual({ desktop: { mode: 'auto', fontSize: 10, zoom: 200 }, mobile: DEFAULT_DISPLAY.mobile, mobileLandscape: DEFAULT_DISPLAY.mobile })
  })

  it('migrates only the legacy desktop fit 14/100 default and keeps later explicit fit', () => {
    localStorage.setItem('herdrx.terminal-display.v2', JSON.stringify({ desktop: { fontSize: 14, zoom: 100, mode: 'fit' }, mobile: { fontSize: 14, zoom: 100, mode: 'responsive' } }))
    expect(readDisplayProfiles().desktop).toEqual(DEFAULT_DISPLAY.desktop)
    localStorage.setItem('herdrx.terminal-display.v2', JSON.stringify({ desktop: { fontSize: 16, zoom: 100, mode: 'fit' }, mobile: { fontSize: 14, zoom: 100, mode: 'responsive' } }))
    expect(readDisplayProfiles().desktop).toEqual({ fontSize: 16, zoom: 100, mode: 'fit' })
    localStorage.setItem('herdrx.terminal-display.v2', JSON.stringify({ desktop: { fontSize: 14, zoom: 120, mode: 'fit' }, mobile: { fontSize: 14, zoom: 100, mode: 'responsive' } }))
    expect(readDisplayProfiles().desktop).toEqual({ fontSize: 14, zoom: 120, mode: 'fit' })
    localStorage.setItem(DISPLAY_STORAGE_KEY, JSON.stringify({ desktop: { fontSize: 14, zoom: 100, mode: 'fit' }, mobile: DEFAULT_DISPLAY.mobile }))
    expect(readDisplayProfiles().desktop).toEqual({ fontSize: 14, zoom: 100, mode: 'fit' })
    const { result, unmount } = renderHook(() => useTerminalDisplay(false))
    expect(result.current.display).toEqual({ fontSize: 14, zoom: 100, mode: 'fit' })
    act(() => result.current.update({ mode: 'fixed', zoom: 100 }))
    act(() => result.current.update({ mode: 'fit', zoom: 100 }))
    unmount()
    expect(readDisplayProfiles().desktop).toEqual({ fontSize: 14, zoom: 100, mode: 'fit' })
  })

  it('migrates the previous fixed 14px desktop default once and keeps later explicit choices', () => {
    localStorage.setItem(DISPLAY_STORAGE_KEY, JSON.stringify({ desktop: { fontSize: 14, zoom: 100, mode: 'fixed' }, mobile: DEFAULT_DISPLAY.mobile }))
    expect(readDisplayProfiles().desktop).toEqual(DEFAULT_DISPLAY.desktop)
    // Visitors who only used the title-bar zoom buttons are migrated too, and keep the zoom.
    localStorage.setItem(DISPLAY_STORAGE_KEY, JSON.stringify({ desktop: { fontSize: 14, zoom: 130, mode: 'fixed' }, mobile: DEFAULT_DISPLAY.mobile }))
    expect(readDisplayProfiles().desktop).toEqual({ fontSize: 14, zoom: 130, mode: 'auto' })
    // A changed font size is an explicit choice, not a shipped default.
    localStorage.setItem(DISPLAY_STORAGE_KEY, JSON.stringify({ desktop: { fontSize: 16, zoom: 100, mode: 'fixed' }, mobile: DEFAULT_DISPLAY.mobile }))
    expect(readDisplayProfiles().desktop).toEqual({ fontSize: 16, zoom: 100, mode: 'fixed' })
    localStorage.setItem(DISPLAY_STORAGE_KEY, JSON.stringify({ desktop: { fontSize: 14, zoom: 100, mode: 'auto' }, mobile: DEFAULT_DISPLAY.mobile }))
    expect(readDisplayProfiles().desktop).toEqual(DEFAULT_DISPLAY.desktop)
    // The migration runs once, so picking fixed 14px afterwards survives a reload.
    const { result, unmount } = renderHook(() => useTerminalDisplay(false))
    act(() => result.current.update({ fontSize: 14, zoom: 100, mode: 'fixed' }))
    unmount()
    expect(localStorage.getItem(DISPLAY_MIGRATION_KEY)).toBe('1')
    expect(readDisplayProfiles().desktop).toEqual({ fontSize: 14, zoom: 100, mode: 'fixed' })
    // A browser that already wrote this release's record is never migrated.
    localStorage.clear()
    const fresh = renderHook(() => useTerminalDisplay(false))
    act(() => fresh.result.current.update({ fontSize: 14, zoom: 100, mode: 'fixed' }))
    fresh.unmount()
    expect(readDisplayProfiles().desktop).toEqual({ fontSize: 14, zoom: 100, mode: 'fixed' })
  })

  it('moves the previous fixed 14px phone default to auto 13px once and keeps later choices', () => {
    localStorage.setItem(DISPLAY_STORAGE_KEY, JSON.stringify({ desktop: DEFAULT_DISPLAY.desktop, mobile: { fontSize: 14, zoom: 100, mode: 'fixed' } }))
    expect(readDisplayProfiles().mobile).toEqual({ fontSize: 13, zoom: 100, mode: 'auto' })
    // Any other phone record is a choice: a zoom, another size or another mode.
    for (const mobile of [{ fontSize: 14, zoom: 120, mode: 'fixed' }, { fontSize: 16, zoom: 100, mode: 'fixed' }, { fontSize: 14, zoom: 100, mode: 'fit' }] as const) {
      localStorage.setItem(DISPLAY_STORAGE_KEY, JSON.stringify({ desktop: DEFAULT_DISPLAY.desktop, mobile }))
      expect(readDisplayProfiles().mobile).toEqual(mobile)
    }
    const { result, unmount } = renderHook(() => useTerminalDisplay(true))
    act(() => result.current.update({ fontSize: 14, zoom: 100, mode: 'fixed' }))
    unmount()
    expect(localStorage.getItem(DISPLAY_MOBILE_MIGRATION_KEY)).toBe('1')
    expect(readDisplayProfiles().mobile).toEqual({ fontSize: 14, zoom: 100, mode: 'fixed' })
  })

  it('remembers phone portrait and landscape separately, starting landscape from portrait', () => {
    localStorage.setItem(DISPLAY_STORAGE_KEY, JSON.stringify({ desktop: DEFAULT_DISPLAY.desktop, mobile: { fontSize: 15, zoom: 100, mode: 'fixed' } }))
    const { result, rerender } = renderHook(({ landscape }) => useTerminalDisplay(true, landscape), { initialProps: { landscape: true } })
    expect(result.current.profile).toBe('mobileLandscape')
    expect(result.current.display).toEqual({ fontSize: 15, zoom: 100, mode: 'fixed' })
    act(() => result.current.update({ fontSize: 11 }))
    rerender({ landscape: false })
    expect(result.current.profile).toBe('mobile')
    expect(result.current.display.fontSize).toBe(15)
    act(() => result.current.reset())
    rerender({ landscape: true })
    expect(result.current.display.fontSize).toBe(11)
    expect(readDisplayProfiles().mobile).toEqual(DEFAULT_DISPLAY.mobile)
  })

  it('reports the covered height only while a touch screen edits, through the close animation', async () => {
    const viewport = Object.assign(new EventTarget(), { scale: 1, height: 800, width: 390, offsetTop: 0 })
    vi.stubGlobal('visualViewport', viewport)
    vi.stubGlobal('innerHeight', 800)
    vi.stubGlobal('scrollTo', vi.fn())
    let coarse = true
    vi.stubGlobal('matchMedia', (query: string) => ({ matches: query.includes('coarse') ? coarse : true, addEventListener() {}, removeEventListener() {} }))
    const input = document.body.appendChild(document.createElement('textarea'))
    const resize = (height: number) => act(() => { viewport.height = height; viewport.dispatchEvent(new Event('resize')) })
    try {
      const { result, unmount } = renderHook(useWorkbenchViewport)
      // Browser bars collapsing or a page without focus are not a keyboard.
      resize(500)
      expect(result.current).toMatchObject({ keyboardOpen: false, keyboardInset: 0 })
      resize(800)
      act(() => input.focus())
      // The keyboard animates in steps; every step already counts as covered.
      resize(740)
      expect(result.current).toMatchObject({ keyboardOpen: false, keyboardInset: 60 })
      resize(460)
      expect(result.current).toMatchObject({ keyboardOpen: true, keyboardInset: 340, height: 460 })
      // A candidate strip alone covers too little to be called a keyboard.
      resize(720)
      expect(result.current).toMatchObject({ keyboardOpen: false, keyboardInset: 80 })
      resize(460)
      // Losing focus starts the close animation: still covered until it grows back.
      act(() => input.blur())
      resize(520)
      expect(result.current).toMatchObject({ keyboardOpen: true, keyboardInset: 280 })
      resize(800)
      expect(result.current).toMatchObject({ keyboardOpen: false, keyboardInset: 0 })
      // A viewport that stays short after the settle window is simply shorter.
      act(() => input.focus())
      resize(460)
      act(() => input.blur())
      await act(() => new Promise((done) => setTimeout(done, 1100)))
      expect(result.current).toMatchObject({ keyboardOpen: false, keyboardInset: 0 })
      unmount()
      // A desktop window resized with the terminal focused keeps reflowing.
      coarse = false
      viewport.height = 800
      const desktop = renderHook(useWorkbenchViewport)
      act(() => input.focus())
      resize(400)
      expect(desktop.result.current).toMatchObject({ keyboardOpen: false, keyboardInset: 0 })
      desktop.unmount()
    } finally { input.remove() }
  })

  it('follows the software keyboard viewport while leaving native pinch zoom alone', () => {
    const viewport = Object.assign(new EventTarget(), { scale: 1, height: 800, offsetTop: 0 })
    vi.stubGlobal('visualViewport', viewport)
    vi.stubGlobal('innerHeight', 800)
    vi.stubGlobal('scrollTo', vi.fn())
    vi.stubGlobal('matchMedia', () => ({ matches: true, addEventListener() {}, removeEventListener() {} }))
    const { result } = renderHook(useWorkbenchViewport)
    expect(result.current.height).toBe(800)
    act(() => { viewport.height = 350; viewport.dispatchEvent(new Event('resize')) })
    expect(result.current.height).toBe(350)
    act(() => { viewport.scale = 2; viewport.height = 175; viewport.dispatchEvent(new Event('resize')) })
    expect(result.current.height).toBe(800)
  })

  it('resets document scroll and records visualViewport offsetTop on keyboard scroll', () => {
    const viewport = Object.assign(new EventTarget(), { scale: 1, height: 400, offsetTop: 120 })
    const scrollTo = vi.fn()
    vi.stubGlobal('visualViewport', viewport)
    vi.stubGlobal('innerHeight', 844)
    vi.stubGlobal('scrollTo', scrollTo)
    vi.stubGlobal('matchMedia', () => ({ matches: true, addEventListener() {}, removeEventListener() {} }))
    const { result } = renderHook(useWorkbenchViewport)
    expect(result.current.height).toBe(400)
    expect(result.current.offsetTop).toBe(120)
    expect(scrollTo).toHaveBeenCalledWith(0, 0)
    act(() => { viewport.offsetTop = 80; viewport.height = 350; viewport.dispatchEvent(new Event('scroll')) })
    expect(result.current.height).toBe(350)
    expect(result.current.offsetTop).toBe(80)
    expect(scrollTo).toHaveBeenLastCalledWith(0, 0)
  })
})
