// Adapted from Cindy (https://github.com/makecindy/cindy, Apache-2.0) apps/desktop/src/renderer/components/new-chat/PlanActionCard.tsx@19dad9c225
import { memo } from 'react'
import { Check, Pencil } from 'lucide-react'
import { planActions } from '../../lib/console/interaction'
import { ChatMarkdown } from '../chat/ChatMarkdown'
import { answeringAt, CardStatus, FreeTextEntry, KeyBadge, typingAt, type CardProps } from './cardParts'
import { InteractionCardShell } from './InteractionCardShell'
import { useDigitShortcuts } from './shortcuts'

// 计划正文只在内容变化时重新解析 Markdown，不随作答状态重渲染。
const PlanBody = memo(function PlanBody({ plan }: { plan: string }) {
  return plan.trim()
    ? <div className="console-plan"><ChatMarkdown text={plan}/></div>
    : <p className="console-card-note">没有在对话记录里读到计划正文，请到 Shell 模式查看完整计划后再决定。</p>
})

/**
 * ExitPlanMode 的批准卡片：计划正文取对话记录（卡内滚动），动作按屏幕选项文字对上：
 * 批准并保持免授权 / 批准、改动逐个确认 / 提修改意见（先作答到该项，再在卡片里写意见提交）。
 */
export function PlanCard({ dialog, plan, session, collapsed, onCollapsedChange, draftScope }: CardProps & { plan: string; draftScope: string }) {
  const actions = planActions(dialog)
  const feedback = actions.find((action) => action.free)
  const typing = typingAt(session, feedback?.index)
  const choose = (index: number) => void session.answer(index, { typing: index === feedback?.index })
  useDigitShortcuts(!collapsed && !session.busy && !typing, actions.map((action) => action.index), choose)

  return <InteractionCardShell
    label="Claude 的计划在等你批准"
    collapsed={collapsed}
    onCollapsedChange={onCollapsedChange}
    header={<>
      <span className="console-chip console-chip-info">计划</span>
      <span className="console-card-title">Claude 写好了计划，等你批准</span>
    </>}
    pinned={<div className="console-actions-list" role="list" aria-label="计划的处理方式">
      {actions.map((action) => <div role="listitem" key={action.index}>
        {action.free && typing
          ? <FreeTextEntry draftKey={`${draftScope}:plan`} label="修改意见" placeholder="告诉 Claude 要怎么改，回车提交" submitLabel="提交意见" session={session}/>
          : <button type="button" className={`console-option console-action${action.tone === 'primary' ? ' console-action-primary' : ''}`} disabled={session.busy} aria-busy={answeringAt(session, action.index) || undefined} data-tooltip={action.screenLabel} onClick={() => choose(action.index)}>
            {action.free ? <Pencil size={15} aria-hidden="true"/> : <Check size={15} aria-hidden="true"/>}
            <span className="console-option-text"><span className="console-option-label">{action.label}</span></span>
            <KeyBadge index={action.index}/>
          </button>}
      </div>)}
    </div>}
    footer={<CardStatus session={session}/>}
  >
    <PlanBody plan={plan}/>
  </InteractionCardShell>
}
