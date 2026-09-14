import { useEffect, useState } from 'react'

export type TerminalDisplay = { fontSize: number; zoom: number; mode: 'auto' | 'fit' | 'fixed' | 'responsive' }
type DisplayProfiles = { desktop: TerminalDisplay; mobile: TerminalDisplay }
export const DISPLAY_STORAGE_KEY = 'herdrx.terminal-display.v3'
export const DISPLAY_MIGRATION_KEY = 'herdrx.terminal-display.default-migrated'
const DISPLAY_STORAGE_V2 = 'herdrx.terminal-display.v2'
const DISPLAY_STORAGE_V1 = 'herdrx.terminal-display.v1'
export const DEFAULT_DISPLAY: DisplayProfiles = {
  desktop: { fontSize: 14, zoom: 100, mode: 'auto' },
  mobile: { fontSize: 14, zoom: 100, mode: 'responsive' },
}
export const LEGACY_DESKTOP_DEFAULT: TerminalDisplay = { fontSize: 14, zoom: 100, mode: 'fit' }

function bounded(value: unknown, fallback: number, min: number, max: number) {
  return typeof value === 'number' && Number.isFinite(value) ? Math.min(max, Math.max(min, Math.round(value))) : fallback
}

function matchesDesktopDefault(value: unknown, mode: string) {
  if (!value || typeof value !== 'object') return false
  const item = value as { fontSize?: unknown; zoom?: unknown; mode?: unknown }
  return item.mode === mode && item.fontSize === 14 && item.zoom === 100
}

// A shipped default cannot be told apart from a visitor who never opened the
// display controls, so each version's default is migrated to the current one.
function isLegacyDesktopDefault(value: unknown) {
  return matchesDesktopDefault(value, 'fit')
}

// v3 shipped fixed 14px, and the title-bar zoom buttons wrote only the zoom, so
// a stored fixed 14px record is treated as that default whatever zoom it carries
// and the zoom is kept. This cannot tell an untouched default apart from a
// fixed-14px choice made before this release; the one-shot marker below keeps
// choices made after it from being migrated again. A different font size is a
// real choice and is always preserved.
function isPreviousDesktopDefault(value: unknown) {
  if (!value || typeof value !== 'object') return false
  const item = value as { fontSize?: unknown; mode?: unknown }
  return item.mode === 'fixed' && item.fontSize === 14
}

// The migration above applies only to records written before this release.
// Writing the profile claims that slot, so a visitor who deliberately picks
// fixed 14px afterwards stores the same shape and must keep it on reload.
function defaultMigrationDone() {
  try { return localStorage.getItem(DISPLAY_MIGRATION_KEY) !== null } catch { return true }
}

function markDefaultMigrationDone() {
  try { localStorage.setItem(DISPLAY_MIGRATION_KEY, '1') } catch { /* storage may be unavailable */ }
}

export function readDisplayProfiles(): DisplayProfiles {
  try {
    const current = localStorage.getItem(DISPLAY_STORAGE_KEY)
    const previous = localStorage.getItem(DISPLAY_STORAGE_V2)
    const stored = JSON.parse(current || previous || localStorage.getItem(DISPLAY_STORAGE_V1) || '{}')
    if (!current) {
      // v1 persisted the old mobile default even when it was never selected.
      // Migrate existing visitors to reflow while retaining their chosen font.
      if (!previous && stored?.mobile) stored.mobile = { ...stored.mobile, mode: 'responsive', zoom: 100 }
      // Old v1/v2 cannot tell "never changed" from "explicitly chose fit 14/100".
      if (isLegacyDesktopDefault(stored?.desktop)) stored.desktop = { ...DEFAULT_DISPLAY.desktop }
    } else if (!defaultMigrationDone() && isPreviousDesktopDefault(stored?.desktop)) {
      // Keep the zoom the visitor chose along with the migrated default mode.
      stored.desktop = { ...DEFAULT_DISPLAY.desktop, zoom: bounded(stored.desktop.zoom, DEFAULT_DISPLAY.desktop.zoom, 50, 200) }
    }
    const profile = (key: keyof DisplayProfiles): TerminalDisplay => {
      const value = stored?.[key]
      const fallback = DEFAULT_DISPLAY[key]
      return {
        fontSize: bounded(value?.fontSize, fallback.fontSize, 10, 28),
        zoom: bounded(value?.zoom, fallback.zoom, 50, 200),
        mode: ['auto', 'fit', 'fixed', 'responsive'].includes(value?.mode) ? value.mode : fallback.mode,
      }
    }
    return { desktop: profile('desktop'), mobile: profile('mobile') }
  } catch { return { desktop: { ...DEFAULT_DISPLAY.desktop }, mobile: { ...DEFAULT_DISPLAY.mobile } } }
}

export function useTerminalDisplay(mobile: boolean) {
  const [profiles, setProfiles] = useState(readDisplayProfiles)
  const key = mobile ? 'mobile' : 'desktop'
  useEffect(() => {
    try { localStorage.setItem(DISPLAY_STORAGE_KEY, JSON.stringify(profiles)) } catch { /* Private storage may be unavailable; keep this visit usable. */ }
    markDefaultMigrationDone()
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
  const [offsetTop, setOffsetTop] = useState(0)
  useEffect(() => {
    const media = window.matchMedia(COMPACT_WORKBENCH_QUERY)
    const apply = (nextHeight: number, nextOffset: number) => {
      document.documentElement.style.setProperty('--workbench-height', `${nextHeight}px`)
      setHeight(nextHeight)
      setOffsetTop(nextOffset)
    }
    const update = () => {
      setMobile(media.matches)
      const viewport = window.visualViewport
      // A software keyboard reduces visualViewport without resizing the page.
      // Native pinch zoom must remain a browser operation without reflow.
      if (!viewport) {
        apply(window.innerHeight, 0)
        return
      }
      if (viewport.scale !== 1) {
        apply(window.innerHeight, 0)
        return
      }
      window.scrollTo(0, 0)
      apply(viewport.height, viewport.offsetTop || 0)
    }
    update()
    media.addEventListener('change', update)
    window.addEventListener('resize', update)
    window.visualViewport?.addEventListener('resize', update)
    window.visualViewport?.addEventListener('scroll', update)
    return () => {
      media.removeEventListener('change', update)
      window.removeEventListener('resize', update)
      window.visualViewport?.removeEventListener('resize', update)
      window.visualViewport?.removeEventListener('scroll', update)
      document.documentElement.style.removeProperty('--workbench-height')
    }
  }, [])
  return { mobile, height, offsetTop }
}
