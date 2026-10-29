import { useCallback, useEffect, useRef } from 'react'

// useVisiblePoll runs load now, every intervalMs while the page is visible,
// and again as soon as a hidden page becomes visible. At most one load runs at
// a time; unmounting aborts it. The returned function triggers a load by hand.
export function useVisiblePoll(load: (signal: AbortSignal) => Promise<void>, intervalMs: number) {
  const latest = useRef(load)
  latest.current = load
  const inFlight = useRef<AbortController | null>(null)
  const run = useCallback(async () => {
    if (inFlight.current) return
    const controller = new AbortController()
    inFlight.current = controller
    try { await latest.current(controller.signal) }
    finally { if (inFlight.current === controller) inFlight.current = null }
  }, [])
  useEffect(() => {
    const refresh = () => { if (document.visibilityState === 'visible') void run() }
    refresh()
    const timer = window.setInterval(refresh, intervalMs)
    document.addEventListener('visibilitychange', refresh)
    return () => {
      window.clearInterval(timer)
      document.removeEventListener('visibilitychange', refresh)
      inFlight.current?.abort()
      inFlight.current = null
    }
  }, [run, intervalMs])
  return run
}
