import { currentSessionID, onAuthEvent } from './api'

// pane 级视图模式（终端 / 对话）。键包含登录会话、主机和 pane，
// 保证同一浏览器的不同登录、不同主机、不同分屏互不影响。
const STORAGE_PREFIX = 'herdrx.pane-view.v1.'

export type PaneViewMode = 'terminal' | 'chat'

const modes = new Map<string, PaneViewMode>()
const listeners = new Set<() => void>()
let listening = false

function identityKey(sessionID: string, hostID: string, paneID: string) {
  return `${sessionID}\n${hostID}\n${paneID}`
}

function storageKey(sessionID: string, hostID: string, paneID: string) {
  return `${STORAGE_PREFIX}${encodeURIComponent(sessionID)}/${encodeURIComponent(hostID)}/${encodeURIComponent(paneID)}`
}

function notify() {
  for (const listener of listeners) listener()
}

function liveKey(hostID: string, paneID: string) {
  const sessionID = currentSessionID()
  if (!sessionID || !hostID || !paneID) return ''
  return identityKey(sessionID, hostID, paneID)
}

function ensureAuthListener() {
  if (listening) return
  listening = true
  onAuthEvent((event) => {
    if (event.kind === 'expired') clearPaneViewModes()
    else retainPaneViewModes(event.sessionID)
  })
}

function eachStorageKey(visit: (key: string) => void) {
  try {
    for (let index = localStorage.length - 1; index >= 0; index--) {
      const key = localStorage.key(index)
      if (key?.startsWith(STORAGE_PREFIX)) visit(key)
    }
  } catch {
    // 隐私模式下存储可能不可用，内存态仍然有效。
  }
}

export function subscribePaneViewModes(listener: () => void) {
  ensureAuthListener()
  listeners.add(listener)
  return () => { listeners.delete(listener) }
}

export function readPaneViewMode(hostID: string, paneID: string): PaneViewMode {
  ensureAuthListener()
  const key = liveKey(hostID, paneID)
  if (!key) return 'terminal'
  const cached = modes.get(key)
  if (cached) return cached
  const sessionID = currentSessionID()
  try {
    const stored = localStorage.getItem(storageKey(sessionID, hostID, paneID))
    const mode: PaneViewMode = stored === 'chat' ? 'chat' : 'terminal'
    modes.set(key, mode)
    return mode
  } catch {
    modes.set(key, 'terminal')
    return 'terminal'
  }
}

export function writePaneViewMode(hostID: string, paneID: string, mode: PaneViewMode) {
  ensureAuthListener()
  const sessionID = currentSessionID()
  const key = liveKey(hostID, paneID)
  if (!sessionID || !key) return
  modes.set(key, mode)
  try {
    const stored = storageKey(sessionID, hostID, paneID)
    if (mode === 'chat') localStorage.setItem(stored, 'chat')
    else localStorage.removeItem(stored)
  } catch {
    // 存储不可用时只保留内存态。
  }
  notify()
}

export function retainPaneViewModes(sessionID: string) {
  const keep = `${sessionID}\n`
  const prefix = `${STORAGE_PREFIX}${encodeURIComponent(sessionID)}/`
  for (const key of Array.from(modes.keys())) {
    if (!key.startsWith(keep)) modes.delete(key)
  }
  eachStorageKey((key) => {
    if (!key.startsWith(prefix)) {
      try { localStorage.removeItem(key) } catch { /* ignore */ }
    }
  })
  notify()
}

export function clearPaneViewModes() {
  modes.clear()
  eachStorageKey((key) => {
    try { localStorage.removeItem(key) } catch { /* ignore */ }
  })
  notify()
}
