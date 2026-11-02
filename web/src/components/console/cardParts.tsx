import { useLayoutEffect, useRef, useState, type KeyboardEvent as ReactKeyboardEvent } from 'react'
import { CornerDownLeft, LoaderCircle } from 'lucide-react'
import type { DialogSession } from '../../lib/console/useDialogSession'
import type { PaneDialog } from '../../types'

/** 各种交互卡片共用的入参。 */
export type CardProps = {
  dialog: PaneDialog
  session: DialogSession
  collapsed: boolean
  onCollapsedChange: (collapsed: boolean) => void
}

/** 正在为这个编号按键：按钮显示忙碌。 */
export const answeringAt = (session: DialogSession, index?: number) => session.phase.kind === 'answering' && session.phase.index === index
/** 这个编号是正在填写（或提交中）的自由输入项。 */
export const typingAt = (session: DialogSession, index?: number) => (session.phase.kind === 'typing' || session.phase.kind === 'sending') && session.phase.index === index

// 与 Composer 相同：Safari 提交输入法候选的那次回车可能不带 isComposing。
const composing = (event: ReactKeyboardEvent) => event.nativeEvent.isComposing || event.key === 'Process' || event.nativeEvent.keyCode === 229

// 自由填写的草稿按题保存在本页内存里：切走再回来、收起再展开都不丢，不写进存储，也不离开浏览器。
const drafts = new Map<string, string>()

/** 卡片里的自由输入框：回车提交（Shift+回车换行，输入法组字时不提交），Esc 返回选项。 */
export function FreeTextEntry({ draftKey, label, placeholder, submitLabel, session }: {
  draftKey: string
  label: string
  placeholder: string
  submitLabel: string
  session: DialogSession
}) {
  const [text, setText] = useState(() => drafts.get(draftKey) || '')
  const ref = useRef<HTMLTextAreaElement>(null)
  const sending = session.phase.kind === 'sending'

  useLayoutEffect(() => {
    const node = ref.current
    if (!node) return
    node.style.height = 'auto'
    node.style.height = `${node.scrollHeight}px`
  }, [text])

  const update = (value: string) => {
    setText(value)
    if (value) drafts.set(draftKey, value)
    else drafts.delete(draftKey)
  }
  const submit = async () => {
    if (sending || !text.trim()) return
    if (await session.submitText(text)) drafts.delete(draftKey)
  }

  return <div className="console-free-entry">
    <textarea
      ref={ref}
      className="console-free-input"
      aria-label={label}
      placeholder={placeholder}
      value={text}
      rows={1}
      autoFocus
      readOnly={sending}
      onChange={(event) => update(event.target.value)}
      onKeyDown={(event) => {
        if (event.key === 'Enter' && !event.shiftKey && !composing(event)) {
          event.preventDefault()
          void submit()
        } else if (event.key === 'Escape' && !sending) {
          event.preventDefault()
          session.cancelTyping()
        }
      }}
    />
    <div className="console-free-actions">
      <button type="button" className="console-button" disabled={sending} onClick={session.cancelTyping}>返回选项</button>
      <button type="button" className="console-button console-button-primary" disabled={sending || !text.trim()} aria-busy={sending || undefined} onClick={() => void submit()}>
        {sending ? <LoaderCircle className="console-spin" size={14} aria-hidden="true"/> : <CornerDownLeft size={14} aria-hidden="true"/>}{submitLabel}
      </button>
    </div>
  </div>
}

/** 卡片底部的进度与结果说明。 */
export function CardStatus({ session }: { session: DialogSession }) {
  const { phase, notice } = session
  const progress = phase.kind === 'answering' ? '正在按键…' : phase.kind === 'confirming' ? '已作答，正在确认终端…' : phase.kind === 'sending' ? '正在提交…' : ''
  if (!progress && !notice) return null
  return <p className={`console-card-status${progress ? '' : ' console-card-status-notice'}`} role="status" aria-live="polite">
    {progress ? <><LoaderCircle className="console-spin" size={13} aria-hidden="true"/>{progress}</> : notice}
  </p>
}

/** 选项右侧的编号徽标：数字键快捷作答的提示。 */
export function KeyBadge({ index }: { index: number }) {
  return <span className="console-key" aria-hidden="true">{index}</span>
}
