import { useCallback, useEffect, useRef } from 'react'
import { api, currentSessionID } from './api'
import { cacheWorkbenchSession, peekWorkbenchSession, workbenchSessionPayload, type WorkbenchLocation, type WorkbenchSession } from './workbenchSession'

// One page writer spans workbench mounts; the server rejects its old sequence
// numbers even if an aborted request reaches the database after a newer one.
const writerID = globalThis.crypto?.randomUUID?.() || `page-${Date.now().toString(16)}-${Math.random().toString(16).slice(2)}`
let sequence = 0
type PositionWrite = { payload: WorkbenchSession & { writer_id: string; sequence: number }; sessionID: string }
let pendingWrite: PositionWrite | null = null
let activeWrite: Promise<void> | null = null
let activeSessionID = ''
let exitWrite: Promise<void> | null = null
let exitSessionID = ''

function withDeadline<T>(promise: Promise<T>, milliseconds: number): Promise<T> {
  return new Promise((resolve, reject) => {
    const timer = window.setTimeout(() => reject(new Error('工作台位置请求超时')), milliseconds)
    promise.then(resolve, reject).finally(() => window.clearTimeout(timer))
  })
}

export function waitForWorkbenchSessionWrites() {
  return withDeadline(Promise.all([activeWrite, exitWrite]), 1000).then(() => {}, () => {})
}

function hasCurrentSessionWrites() {
  const sessionID = currentSessionID()
  return (activeWrite && activeSessionID === sessionID) || (exitWrite && exitSessionID === sessionID)
}

export async function readWorkbenchSession() {
  if (hasCurrentSessionWrites()) {
    await waitForWorkbenchSessionWrites()
    // A slow save must neither block terminal access nor replace our pending
    // local position with the older server copy.
    if (hasCurrentSessionWrites()) return { session: peekWorkbenchSession() ?? null }
  }
  return api.workbenchSession()
}

async function sendPosition(next: PositionWrite) {
  if (currentSessionID() !== next.sessionID) return
  try {
    await withDeadline(api.saveWorkbenchSession(next.payload), 5000)
  } catch {
    console.warn('无法保存工作台位置；下次打开时可能恢复到先前位置。')
  }
}

function startWrites() {
  if (activeWrite || !pendingWrite) return
  activeWrite = (async () => {
    while (pendingWrite) {
      const next = pendingWrite
      pendingWrite = null
      activeSessionID = next.sessionID
      await sendPosition(next)
    }
  })().finally(() => { activeWrite = null; activeSessionID = ''; startWrites() })
}

function saveWorkbenchSession(payload: WorkbenchSession, sessionID: string, exiting: boolean) {
  if (!sessionID || currentSessionID() !== sessionID) return
  cacheWorkbenchSession(payload)
  pendingWrite = { payload: { ...payload, writer_id: writerID, sequence: ++sequence }, sessionID }
  if (exiting) {
    // Start keepalive fetch inside the exit event: a queued JS continuation may
    // never run after the browser closes. Server sequence checks make this safe
    // even while the previous request is still in flight.
    const next = pendingWrite
    pendingWrite = null
    exitSessionID = sessionID
    const sending = sendPosition(next).finally(() => {
      if (exitWrite === sending) { exitWrite = null; exitSessionID = '' }
    })
    exitWrite = sending
    return
  }
  startWrites()
}

export function useWorkbenchSessionPersistence(location: WorkbenchLocation | null, mobile: boolean) {
  const pending = useRef<{ payload: WorkbenchSession; sessionID: string } | null>(null)
  const flush = useCallback((exiting = false) => {
    const next = pending.current
    pending.current = null
    if (next) saveWorkbenchSession(next.payload, next.sessionID, exiting)
  }, [])
  const hostID = location?.host_id
  const workspaceID = location?.workspace_id
  const tabID = location?.tab_id
  const paneID = location?.pane_id

  useEffect(() => {
    if (!hostID || !workspaceID) return
    pending.current = {
      payload: workbenchSessionPayload({ host_id: hostID, workspace_id: workspaceID, tab_id: tabID, pane_id: paneID }, mobile),
      sessionID: currentSessionID(),
    }
    const timer = window.setTimeout(() => flush(), 300)
    return () => window.clearTimeout(timer)
  }, [hostID, workspaceID, tabID, paneID, mobile, flush])

  useEffect(() => {
    const onExit = () => flush(true)
    const onHidden = () => { if (document.visibilityState === 'hidden') onExit() }
    window.addEventListener('pagehide', onExit)
    document.addEventListener('visibilitychange', onHidden)
    return () => {
      window.removeEventListener('pagehide', onExit)
      document.removeEventListener('visibilitychange', onHidden)
      onExit()
    }
  }, [flush])
}
