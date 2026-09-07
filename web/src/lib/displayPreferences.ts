import { useEffect, useState } from 'react'

export type TerminalDisplay = { fontSize: number; zoom: number; mode: 'fit' | 'fixed' | 'responsive' }
type DisplayProfiles = { desktop: TerminalDisplay; mobile: TerminalDisplay }
export const DISPLAY_STORAGE_KEY = 'herdrx.terminal-display.v2'
export const DEFAULT_DISPLAY: DisplayProfiles = {
  desktop: { fontSize: 14, zoom: 100, mode: 'fit' },
  mobile: { fontSize: 14, zoom: 100, mode: 'responsive' },
}

function bounded(value: unknown, fallback: number, min: number, max: number) {
  return typeof value === 'number' && Number.isFinite(value) ? Math.min(max, Math.max(min, Math.round(value))) : fallback
}

export function readDisplayProfiles(): DisplayProfiles {
  try {
    const current = localStorage.getItem(DISPLAY_STORAGE_KEY)
    const stored = JSON.parse(current || localStorage.getItem('herdrx.terminal-display.v1') || '{}')
    // v1 persisted the old mobile default even when it was never selected.
    // Migrate existing visitors to reflow while retaining their chosen font.
    if (!current && stored?.mobile) stored.mobile = { ...stored.mobile, mode: 'responsive', zoom: 100 }
    const profile = (key: keyof DisplayProfiles): TerminalDisplay => {
      const value = stored?.[key]
      const fallback = DEFAULT_DISPLAY[key]
      return {
        fontSize: bounded(value?.fontSize, fallback.fontSize, 10, 28),
        zoom: bounded(value?.zoom, fallback.zoom, 50, 200),
        mode: ['fit', 'fixed', 'responsive'].includes(value?.mode) ? value.mode : fallback.mode,
      }
    }
    return { desktop: profile('desktop'), mobile: profile('mobile') }
  } catch { return { ...DEFAULT_DISPLAY } }
}

export function useTerminalDisplay(mobile: boolean) {
  const [profiles, setProfiles] = useState(readDisplayProfiles)
  const key = mobile ? 'mobile' : 'desktop'
  useEffect(() => {
    try { localStorage.setItem(DISPLAY_STORAGE_KEY, JSON.stringify(profiles)) } catch { /* Private storage may be unavailable; keep this visit usable. */ }
  }, [profiles])
  const update = (patch: Partial<TerminalDisplay>) => setProfiles((current) => ({ ...current, [key]: { ...current[key], ...patch } }))
  return { display: profiles[key], update, reset: () => update(DEFAULT_DISPLAY[key]) }
}

// Keep phones in the single-pane layout when rotated; larger tablets retain
// the desktop layout with touch-sized controls. Browser page zoom changes the
// layout viewport and therefore follows the same breakpoint.
export const COMPACT_WORKBENCH_QUERY = '(max-width: 767px), (pointer: coarse) and (max-width: 1023px) and (max-height: 600px)'

export function useWorkbenchViewport() {
  const [mobile, setMobile] = useState(() => window.matchMedia(COMPACT_WORKBENCH_QUERY).matches)
  const [height, setHeight] = useState(() => window.innerHeight)
  useEffect(() => {
    const media = window.matchMedia(COMPACT_WORKBENCH_QUERY)
    const update = () => {
      setMobile(media.matches)
      const viewport = window.visualViewport
      // A software keyboard reduces visualViewport without resizing the page.
      // Native pinch zoom must remain a browser operation without reflow.
      setHeight(viewport && viewport.scale === 1 ? viewport.height : window.innerHeight)
    }
    update()
    media.addEventListener('change', update)
    window.addEventListener('resize', update)
    window.visualViewport?.addEventListener('resize', update)
    return () => {
      media.removeEventListener('change', update)
      window.removeEventListener('resize', update)
      window.visualViewport?.removeEventListener('resize', update)
    }
  }, [])
  return { mobile, height }
}
