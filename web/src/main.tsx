import { Component, StrictMode, type ErrorInfo, type ReactNode } from 'react'
import { createRoot } from 'react-dom/client'
import '@xterm/xterm/css/xterm.css'
import './styles.css'
import './ui-theme.css'
import App from './App'
import { Tooltips } from './components/Tooltips'
import './controls.css'
import { AuthProvider } from './auth'
import { initializeAppearance } from './lib/appearance'
import { preloadWorkbenchChunk } from './lib/preload'
import { startPWA } from './lib/pwa'
import { PWAStatus } from './components/PWA'
import './pwa.css'

window.__herdrxBooted = true
preloadWorkbenchChunk()
initializeAppearance()
startPWA()

class ErrorBoundary extends Component<{ children: ReactNode }, { error: Error | null }> {
  state = { error: null as Error | null }
  static getDerivedStateFromError(error: Error) { return { error } }
  componentDidCatch(error: Error, info: ErrorInfo) { console.error('herdrx render failed', error, info.componentStack) }
  render() {
    if (!this.state.error) return this.props.children
    return <main className="boot-error"><section><h1>页面加载失败</h1><p>{this.state.error.message}</p><button onClick={() => void clearBrowserCache()}>清理缓存并重新加载</button></section></main>
  }
}

async function clearBrowserCache() {
  const registrations = await navigator.serviceWorker?.getRegistrations?.() || []
  await Promise.all(registrations.filter((registration) => registration.scope === `${location.origin}/`).map((registration) => registration.unregister()))
  if ('caches' in window) await Promise.all((await caches.keys()).filter((key) => /^herdrx-(shell|assets)-/.test(key)).map((key) => caches.delete(key)))
  window.location.reload()
}

createRoot(document.getElementById('root')!).render(<StrictMode><ErrorBoundary><AuthProvider><App /><Tooltips /><PWAStatus /></AuthProvider></ErrorBoundary></StrictMode>)
