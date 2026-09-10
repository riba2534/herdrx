import { useSyncExternalStore } from 'react'

type PWAState = { online: boolean; updateReady: boolean; error: string }
let state: PWAState = { online: navigator.onLine, updateReady: false, error: '' }
let registration: ServiceWorkerRegistration | undefined
let reloadRequested = false
let started = false
const listeners = new Set<() => void>()
const subscribe = (listener: () => void) => { listeners.add(listener); return () => { listeners.delete(listener) } }
const snapshot = () => state
function update(next: Partial<PWAState>) { state = { ...state, ...next }; listeners.forEach((listener) => listener()) }
export function usePWA() { return useSyncExternalStore(subscribe, snapshot) }

export function isStandalone() {
  const nav = navigator as Navigator & { standalone?: boolean }
  return window.matchMedia('(display-mode: standalone)').matches || nav.standalone === true
}

export function needsHomeScreenForNotifications() {
  const ios = /iPad|iPhone|iPod/.test(navigator.userAgent) || (navigator.platform === 'MacIntel' && navigator.maxTouchPoints > 1)
  return !('Notification' in window) && ios
}

export function startPWA() {
  if (started) return
  started = true
  const syncStandalone = () => document.documentElement.classList.toggle('pwa-standalone', isStandalone())
  syncStandalone()
  window.matchMedia('(display-mode: standalone)').addEventListener('change', syncStandalone)
  window.addEventListener('offline', () => update({ online: false }))
  window.addEventListener('online', () => { update({ online: true }); void checkForUpdate() })
  const syncOnline = () => { if (state.online !== navigator.onLine) update({ online: navigator.onLine }) }
  window.addEventListener('focus', syncOnline)
  document.addEventListener('visibilitychange', syncOnline)
  if (!import.meta.env.PROD || !window.isSecureContext || !('serviceWorker' in navigator)) return
  let controller = navigator.serviceWorker.controller
  navigator.serviceWorker.addEventListener('controllerchange', () => {
    const next = navigator.serviceWorker.controller
    if (reloadRequested && next) { window.location.reload(); return }
    if (controller && next && next !== controller) update({ updateReady: true })
    controller = next
  })
  const register = async () => {
    try {
      registration = await navigator.serviceWorker.register('/sw.js', { updateViaCache: 'none' })
      const waiting = () => {
        const candidate = registration?.waiting
        const current = navigator.serviceWorker.controller
        if (candidate?.state === 'installed' && current && candidate !== current) update({ updateReady: true })
      }
      waiting()
      registration.addEventListener('updatefound', () => registration?.installing?.addEventListener('statechange', waiting))
      await checkForUpdate()
    } catch { update({ error: '暂时无法准备离线页面，可联网使用并稍后重试。' }) }
  }
  if (document.readyState === 'complete') void register()
  else window.addEventListener('load', () => void register(), { once: true })
  let lastCheck = Date.now()
  document.addEventListener('visibilitychange', () => {
    if (!document.hidden && Date.now() - lastCheck > 60_000) { lastCheck = Date.now(); void checkForUpdate() }
  })
}

export async function checkForUpdate() {
  if (!registration || !navigator.onLine) return
  try { await registration.update(); update({ error: '' }) }
  catch { update({ error: '暂时无法检查更新，请恢复网络后重试。' }) }
}

export function applyUpdate() {
  if (!state.updateReady || reloadRequested) return
  reloadRequested = true
  if (registration?.waiting) registration.waiting.postMessage({ type: 'ACTIVATE_UPDATE' })
  else window.location.reload()
}
