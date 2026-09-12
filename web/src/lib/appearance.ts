import { useSyncExternalStore } from 'react'

export type Appearance = 'dark' | 'light' | 'solarized-light'
export type AppearanceScope = 'site' | 'workbench'
const keys: Record<AppearanceScope, string> = { site: 'herdrx.site-appearance.v1', workbench: 'herdrx.workbench-appearance.v1' }
const changed = 'herdrx:appearance'
const defaults: Record<AppearanceScope, Appearance> = { site: 'light', workbench: 'dark' }
const fallback = { ...defaults }
let activeScope: AppearanceScope = 'site'

export function readAppearance(scope: AppearanceScope = activeScope): Appearance {
  try {
    const value = localStorage.getItem(keys[scope])
    return value === 'light' || value === 'dark' || (scope === 'workbench' && value === 'solarized-light') ? value : fallback[scope]
  } catch { return fallback[scope] }
}

function applyAppearance() {
  const appearance = readAppearance()
  const dark = appearance === 'dark'
  const background = dark ? '#142c3c' : appearance === 'solarized-light' ? '#fdf6e3' : '#f7f8fa'
  document.documentElement.dataset.appearanceScope = activeScope
  document.documentElement.dataset.appearance = appearance
  document.documentElement.style.colorScheme = dark ? 'dark' : 'light'
  document.documentElement.style.backgroundColor = background
  document.querySelector('meta[name="theme-color"]')?.setAttribute('content', background)
}

export function setAppearance(value: Appearance, scope: AppearanceScope = activeScope) {
  fallback[scope] = value
  try { localStorage.setItem(keys[scope], value) } catch { /* Keep this visit usable when storage is unavailable. */ }
  applyAppearance()
  window.dispatchEvent(new Event(changed))
}

export function setAppearanceScope(scope: AppearanceScope) {
  activeScope = scope
  applyAppearance()
  window.dispatchEvent(new Event(changed))
}

export function initializeAppearance() {
  setAppearanceScope(/^\/h\/[^/]+$/.test(window.location.pathname) && !window.location.hash.startsWith('#pair=') ? 'workbench' : 'site')
  const sync = (event: StorageEvent) => {
    if (Object.values(keys).includes(event.key || '') || event.key === null) {
      for (const scope of ['site', 'workbench'] as const) if (event.key === null || event.key === keys[scope]) fallback[scope] = defaults[scope]
      applyAppearance()
      window.dispatchEvent(new Event(changed))
    }
  }
  window.addEventListener('storage', sync)
  return () => window.removeEventListener('storage', sync)
}

function subscribe(listener: () => void) {
  window.addEventListener(changed, listener)
  return () => window.removeEventListener(changed, listener)
}

export function useAppearance(scope: AppearanceScope = 'site') {
  return [useSyncExternalStore(subscribe, () => readAppearance(scope), () => defaults[scope]), (value: Appearance) => setAppearance(value, scope)] as const
}
