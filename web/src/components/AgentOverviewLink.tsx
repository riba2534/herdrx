import { useState, type MouseEvent as ReactMouseEvent } from 'react'
import { Bot } from 'lucide-react'
import { api } from '../lib/api'
import { navigate } from '../lib/navigation'
import { useVisiblePoll } from '../lib/useVisiblePoll'
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

// followLink keeps modified clicks (new tab, new window) native and routes a
// plain click through the single-page navigation.
export function followLink(url: string) {
  return (event: ReactMouseEvent<HTMLAnchorElement>) => {
    if (event.button !== 0 || event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) return
    event.preventDefault()
    navigate(url)
  }
}

// useAgentAttentionCount polls the overview at a low rate while the page is
// visible, for entry points that only need the "needs you" number.
function useAgentAttentionCount() {
  const [count, setCount] = useState(0)
  useVisiblePoll(async (signal) => {
    try {
      const overview = await api.agents({ signal })
      if (signal.aborted) return
      setCount(overview.attention_count)
      syncAppBadge(overview.attention_count)
    } catch {
      // Keep the last count; the overview page shows the reason.
    }
  }, ENTRY_REFRESH_MS)
  return count
}

export function AgentOverviewLink({ className = '', compact = false }: { className?: string; compact?: boolean }) {
  const count = useAgentAttentionCount()
  const label = count > 0 ? `Agent 总览，${count} 个需要你处理` : 'Agent 总览'
  return <a href="/agents" className={`agent-overview-link${compact ? ' agent-overview-compact' : ''} ${className}`} aria-label={label} data-tooltip={label} onClick={followLink('/agents')}>
    <Bot size={compact ? 18 : 16} aria-hidden="true"/>
    {!compact && <span className="nav-action-label">Agent 总览</span>}
    {count > 0 && <span className="agent-overview-count" aria-hidden="true">{count > 99 ? '99+' : count}</span>}
  </a>
}
