// Adapted from Cindy (https://github.com/makecindy/cindy, Apache-2.0) apps/desktop/src/renderer/components/chat/WorkGroupBlock.tsx@19dad9c225
//
// 「工作过程」组：
//   - 运行中：组头是计时器，下面只露最近 5 步；点组头在最近 5 步与全部之间切换。全部不超过
//     5 步时组头不可点，避免「点了闪一下又弹回来」。
//   - 已完成：收起成一行「已工作 18 秒 · 编辑 1 个文件和读取 2 个文件」，展开后左侧 2px 竖线，
//     按原顺序列出中间文字、思考和每一次工具调用。
// 时间戳缺失（旧记录）时退化为「工作过程」，不显示时长。

import { useEffect, useState } from 'react'
import { ChevronRight, Layers, LoaderCircle, PauseCircle } from 'lucide-react'
import { formatChatDuration } from '../../lib/structuredChat'
import { explorationSummary, verbSummary } from '../../lib/chat/verbSummary'
import { workDescriptors, type WorkBlock, type WorkEntry } from '../../lib/chat/workRuns'
import { ActionRow, isRiskyAction } from './ActionRow'
import { ChatMarkdown } from './ChatMarkdown'
import { ChatThinking } from './ChatThinking'
import { ElisionNote } from './ElisionNote'

/** 运行中默认只露出最近这么多步。 */
const MAX_LIVE_ACTIVITIES = 5

/** 完成态组头：「已工作 18 秒 · …」；纯探索给读取 / 搜索计数，否则给动词汇总。 */
export function workSummary(block: WorkBlock) {
  const base = block.durationMs === undefined ? '工作过程' : `已工作 ${formatChatDuration(Math.max(1000, block.durationMs))}`
  const descriptors = workDescriptors(block)
  const detail = explorationSummary(descriptors) || verbSummary(descriptors)
  return detail ? `${base} · ${detail}` : base
}

function WorkEntryView({ entry, live }: { entry: WorkEntry; live: boolean }) {
  if (entry.kind === 'tool') return <ActionRow entry={entry} live={live}/>
  if (entry.kind === 'thinking') return <ChatThinking entry={entry}/>
  return <div className="chat-work-prose"><ChatMarkdown text={entry.text}/><ElisionNote text={entry.elided}/></div>
}

/** 运行中的计时：每秒只重绘这一小段文字，不带着整组工具行一起重新渲染。 */
function Elapsed({ startAt }: { startAt: number }) {
  const [now, setNow] = useState(() => Date.now())
  useEffect(() => {
    const timer = window.setInterval(() => setNow(Date.now()), 1000)
    return () => window.clearInterval(timer)
  }, [])
  return <span className="chat-work-timer">{formatChatDuration(Math.max(0, now - startAt))}</span>
}

export function WorkGroup({ block }: { block: WorkBlock }) {
  const [expanded, setExpanded] = useState(false)
  const live = block.live
  // 运行中最近 5 步之外还有内容时才能展开；已完成的组总能展开。
  const hidden = live ? Math.max(0, block.entries.length - MAX_LIVE_ACTIVITIES) : 0
  const canToggle = !live || hidden > 0
  const open = expanded && canToggle
  const shown = !live ? (open ? block.entries : []) : open ? block.entries : block.entries.slice(-MAX_LIVE_ACTIVITIES)
  const risky = live ? 0 : block.entries.filter((entry) => entry.kind === 'tool' && isRiskyAction(entry)).length
  const head = live ? <>
    <span className="chat-row-icon" aria-hidden="true">{block.waiting ? <PauseCircle size={14}/> : <LoaderCircle size={14} className="spin"/>}</span>
    <span className="chat-work-title">{block.waiting ? '等待处理…' : '正在工作…'}</span>
    {hidden > 0 && !open && <span className="chat-work-more">另有 {hidden} 步</span>}
    <span className="chat-row-spacer"/>
    {block.startAt !== undefined && <Elapsed startAt={block.startAt}/>}
  </> : <>
    <span className="chat-row-icon" aria-hidden="true"><Layers size={14}/></span>
    <span className="chat-work-title">{workSummary(block)}</span>
    {risky > 0 && <span className="chat-risk" data-tooltip="这段工作里有高风险命令，展开可查看">高风险 {risky}</span>}
    <span className="chat-row-spacer"/>
  </>
  return <section className="chat-work" data-state={live ? 'live' : 'done'} data-expanded={open || undefined} aria-label={live ? '正在进行的工作' : '工作过程'}>
    {canToggle
      ? <button type="button" className="chat-work-head" aria-expanded={open} onClick={() => setExpanded((value) => !value)}>
        {head}<span className="chat-row-chevron" aria-hidden="true"><ChevronRight size={14}/></span>
      </button>
      : <div className="chat-work-head">{head}</div>}
    {shown.length > 0 && <div className="chat-work-body">{shown.map((entry) => <WorkEntryView key={entry.key} entry={entry} live={live}/>)}</div>}
  </section>
}
