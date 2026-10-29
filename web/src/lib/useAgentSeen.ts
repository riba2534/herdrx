import { useEffect, useRef, useState } from 'react'
import { api } from './api'
import type { Agent } from '../types'

type SeenAgent = Pick<Agent, 'pane_id' | 'state_change_seq'>

const SEEN_DEBOUNCE_MS = 1000

function useDocumentVisible() {
  const [visible, setVisible] = useState(() => document.visibilityState === 'visible')
  useEffect(() => {
    const update = () => setVisible(document.visibilityState === 'visible')
    document.addEventListener('visibilitychange', update)
    return () => document.removeEventListener('visibilitychange', update)
  }, [])
  return visible
}

// useAgentSeen records the Web read marker for agents whose pane is on screen
// while the page is visible. It only writes herdrx's own marker; it never calls
// Herdr, and nothing is sent when the page is hidden or being left.
export function useAgentSeen(hostID: string, visiblePaneIDs: readonly string[] | undefined, agents: readonly SeenAgent[] | undefined) {
  const visible = useDocumentVisible()
  const sent = useRef(new Map<string, number>())
  const onScreen = new Set(visiblePaneIDs)
  const targets = (agents || []).filter((agent) => onScreen.has(agent.pane_id)).map((agent) => ({ paneID: agent.pane_id, seq: agent.state_change_seq || 0 }))
  const latest = useRef(targets)
  latest.current = targets
  // Snapshots arrive often; restart the debounce only when a pane or round changes.
  const key = targets.map((target) => `${target.paneID}:${target.seq}`).sort().join(' ')
  useEffect(() => {
    if (!visible || !key) return
    const timer = window.setTimeout(() => {
      for (const { paneID, seq } of latest.current) {
        const sentKey = `${hostID}\u0000${paneID}`
        // Exact match: Herdr restarts its counter at 0, so a lower value is a new run.
        if (sent.current.get(sentKey) === seq) continue
        sent.current.set(sentKey, seq)
        // Best effort: a failed mark is retried the next time the pane is seen.
        void (async () => {
          try { await api.markAgentSeen(hostID, paneID, seq) }
          catch { if (sent.current.get(sentKey) === seq) sent.current.delete(sentKey) }
        })()
      }
    }, SEEN_DEBOUNCE_MS)
    return () => window.clearTimeout(timer)
  }, [hostID, key, visible])
}
