import { useEffect, useSyncExternalStore } from 'react'
import { APIError, api } from '../../lib/api'
import { readComposerDraft, writeComposerDraft } from '../../lib/composerDrafts'
import type { Pane } from '../../types'

/**
 * 新建 Claude 会话时填写的首条消息：会话刚启动时 Claude 还没就绪（可能先弹信任目录对话框），
 * 所以先记下，等新 pane 的 Agent 变成 Claude 且空闲、读屏没有对话框时**只发一次**。
 *
 * - 发送走 `api.panePrompt`（服务端读屏防护），`client_msg_id` 由新建会话的 `request_id` 固定为
 *   `first-<request_id>`，网络断在半路后服务端能认出重复请求。
 * - 读屏看到对话框，或服务端明确说什么都没打进终端（对话框、输入框里有字、发送太忙、读屏失败、
 *   Herdr 拒收、可重试的连接错误）时，下次就绪再试，编号不变。
 * - 其余失败和结果不确定的情况都不重发：把原文放回该 pane 的输入草稿并提示。
 * - 超过等待时限仍没就绪、或 pane 已关闭时同样放弃，不往一个普通 Shell 里打字。
 *
 * 记录放在模块内存里：切换主机再回来仍会继续等待；刷新页面会丢失（不落盘，避免重复发送）。
 */
export const FIRST_MESSAGE_WAIT_MS = 120_000
export const FIRST_MESSAGE_POLL_MS = 1500

export type FirstMessageNotice = { tone: 'ok' | 'warn' | 'error'; text: string }
type Entry = { hostID: string; paneID: string; text: string; clientMsgID: string; deadline: number; sending: boolean }

const entries = new Map<string, Entry>()
const listeners = new Set<() => void>()
const key = (hostID: string, paneID: string) => `${hostID}\n${paneID}`

function changed() {
  for (const listener of listeners) listener()
}

function subscribe(listener: () => void) {
  listeners.add(listener)
  return () => { listeners.delete(listener) }
}

export function queueFirstMessage(hostID: string, paneID: string, text: string, requestID: string, now = Date.now()) {
  if (!text.trim()) return
  entries.set(key(hostID, paneID), { hostID, paneID, text, clientMsgID: `first-${requestID}`, deadline: now + FIRST_MESSAGE_WAIT_MS, sending: false })
  changed()
}

export function firstMessageState(hostID: string, paneID: string): 'waiting' | 'sending' | null {
  const entry = entries.get(key(hostID, paneID))
  return entry ? entry.sending ? 'sending' : 'waiting' : null
}

/** 把原文放回 pane 的输入草稿（已有草稿时接在后面），不发送。 */
function restoreDraft(entry: Entry) {
  const current = readComposerDraft(entry.hostID, entry.paneID)
  writeComposerDraft(entry.hostID, entry.paneID, current.trim() ? `${current}\n${entry.text}` : entry.text)
}

function finish(entry: Entry) {
  if (entries.get(key(entry.hostID, entry.paneID)) !== entry) return false
  entries.delete(key(entry.hostID, entry.paneID))
  changed()
  return true
}

/** 用户取消自动发送：原文放回输入框。发送进行中时不能取消。 */
export function cancelFirstMessage(hostID: string, paneID: string) {
  const entry = entries.get(key(hostID, paneID))
  if (!entry || entry.sending || !finish(entry)) return false
  restoreDraft(entry)
  return true
}

// 服务端明确“什么都没打进终端”、过一会儿可能就好的拒绝（见 internal/httpapi/prompt.go）。
const RETRY_LATER = new Set(['pane_dialog', 'pane_draft', 'prompt_busy', 'pane_read_failed', 'pane_send_failed'])
function retryLater(error: unknown) {
  if (!(error instanceof APIError)) return false
  // 连接主机失败发生在读屏之前，带 retryable 字段。
  return RETRY_LATER.has(error.code) || (error.details as { retryable?: unknown } | undefined)?.retryable === true
}

async function attempt(entry: Entry, pane: Pane | undefined, notify: (notice: FirstMessageNotice) => void, now: number) {
  if (entry.sending) return
  if (!pane) {
    if (now >= entry.deadline && finish(entry)) notify({ tone: 'warn', text: '新会话的终端已经不在了，首条消息没有发送。' })
    return
  }
  if (now >= entry.deadline) {
    if (!finish(entry)) return
    restoreDraft(entry)
    notify({ tone: 'warn', text: 'Claude 在 2 分钟内没有就绪，首条消息没有发送，已放回这个会话的输入框。' })
    return
  }
  if (pane.agent !== 'claude' || pane.agent_status !== 'idle') return
  entry.sending = true
  changed()
  try {
    // 先读屏：信任目录等对话框还开着时文字会被当成选项，等用户答完再发。读不到画面也先不发。
    const dialog = await api.paneDialog(entry.hostID, entry.paneID).then((result) => result.dialog, () => undefined)
    if (dialog !== null) return
    try {
      await api.panePrompt(entry.hostID, entry.paneID, { text: entry.text, submit: true, client_msg_id: entry.clientMsgID })
    } catch (error) {
      // 什么都没打进终端：下次就绪再试，同一个编号可以复用。
      if (retryLater(error)) return
      if (!finish(entry)) return
      restoreDraft(entry)
      const reason = error instanceof APIError ? error.message : '网络中断，没有收到服务端的回应'
      notify({ tone: 'error', text: `首条消息未发送或结果不确定：${reason}。原文已放回这个会话的输入框，不会自动重发，请先看对话记录再决定是否手动发送。` })
      return
    }
    if (finish(entry)) notify({ tone: 'ok', text: '首条消息已发送给 Claude。' })
  } finally {
    entry.sending = false
    changed()
  }
}

/**
 * 在工作台里驱动等待中的首条消息：快照变化时立即检查，等待期间按 FIRST_MESSAGE_POLL_MS 轮询
 * （对话框答完后 Herdr 状态不一定变化）。断线时不尝试，但等待时限照常计算。
 */
export function useFirstMessages(hostID: string, panes: Pane[] | undefined, connected: boolean, notify: (notice: FirstMessageNotice) => void) {
  // 只订阅“这台主机有没有等待中的消息”，发送状态的来回切换不让整个工作台重渲染。
  const hasPending = useSyncExternalStore(subscribe, () => [...entries.values()].some((entry) => entry.hostID === hostID))

  useEffect(() => {
    if (!hasPending || !panes) return
    const tick = () => {
      const now = Date.now()
      for (const entry of entries.values()) {
        if (entry.hostID !== hostID) continue
        const pane = panes.find((item) => item.pane_id === entry.paneID)
        if (!connected && now < entry.deadline) continue
        void attempt(entry, pane, notify, now)
      }
    }
    tick()
    const timer = window.setInterval(tick, FIRST_MESSAGE_POLL_MS)
    return () => window.clearInterval(timer)
  }, [hostID, hasPending, panes, connected, notify])
}

/** 某个 pane 的首条消息状态，随存储变化重新渲染。 */
export function useFirstMessageState(hostID: string, paneID: string) {
  return useSyncExternalStore(subscribe, () => firstMessageState(hostID, paneID))
}

/** 仅供测试。 */
export function resetFirstMessagesForTest() {
  entries.clear()
  changed()
}
