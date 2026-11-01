// Adapted from Cindy (https://github.com/makecindy/cindy, Apache-2.0) apps/desktop/src/renderer/components/new-chat/PlanActionCard.tsx@19dad9c225
import { Check, Pencil } from 'lucide-react'
import { planActions } from '../../lib/console/interaction'
import type { DialogSession } from '../../lib/console/useDialogSession'
import type { PaneDialog } from '../../types'
import { ChatMarkdown } from '../chat/ChatMarkdown'
import { CardStatus, FreeTextEntry, KeyBadge, sessionBusy } from './cardParts'
import { InteractionCardShell } from './InteractionCardShell'
import { useDigitShortcuts } from './shortcuts'

/**
 * ExitPlanMode 的批准卡片：计划正文取对话记录（卡内滚动），动作按屏幕选项文字对上：
 * 批准并保持免授权 / 批准、改动逐个确认 / 提修改意见（先作答到该项，再在卡片里写意见提交）。
 */
export function PlanCard({ dialog, plan, session, collapsed, onCollapsedChange, draftScope }: {
  dialog: PaneDialog
  plan: string
  session: DialogSession
  collapsed: boolean
  onCollapsedChange: (collapsed: boolean) => void
  draftScope: string
}) {
  const actions = planActions(dialog)
  const busy = sessionBusy(session)
  const { phase } = session
  const feedback = actions.find((action) => action.free)
  const typing = (phase.kind === 'typing' || phase.kind === 'sending') && phase.index === feedback?.index
  const choose = (index: number) => void session.answer(index, { typing: index === feedback?.index })
  useDigitShortcuts(!collapsed && !busy && !typing, (digit) => {
    if (!actions.some((action) => action.index === digit)) return false
    choose(digit)
    return true
  })

  return <InteractionCardShell
    label="Claude 的计划在等你批准"
    collapsed={collapsed}
    onCollapsedChange={onCollapsedChange}
    barTitle="Claude 的计划在等你批准"
    header={<>
      <span className="console-chip console-chip-info">计划</span>
      <span className="console-card-title">Claude 写好了计划，等你批准</span>
    </>}
    pinned={<div className="console-actions-list" role="list" aria-label="计划的处理方式">
      {actions.map((action) => <div role="listitem" key={action.index}>
        {action.free && typing
          ? <FreeTextEntry draftKey={`${draftScope}:plan`} label="修改意见" placeholder="告诉 Claude 要怎么改，回车提交" submitLabel="提交意见" session={session}/>
          : <button type="button" className={`console-option console-action${action.tone === 'primary' ? ' console-action-primary' : ''}`} disabled={busy} aria-busy={(phase.kind === 'answering' && phase.index === action.index) || undefined} data-tooltip={action.screenLabel} onClick={() => choose(action.index)}>
            {action.free ? <Pencil size={15} aria-hidden="true"/> : <Check size={15} aria-hidden="true"/>}
            <span className="console-option-text"><span className="console-option-label">{action.label}</span></span>
            <KeyBadge index={action.index}/>
          </button>}
      </div>)}
    </div>}
    footer={<CardStatus session={session}/>}
  >
    {plan.trim()
      ? <div className="console-plan"><ChatMarkdown text={plan}/></div>
      : <p className="console-card-note">没有在对话记录里读到计划正文，请到 Shell 模式查看完整计划后再决定。</p>}
  </InteractionCardShell>
}
