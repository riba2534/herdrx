import { useEffect, useState } from 'react'
import { Bot } from 'lucide-react'
import { api } from '../lib/api'
import { navigate } from '../lib/navigation'
import './AgentOverviewLink.css'

const ENTRY_REFRESH_MS = 30_000

// syncAppBadge mirrors the exact attention count onto the installed app icon.
// Browsers without the Badging API simply skip it.
export function syncAppBadge(count: number) {
  const badging = navigator as Navigator & { setAppBadge?: (count?: number) => Promise<void>; clearAppBadge?: () => Promise<void> }
  if (!badging.setAppBadge) return
  const result = count > 0 ? badging.setAppBadge(count) : badging.clearAppBadge?.()
  void result?.catch(() => {})
}

// useAgentAttentionCount polls the overview at a low rate while the page is
// visible, for entry points that only need the "needs you" number.
function useAgentAttentionCount() {
  const [count, setCount] = useState(0)
  useEffect(() => {
    let cancelled = false
    let controller: AbortController | null = null
    const refresh = () => {
      if (document.visibilityState !== 'visible' || controller) return
      const current = new AbortController()
      controller = current
      // Any failure, even a synchronous one, only leaves the last count shown.
      Promise.resolve().then(() => api.agents({ signal: current.signal })).then((overview) => {
        if (cancelled) return
        setCount(overview.attention_count)
        syncAppBadge(overview.attention_count)
      }, () => {}).finally(() => { if (controller === current) controller = null })
    }
    refresh()
    const timer = window.setInterval(refresh, ENTRY_REFRESH_MS)
    document.addEventListener('visibilitychange', refresh)
    return () => {
      cancelled = true
      controller?.abort()
      window.clearInterval(timer)
      document.removeEventListener('visibilitychange', refresh)
    }
  }, [])
  return count
}

export function AgentOverviewLink({ className = '', compact = false }: { className?: string; compact?: boolean }) {
  const count = useAgentAttentionCount()
  const label = count > 0 ? `Agent 总览，${count} 个需要你处理` : 'Agent 总览'
  return <a href="/agents" className={`agent-overview-link${compact ? ' agent-overview-compact' : ''} ${className}`} aria-label={label} data-tooltip={label} onClick={(event) => {
    if (event.button !== 0 || event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) return
    event.preventDefault()
    navigate('/agents')
  }}>
    <Bot size={compact ? 18 : 16} aria-hidden="true"/>
    {!compact && <span className="nav-action-label">Agent 总览</span>}
    {count > 0 && <span className="agent-overview-count" aria-hidden="true">{count > 99 ? '99+' : count}</span>}
  </a>
}
