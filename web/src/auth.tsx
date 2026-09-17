import { createContext, useCallback, useContext, useEffect, useMemo, useRef, useState, type ReactNode } from 'react'
import { APIError, api, authenticationGeneration, currentSessionID, invalidateAuthentication, onAuthEvent } from './lib/api'
import type { User } from './types'

type AuthState = {
  loading: boolean
  bootstrapRequired: boolean
  registration: 'invite' | 'closed'
  user: User | null
  sessionID: string
  error: string
  notice: string
  refresh: () => Promise<void>
  setAuthenticated: (user: User) => void
  signOut: () => Promise<void>
}
const AuthContext = createContext<AuthState | null>(null)

/** Bound login checks even when a dropped connection leaves fetch pending. */
export const AUTH_CHECK_TIMEOUT_MS = 8_000

function withTimeout<T>(promise: Promise<T>, controller: AbortController): Promise<T> {
  return new Promise((resolve, reject) => {
    const timer = window.setTimeout(() => controller.abort(new Error('无法连接工作台，请重试')), AUTH_CHECK_TIMEOUT_MS)
    const cleanup = () => { window.clearTimeout(timer); controller.signal.removeEventListener('abort', aborted) }
    const aborted = () => { cleanup(); reject(controller.signal.reason) }
    controller.signal.addEventListener('abort', aborted, { once: true })
    if (controller.signal.aborted) aborted()
    promise.then(
      (value) => { cleanup(); resolve(value) },
      (reason) => { cleanup(); reject(reason) },
    )
  })
}

export function AuthProvider({ children }: { children: ReactNode }) {
  const [loading, setLoading] = useState(true)
  const [bootstrapRequired, setBootstrapRequired] = useState(false)
  const [registration, setRegistration] = useState<'invite' | 'closed'>('closed')
  const [user, setUser] = useState<User | null>(null)
  const [sessionID, setSessionID] = useState('')
  const [error, setError] = useState('')
  const [notice, setNotice] = useState('')
  const refreshSequence = useRef(0)
  const activeCheck = useRef<AbortController | null>(null)
  const channel = useRef<BroadcastChannel | null>(null)

  const checkAuthentication = useCallback(async (restart = false) => {
    if (activeCheck.current && !restart) return
    const sequence = ++refreshSequence.current
    activeCheck.current?.abort()
    const controller = new AbortController()
    activeCheck.current = controller
    const signal = controller.signal
    const epoch = authenticationGeneration()
    try {
      await withTimeout((async () => {
        const bootstrap = await api.bootstrapStatus({ signal })
        if (sequence !== refreshSequence.current || epoch !== authenticationGeneration()) return
        setBootstrapRequired(bootstrap.required)
        setRegistration(bootstrap.registration === 'invite' ? 'invite' : 'closed')
        if (bootstrap.required) { if (currentSessionID()) invalidateAuthentication(); setUser(null); setSessionID(''); setError('') }
        else {
          const result = await api.me({ signal })
          if (sequence !== refreshSequence.current || currentSessionID() !== result.session_id) return
          setUser(result.user)
          setSessionID(result.session_id)
          setError('')
        }
      })(), controller)
    } catch (reason) {
      if (sequence !== refreshSequence.current) return
      if (reason instanceof APIError && reason.status === 401) { setUser(null); setSessionID(''); setError('') }
      else if (!(reason instanceof APIError && reason.code === 'auth_changed')) {
        const aborted = reason instanceof DOMException && reason.name === 'AbortError'
        setError(aborted || !(reason instanceof Error) ? '无法连接工作台，请重试' : reason.message)
      }
    } finally {
      if (activeCheck.current === controller) activeCheck.current = null
      if (sequence === refreshSequence.current) setLoading(false)
    }
  }, [])
  // A user-requested reconnect replaces a stalled check; automatic events
  // still share the current request to avoid a focus/online request burst.
  const refresh = useCallback(() => checkAuthentication(true), [checkAuthentication])

  useEffect(() => {
    const off = onAuthEvent((event) => {
      if (event.kind === 'expired') {
        refreshSequence.current++
        activeCheck.current?.abort(); activeCheck.current = null
        setUser(null); setSessionID(''); setLoading(false); setError('')
        setNotice(event.hadSession ? '登录已失效，请重新登录。远程任务仍在运行。' : '')
        if (event.hadSession) channel.current?.postMessage('revalidate')
      } else {
        setUser(event.user); setSessionID(event.sessionID); setNotice('')
      }
    })
    if (typeof BroadcastChannel !== 'undefined') {
      const current = new BroadcastChannel('herdrx-auth')
      channel.current = current
      current.onmessage = (event) => { if (event.data === 'revalidate') void checkAuthentication() }
    }
    void checkAuthentication()
    const revalidate = () => { if (!document.hidden) void checkAuthentication() }
    window.addEventListener('focus', revalidate)
    window.addEventListener('online', revalidate)
    document.addEventListener('visibilitychange', revalidate)
    return () => { refreshSequence.current++; activeCheck.current?.abort(); activeCheck.current = null; off(); channel.current?.close(); channel.current = null; window.removeEventListener('focus', revalidate); window.removeEventListener('online', revalidate); document.removeEventListener('visibilitychange', revalidate) }
  }, [checkAuthentication])

  // Some suspended PWA/browser windows miss the online event. Recover from a
  // failed initial login check as well as from an explicit network transition.
  useEffect(() => {
    if (!error || user || loading) return
    const timer = window.setInterval(() => { if (navigator.onLine && !document.hidden) void checkAuthentication() }, 5000)
    return () => window.clearInterval(timer)
  }, [error, user, loading, checkAuthentication])

  const setAuthenticated = useCallback((nextUser: User) => {
    refreshSequence.current++
    activeCheck.current?.abort(); activeCheck.current = null
    setUser(nextUser); setSessionID(currentSessionID()); setBootstrapRequired(false)
    setLoading(false); setError(''); setNotice('')
    channel.current?.postMessage('revalidate')
  }, [])

  const signOut = useCallback(async () => {
    const epoch = authenticationGeneration()
    try { await api.logout() }
    catch (reason) { if (!(reason instanceof APIError && reason.status === 401)) throw reason }
    invalidateAuthentication(epoch)
    setNotice('已退出工作台。远程任务仍在运行。')
  }, [])

  const value = useMemo<AuthState>(() => ({ loading, bootstrapRequired, registration, user, sessionID, error, notice, refresh, setAuthenticated, signOut }),
    [loading, bootstrapRequired, registration, user, sessionID, error, notice, refresh, setAuthenticated, signOut])
  return <AuthContext.Provider value={value}>{children}</AuthContext.Provider>
}

export function useAuth() {
  const state = useContext(AuthContext)
  if (!state) throw new Error('AuthProvider is missing')
  return state
}
