import { useCallback, useEffect, useId, useRef, useState, useSyncExternalStore, type KeyboardEvent as ReactKeyboardEvent } from 'react'
import { MessageSquareWarning, X } from 'lucide-react'
import { APIError, api } from '../lib/api'
import type { PaneDialog } from '../types'
import { Toast } from './Toast'
import './DialogCard.css'

/** 卡片可见时的轮询间隔。 */
export const DIALOG_POLL_MS = 2000

// 工作台挂着的“主卡片”（pane 处于 blocked 时显示）。Composer 被拒绝后只在没有主卡片时
// 才自己弹一张，免得同一个对话框出现两张卡。
const primaryCards = new Map<string, number>()
const primaryListeners = new Set<() => void>()
const primaryKey = (hostID: string, paneID: string) => `${hostID}\n${paneID}`

function setPrimary(key: string, delta: number) {
  const next = (primaryCards.get(key) || 0) + delta
  if (next > 0) primaryCards.set(key, next)
  else primaryCards.delete(key)
  for (const listener of primaryListeners) listener()
}

function subscribePrimary(listener: () => void) {
  primaryListeners.add(listener)
  return () => { primaryListeners.delete(listener) }
}

/** 这个 pane 是否已经有一张工作台主卡片。 */
export function usePrimaryDialogCard(hostID: string, paneID: string) {
  const key = primaryKey(hostID, paneID)
  return useSyncExternalStore(subscribePrimary, () => primaryCards.has(key))
}

const answerErrors: Record<string, string> = {
  dialog_changed: '对话框已变化，请重新选择',
  dialog_gone: '对话框已经关闭，可能已在电脑上回答',
}

/**
 * 对话框答题卡片：显示服务端从 pane 画面认出的选择对话框，点选项即按对应的键。
 * 只显示认得出的对话框；认不出时卡片不出现，用户回终端操作。
 */
export function DialogCard({ hostID, paneID, initial, primary = false, onClose }: {
  hostID: string
  paneID: string
  /** Composer 被拒绝时服务端带回的对话框，先显示它再轮询。 */
  initial?: PaneDialog | null
  /** 工作台主卡片：登记自己，让 Composer 不再重复弹卡。 */
  primary?: boolean
  /** 对话框消失或用户收起时调用；不传时卡片自己隐藏。 */
  onClose?: () => void
}) {
  const [dialog, setDialog] = useState<PaneDialog | null>(initial || null)
  const [dismissed, setDismissed] = useState('')
  const [answering, setAnswering] = useState<number | null>(null)
  const [toast, setToast] = useState('')
  const titleID = useId()
  const listRef = useRef<HTMLDivElement>(null)
  const controllerRef = useRef<AbortController | null>(null)
  // 回答进行中不接收轮询结果；回答后的补读定时器随卡片卸载清掉，免得卸载后还去关别的 pane 的卡片。
  const answeringRef = useRef(false)
  const followUpRef = useRef(0)
  const onCloseRef = useRef(onClose)
  onCloseRef.current = onClose
  // initial 只作首屏内容，之后以轮询为准；换 pane 时由调用方用 key 重建卡片。
  const startedWithDialog = useRef(Boolean(initial))

  useEffect(() => {
    if (!primary) return
    const key = primaryKey(hostID, paneID)
    setPrimary(key, 1)
    return () => setPrimary(key, -1)
  }, [primary, hostID, paneID])

  const refresh = useCallback(async () => {
    controllerRef.current?.abort()
    const controller = new AbortController()
    controllerRef.current = controller
    try {
      const result = await api.paneDialog(hostID, paneID, { signal: controller.signal })
      if (controller.signal.aborted || answeringRef.current) return
      setDialog(result.dialog)
      if (!result.dialog) onCloseRef.current?.()
    } catch {
      // 读屏暂时失败：保留上一次的卡片，下一轮再试。
    }
  }, [hostID, paneID])

  useEffect(() => {
    let timer = 0
    const tick = () => {
      window.clearTimeout(timer)
      if (document.visibilityState === 'visible' && !answeringRef.current) void refresh()
      timer = window.setTimeout(tick, DIALOG_POLL_MS)
    }
    if (startedWithDialog.current) timer = window.setTimeout(tick, DIALOG_POLL_MS)
    else tick()
    const onVisible = () => { if (document.visibilityState === 'visible') tick() }
    document.addEventListener('visibilitychange', onVisible)
    return () => {
      window.clearTimeout(timer)
      window.clearTimeout(followUpRef.current)
      document.removeEventListener('visibilitychange', onVisible)
      controllerRef.current?.abort()
    }
  }, [refresh])

  const answer = async (index: number, label: string) => {
    if (!dialog || answering !== null) return
    // 进行中的轮询可能在按键之前读到旧画面，回来后会把已回答的卡片又显示出来：先中止它。
    controllerRef.current?.abort()
    answeringRef.current = true
    setAnswering(index)
    let reread = false
    try {
      await api.answerPaneDialog(hostID, paneID, dialog.fingerprint, index)
      setToast(`已选择：${label}`)
      // 终端重绘前再读到的还是这个对话框：按指纹收起，出现新的对话框时才再显示。
      setDismissed(dialog.fingerprint)
      reread = true
    } catch (error) {
      const code = error instanceof APIError ? error.code : ''
      setToast(answerErrors[code] || (error instanceof Error ? error.message : '回答失败，请到终端里操作'))
      reread = code === 'dialog_changed' || code === 'dialog_gone'
    } finally {
      answeringRef.current = false
      setAnswering(null)
    }
    if (reread) {
      window.clearTimeout(followUpRef.current)
      followUpRef.current = window.setTimeout(() => void refresh(), 600)
    }
  }

  const moveFocus = (event: ReactKeyboardEvent<HTMLDivElement>) => {
    if (!['ArrowDown', 'ArrowUp', 'Home', 'End'].includes(event.key)) return
    const buttons = [...(listRef.current?.querySelectorAll<HTMLButtonElement>('button') || [])]
    const current = buttons.indexOf(document.activeElement as HTMLButtonElement)
    if (current < 0) return
    event.preventDefault()
    const next = event.key === 'Home' ? 0 : event.key === 'End' ? buttons.length - 1 : (current + (event.key === 'ArrowDown' ? 1 : -1) + buttons.length) % buttons.length
    buttons[next]?.focus()
  }

  const close = () => {
    if (dialog) setDismissed(dialog.fingerprint)
    onCloseRef.current?.()
  }

  const toastNode = <Toast message={toast} onClear={() => setToast('')}/>
  if (!dialog || dialog.fingerprint === dismissed) return toastNode
  return <>
    <section className="dialog-card" role="group" aria-labelledby={titleID}>
      <header className="dialog-card-head">
        <MessageSquareWarning size={16} aria-hidden="true"/>
        <strong id={titleID}>{dialog.title || '终端在等你选择'}</strong>
        <button type="button" className="dialog-card-close" aria-label="收起对话框卡片" onClick={close}><X size={14} aria-hidden="true"/></button>
      </header>
      {dialog.lines.length > 0 && <div className="dialog-card-lines">{dialog.lines.map((line, index) => <p key={index}>{line}</p>)}</div>}
      <div className="dialog-card-options" ref={listRef} role="list" aria-label="选项" onKeyDown={moveFocus}>
        {dialog.options.map((option) => <div role="listitem" key={option.index}>
          <button
            type="button"
            className={`dialog-card-option${option.selected ? ' dialog-card-option-selected' : ''}`}
            data-tooltip={option.label}
            disabled={answering !== null}
            aria-busy={answering === option.index || undefined}
            aria-describedby={option.selected ? `${titleID}-current` : undefined}
            onClick={() => void answer(option.index, option.label)}
          >
            <span className="dialog-card-index" aria-hidden="true">{option.index}</span>
            <span className="dialog-card-label">{option.label}</span>
          </button>
        </div>)}
      </div>
      <span id={`${titleID}-current`} hidden>终端光标当前所在</span>
      <p className="dialog-card-hint">选择后会直接按下对应的键。认不出的对话框请回终端操作。</p>
    </section>
    {toastNode}
  </>
}
