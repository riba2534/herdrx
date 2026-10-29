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
  if (state.status === 'refused') return compact ? `未发送：${state.error}。` : `未发送：${state.error}。内容已保留，什么都没有送入终端。`
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
 * 实测（本仓库 `internal/herdr/input_integration_test.go`，herdr 0.9.0 + 真 PTY）：
 * 单次 `{text, keys:['Enter']}` 会把 `\x1b[200~正文\x1b[201~\r` 交给目标的**同一次
 * `read()`**；拆成两次 RPC 后，正文与 `\r` 默认落在不同的 `read()`。
 *
 * 但「落在不同 `read()`」不是客户端能单方面保证的：PTY 没有消息边界，分隔取决于**目标
 * 何时 read**。目标忙于重绘/流式输出、两次写之间没有 read 时，内核会把两腿合并成一次
 * `read()`，字节流退化成与单次调用完全相同、回车重新落进粘贴框架的致病形态（已用延迟
 * 读取的 fixture 复现）。因此第二腿前留一次 settle（`COMPOSER_SUBMIT_SETTLE_MS`），
 * 给目标一次 read 正文的机会再发回车——与只读参考仓库 orca 的做法一致
 * （`src/renderer/src/lib/agent-paste-draft.ts` 的 `POST_PASTE_SUBMIT_DELAY_MS`）。
 * 这是降低概率，不是消除竞态：目标停顿超过 settle 时仍可能合并，客户端无法观测目标是否
 * 已消费输入，故不加更重的同步。
 */
export type ComposerSubmit = (paneID: string, text: string, keys: string[]) => Promise<void>

/**
 * 两腿之间的 settle。参考 orca 的 `POST_PASTE_SUBMIT_DELAY_MS = 50`：够目标从 PTY
 * 读走正文并处理 paste 结束符，人眼无感。不要调成 0——0 会让两腿在目标一次 `read()`
 * 里合并，退回被吞回车的旧形态。
 */
export const COMPOSER_SUBMIT_SETTLE_MS = 50

function settleBeforeEnter() {
  return new Promise<void>((resolve) => setTimeout(resolve, COMPOSER_SUBMIT_SETTLE_MS))
}

function sendErrorText(error: unknown) {
  return error instanceof Error ? error.message : '发送失败'
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
    // settle：等目标读走正文再发回车，避免两腿被内核合并成一次 read（见上）。
    await settleBeforeEnter()
    if (!inflight.has(key)) return
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
