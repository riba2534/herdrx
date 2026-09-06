import { useSyncExternalStore } from 'react'

type InstallPrompt = Event & { prompt: () => Promise<{ outcome: 'accepted' | 'dismissed' }> }
type PWAState = { online: boolean; standalone: boolean; canInstall: boolean; updateReady: boolean; error: string }
let state: PWAState = { online: navigator.onLine, standalone: false, canInstall: false, updateReady: false, error: '' }
let installPrompt: InstallPrompt | null = null
let registration: ServiceWorkerRegistration | undefined
let reloadRequested = false
let started = false
const listeners = new Set<() => void>()
const subscribe = (listener: () => void) => { listeners.add(listener); return () => { listeners.delete(listener) } }
const snapshot = () => state
function update(next: Partial<PWAState>) { state = { ...state, ...next }; listeners.forEach((listener) => listener()) }
export function usePWA() { return useSyncExternalStore(subscribe, snapshot) }

export function startPWA() {
  if (started) return
  started = true
  const display = window.matchMedia('(display-mode: standalone)')
  const checkDisplay = () => update({ standalone: display.matches || Boolean((navigator as Navigator & { standalone?: boolean }).standalone) })
  checkDisplay()
  display.addEventListener('change', checkDisplay)
  window.addEventListener('beforeinstallprompt', (event) => { event.preventDefault(); installPrompt = event as InstallPrompt; update({ canInstall: true }) })
  window.addEventListener('appinstalled', () => { installPrompt = null; update({ canInstall: false }); checkDisplay() })
  window.addEventListener('offline', () => update({ online: false }))
  window.addEventListener('online', () => { update({ online: true }); void checkForUpdate() })
  const syncOnline = () => { if (state.online !== navigator.onLine) update({ online: navigator.onLine }) }
  window.addEventListener('focus', syncOnline)
  document.addEventListener('visibilitychange', syncOnline)
  if (!import.meta.env.PROD || !window.isSecureContext || !('serviceWorker' in navigator)) return
  let controlled = Boolean(navigator.serviceWorker.controller)
  navigator.serviceWorker.addEventListener('controllerchange', () => {
    if (reloadRequested) { window.location.reload(); return }
    if (controlled) update({ updateReady: true })
    controlled = true
  })
  const register = async () => {
    try {
      registration = await navigator.serviceWorker.register('/sw.js', { updateViaCache: 'none' })
      const waiting = () => { if (registration?.waiting && navigator.serviceWorker.controller) update({ updateReady: true }) }
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

export async function installApp() {
  const prompt = installPrompt
  if (!prompt) return
  installPrompt = null
  update({ canInstall: false })
  try { await prompt.prompt() }
  catch { update({ error: '安装未完成，请使用浏览器菜单安装。' }) }
}

export function applyUpdate() {
  if (!state.updateReady || reloadRequested) return
  reloadRequested = true
  if (registration?.waiting) registration.waiting.postMessage({ type: 'ACTIVATE_UPDATE' })
  else window.location.reload()
}
