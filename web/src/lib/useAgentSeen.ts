import { useEffect, useRef, useState } from 'react'
import { api } from './api'

// Herdr's snapshot carries state_change_seq on each agent; it is omitted when 0.
type SeenAgent = { pane_id: string; state_change_seq?: number }

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
  const targets = (agents || []).filter((agent) => onScreen.has(agent.pane_id)).map((agent) => `${agent.pane_id}\u0000${agent.state_change_seq || 0}`)
  // A stable string key keeps snapshot updates that change nothing relevant
  // from restarting the debounce.
  const key = targets.sort().join('\u0001')
  useEffect(() => {
    if (!visible || !key) return
    const timer = window.setTimeout(() => {
      if (document.visibilityState !== 'visible') return
      for (const target of key.split('\u0001')) {
        const [paneID, rawSeq] = target.split('\u0000')
        const seq = Number(rawSeq)
        const sentKey = `${hostID}\u0000${paneID}`
        if ((sent.current.get(sentKey) ?? -1) >= seq) continue
        sent.current.set(sentKey, seq)
        // Best effort: a failed mark is retried the next time the pane is seen.
        Promise.resolve().then(() => api.markAgentSeen(hostID, paneID, seq)).catch(() => {
          if (sent.current.get(sentKey) === seq) sent.current.delete(sentKey)
        })
      }
    }, SEEN_DEBOUNCE_MS)
    return () => window.clearTimeout(timer)
  }, [hostID, key, visible])
}
