import { useEffect, useMemo, useRef, useState, type ReactNode } from 'react'
import { MessageSquareWarning } from 'lucide-react'
import { findPendingInteraction, planShownOnScreen } from '../../lib/console/interaction'
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
  /** 有展开的卡片时为 true：ChatView 收起输入框，由卡片顶替；卡片收成 44px 的条时为 false，输入框回来。 */
  onActiveChange: (active: boolean) => void
  /** 多选题、认不出的情况引导去 Shell 模式；不传时只显示文字说明。 */
  onOpenShell?: () => void
  /** 有卡片（展开或收起）或读屏认出对话框时为 true：这时不能往终端按 Esc、Shift+Tab。 */
  onDialogChange?: (present: boolean) => void
}

/** 连续这么多次读不到对话框、pane 又不是 blocked，就收起卡片、停止读屏（Claude 被杀等拿不到结果的情况）。 */
export const MISSES_BEFORE_IDLE = 3

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

function InteractionPanelBody({ hostID, pane, records, live, onActiveChange, onOpenShell, onDialogChange }: InteractionPanelProps) {
  const paneID = pane.pane_id
  const pending = useMemo(() => findPendingInteraction(records), [records])
  const blocked = pane.agent_status === 'blocked'
  // 读不到对话框时停下来，等对话记录或 pane 状态有变化再恢复。
  const [idleSince, setIdleSince] = useState<{ records: readonly ChatRecord[]; status: string } | null>(null)
  const idle = idleSince !== null && idleSince.records === records && idleSince.status === pane.agent_status
  const wanted = (Boolean(pending) || blocked) && !idle
  const session = useDialogSession({ hostID, paneID, enabled: wanted })
  const { dialog, phase } = session
  useEffect(() => {
    if (session.misses >= MISSES_BEFORE_IDLE && !blocked && phase.kind === 'idle') setIdleSince({ records, status: pane.agent_status })
  }, [session.misses, blocked, phase.kind, records, pane.agent_status])
  const [, setCollapseVersion] = useState(0)
  // 兜底卡片（DialogCard）自己读屏，对话框消失时也会调用 onClose：只在这个对话框还在时保持收起。
  const [fallbackFolded, setFallbackFolded] = useState('')
  if (fallbackFolded && dialog?.fingerprint !== fallbackFolded) setFallbackFolded('')

  // 作答后等确认时，画面一有变化就补读一次对话框。
  const confirming = phase.kind === 'confirming'
  const liveKey = confirming ? `${live.spinner?.verb ?? ''}:${live.spinner?.seconds ?? ''}\n${live.preview.join('\n')}` : ''
  const refresh = session.refresh
  useEffect(() => {
    if (confirming) refresh()
  }, [confirming, liveKey, refresh])

  // 这次交互的键：收起状态与自由填写草稿都按它保存。
  const interaction = `${hostID}\n${paneID}\n${pending?.callID || dialog?.fingerprint || ''}`
  const collapsed = collapsedCards.has(interaction)
  const setCollapsed = (value: boolean) => {
    if (value) collapsedCards.add(interaction)
    else collapsedCards.delete(interaction)
    setCollapseVersion((version) => version + 1)
  }
  const questions = pending?.tool === 'AskUserQuestion' ? pending.questions : []
  const multiSelect = questions.filter((question) => question.multiSelect)
  const common = { session, collapsed, onCollapsedChange: setCollapsed }
  // 认不出的对话框交给 DialogCard，它的收起状态单独记。
  const fallback = dialog && !dialog.kind ? dialog : null
  const folded = fallback ? fallbackFolded === fallback.fingerprint : collapsed

  let card: ReactNode = null
  if (dialog?.kind === 'question') card = <QuestionCard dialog={dialog} questions={questions} draftScope={interaction} onOpenShell={onOpenShell} {...common}/>
  else if (dialog?.kind === 'review') card = <ReviewCard dialog={dialog} {...common}/>
  else if (dialog?.kind === 'plan') {
    // 只用还没有结果的那次 ExitPlanMode：旧计划配上屏幕上新计划的按钮，会批准一份没看过的计划。
    const plan = pending?.tool === 'ExitPlanMode' ? pending.plan : ''
    card = <PlanCard dialog={dialog} plan={plan} matches={planShownOnScreen(plan, dialog, live.preview)} draftScope={interaction} {...common}/>
  }
  else if (dialog?.kind === 'trust') card = <TrustCard dialog={dialog} {...common}/>
  else if (fallback) {
    // 其他对话框（如权限确认）由现有卡片兜底；它自己的「收起」在这里变成 44px 的条。
    card = folded
      ? <InteractionCardShell label="终端在等你选择" collapsed onCollapsedChange={() => setFallbackFolded('')} barTitle={`终端在等你选择：${fallback.title || '对话框'}`} header={null}/>
      : <DialogCard key={fallback.fingerprint} hostID={hostID} paneID={paneID} initial={fallback} primary onClose={() => setFallbackFolded(fallback.fingerprint)}/>
  } else if (session.loaded && multiSelect.length > 0) {
    // 多选题的对话框服务端不出卡片（不能一键作答），只能按对话记录说明。
    card = <InteractionCardShell
      label="Claude 在等你回答多选题"
      collapsed={collapsed}
      onCollapsedChange={setCollapsed}
      header={<><span className="console-chip"><MessageSquareWarning size={13} aria-hidden="true"/>多选题</span><span className="console-card-title">Claude 在等你回答</span></>}
    >
      {multiSelect.map((question, index) => <p className="console-card-question" key={index}>{question.question}</p>)}
      <MultiSelectNotice onOpenShell={onOpenShell}/>
    </InteractionCardShell>
  }

  const shown = card !== null
  const active = shown && !folded
  const present = shown || dialog !== null
  const notifyRef = useRef({ onActiveChange, onDialogChange })
  notifyRef.current = { onActiveChange, onDialogChange }
  useEffect(() => {
    notifyRef.current.onActiveChange(active)
  }, [active])
  useEffect(() => {
    notifyRef.current.onDialogChange?.(present)
  }, [present])
  useEffect(() => () => {
    notifyRef.current.onActiveChange(false)
    notifyRef.current.onDialogChange?.(false)
  }, [])

  // 卡片已经消失（比如对话框已在电脑上回答）时，结果说明在原处单独显示一会儿。
  const [orphan, setOrphan] = useState('')
  useEffect(() => {
    if (shown || !session.notice) {
      setOrphan('')
      return
    }
    setOrphan(session.notice)
    const timer = window.setTimeout(() => setOrphan(''), 6000)
    return () => window.clearTimeout(timer)
  }, [shown, session.notice])

  if (!card && !orphan) return null
  return <div className="console-interaction">
    {card || <p className="console-interaction-notice" role="status">{orphan}</p>}
  </div>
}
