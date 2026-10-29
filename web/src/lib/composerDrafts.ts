import { APIError, api, currentSessionID, onAuthEvent } from './api'
import type { PaneDialog } from '../types'

const STORAGE_PREFIX = 'herdrx.composer.v1.'

// refused：服务端读屏后拒绝发送（对话框或电脑上的草稿），什么都没有送进终端。
export type ComposerSendStatus = 'idle' | 'sending' | 'delivered' | 'failed' | 'unknown' | 'refused'
export type ComposerSendState = { status: ComposerSendStatus; error: string; revision: number }

type DraftRecord = { text: string; revision: number }

const drafts = new Map<string, DraftRecord>()
const sends = new Map<string, ComposerSendState>()
const inflight = new Set<string>()
/**
 * 每个 pane 当前这段草稿的 client_msg_id。正文不变时沿用同一个 id：网络断在半路后用户再点
 * 发送，服务端能认出已送达的那次（返回 duplicate）或如实说明结果不确定，而不是再打一遍。
 * 服务端明确说“结果不确定”后换新 id——用户看过终端、主动再点发送就是一条新消息。
 */
const messageIDs = new Map<string, { id: string; text: string }>()
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
  for (const key of Array.from(messageIDs.keys())) {
    if (!key.startsWith(keep)) messageIDs.delete(key)
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
  messageIDs.clear()
  eachStorageKey((key) => {
    try { localStorage.removeItem(key) } catch { /* ignore */ }
  })
  notify()
}

export function composerStatusText(state: ComposerSendState, compact = false) {
  if (state.status === 'sending') return '发送中…'
  if (state.status === 'delivered') return compact ? '已送达（未确认远端已执行）' : '已送达。这只表示输入已被 Herdr 接受，不代表远端程序已执行成功。'
  if (state.status === 'failed') return compact ? `失败：${state.error}。已保留，请核对后手动重试。` : `发送失败：${state.error}。内容已保留，请检查后手动重试。`
  if (state.status === 'refused') return compact ? `未发送：${state.error}。` : `未发送：${state.error}。内容已保留，什么都没有送入终端。`
  if (state.status === 'unknown') return compact ? `结果未知：请先看终端再手动重试，不会自动重发。` : `结果未知：${state.error}。内容已保留，请先核对终端结果再手动重试，不会自动重发。`
  return compact ? '本地编辑，发送后整段提交一次。' : '编辑仅在本地进行，点击发送后整段提交一次。'
}

function newMessageID() {
  const bytes = new Uint8Array(16)
  crypto.getRandomValues(bytes)
  return Array.from(bytes, (byte) => byte.toString(16).padStart(2, '0')).join('')
}

function messageIDFor(key: string, text: string) {
  const current = messageIDs.get(key)
  if (current && current.text === text) return current.id
  const id = newMessageID()
  messageIDs.set(key, { id, text })
  return id
}

export type ComposerPromptResult =
  | { kind: 'sent' }
  | { kind: 'dialog'; dialog: PaneDialog | null }
  | { kind: 'draft' }
  | { kind: 'failed' }
  | { kind: 'skipped' }

/**
 * 经服务端 `POST …/prompt` 发送整段草稿：服务端先读屏，弹着对话框或电脑上有未发送的输入时
 * 拒绝（什么都不打），再按“正文 + 单独回车”两腿送入。`force` 只跳过草稿检查。
 * 任何结果都不自动重发。
 */
export async function runComposerPrompt(hostID: string, paneID: string, options: { force?: boolean } = {}): Promise<ComposerPromptResult> {
  ensureAuthListener()
  const sessionID = currentSessionID()
  const key = liveKey(hostID, paneID)
  if (!sessionID || !key) return { kind: 'skipped' }
  const text = readComposerDraft(hostID, paneID)
  const revision = drafts.get(key)?.revision || 0
  if (!text.trim() || inflight.has(key)) return { kind: 'skipped' }
  inflight.add(key)
  sends.set(key, { status: 'sending', error: '', revision })
  notify()
  const settle = (status: ComposerSendStatus, error: string) => {
    if (!inflight.has(key)) return
    sends.set(key, { status, error, revision })
    notify()
  }
  try {
    const clientMsgID = messageIDFor(key, text)
    try {
      await api.panePrompt(hostID, paneID, { text, submit: true, client_msg_id: clientMsgID, force: options.force || undefined })
    } catch (error) {
      if (!(error instanceof APIError)) {
        // 请求可能已到达服务端：结果未知，保留同一个 id 供用户手动重试时去重。
        settle('unknown', '网络中断，没有收到服务端的回应')
        return { kind: 'failed' }
      }
      if (error.code === 'pane_dialog') {
        settle('refused', '终端正弹着一个选择对话框，请先回答它再发送')
        const details = error.details as { dialog?: PaneDialog } | undefined
        return { kind: 'dialog', dialog: details?.dialog || null }
      }
      if (error.code === 'pane_draft') {
        settle('refused', '电脑上的输入框里有未发送的内容')
        return { kind: 'draft' }
      }
      if (error.code === 'delivery_unknown') {
        messageIDs.delete(key)
        settle('unknown', error.message)
      } else {
        settle('failed', error.message)
      }
      return { kind: 'failed' }
    }
    if (!inflight.has(key)) return { kind: 'skipped' }
    messageIDs.delete(key)
    // 草稿在发送期间被改写则保留用户的新内容。
    const latest = drafts.get(key)
    if (latest && latest.revision === revision && latest.text === text) {
      persistDraft(key, sessionID, hostID, paneID, { text: '', revision })
    }
    settle('delivered', '')
    return { kind: 'sent' }
  } finally {
    inflight.delete(key)
    notify()
  }
}
