import { useEffect, useState } from 'react'

export type TerminalDisplay = { fontSize: number; zoom: number; mode: 'auto' | 'fit' | 'fixed' }
export type DisplayProfileKey = 'desktop' | 'mobile' | 'mobileLandscape'
type DisplayProfiles = Record<DisplayProfileKey, TerminalDisplay>
export const DISPLAY_STORAGE_KEY = 'herdrx.terminal-display.v3'
export const DISPLAY_MIGRATION_KEY = 'herdrx.terminal-display.default-migrated'
export const DISPLAY_MOBILE_MIGRATION_KEY = 'herdrx.terminal-display.mobile-default-migrated'
const DISPLAY_STORAGE_V2 = 'herdrx.terminal-display.v2'
const DISPLAY_STORAGE_V1 = 'herdrx.terminal-display.v1'
// Phones start in auto: the complete grid, else the full width with vertical
// panning, else a readable 13 px frame that pans both ways. Portrait and
// landscape are separate profiles because the same pane reads differently.
export const DEFAULT_DISPLAY: DisplayProfiles = {
  desktop: { fontSize: 14, zoom: 100, mode: 'auto' },
  mobile: { fontSize: 13, zoom: 100, mode: 'auto' },
  mobileLandscape: { fontSize: 13, zoom: 100, mode: 'auto' },
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

// Phones shipped fixed 14 px at 100% before the auto tiers existed. Like the
// desktop migration, an untouched default cannot be told apart from choosing
// the same values, so only that exact record moves, and only once.
function isPreviousMobileDefault(value: unknown) {
  if (!value || typeof value !== 'object') return false
  const item = value as { fontSize?: unknown; zoom?: unknown; mode?: unknown }
  return item.mode === 'fixed' && item.fontSize === 14 && item.zoom === 100
}

function mobileMigrationDone() {
  try { return localStorage.getItem(DISPLAY_MOBILE_MIGRATION_KEY) !== null } catch { return true }
}

function markMobileMigrationDone() {
  try { localStorage.setItem(DISPLAY_MOBILE_MIGRATION_KEY, '1') } catch { /* storage may be unavailable */ }
}

export function readDisplayProfiles(): DisplayProfiles {
  try {
    const current = localStorage.getItem(DISPLAY_STORAGE_KEY)
    const previous = localStorage.getItem(DISPLAY_STORAGE_V2)
    const stored = JSON.parse(current || previous || localStorage.getItem(DISPLAY_STORAGE_V1) || '{}')
    if (!current) {
      // Old v1/v2 cannot tell "never changed" from "explicitly chose fit 14/100".
      if (isLegacyDesktopDefault(stored?.desktop)) stored.desktop = { ...DEFAULT_DISPLAY.desktop }
    } else if (!defaultMigrationDone() && isPreviousDesktopDefault(stored?.desktop)) {
      // Keep the zoom the visitor chose along with the migrated default mode.
      stored.desktop = { ...DEFAULT_DISPLAY.desktop, zoom: bounded(stored.desktop.zoom, DEFAULT_DISPLAY.desktop.zoom, 50, 200) }
    }
    if (!mobileMigrationDone() && isPreviousMobileDefault(stored?.mobile)) stored.mobile = { ...DEFAULT_DISPLAY.mobile }
    const profile = (value: { fontSize?: unknown; zoom?: unknown; mode?: unknown } | undefined, fallback: TerminalDisplay): TerminalDisplay => ({
      fontSize: bounded(value?.fontSize, fallback.fontSize, 10, 28),
      zoom: bounded(value?.zoom, fallback.zoom, 50, 200),
      // Remote resize is a current-visit action, never a saved preference.
      // Restoring it on open would silently take geometry from native Herdr.
      mode: typeof value?.mode === 'string' && ['auto', 'fit', 'fixed'].includes(value.mode) ? value.mode as TerminalDisplay['mode'] : fallback.mode,
    })
    const mobile = profile(stored?.mobile, DEFAULT_DISPLAY.mobile)
    // Landscape starts from the phone's existing choice until it is changed there.
    return { desktop: profile(stored?.desktop, DEFAULT_DISPLAY.desktop), mobile, mobileLandscape: profile(stored?.mobileLandscape, mobile) }
  } catch { return { desktop: { ...DEFAULT_DISPLAY.desktop }, mobile: { ...DEFAULT_DISPLAY.mobile }, mobileLandscape: { ...DEFAULT_DISPLAY.mobileLandscape } } }
}

export function displayProfileKey(mobile: boolean, landscape = false): DisplayProfileKey {
  return mobile ? landscape ? 'mobileLandscape' : 'mobile' : 'desktop'
}

export function useTerminalDisplay(mobile: boolean, landscape = false) {
  const [profiles, setProfiles] = useState(readDisplayProfiles)
  const key = displayProfileKey(mobile, landscape)
  useEffect(() => {
    const saved = profiles
    try { localStorage.setItem(DISPLAY_STORAGE_KEY, JSON.stringify(saved)) } catch { /* Private storage may be unavailable; keep this visit usable. */ }
    markDefaultMigrationDone()
    markMobileMigrationDone()
  }, [profiles])
  const update = (patch: Partial<TerminalDisplay>) => setProfiles((current) => ({ ...current, [key]: { ...current[key], ...patch } }))
  return { display: profiles[key], profile: key, update, reset: () => update(DEFAULT_DISPLAY[key]) }
}

// Keep phones in the single-pane layout when rotated; larger tablets retain
// the desktop layout with touch-sized controls. Browser page zoom changes the
// layout viewport and therefore follows the same breakpoint.
export const COMPACT_WORKBENCH_QUERY = '(max-width: 767px), (pointer: coarse) and (max-width: 1023px) and (max-height: 600px)'

const EDITABLE_SELECTOR = 'textarea, select, [contenteditable]:not([contenteditable="false"]), input:not([type=button], [type=checkbox], [type=radio], [type=range], [type=submit], [type=reset], [type=file], [type=color])'
// A software keyboard takes far more than this; smaller changes are browser
// bars collapsing or an input method's candidate strip.
export const KEYBOARD_MIN_HEIGHT = 120
// After an input loses focus its keyboard still animates away; keep treating
// the shorter viewport as covered until it grows back or this much time passes.
export const KEYBOARD_SETTLE_MS = 1000

export function editableFocused(active: Element | null = document.activeElement) {
  return active instanceof HTMLElement && active.matches(EDITABLE_SELECTOR)
}

// Orientation follows the device, not the viewport: an Android keyboard with
// resizes-content can make the remaining page wider than tall while portrait.
export function deviceLandscape() {
  const type = window.screen?.orientation?.type
  if (type) return type.startsWith('landscape')
  const angle = (window as { orientation?: unknown }).orientation
  if (typeof angle === 'number') return Math.abs(angle) === 90
  return window.innerWidth > window.innerHeight
}

export function useWorkbenchViewport() {
  const [mobile, setMobile] = useState(() => window.matchMedia(COMPACT_WORKBENCH_QUERY).matches)
  const [coarse, setCoarse] = useState(() => window.matchMedia('(pointer: coarse)').matches)
  const [landscape, setLandscape] = useState(deviceLandscape)
  const [keyboardOpen, setKeyboardOpen] = useState(false)
  // Pixels of the visual viewport a software keyboard (or its candidate strip)
  // covers right now, including the steps of its open and close animations.
  const [keyboardInset, setKeyboardInset] = useState(0)
  const [height, setHeight] = useState(() => window.innerHeight)
  const [offsetTop, setOffsetTop] = useState(0)
  useEffect(() => {
    const media = window.matchMedia(COMPACT_WORKBENCH_QUERY)
    const pointer = window.matchMedia('(pointer: coarse)')
    // Height of this width before an input took focus; a keyboard only ever
    // shrinks the visual viewport below it while something is being edited.
    let baseline = { width: -1, height: 0 }
    let settleUntil = 0
    const apply = (nextHeight: number, nextOffset: number) => {
      document.documentElement.style.setProperty('--workbench-height', `${nextHeight}px`)
      setHeight(nextHeight)
      setOffsetTop(nextOffset)
    }
    const update = () => {
      setMobile(media.matches)
      setCoarse(pointer.matches)
      setLandscape(deviceLandscape())
      const viewport = window.visualViewport
      // Only touch screens have a software keyboard; a desktop window resized
      // while the terminal has focus must keep reflowing normally.
      const editing = pointer.matches && editableFocused()
      const settling = editing || performance.now() < settleUntil
      const visibleHeight = viewport ? viewport.height : window.innerHeight
      const width = Math.round(viewport ? viewport.width : window.innerWidth)
      if (width !== baseline.width) baseline = { width, height: visibleHeight }
      else if (!settling || visibleHeight > baseline.height) baseline.height = visibleHeight
      const zoomed = Boolean(viewport && Math.abs(viewport.scale - 1) > 0.01)
      const inset = settling && !zoomed ? Math.max(0, Math.round(baseline.height - visibleHeight)) : 0
      setKeyboardInset(inset)
      setKeyboardOpen(inset > KEYBOARD_MIN_HEIGHT)
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
    // Focus moves before the keyboard animates; re-check once it has settled.
    let focusTimer = 0
    let settleTimer = 0
    const focusChanged = (event: FocusEvent) => {
      if (event.type === 'focusout' && pointer.matches) {
        settleUntil = performance.now() + KEYBOARD_SETTLE_MS
        window.clearTimeout(settleTimer)
        settleTimer = window.setTimeout(update, KEYBOARD_SETTLE_MS + 20)
      }
      window.clearTimeout(focusTimer)
      focusTimer = window.setTimeout(update, 0)
    }
    update()
    media.addEventListener('change', update)
    pointer.addEventListener?.('change', update)
    window.addEventListener('resize', update)
    window.screen?.orientation?.addEventListener?.('change', update)
    window.visualViewport?.addEventListener('resize', update)
    window.visualViewport?.addEventListener('scroll', update)
    document.addEventListener('focusin', focusChanged)
    document.addEventListener('focusout', focusChanged)
    return () => {
      window.clearTimeout(focusTimer)
      window.clearTimeout(settleTimer)
      media.removeEventListener('change', update)
      pointer.removeEventListener?.('change', update)
      window.removeEventListener('resize', update)
      window.screen?.orientation?.removeEventListener?.('change', update)
      window.visualViewport?.removeEventListener('resize', update)
      window.visualViewport?.removeEventListener('scroll', update)
      document.removeEventListener('focusin', focusChanged)
      document.removeEventListener('focusout', focusChanged)
      document.documentElement.style.removeProperty('--workbench-height')
    }
  }, [])
  return { mobile, coarse, landscape, keyboardOpen, keyboardInset, height, offsetTop }
}
