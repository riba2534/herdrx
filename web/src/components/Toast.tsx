import { useEffect } from 'react'

export function Toast({ message, onClear }: { message: string; onClear?: () => void }) {
  useEffect(() => {
    if (!message || !onClear) return
    const timer = window.setTimeout(onClear, 6000)
    return () => window.clearTimeout(timer)
  }, [message, onClear])
  if (!message) return null
  return <div className="site-toast" role="status" aria-live="polite">{message}</div>
}
