import { useEffect, useRef, useState } from 'react'
import type { Snapshot } from '../types'

/**
 * 工作台深链 `/h/<host>?pane=<pane>`：推送通知和 Agent 总览用它直接打开某个 pane。
 * 参数只用于这一次聚焦，处理完就从地址栏去掉，刷新或复制链接不会反复跳转。
 */
export const PANE_PARAM_LIMIT = 256

export function workbenchPaneURL(hostID: string, paneID: string) {
  return `/h/${encodeURIComponent(hostID)}?pane=${encodeURIComponent(paneID)}`
}

export function readPaneParam(search: string) {
  const pane = new URLSearchParams(search).get('pane') || ''
  return pane.length <= PANE_PARAM_LIMIT ? pane : ''
}

export function stripPaneParam() {
  const url = new URL(window.location.href)
  if (!url.searchParams.has('pane')) return
  url.searchParams.delete('pane')
  window.history.replaceState(window.history.state, '', url.pathname + url.search + url.hash)
}

export type PaneTarget = { workspaceID: string; tabID: string; paneID: string }

export function findPaneTarget(snapshot: Snapshot, paneID: string): PaneTarget | null {
  const pane = snapshot.panes.find((item) => item.pane_id === paneID)
  return pane ? { workspaceID: pane.workspace_id, tabID: pane.tab_id, paneID } : null
}

/** Service Worker 在已打开的同主机窗口上发的聚焦请求；不导航，保护未提交的输入。 */
export function focusPaneMessage(data: unknown) {
  if (!data || typeof data !== 'object') return ''
  const { type, pane } = data as { type?: unknown; pane?: unknown }
  return type === 'FOCUS_PANE' && typeof pane === 'string' && pane.length <= PANE_PARAM_LIMIT ? pane : ''
}

/**
 * 等第一份快照到达后聚焦深链或通知点名的 pane；pane 已不存在时调用 onMissing。
 * 来源有三个：进入页面时的 `?pane=`、同页内的前进后退，以及 Service Worker 的 FOCUS_PANE。
 */
export function usePaneDeepLink(snapshot: Snapshot | null, onFocus: (target: PaneTarget) => void, onMissing: () => void) {
  // 用对象包一层：同一个 pane 连续点两次通知也要再聚焦一次。
  const [request, setRequest] = useState<{ pane: string } | null>(() => {
    const pane = readPaneParam(window.location.search)
    return pane ? { pane } : null
  })
  const handlers = useRef({ onFocus, onMissing })
  handlers.current = { onFocus, onMissing }
  useEffect(() => {
    const fromURL = () => {
      const pane = readPaneParam(window.location.search)
      if (pane) setRequest({ pane })
    }
    const fromWorker = (event: MessageEvent) => {
      const pane = focusPaneMessage(event.data)
      if (pane) setRequest({ pane })
    }
    const worker = 'serviceWorker' in navigator ? navigator.serviceWorker : null
    window.addEventListener('popstate', fromURL)
    worker?.addEventListener('message', fromWorker)
    return () => {
      window.removeEventListener('popstate', fromURL)
      worker?.removeEventListener('message', fromWorker)
    }
  }, [])
  useEffect(() => {
    if (!request || !snapshot) return
    const target = findPaneTarget(snapshot, request.pane)
    if (target) handlers.current.onFocus(target)
    else handlers.current.onMissing()
    setRequest(null)
    stripPaneParam()
  }, [request, snapshot])
}
