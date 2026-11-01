import { FolderLock, ListChecks } from 'lucide-react'
import { findOption } from '../../lib/console/interaction'
import type { DialogSession } from '../../lib/console/useDialogSession'
import type { PaneDialog, PaneDialogOption } from '../../types'
import { CardStatus, KeyBadge, sessionBusy } from './cardParts'
import { InteractionCardShell } from './InteractionCardShell'
import { useDigitShortcuts } from './shortcuts'

type ChoiceProps = {
  dialog: PaneDialog
  session: DialogSession
  collapsed: boolean
  onCollapsedChange: (collapsed: boolean) => void
}

type Choice = { option: PaneDialogOption; label: string; primary: boolean }

/** 认出的两个动作；屏幕文字对不上时按屏幕原文列出全部选项。 */
function choices(dialog: PaneDialog, known: Array<{ pattern: RegExp; label: string; primary: boolean }>): Choice[] {
  const matched = known.flatMap(({ pattern, label, primary }) => {
    const option = findOption(dialog, pattern)
    return option ? [{ option, label, primary }] : []
  })
  if (matched.length === known.length) return matched
  return dialog.options.map((option) => ({ option, label: option.label, primary: false }))
}

function ChoiceButtons({ items, session, numbered }: { items: Choice[]; session: DialogSession; numbered: boolean }) {
  const busy = sessionBusy(session)
  const { phase } = session
  return <div className="console-choice-row">
    {items.map(({ option, label, primary }) => <button
      type="button"
      key={option.index}
      className={`console-button${primary ? ' console-button-primary' : ''}`}
      disabled={busy}
      aria-busy={(phase.kind === 'answering' && phase.index === option.index) || undefined}
      data-tooltip={option.label}
      onClick={() => void session.answer(option.index)}
    >{label}{numbered && <KeyBadge index={option.index}/>}</button>)}
  </div>
}

/** 多题提问最后的确认页：列出每题的回答，提交或取消。 */
export function ReviewCard({ dialog, session, collapsed, onCollapsedChange }: ChoiceProps) {
  const items = choices(dialog, [
    { pattern: /^submit answers?\b/i, label: '提交答案', primary: true },
    { pattern: /^cancel\b/i, label: '取消', primary: false },
  ])
  const pairs: Array<{ question: string; answer: string }> = []
  for (const line of dialog.lines) {
    const text = line.trim()
    if (/^[●•]/.test(text)) pairs.push({ question: text.replace(/^[●•]\s*/, ''), answer: '' })
    else if (text.startsWith('→') && pairs.length) pairs[pairs.length - 1].answer = text.replace(/^→\s*/, '')
  }
  useDigitShortcuts(!collapsed && !sessionBusy(session), (digit) => {
    if (!items.some(({ option }) => option.index === digit)) return false
    void session.answer(digit)
    return true
  })
  return <InteractionCardShell
    label="确认你的回答"
    collapsed={collapsed}
    onCollapsedChange={onCollapsedChange}
    barTitle="Claude 在等你确认回答"
    header={<><span className="console-chip"><ListChecks size={13} aria-hidden="true"/>确认回答</span><span className="console-card-title">提交这些回答吗？</span></>}
    pinned={<ChoiceButtons items={items} session={session} numbered/>}
    footer={<CardStatus session={session}/>}
  >
    {pairs.length > 0
      ? <dl className="console-review">{pairs.map((pair, index) => <div key={index}><dt>{pair.question}</dt><dd>{pair.answer || '（未回答）'}</dd></div>)}</dl>
      : <div className="console-card-lines">{dialog.lines.map((line, index) => <p key={index}>{line}</p>)}</div>}
  </InteractionCardShell>
}

/** 首次进入目录时的信任确认。不开数字键：默认的第一项是退出。 */
export function TrustCard({ dialog, session, collapsed, onCollapsedChange }: ChoiceProps) {
  const items = choices(dialog, [
    { pattern: /^yes,? i trust/i, label: '信任此目录并继续', primary: true },
    { pattern: /^no,? exit/i, label: '退出', primary: false },
  ])
  const folder = dialog.lines.map((line) => line.trim()).find((line) => /^[/~]/.test(line))
  return <InteractionCardShell
    label="要信任这个目录吗"
    collapsed={collapsed}
    onCollapsedChange={onCollapsedChange}
    barTitle="Claude 在等你确认是否信任这个目录"
    header={<><span className="console-chip console-chip-warning"><FolderLock size={13} aria-hidden="true"/>信任目录</span><span className="console-card-title">要信任这个目录吗？</span></>}
    pinned={<ChoiceButtons items={items} session={session} numbered={false}/>}
    footer={<CardStatus session={session}/>}
  >
    {folder && <p className="console-card-path">{folder}</p>}
    <p className="console-card-note">信任后 Claude Code 可以读取、修改并执行这个目录里的文件。确认是你自己的代码或可信来源后再继续；选「退出」会结束这次 Claude。</p>
  </InteractionCardShell>
}
