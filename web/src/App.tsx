import { BrandIcon } from './components/Brand'
import { lazy, Suspense, useEffect, useLayoutEffect, useState } from 'react'
import { LoaderCircle } from 'lucide-react'
import { useAuth } from './auth'
import { AuthPage } from './pages/AuthPage'
import { HostsPage } from './pages/HostsPage'
import { PairPage } from './pages/PairPage'
import { Button } from './components/ui'
import { setAppearanceScope } from './lib/appearance'
import { usePWA } from './lib/pwa'

const WorkbenchPage = lazy(() => import('./pages/WorkbenchPage').then((module) => ({ default: module.WorkbenchPage })))
const AdminPage = lazy(() => import('./pages/AdminPage').then((module) => ({ default: module.AdminPage })))
const KeysPage = lazy(() => import('./pages/KeysPage').then((module) => ({ default: module.KeysPage })))

function routePath() {
  return window.location.hash.startsWith('#pair=') ? '/pair' : window.location.pathname
}

export default function App() {
  const auth = useAuth()
  const pwa = usePWA()
  const [path, setPath] = useState(routePath)
  useEffect(() => {
    const update = () => setPath(routePath())
    window.addEventListener('popstate', update)
    return () => window.removeEventListener('popstate', update)
  }, [])
  const appearanceScope = auth.user && !auth.bootstrapRequired && !window.location.hash.startsWith('#pair=') && /^\/h\/[^/]+$/.test(path) ? 'workbench' : 'site'
  useLayoutEffect(() => { if (!auth.loading) setAppearanceScope(appearanceScope) }, [auth.loading, appearanceScope])

  if (auth.loading) return <main className="loading-screen"><BrandIcon/><LoaderCircle className="spin"/><span>正在打开 herdrx…</span></main>
  if (auth.error && !auth.user) return <main className="auth-shell"><section className="auth-card"><h1>{pwa.online ? '无法读取登录状态' : '当前处于离线状态'}</h1><p role="alert">{pwa.online ? auth.error : '远程 Herdr 和任务仍独立运行。恢复网络后将重新验证登录并连接原工作台。'}</p><Button className="button-primary" onClick={() => void auth.refresh()}>重新连接</Button></section></main>
  if (auth.bootstrapRequired) return <AuthPage bootstrap />
  if (!auth.user) return <AuthPage bootstrap={false} />
  const authKey = `${auth.user.id}:${auth.sessionID}`
  if (path === '/keys') return <Suspense fallback={<main className="loading-screen">正在加载密钥…</main>}><KeysPage key={authKey}/></Suspense>
  if (path === '/admin') {
    if (auth.user.role !== 'admin') return <main className="auth-shell"><section className="auth-card"><h1>需要管理员权限</h1><p>当前账号无法访问管理页面。</p><a href="/">返回工作台</a></section></main>
    return <Suspense fallback={<main className="loading-screen">正在加载管理页面…</main>}><AdminPage key={authKey} /></Suspense>
  }
  if (window.location.hash.startsWith('#pair=') || path === '/pair') return <PairPage key={authKey} />
  const match = path.match(/^\/h\/([^/]+)$/)
  if (match) return <Suspense fallback={<main className="loading-screen"><LoaderCircle className="spin"/><span>加载终端工作台…</span></main>}><WorkbenchPage key={`${authKey}:${match[1]}`} hostID={decodeURIComponent(match[1])} /></Suspense>
  return <HostsPage key={authKey} />
}
