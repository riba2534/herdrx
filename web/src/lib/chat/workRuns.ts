// Adapted from Cindy (https://github.com/makecindy/cindy, Apache-2.0) packages/maker-shared/src/workRunGrouping.ts@19dad9c225
// Adapted from Cindy (https://github.com/makecindy/cindy, Apache-2.0) packages/maker-shared/src/historyGap.ts@19dad9c225
//
// 把结构化记录排成对话流：用户提问切开轮次；一轮里的思考、工具调用和中间文字收成「工作组」，
// 最后一段连续正文是最终回复，保持可见。正在进行的那一轮只把最新一段动作留成「运行中」的组，
// 之前的过程已经完成，照常收起。纯函数，不碰 React。

import { STRUCTURED_CHAT_ELISION_PREFIX, type ChatRecord } from '../structuredChatTypes'
import { describeToolUse, INTERACTION_TOOLS, type ToolDescriptor } from './toolDescriptor'

/**
 * 分页窗口之间可能隔着大段没加载的历史（只读了尾部窗口、或者会话中间停了很久），
 * 相邻两条记录相隔超过 30 分钟时切开，不把不相干的两段并成一个「已工作 47 小时」。
 */
export const HISTORY_GAP_SPLIT_MS = 30 * 60 * 1000

/** `elided` 是服务端截断超大块后追加的说明（「…（已截断 N 字节）」），跟着被截断的那一块显示。 */
export type ToolResult = { output: string; isError: boolean; at?: number; elided?: string }

export type ToolEntry = {
  kind: 'tool'
  key: string
  recordID: string
  callID: string
  name: string
  input: unknown
  descriptor: ToolDescriptor
  /** 同一会话里 call_id 相同的工具结果；还没写盘时缺省。 */
  result?: ToolResult
  elided?: string
  at?: number
}
export type TextEntry = { kind: 'text'; key: string; recordID: string; text: string; elided?: string; at?: number }
export type ThinkingEntry = { kind: 'thinking'; key: string; recordID: string; text: string; durationMs?: number; at?: number }
/** 系统提示（中断、上下文压缩、API 错误等），原样显示。 */
export type NoticeEntry = { kind: 'system'; key: string; record: ChatRecord; at?: number }
/** Agent 工作中用户插入、被并入当前轮的消息。 */
export type QueuedEntry = { kind: 'queued'; key: string; record: ChatRecord; at?: number }
export type ChatEntry = ToolEntry | TextEntry | ThinkingEntry | NoticeEntry | QueuedEntry
export type WorkEntry = ToolEntry | ThinkingEntry | TextEntry

export type WorkBlock = {
  kind: 'work'
  key: string
  entries: WorkEntry[]
  /** 正在进行的那一段：显示计时与最近几步，不收起。 */
  live: boolean
  /** 运行中的那一段在等用户处理（Agent 状态 blocked），不是在跑。 */
  waiting?: boolean
  /** 计时起点：上一个可见边界（提问 / 上一段正文），缺失时取第一步的时间。 */
  startAt?: number
  durationMs?: number
}
export type AnswerBlock = { kind: 'answer'; key: string; entries: TextEntry[] }
export type InteractionBlock = { kind: 'interaction'; key: string; entry: ToolEntry }
export type TurnBlock = WorkBlock | AnswerBlock | InteractionBlock | NoticeEntry | QueuedEntry

export type ChatTurnView = { key: string; user?: ChatRecord; live: boolean; blocks: TurnBlock[] }

function parseAt(value?: string) {
  if (!value) return undefined
  const at = Date.parse(value)
  return Number.isNaN(at) ? undefined : at
}

type FlatItem = ChatEntry | { kind: 'user'; key: string; record: ChatRecord; at?: number }

const isElision = (text: string) => text.startsWith(STRUCTURED_CHAT_ELISION_PREFIX)

/** 记录 → 按块展开的条目。工具结果按 call_id 回填到调用上；找不到调用的结果单独成行。 */
function flatten(messages: readonly ChatRecord[]): FlatItem[] {
  const results = new Map<string, ToolResult>()
  const calls = new Set<string>()
  for (const record of messages) {
    const at = parseAt(record.at)
    let last: ToolResult | undefined
    for (const block of record.blocks) {
      if (block.type === 'tool-call' && block.call_id) calls.add(block.call_id)
      if (block.type === 'tool-result') {
        last = { output: block.output, isError: block.is_error, at }
        if (block.call_id) results.set(block.call_id, last)
      } else if (block.type === 'text' && last && isElision(block.text)) {
        last.elided = block.text
      }
    }
  }
  const items: FlatItem[] = []
  for (const record of messages) {
    const at = parseAt(record.at)
    if (record.role === 'user') {
      items.push({ kind: record.queued ? 'queued' : 'user', key: record.id, record, at })
      continue
    }
    if (record.role === 'system') {
      items.push({ kind: 'system', key: record.id, record, at })
      continue
    }
    let previous: ToolEntry | TextEntry | undefined
    let previousResult = false
    record.blocks.forEach((block, index) => {
      const key = `${record.id}:${index}`
      if (block.type === 'text') {
        if (!block.text.trim()) return
        if (isElision(block.text)) {
          // 截断说明跟着被截断的那一块；工具结果的说明已经在上面挂到结果上了。
          if (previousResult) return
          if (previous) previous.elided = block.text
          else items.push({ kind: 'system', key, record: { id: key, role: 'system', blocks: [block] }, at })
          return
        }
        previous = { kind: 'text', key, recordID: record.id, text: block.text, at }
        items.push(previous)
      } else if (block.type === 'thinking') {
        // 没有正文也没有耗时的思考块只是占位，不生成「思考了 0 秒」这样的噪音行。
        if (block.text.trim() || block.duration_ms !== undefined) {
          items.push({ kind: 'thinking', key, recordID: record.id, text: block.text, ...(block.duration_ms !== undefined ? { durationMs: block.duration_ms } : {}), at })
        }
      } else if (block.type === 'tool-call') {
        const result = block.call_id ? results.get(block.call_id) : undefined
        previous = { kind: 'tool', key, recordID: record.id, callID: block.call_id, name: block.name, input: block.input, descriptor: describeToolUse(block.name, block.input), ...(result ? { result } : {}), at }
        items.push(previous)
      } else {
        previousResult = true
        if (block.call_id && calls.has(block.call_id)) return
        // 调用在更早、尚未加载的记录里：只显示结果，名字未知。
        const result = (block.call_id && results.get(block.call_id)) || { output: block.output, isError: block.is_error, at }
        items.push({ kind: 'tool', key, recordID: record.id, callID: block.call_id, name: '', input: undefined, descriptor: describeToolUse('', undefined), result, at })
      }
      if (block.type !== 'tool-result') previousResult = false
    })
  }
  return items
}

const isAnswer = (entry: ChatEntry): entry is TextEntry => entry.kind === 'text'
const isActivity = (entry: ChatEntry): entry is ToolEntry | ThinkingEntry =>
  entry.kind === 'thinking' || (entry.kind === 'tool' && !INTERACTION_TOOLS.has(entry.name))
const isArchivable = (entry: ChatEntry): entry is WorkEntry => isActivity(entry) || isAnswer(entry)

function entryEnd(entry: ChatEntry) {
  return entry.kind === 'tool' ? entry.result?.at ?? entry.at : entry.at
}

function workBlock(run: WorkEntry[], next: ChatEntry | undefined, boundary: number | undefined): WorkBlock {
  const startAt = boundary ?? run.find((entry) => entry.at !== undefined)?.at
  let endAt = next?.at
  if (endAt === undefined) {
    for (const entry of run) {
      const end = entryEnd(entry)
      if (end !== undefined) endAt = endAt === undefined ? end : Math.max(endAt, end)
    }
  }
  const durationMs = startAt !== undefined && endAt !== undefined && endAt >= startAt ? endAt - startAt : undefined
  return { kind: 'work', key: `work:${run[0].key}`, entries: run, live: false, ...(startAt !== undefined ? { startAt } : {}), ...(durationMs !== undefined ? { durationMs } : {}) }
}

function pushStandalone(out: TurnBlock[], entry: ChatEntry) {
  if (entry.kind === 'text') {
    const last = out[out.length - 1]
    if (last?.kind === 'answer') last.entries.push(entry)
    else out.push({ kind: 'answer', key: `answer:${entry.key}`, entries: [entry] })
  } else if (entry.kind === 'tool') {
    out.push({ kind: 'interaction', key: entry.key, entry })
  } else if (entry.kind !== 'thinking') {
    out.push(entry)
  }
}

/**
 * 一段连续记录的分组。
 *
 * - 已结束的一段：最后一段连续正文之后没有动作时，它就是最终回复；之前的动作与中间文字
 *   收成完成态的工作组（被提问 / 系统提示 / 交互卡片隔开时分成多组）。最后一段正文之后
 *   还有动作（被中断、没写完）就没有最终回复，正文全部留在外面，只收动作。
 * - 进行中的一段：同样收起最新正文之前的过程；最新正文之后的动作是运行中的组。
 */
/** 最后一轮的进行状态：Agent 在工作、在等用户处理，或者没有在进行（null）。 */
export type TurnActivity = 'working' | 'blocked' | null

function groupSegment(items: readonly ChatEntry[], start: number | undefined, activity: TurnActivity): TurnBlock[] {
  const active = activity !== null
  const waiting = activity === 'blocked' ? { waiting: true } : {}
  let lastAnswer = -1
  items.forEach((entry, index) => { if (isAnswer(entry)) lastAnswer = index })
  const activityAfter = items.some((entry, index) => index > lastAnswer && isActivity(entry))
  const answered = active || (lastAnswer >= 0 && !activityAfter)
  const answers = new Set<number>()
  if (answered && lastAnswer >= 0) {
    for (let index = lastAnswer; index >= 0 && isAnswer(items[index]); index--) answers.add(index)
  }
  const keep = (entry: ChatEntry, index: number) => answered ? answers.has(index) || !isArchivable(entry) : !isActivity(entry)
  const out: TurnBlock[] = []
  let run: WorkEntry[] = []
  let boundary = start
  const flush = (next?: ChatEntry) => {
    if (run.length) out.push(workBlock(run, next, boundary))
    run = []
  }
  items.forEach((entry, index) => {
    if (!keep(entry, index)) {
      run.push(entry as WorkEntry)
      return
    }
    flush(entry)
    pushStandalone(out, entry)
    boundary = entryEnd(entry) ?? boundary
  })
  if (active && run.length) {
    const live = workBlock(run, undefined, boundary)
    out.push({ ...live, live: true, durationMs: undefined, ...waiting })
  } else {
    flush()
    // Agent 已经在工作、但这一段还没写下新的动作：留一个只有计时的运行中占位。
    if (active) out.push({ kind: 'work', key: 'work:live-tail', entries: [], live: true, ...waiting, ...(boundary !== undefined ? { startAt: boundary } : {}) })
  }
  return out
}

function groupTurn(items: readonly ChatEntry[], turnStart: number | undefined, activity: TurnActivity): TurnBlock[] {
  const out: TurnBlock[] = []
  let segment: ChatEntry[] = []
  let segmentStart = turnStart
  let previousEnd = turnStart
  for (const entry of items) {
    if (previousEnd !== undefined && entry.at !== undefined && entry.at - previousEnd > HISTORY_GAP_SPLIT_MS) {
      out.push(...groupSegment(segment, segmentStart, null))
      segment = []
      // 空洞另一侧的提问不能给这一段计时。
      segmentStart = undefined
    }
    segment.push(entry)
    const end = entryEnd(entry)
    // 并行的工具可能乱序完成；缺时间戳的条目不能抹掉已有的锚点。
    if (end !== undefined) previousEnd = previousEnd === undefined ? end : Math.max(previousEnd, end)
  }
  out.push(...groupSegment(segment, segmentStart, activity))
  return out
}

/**
 * 对话流的全部轮次。`activity` 只影响最后一轮：Agent 在工作或等用户处理时，它的最新动作
 * 保持展开、计时，不被当成已经结束的过程收起来。
 */
export function buildChatTurns(messages: readonly ChatRecord[], activity: TurnActivity): ChatTurnView[] {
  const turns: { key: string; user?: ChatRecord; at?: number; items: ChatEntry[] }[] = []
  for (const item of flatten(messages)) {
    if (item.kind === 'user') {
      turns.push({ key: item.key, user: item.record, at: item.at, items: [] })
      continue
    }
    if (!turns.length) turns.push({ key: item.key, items: [] })
    turns[turns.length - 1].items.push(item)
  }
  if (activity && !turns.length) turns.push({ key: 'turn:live', items: [] })
  return turns.map((turn, index) => {
    const current = index === turns.length - 1 ? activity : null
    return { key: turn.key, ...(turn.user ? { user: turn.user } : {}), live: current !== null, blocks: groupTurn(turn.items, turn.at, current) }
  })
}

/** 工作组里的工具描述（动词汇总与探索摘要的输入）。 */
export function workDescriptors(block: WorkBlock) {
  return block.entries.flatMap((entry) => entry.kind === 'tool' ? [entry.descriptor] : [])
}
