import { currentSessionID, onAuthEvent } from './api'

const STORAGE_PREFIX = 'herdrx.composer.v1.'

export type ComposerSendStatus = 'idle' | 'sending' | 'delivered' | 'failed' | 'unknown'
export type ComposerSendState = { status: ComposerSendStatus; error: string; revision: number }

type DraftRecord = { text: string; revision: number }

const drafts = new Map<string, DraftRecord>()
const sends = new Map<string, ComposerSendState>()
const inflight = new Set<string>()
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
    if (event.kind === 'expired') clearComposerDrafts()
    else retainComposerDrafts(event.sessionID)
  })
}

function eachStorageKey(visit: (key: string) => void) {
  try {
    for (let index = localStorage.length - 1; index >= 0; index--) {
      const key = localStorage.key(index)
      if (key?.startsWith(STORAGE_PREFIX)) visit(key)
    }
  } catch {
    // Private storage may be unavailable; keep this visit usable.
  }
}

function persistDraft(key: string, sessionID: string, hostID: string, paneID: string, record: DraftRecord) {
  drafts.set(key, record)
  try {
    const stored = storageKey(sessionID, hostID, paneID)
    if (record.text) localStorage.setItem(stored, record.text)
    else localStorage.removeItem(stored)
  } catch {
    // Private storage may be unavailable; in-memory draft still lasts this visit.
  }
}

export function subscribeComposer(listener: () => void) {
  ensureAuthListener()
  listeners.add(listener)
  return () => { listeners.delete(listener) }
}

export function readComposerDraft(hostID: string, paneID: string) {
  ensureAuthListener()
  const key = liveKey(hostID, paneID)
  if (!key) return ''
  const cached = drafts.get(key)
  if (cached) return cached.text
  const sessionID = currentSessionID()
  try {
    const text = localStorage.getItem(storageKey(sessionID, hostID, paneID)) || ''
    drafts.set(key, { text, revision: 0 })
    return text
  } catch {
    drafts.set(key, { text: '', revision: 0 })
    return ''
  }
}

export function composerDraftRevision(hostID: string, paneID: string) {
  readComposerDraft(hostID, paneID)
  return drafts.get(liveKey(hostID, paneID))?.revision || 0
}

export function writeComposerDraft(hostID: string, paneID: string, text: string) {
  ensureAuthListener()
  const sessionID = currentSessionID()
  const key = liveKey(hostID, paneID)
  if (!sessionID || !key) return
  const previous = drafts.get(key)
  persistDraft(key, sessionID, hostID, paneID, { text, revision: (previous?.revision || 0) + 1 })
  notify()
}

export function readComposerSend(hostID: string, paneID: string): ComposerSendState {
  ensureAuthListener()
  const key = liveKey(hostID, paneID)
  if (!key) return { status: 'idle', error: '', revision: 0 }
  return sends.get(key) || { status: 'idle', error: '', revision: 0 }
}

export function composerInFlight(hostID: string, paneID: string) {
  const key = liveKey(hostID, paneID)
  return Boolean(key && inflight.has(key))
}

export function idleComposerSend(hostID: string, paneID: string) {
  const key = liveKey(hostID, paneID)
  const current = key ? sends.get(key) : undefined
  if (!key || !current || current.status !== 'delivered') return
  sends.set(key, { status: 'idle', error: '', revision: current.revision })
  notify()
}

export function retainComposerDrafts(sessionID: string) {
  const prefix = `${STORAGE_PREFIX}${encodeURIComponent(sessionID)}/`
  const keep = `${sessionID}\n`
  for (const key of Array.from(drafts.keys())) {
    if (!key.startsWith(keep)) drafts.delete(key)
  }
  for (const key of Array.from(sends.keys())) {
    if (!key.startsWith(keep)) sends.delete(key)
  }
  for (const key of Array.from(inflight)) {
    if (!key.startsWith(keep)) inflight.delete(key)
  }
  eachStorageKey((key) => {
    if (!key.startsWith(prefix)) {
      try { localStorage.removeItem(key) } catch { /* ignore */ }
    }
  })
  notify()
}

export function clearComposerDrafts() {
  drafts.clear()
  sends.clear()
  inflight.clear()
  eachStorageKey((key) => {
    try { localStorage.removeItem(key) } catch { /* ignore */ }
  })
  notify()
}

export function composerSubmitParams(paneID: string, text: string) {
  return { pane_id: paneID, text, keys: ['Enter'] }
}

export function composerOutcome(error: unknown): 'failed' | 'unknown' {
  const message = error instanceof Error ? error.message : String(error || '')
  const lower = message.toLowerCase()
  if (
    message === '请求超时' ||
    message === '连接已断开' ||
    message === '连接已关闭' ||
    message.includes('结果未知') ||
    message.includes('超时') ||
    lower.includes('deadline') ||
    /\btimeout\b/.test(lower) ||
    /read (?:remote )?herdr response/i.test(message) ||
    /write (?:remote )?herdr request/i.test(message) ||
    /\bunexpected eof\b/.test(lower) ||
    /\beof\b/.test(lower) ||
    lower.includes('connection reset') ||
    lower.includes('econnreset') ||
    lower.includes('broken pipe') ||
    lower.includes('epipe') ||
    lower.includes('connection aborted') ||
    lower.includes('econnaborted')
  ) return 'unknown'
  return 'failed'
}

export function composerStatusText(state: ComposerSendState, compact = false) {
  if (state.status === 'sending') return '发送中…'
  if (state.status === 'delivered') return compact ? '已送达（未确认远端已执行）' : '已送达。这只表示输入已被 Herdr 接受，不代表远端程序已执行成功。'
  if (state.status === 'failed') return compact ? `失败：${state.error}。已保留，请核对后手动重试。` : `发送失败：${state.error}。内容已保留，请检查后手动重试。`
  if (state.status === 'unknown') return compact ? `结果未知：请先看终端再手动重试，不会自动重发。` : `结果未知：${state.error}。内容已保留，请先核对终端结果再手动重试，不会自动重发。`
  return compact ? '本地编辑，发送后整段提交一次。' : '编辑仅在本地进行，点击发送后整段提交一次。'
}

export async function runComposerSend(hostID: string, paneID: string, submit: (paneID: string, text: string) => Promise<void>) {
  ensureAuthListener()
  const sessionID = currentSessionID()
  const key = liveKey(hostID, paneID)
  if (!sessionID || !key) return
  const text = readComposerDraft(hostID, paneID)
  const revision = drafts.get(key)?.revision || 0
  if (!text.trim() || inflight.has(key)) return
  inflight.add(key)
  sends.set(key, { status: 'sending', error: '', revision })
  notify()
  try {
    await submit(paneID, text)
    if (!inflight.has(key)) return
    const latest = drafts.get(key)
    if (latest && latest.revision === revision && latest.text === text) {
      persistDraft(key, sessionID, hostID, paneID, { text: '', revision })
    }
    sends.set(key, { status: 'delivered', error: '', revision })
    notify()
  } catch (error) {
    if (!inflight.has(key)) return
    sends.set(key, {
      status: composerOutcome(error),
      error: error instanceof Error ? error.message : '发送失败',
      revision,
    })
    notify()
  } finally {
    inflight.delete(key)
    notify()
  }
}
