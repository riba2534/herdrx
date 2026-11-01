import { useCallback, useEffect, useRef, useState } from 'react'
import { APIError, api } from '../api'
import { useVisiblePoll } from '../useVisiblePoll'
import { createClientID } from '../workbench'
import type { PaneDialog } from '../../types'

/** 有待处理交互时读屏的间隔。 */
export const INTERACTION_POLL_MS = 1500
/** 作答后最多等这么久确认对话框变化；超时只提示，绝不重按。 */
export const RECEIPT_TIMEOUT_MS = 15_000

// 作答的状态机：
//   idle → answering（按键请求中）→ confirming（等读屏看到对话框变化或消失）→ idle
//   选自由填写项时 confirming 等的是光标停到该项，确认后进入 typing（卡片里出现输入框），
//   typing → sending（panePrompt 中）→ confirming → idle。
// 只有在作答之后才开始的读屏才能确认结果（按读屏序号判断），作答前发出、作答后才回来的旧读屏不算。
export type DialogPhase =
  | { kind: 'idle' }
  | { kind: 'answering'; index: number }
  | { kind: 'confirming'; fingerprint: string; title: string; index: number; since: number; after: number; expect: 'change' | 'select' }
  | { kind: 'typing'; fingerprint: string; title: string; index: number }
  | { kind: 'sending'; fingerprint: string; title: string; index: number }

export type DialogSession = {
  /** 最近一次读到的对话框；没有对话框或还没读到时为 null。 */
  dialog: PaneDialog | null
  /** 这一轮读屏至少完成过一次。 */
  loaded: boolean
  phase: DialogPhase
  /** 给用户看的结果说明（失败、超时、对话框已变化）。 */
  notice: string
  /** 按编号作答；`typing` 表示这是自由填写项，确认后进入输入状态。 */
  answer: (index: number, options?: { typing?: boolean }) => Promise<void>
  /** 在 typing 状态下把文字送进对话框自己的输入框并回车；返回是否已送达。 */
  submitText: (text: string) => Promise<boolean>
  cancelTyping: () => void
  refresh: () => void
}

const IDLE: DialogPhase = { kind: 'idle' }

const answerErrors: Record<string, string> = {
  dialog_changed: '对话框已变化，请重新选择',
  dialog_gone: '对话框已经关闭，可能已在电脑上回答',
}

const busy = (phase: DialogPhase) => phase.kind === 'answering' || phase.kind === 'confirming' || phase.kind === 'sending'

/**
 * 读屏对话框 + 作答。`enabled` 为 false 且没有进行中的作答时停止读屏并清空对话框。
 * 结果不确定的按键和输入一律不自动重发：只说明情况，由用户看过终端后自己决定。
 */
export function useDialogSession({ hostID, paneID, enabled }: { hostID: string; paneID: string; enabled: boolean }): DialogSession {
  const [dialog, setDialog] = useState<PaneDialog | null>(null)
  const [loaded, setLoaded] = useState(false)
  const [phase, setPhaseState] = useState<DialogPhase>(IDLE)
  const [notice, setNotice] = useState('')
  const phaseRef = useRef<DialogPhase>(IDLE)
  const dialogRef = useRef<PaneDialog | null>(null)
  // 同一段文字重试时沿用同一个 client_msg_id，服务端据此认出已送达的那次。
  const messageRef = useRef<{ text: string; id: string } | null>(null)
  // 已发出的读屏个数：确认只认作答完成之后才发出的读屏。
  const readsRef = useRef(0)
  const polling = enabled || phase.kind !== 'idle'
  const pollingRef = useRef(polling)
  pollingRef.current = polling

  const setPhase = useCallback((next: DialogPhase) => {
    phaseRef.current = next
    setPhaseState(next)
  }, [])

  const applyRead = useCallback((next: PaneDialog | null, read: number) => {
    dialogRef.current = next
    setDialog(next)
    setLoaded(true)
    const current = phaseRef.current
    if ((current.kind === 'typing' || current.kind === 'sending') && (!next || (next.title || '') !== current.title)) {
      // 输入期间对话框没了或换了题：输入框跟着收起（草稿由卡片保留）。发送中的请求自己收尾。
      if (current.kind === 'typing') setPhase(IDLE)
      return
    }
    if (current.kind !== 'confirming' || read <= current.after) return
    if (current.expect === 'change') {
      if (!next || next.fingerprint !== current.fingerprint) setPhase(IDLE)
      return
    }
    if (!next || (next.title || '') !== current.title) setPhase(IDLE)
    else if (next.options.some((option) => option.index === current.index && option.selected)) {
      setPhase({ kind: 'typing', fingerprint: next.fingerprint, title: current.title, index: current.index })
    }
  }, [setPhase])

  const run = useVisiblePoll(async (signal) => {
    if (!pollingRef.current || document.visibilityState === 'hidden') return
    const read = ++readsRef.current
    try {
      const result = await api.paneDialog(hostID, paneID, { signal })
      if (!signal.aborted && pollingRef.current) applyRead(result.dialog, read)
    } catch {
      // 读屏暂时失败：保留上一次的卡片，下一轮再试。
    }
  }, INTERACTION_POLL_MS)

  useEffect(() => {
    if (polling) {
      void run()
      return
    }
    dialogRef.current = null
    setDialog(null)
    setLoaded(false)
  }, [polling, run])

  // 对话记录已经有结果（或 pane 不再阻塞）：交互已结束，进行中的确认随之结束。
  useEffect(() => {
    if (!enabled && phaseRef.current.kind !== 'answering' && phaseRef.current.kind !== 'sending') setPhase(IDLE)
  }, [enabled, setPhase])

  useEffect(() => {
    if (phase.kind !== 'confirming') return
    const timer = window.setTimeout(() => {
      if (phaseRef.current !== phase) return
      setPhase(IDLE)
      setNotice('没有确认到结果，请查看终端后再决定')
    }, Math.max(0, phase.since + RECEIPT_TIMEOUT_MS - Date.now()))
    return () => window.clearTimeout(timer)
  }, [phase, setPhase])

  const answer = useCallback(async (index: number, options: { typing?: boolean } = {}) => {
    const current = dialogRef.current
    if (!current || busy(phaseRef.current)) return
    const title = current.title || ''
    setNotice('')
    if (options.typing && current.options.some((option) => option.index === index && option.selected)) {
      // 光标已经在自由填写项上，不用再按键。
      setPhase({ kind: 'typing', fingerprint: current.fingerprint, title, index })
      return
    }
    setPhase({ kind: 'answering', index })
    try {
      await api.answerPaneDialog(hostID, paneID, current.fingerprint, index)
      setPhase({ kind: 'confirming', fingerprint: current.fingerprint, title, index, since: Date.now(), after: readsRef.current, expect: options.typing ? 'select' : 'change' })
      void run()
    } catch (error) {
      setPhase(IDLE)
      const code = error instanceof APIError ? error.code : ''
      setNotice(answerErrors[code] || (error instanceof APIError ? error.message : '没有收到服务端的回应，请查看终端后再决定'))
      if (code === 'dialog_changed' || code === 'dialog_gone') void run()
    }
  }, [hostID, paneID, run, setPhase])

  const submitText = useCallback(async (text: string) => {
    const current = phaseRef.current
    if (current.kind !== 'typing' || !text.trim()) return false
    if (messageRef.current?.text !== text) messageRef.current = { text, id: createClientID() }
    const id = messageRef.current.id
    setNotice('')
    setPhase({ ...current, kind: 'sending' })
    try {
      await api.panePrompt(hostID, paneID, { text, submit: true, client_msg_id: id })
      messageRef.current = null
      setPhase({ kind: 'confirming', fingerprint: current.fingerprint, title: current.title, index: current.index, since: Date.now(), after: readsRef.current, expect: 'change' })
      void run()
      return true
    } catch (error) {
      setPhase(current)
      if (!(error instanceof APIError)) {
        // 请求可能已经到达：保留同一个编号，用户看过终端后再点提交时服务端能去重。
        setNotice('网络中断，没有收到服务端的回应。请先查看终端，确认没有发出去再重新提交')
      } else if (error.code === 'pane_dialog') {
        setNotice('终端没有进入输入状态，这段文字没有发出去。请到 Shell 模式操作')
      } else {
        if (error.code === 'delivery_unknown') messageRef.current = null
        setNotice(error.message)
      }
      return false
    }
  }, [hostID, paneID, run, setPhase])

  const cancelTyping = useCallback(() => {
    if (phaseRef.current.kind === 'typing') setPhase(IDLE)
  }, [setPhase])

  const refresh = useCallback(() => { void run() }, [run])

  return { dialog, loaded, phase, notice, answer, submitText, cancelTyping, refresh }
}
