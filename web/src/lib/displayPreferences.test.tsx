import { act, renderHook } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { DEFAULT_DISPLAY, DISPLAY_STORAGE_KEY, readDisplayProfiles, useTerminalDisplay, useWorkbenchViewport } from './displayPreferences'

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
    expect(reloaded.result.current.display).toEqual({ fontSize: 16, zoom: 120, mode: 'responsive' })
    act(() => reloaded.result.current.reset())
    expect(readDisplayProfiles().mobile).toEqual(DEFAULT_DISPLAY.mobile)
    expect(readDisplayProfiles().desktop.fontSize).toBe(20)
  })

  it('migrates existing mobile visitors to reflow and remembers a later opt-out', () => {
    localStorage.setItem('herdrx.terminal-display.v1', JSON.stringify({ desktop: { fontSize: 20, zoom: 140, mode: 'fixed' }, mobile: { fontSize: 16, zoom: 170, mode: 'fixed' } }))
    expect(readDisplayProfiles()).toEqual({ desktop: { fontSize: 20, zoom: 140, mode: 'fixed' }, mobile: { fontSize: 16, zoom: 100, mode: 'responsive' } })
    const { result, unmount } = renderHook(() => useTerminalDisplay(true))
    act(() => result.current.update({ mode: 'fixed' }))
    unmount()
    expect(readDisplayProfiles().mobile.mode).toBe('fixed')
  })

  it('recovers corrupt settings and clamps invalid persisted dimensions', () => {
    localStorage.setItem(DISPLAY_STORAGE_KEY, '{broken')
    expect(readDisplayProfiles()).toEqual(DEFAULT_DISPLAY)
    localStorage.setItem(DISPLAY_STORAGE_KEY, JSON.stringify({ desktop: { mode: 'unsupported', fontSize: -50, zoom: 900 }, mobile: { fontSize: '12', zoom: null } }))
    expect(readDisplayProfiles()).toEqual({ desktop: { mode: 'fixed', fontSize: 10, zoom: 200 }, mobile: DEFAULT_DISPLAY.mobile })
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

  it('follows the software keyboard viewport while leaving native pinch zoom alone', () => {
    const viewport = Object.assign(new EventTarget(), { scale: 1, height: 800 })
    vi.stubGlobal('visualViewport', viewport)
    vi.stubGlobal('innerHeight', 800)
    vi.stubGlobal('matchMedia', () => ({ matches: true, addEventListener() {}, removeEventListener() {} }))
    const { result } = renderHook(useWorkbenchViewport)
    expect(result.current.height).toBe(800)
    act(() => { viewport.height = 350; viewport.dispatchEvent(new Event('resize')) })
    expect(result.current.height).toBe(350)
    act(() => { viewport.scale = 2; viewport.height = 175; viewport.dispatchEvent(new Event('resize')) })
    expect(result.current.height).toBe(800)
  })
})
