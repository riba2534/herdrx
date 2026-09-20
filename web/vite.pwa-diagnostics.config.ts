// Explicit CI investigation build only. The normal Vite config never imports
// this file, so release assets contain none of these diagnostic hooks.
import { defineConfig } from 'vite'
import base from './vite.config'

const runtime = `
const __pwaTrace = (event, details = {}) => {
  try { globalThis.__herdrxAuthDiagnostics?.record(event, details) } catch {}
};
const __pwaID = (value) => {
  try { return globalThis.__herdrxAuthDiagnostics?.identity(value) ?? null } catch { return null }
};
const __pwaReason = (reason) => {
  try {
    return { type: typeof reason, string: String(reason).slice(0, 500),
      tag: Object.prototype.toString.call(reason), constructor: reason?.constructor?.name,
      name: reason?.name, message: typeof reason?.message === 'string' ? reason.message.slice(0, 500) : null,
      status: reason?.status, code: reason?.code,
      isError: reason instanceof Error, isDOMException: reason instanceof DOMException };
  } catch { return { type: typeof reason, unreadable: true } }
};
`

function replaceOnce(source: string, original: string, replacement: string) {
  const pieces = source.split(original)
  if (pieces.length !== 2) throw new Error(`PWA diagnostic hook no longer matches exactly once: ${original}`)
  return pieces[0] + replacement + pieces[1]
}

function instrumentAPI(source: string) {
  const replace = (original: string, replacement: string) => { source = replaceOnce(source, original, replacement) }
  replace('authListeners.add(listener)', `authListeners.add(listener)
  __pwaTrace('listener.subscribe', { listener: __pwaID(listener), count: authListeners.size })`)
  replace('return () => { authListeners.delete(listener) }', `return () => {
    authListeners.delete(listener)
    __pwaTrace('listener.unsubscribe', { listener: __pwaID(listener), count: authListeners.size })
  }`)
  replace('if (expected !== generation) return', `__pwaTrace('invalidate.begin', { expected, generation, listeners: authListeners.size })
  if (expected !== generation) { __pwaTrace('invalidate.stale'); return }`)
  replace("for (const listener of authListeners) listener({ kind: 'expired', hadSession })", `for (const listener of authListeners) {
    __pwaTrace('invalidate.listener.begin', { listener: __pwaID(listener), generation, hadSession })
    try {
      listener({ kind: 'expired', hadSession })
      __pwaTrace('invalidate.listener.end', { listener: __pwaID(listener) })
    } catch (reason) {
      __pwaTrace('invalidate.listener.throw', { listener: __pwaID(listener), reason: __pwaReason(reason) })
      throw reason
    }
  }
  __pwaTrace('invalidate.end', { generation })`)
  replace('const epoch = generation', `const epoch = generation
  const __requestID = __pwaID({})
  __pwaTrace('request.begin', { request: __requestID, path, epoch, signal: __pwaID(init.signal), aborted: init.signal?.aborted })`)
  replace("const response = await fetch(path, { ...init, headers, credentials: 'same-origin' })", `let response
  try {
    response = await fetch(path, { ...init, headers, credentials: 'same-origin' })
    __pwaTrace('fetch.resolved', { request: __requestID, path, status: response.status, ok: response.ok, type: response.type, aborted: init.signal?.aborted })
  } catch (reason) {
    __pwaTrace('fetch.rejected', { request: __requestID, path, aborted: init.signal?.aborted, reason: __pwaReason(reason) })
    throw reason
  }`)
  replace('const payload = await response.json().catch(() => ({}))', `__pwaTrace('json.begin', { request: __requestID, path, status: response.status })
  const payload = await response.json().catch((reason) => {
    __pwaTrace('json.rejected', { request: __requestID, path, reason: __pwaReason(reason) })
    return {}
  })
  __pwaTrace('json.finished', { request: __requestID, path, status: response.status, bodyUsed: response.bodyUsed, aborted: init.signal?.aborted })`)
  replace('init.signal?.throwIfAborted()', `__pwaTrace('signal.check.begin', { request: __requestID, signal: __pwaID(init.signal), aborted: init.signal?.aborted })
  try {
    init.signal?.throwIfAborted()
    __pwaTrace('signal.check.end', { request: __requestID })
  } catch (reason) {
    __pwaTrace('signal.check.throw', { request: __requestID, aborted: init.signal?.aborted, reason: __pwaReason(reason) })
    throw reason
  }`)
  replace('if (!response.ok) {', `if (!response.ok) {
    __pwaTrace('request.http-error', { request: __requestID, path, status: response.status, epoch, generation, publicAuth })`)
  replace('return payload as T', `__pwaTrace('request.success', { request: __requestID, path, epoch, generation })
  return payload as T`)
  return runtime + source
}

function instrumentAuth(source: string) {
  const replace = (original: string, replacement: string) => { source = replaceOnce(source, original, replacement) }
  replace("const timer = window.setTimeout(() => controller.abort(new Error('无法连接工作台，请重试')), AUTH_CHECK_TIMEOUT_MS)", `const timer = window.setTimeout(() => {
      __pwaTrace('timeout.fire', { controller: __pwaID(controller), signal: __pwaID(controller.signal) })
      controller.abort(new Error('无法连接工作台，请重试'))
    }, AUTH_CHECK_TIMEOUT_MS)`)
  replace('const aborted = () => { cleanup(); reject(controller.signal.reason) }', `const aborted = () => {
      __pwaTrace('timeout.abort', { controller: __pwaID(controller), reason: __pwaReason(controller.signal.reason) })
      cleanup(); reject(controller.signal.reason)
    }`)
  replace('if (activeCheck.current && !restart) return', `__pwaTrace('check.attempt', { restart, previous: refreshSequence.current, active: __pwaID(activeCheck.current) })
    if (activeCheck.current && !restart) { __pwaTrace('check.coalesced'); return }`)
  replace('const signal = controller.signal\n    const epoch = authenticationGeneration()', `const signal = controller.signal
    const epoch = authenticationGeneration()
    __pwaTrace('check.begin', { sequence, epoch, controller: __pwaID(controller), signal: __pwaID(signal) })`)
  replace('const bootstrap = await api.bootstrapStatus({ signal })', `const bootstrap = await api.bootstrapStatus({ signal })
        __pwaTrace('check.bootstrap', { sequence, current: refreshSequence.current, epoch, generation: authenticationGeneration(), required: bootstrap.required })`)
  replace('const result = await api.me({ signal })', `const result = await api.me({ signal })
          __pwaTrace('check.me', { sequence, current: refreshSequence.current, sessionMatches: currentSessionID() === result.session_id })`)
  replace('} catch (reason) {\n      if (sequence !== refreshSequence.current) return', `} catch (reason) {
      __pwaTrace('check.catch', { sequence, current: refreshSequence.current, reason: __pwaReason(reason), isAPI: reason instanceof APIError })
      if (sequence !== refreshSequence.current) return`)
  replace('if (activeCheck.current === controller) activeCheck.current = null', `__pwaTrace('check.finally', { sequence, current: refreshSequence.current, activeMatches: activeCheck.current === controller })
      if (activeCheck.current === controller) activeCheck.current = null`)
  replace("if (event.kind === 'expired') {", `__pwaTrace('auth.event', { kind: event.kind, sequence: refreshSequence.current, active: __pwaID(activeCheck.current) })
      if (event.kind === 'expired') {`)
  replace("setUser(null); setSessionID(''); setLoading(false); setError('')", `setUser(null); setSessionID(''); setLoading(false); setError('')
        __pwaTrace('auth.expired.state-queued', { sequence: refreshSequence.current })`)
  replace('const value = useMemo<AuthState>', `__pwaTrace('auth.render', { loading, bootstrapRequired, signedIn: Boolean(user), hasSession: Boolean(sessionID), error, sequence: refreshSequence.current })
  const value = useMemo<AuthState>`)
  return runtime + source
}

const transformed = new Set<string>()
export default defineConfig({
  ...base,
  plugins: [{
    name: 'pwa-auth-ci-diagnostics',
    enforce: 'pre',
    transform(source, id) {
      if (id.endsWith('/src/lib/api.ts')) {
        transformed.add('api')
        return instrumentAPI(source)
      }
      if (id.endsWith('/src/auth.tsx')) {
        transformed.add('auth')
        return instrumentAuth(source)
      }
    },
    generateBundle() {
      if (transformed.size !== 2) throw new Error('PWA diagnostic build must instrument both auth and API')
    },
  }, ...(base.plugins ?? [])],
})
