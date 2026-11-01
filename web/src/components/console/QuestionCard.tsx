// Adapted from Cindy (https://github.com/makecindy/cindy, Apache-2.0) apps/desktop/src/renderer/components/new-chat/AskUserQuestionPrompt.tsx@19dad9c225
import { MessageCircle, Pencil, SquareTerminal } from 'lucide-react'
import type { AskQuestion } from '../../lib/console/interaction'
import { matchQuestion, questionOptions } from '../../lib/console/interaction'
import type { DialogSession } from '../../lib/console/useDialogSession'
import type { PaneDialog } from '../../types'
import { CardStatus, FreeTextEntry, KeyBadge, sessionBusy } from './cardParts'
import { InteractionCardShell } from './InteractionCardShell'
import { useDigitShortcuts } from './shortcuts'

type CardProps = {
  dialog: PaneDialog
  session: DialogSession
  collapsed: boolean
  onCollapsedChange: (collapsed: boolean) => void
  /** 自由填写草稿的键前缀（host、pane、工具调用）。 */
  draftScope: string
  onOpenShell?: () => void
}

/** 多选题网页卡片不能作答（勾选要在终端里按空格），只说明并引导去 Shell 模式。 */
export function MultiSelectNotice({ onOpenShell }: { onOpenShell?: () => void }) {
  return <div className="console-card-note">
    <p>这道题可以多选，需要在终端里勾选后提交。请切到 Shell 模式处理，处理完这里会自动更新。</p>
    {onOpenShell && <button type="button" className="console-button" onClick={onOpenShell}><SquareTerminal size={14} aria-hidden="true"/>在 Shell 中打开</button>}
  </div>
}

/** AskUserQuestion 的一题：题目和选项说明取对话记录，作答按读屏的编号。 */
export function QuestionCard({ dialog, questions, session, collapsed, onCollapsedChange, draftScope, onOpenShell }: CardProps & { questions: readonly AskQuestion[] }) {
  const position = matchQuestion(dialog, questions)
  const question = position >= 0 ? questions[position] : undefined
  const options = questionOptions(dialog, question)
  const answers = options.filter((option) => option.role === 'answer')
  const free = options.find((option) => option.role === 'free')
  const chat = options.find((option) => option.role === 'chat')
  const busy = sessionBusy(session)
  const { phase } = session
  const typing = (phase.kind === 'typing' || phase.kind === 'sending') && phase.index === free?.index
  const multiple = Boolean(question?.multiSelect)
  const title = question?.question || dialog.title || '终端在等你回答'
  const page = questions.length > 1 && position >= 0 ? `第 ${position + 1}/${questions.length} 题` : ''

  const choose = (index: number) => {
    if (index === free?.index) void session.answer(index, { typing: true })
    else void session.answer(index)
  }
  useDigitShortcuts(!collapsed && !busy && !typing && !multiple, (digit) => {
    if (!options.some((option) => option.index === digit)) return false
    choose(digit)
    return true
  })

  return <InteractionCardShell
    label={`Claude 在问：${title}`}
    collapsed={collapsed}
    onCollapsedChange={onCollapsedChange}
    barTitle={`Claude 在等你回答：${question?.header || title}`}
    barMeta={page}
    header={<>
      <span className="console-chip">{question?.header || '提问'}</span>
      {page && <span className="console-card-page">{page}</span>}
    </>}
    pinned={!multiple && free && (typing
      ? <FreeTextEntry draftKey={`${draftScope}:${position}`} label="自己填写的答案" placeholder="输入你的答案，回车提交" submitLabel="提交答案" session={session}/>
      : <button type="button" className="console-option console-option-free" disabled={busy} onClick={() => choose(free.index)}>
        <Pencil size={15} aria-hidden="true"/>
        <span className="console-option-text"><span className="console-option-label">自己填写答案</span></span>
        <KeyBadge index={free.index}/>
      </button>)}
    footer={<>
      {!multiple && chat && <button type="button" className="console-link-button" disabled={busy} data-tooltip="不选任何答案，回到对话里和 Claude 说明" onClick={() => choose(chat.index)}>
        <MessageCircle size={14} aria-hidden="true"/>不选，直接和 Claude 聊<KeyBadge index={chat.index}/>
      </button>}
      <CardStatus session={session}/>
    </>}
  >
    <p className="console-card-question">{title}</p>
    {multiple
      ? <MultiSelectNotice onOpenShell={onOpenShell}/>
      : <div className="console-options" role="list" aria-label="选项">
        {answers.map((option) => <div role="listitem" key={option.index}>
          <button type="button" className="console-option" disabled={busy} aria-busy={(phase.kind === 'answering' && phase.index === option.index) || undefined} data-tooltip={option.label} onClick={() => choose(option.index)}>
            <span className="console-option-text">
              <span className="console-option-label">{option.label}</span>
              {option.description && <span className="console-option-description">{option.description}</span>}
            </span>
            <KeyBadge index={option.index}/>
          </button>
        </div>)}
      </div>}
  </InteractionCardShell>
}
