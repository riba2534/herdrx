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

/**
 * `keys` 必填：两腿各自的按键集合必须由调用方显式给出，避免任何一处悄悄退回
 * “正文 + 回车一次发完”的旧形态（那正是回车被当粘贴内容吞掉的形态）。
 */
export function composerSubmitParams(paneID: string, text: string, keys: string[]) {
  return { pane_id: paneID, text, keys }
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

/**
 * 提交分**两腿**发送，而不是一次 `{text, keys:['Enter']}`。
 *
 *  - 第一腿只送正文（`keys: []`）。是否包 `\x1b[200~…\x1b[201~` 完全由 herdr 按目标 pane
 *    当前的 bracketed-paste 状态决定，客户端绝不自己再包一层。
 *  - 第二腿只送一次回车（`text: ''`, `keys: ['Enter']`）。
 *
 * 依据（herdr 0.9.0 + Claude Code 2.1.272，裸 PTY 记录每次 `read()` 的分块）：
 * 单次 `{text, keys:['Enter']}` 把 `\x1b[200~正文\x1b[201~\r` 作为**同一个 chunk** 送达
 * （正文 500 字节时一次 `read()` 就是 n=613），粘贴结束符与回车落在同一次 `read()`；
 * 而拆成两次 RPC 后，正文与 `\r` 各自成为独立 chunk（n=612 与 n=1），
 * **两次调用之间不做任何等待也依然分开**——分隔来自两次 RPC 往返本身，因此不需要固定 sleep。
 * 目标程序于是总能看到"先整段粘贴、后单独回车"，回车不会被当成粘贴内容吞掉。
 */
export type ComposerSubmit = (paneID: string, text: string, keys: string[]) => Promise<void>

function sendErrorText(error: unknown) {
  return error instanceof Error ? error.message : '发送失败'
}

export async function runComposerSend(hostID: string, paneID: string, submit: ComposerSubmit) {
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
    // 第一腿：整段正文。失败时正文没有送达，保留草稿，可安全手动重试。
    try {
      await submit(paneID, text, [])
    } catch (error) {
      if (!inflight.has(key)) return
      sends.set(key, { status: composerOutcome(error), error: sendErrorText(error), revision })
      notify()
      return
    }
    if (!inflight.has(key)) return
    // 第二腿：只发一次回车。目标固定为第一腿的 `paneID`——这里不读取当前选中项，
    // 用户在两腿之间切换 pane 不会把回车送到别的终端；失败也不重放正文。
    try {
      await submit(paneID, '', ['Enter'])
    } catch (error) {
      if (!inflight.has(key)) return
      sends.set(key, {
        status: 'unknown',
        error: `正文已送入终端，但提交回车未确认（${sendErrorText(error)}）`,
        revision,
      })
      notify()
      return
    }
    if (!inflight.has(key)) return
    // 只有两腿都成功才算送达并清空草稿；草稿在两腿之间被改写则保留用户的新内容。
    const latest = drafts.get(key)
    if (latest && latest.revision === revision && latest.text === text) {
      persistDraft(key, sessionID, hostID, paneID, { text: '', revision })
    }
    sends.set(key, { status: 'delivered', error: '', revision })
    notify()
  } finally {
    inflight.delete(key)
    notify()
  }
}
