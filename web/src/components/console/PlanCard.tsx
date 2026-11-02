// Adapted from Cindy (https://github.com/makecindy/cindy, Apache-2.0) apps/desktop/src/renderer/components/new-chat/PlanActionCard.tsx@19dad9c225
import { memo } from 'react'
import { Check, Pencil } from 'lucide-react'
import { planActions } from '../../lib/console/interaction'
import { ChatMarkdown } from '../chat/ChatMarkdown'
import { answeringAt, CardStatus, FreeTextEntry, typingAt, type CardProps } from './cardParts'
import { InteractionCardShell } from './InteractionCardShell'

// 计划正文只在内容变化时重新解析 Markdown，不随作答状态重渲染。
const PlanBody = memo(function PlanBody({ plan }: { plan: string }) {
  return <div className="console-plan"><ChatMarkdown text={plan}/></div>
})

/**
 * ExitPlanMode 的批准卡片：计划正文取对话记录里还没有结果的那次 ExitPlanMode（卡内滚动），动作按
 * 屏幕选项文字对上：批准并切换到免授权 / 批准、改动逐个确认 / 提修改意见（先作答到该项，再在卡片里
 * 写意见提交）。对话记录还没追上、或屏幕上看得见的计划正文对不上时，按钮一律不可点，免得批准
 * 一份没看过的计划。只能点击，不响应数字键。
 */
export function PlanCard({ dialog, plan, matches, session, collapsed, onCollapsedChange, draftScope }: CardProps & {
  /** 还没有结果的 ExitPlanMode 的正文；对话记录还没读到时为空串。 */
  plan: string
  /** 屏幕上看得见的计划正文出自这份计划（见 planShownOnScreen）。 */
  matches: boolean
  draftScope: string
}) {
  const actions = planActions(dialog)
  const feedback = actions.find((action) => action.free)
  const typing = typingAt(session, feedback?.index)
  const loaded = plan.trim() !== ''
  const locked = session.busy || !loaded || !matches
  const choose = (index: number) => void session.answer(index, { typing: index === feedback?.index })

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
          : <button type="button" className={`console-option console-action${action.tone === 'primary' ? ' console-action-primary' : ''}`} disabled={locked} aria-busy={answeringAt(session, action.index) || undefined} data-tooltip={action.screenLabel} onClick={() => choose(action.index)}>
            {action.free ? <Pencil size={15} aria-hidden="true"/> : <Check size={15} aria-hidden="true"/>}
            <span className="console-option-text"><span className="console-option-label">{action.label}</span></span>
          </button>}
      </div>)}
    </div>}
    footer={<CardStatus session={session}/>}
  >
    {!loaded
      ? <p className="console-card-note" role="status">计划加载中…对话记录追上后才能批准。</p>
      : <>
        {!matches && <p className="console-card-note console-card-warning" role="alert">屏幕上的计划和对话记录里的这份对不上，按钮已停用。请到 Shell 模式查看后再决定。</p>}
        <PlanBody plan={plan}/>
      </>}
  </InteractionCardShell>
}
