import { useEffect, useMemo, useRef, useState, type ReactNode } from 'react'
import { MessageSquareWarning } from 'lucide-react'
import { findPendingInteraction, latestPlan } from '../../lib/console/interaction'
import type { ConsoleLive } from '../../lib/console/useConsoleLive'
import { useDialogSession } from '../../lib/console/useDialogSession'
import type { ChatRecord } from '../../lib/structuredChatTypes'
import type { Pane } from '../../types'
import { DialogCard } from '../DialogCard'
import { ReviewCard, TrustCard } from './ChoiceCards'
import { InteractionCardShell } from './InteractionCardShell'
import { PlanCard } from './PlanCard'
import { MultiSelectNotice, QuestionCard } from './QuestionCard'
import './console.css'
import './InteractionPanel.css'

export type InteractionPanelProps = {
  hostID: string
  pane: Pane
  /** ChatView 当前的对话记录（StructuredChatSession 的 messages）。 */
  records: readonly ChatRecord[]
  /** useConsoleLive 的读屏状态；作答后画面一变就立即重读对话框，不等下一轮。 */
  live: ConsoleLive
  /** 有卡片时为 true：ChatView 收起输入框，由卡片顶替。 */
  onActiveChange: (active: boolean) => void
  /** 多选题、认不出的情况引导去 Shell 模式；不传时只显示文字说明。 */
  onOpenShell?: () => void
}

// 收起只影响这台设备的这一页：按「主机 + pane + 这次交互」记在内存里，新的提问自动展开。
const collapsedCards = new Set<string>()

/**
 * Web UI 模式的交互卡片。对话记录里最后一个 AskUserQuestion / ExitPlanMode 还没有结果，
 * 或 pane 处于 blocked 时，每 1.5 秒读一次屏上的对话框，按 `dialog.kind` 显示提问、确认、
 * 计划或信任目录卡片；认不出的对话框交给 DialogCard。ExitPlanMode 等批准时 Herdr 报 idle，
 * 所以不能只看 Herdr 状态。作答一律走读屏校验的按键接口，结果不确定时不自动重按。
 */
export function InteractionPanel(props: InteractionPanelProps) {
  return <InteractionPanelBody key={`${props.hostID}\n${props.pane.pane_id}`} {...props}/>
}

function InteractionPanelBody({ hostID, pane, records, live, onActiveChange, onOpenShell }: InteractionPanelProps) {
  const paneID = pane.pane_id
  const pending = useMemo(() => findPendingInteraction(records), [records])
  const wanted = Boolean(pending) || pane.agent_status === 'blocked'
  const session = useDialogSession({ hostID, paneID, enabled: wanted })
  const { dialog, phase } = session
  const [, setCollapseVersion] = useState(0)

  // 作答后等确认时，画面一有变化就补读一次对话框。
  const liveKey = `${live.spinner?.raw || ''}\n${live.preview.join('\n')}`
  const confirming = phase.kind === 'confirming'
  const refresh = session.refresh
  useEffect(() => {
    if (confirming) refresh()
  }, [confirming, liveKey, refresh])

  const interaction = pending?.callID || dialog?.fingerprint || ''
  const collapseKey = `${hostID}\n${paneID}\n${interaction}`
  const collapsed = collapsedCards.has(collapseKey)
  const setCollapsed = (value: boolean) => {
    if (value) collapsedCards.add(collapseKey)
    else collapsedCards.delete(collapseKey)
    setCollapseVersion((version) => version + 1)
  }
  const draftScope = `${hostID}\n${paneID}\n${pending?.callID || dialog?.fingerprint || ''}`
  const questions = pending?.tool === 'AskUserQuestion' ? pending.questions : []
  const common = { session, collapsed, onCollapsedChange: setCollapsed }

  let card: ReactNode = null
  if (dialog?.kind === 'question') card = <QuestionCard dialog={dialog} questions={questions} draftScope={draftScope} onOpenShell={onOpenShell} {...common}/>
  else if (dialog?.kind === 'review') card = <ReviewCard dialog={dialog} {...common}/>
  else if (dialog?.kind === 'plan') card = <PlanCard dialog={dialog} plan={pending?.tool === 'ExitPlanMode' ? pending.plan : latestPlan(records)} draftScope={draftScope} {...common}/>
  else if (dialog?.kind === 'trust') card = <TrustCard dialog={dialog} {...common}/>
  else if (dialog) {
    // 其他对话框（如权限确认）由现有卡片兜底；它自己的「收起」在这里变成 44px 的条。
    card = collapsed
      ? <InteractionCardShell label="终端在等你选择" collapsed onCollapsedChange={setCollapsed} barTitle={`终端在等你选择：${dialog.title || '对话框'}`} header={null}/>
      : <DialogCard key={dialog.fingerprint} hostID={hostID} paneID={paneID} initial={dialog} primary onClose={() => setCollapsed(true)}/>
  } else if (session.loaded && questions.some((question) => question.multiSelect)) {
    // 多选题的对话框服务端不出卡片（不能一键作答），只能按对话记录说明。
    card = <InteractionCardShell
      label="Claude 在等你回答多选题"
      collapsed={collapsed}
      onCollapsedChange={setCollapsed}
      barTitle="Claude 在等你回答多选题"
      header={<><span className="console-chip"><MessageSquareWarning size={13} aria-hidden="true"/>多选题</span><span className="console-card-title">Claude 在等你回答</span></>}
    >
      {questions.filter((question) => question.multiSelect).map((question, index) => <p className="console-card-question" key={index}>{question.question}</p>)}
      <MultiSelectNotice onOpenShell={onOpenShell}/>
    </InteractionCardShell>
  }

  const active = card !== null
  const notifyRef = useRef(onActiveChange)
  notifyRef.current = onActiveChange
  useEffect(() => {
    notifyRef.current(active)
  }, [active])
  useEffect(() => () => notifyRef.current(false), [])

  // 卡片已经消失（比如对话框已在电脑上回答）时，结果说明在原处单独显示一会儿。
  const [orphan, setOrphan] = useState('')
  useEffect(() => {
    if (active || !session.notice) {
      setOrphan('')
      return
    }
    setOrphan(session.notice)
    const timer = window.setTimeout(() => setOrphan(''), 6000)
    return () => window.clearTimeout(timer)
  }, [active, session.notice])

  if (!card && !orphan) return null
  return <div className="console-interaction" data-pane-id={paneID}>
    {card || <p className="console-interaction-notice" role="status">{orphan}</p>}
  </div>
}
