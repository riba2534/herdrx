// Adapted from Cindy (https://github.com/makecindy/cindy, Apache-2.0) apps/desktop/src/renderer/lib/agent-actions/diffStats.ts@19dad9c225
//
// Edit / MultiEdit / Write 的行级 diff：工作记录行上的 `+增 -删` 与展开后的 diff 共用同一份结果。
// 小输入在主线程同步算（首帧就有统计），大输入交给 module Worker，Worker 不可用时退回
// 有上限的首尾预览，绝不把大段 diffLines 放回主线程。

import { diffLines } from 'diff'
import { readRecord } from './toolDescriptor'

type DiffStat = { add: number; del: number }
export type DiffLine = { type: 'del' | 'add' | 'ctx'; text: string; lineNum: number }
export type DiffDetails = {
  rows: DiffLine[]
  stats: DiffStat
  /** 输入过大，只给了首尾预览；此时统计不可信，调用方不显示 `+增 -删`。 */
  truncated?: boolean
  /** 行数超过渲染上限被截掉的行数；统计仍是精确的。 */
  omittedRows?: number
}
export type DiffSegment = { key: string; oldString: string; newString: string }
export type ToolDiff = { stats: DiffStat; segments: Array<{ key: string; details: DiffDetails }>; truncated: boolean }

/** 主线程同步计算的上限，保证一次渲染不会独占一帧。 */
export const DIFF_MAIN_THREAD_MAX_CHARS = 32_000
const DIFF_MAX_INPUT_CHARS = 1_000_000
const DIFF_MAX_SEGMENTS = 200
const DIFF_MAX_PREVIEW_ROWS = 600
const DIFF_MAX_RENDER_ROWS = 20_000
/** 超过这么多行时 DiffView 只挂载可见的行。 */
export const DIFF_VIRTUALIZE_THRESHOLD = 200
const DIFF_WORKER_TIMEOUT_MS = 5_000

/**
 * 结果按工具入参对象缓存：同一条记录在轮询合并后保持对象身份，重新渲染、收起再展开都不重算，
 * 记录被丢弃时缓存跟着回收，不额外保存任何正文副本。
 */
const cache = new WeakMap<object, ToolDiff>()

function previewLines(text: string) {
  const lines = text.replace(/\n$/, '').split('\n')
  if (!text) return []
  if (lines.length <= DIFF_MAX_PREVIEW_ROWS) return lines
  const half = DIFF_MAX_PREVIEW_ROWS / 2
  return [...lines.slice(0, half), ...lines.slice(-half)]
}

/** 输入大到不能逐行比较时，只给首尾的删除 / 新增预览。 */
function boundedDetails(oldString: string, newString: string): DiffDetails {
  const oldLines = previewLines(oldString)
  const newLines = previewLines(newString)
  let lineNum = 1
  const rows: DiffLine[] = [
    ...oldLines.map((text) => ({ type: 'del' as const, text, lineNum: lineNum++ })),
    ...newLines.map((text) => ({ type: 'add' as const, text, lineNum: lineNum++ })),
  ]
  return { rows, stats: { add: newLines.length, del: oldLines.length }, truncated: true }
}

export function computeDiffDetails(oldString: string, newString: string): DiffDetails {
  if (oldString.length + newString.length > DIFF_MAX_INPUT_CHARS) return boundedDetails(oldString, newString)
  let add = 0
  let del = 0
  let lineNum = 1
  const rows: DiffLine[] = []
  for (const change of diffLines(oldString, newString)) {
    const type: DiffLine['type'] = change.added ? 'add' : change.removed ? 'del' : 'ctx'
    for (const text of change.value.replace(/\n$/, '').split('\n')) {
      if (rows.length < DIFF_MAX_RENDER_ROWS) rows.push({ type, text, lineNum })
      lineNum++
      if (type === 'add') add++
      if (type === 'del') del++
    }
  }
  const omitted = lineNum - 1 - rows.length
  return { rows, stats: { add, del }, ...(omitted > 0 ? { omittedRows: omitted } : {}) }
}

const text = (value: unknown) => (typeof value === 'string' ? value : '')

/**
 * Edit / Write / MultiEdit 的入参统一成若干段 old → new；其它工具、或入参里一个字都没有
 * （记录残缺）时返回 null，调用方改为原样显示入参。
 */
export function diffSegmentsForTool(toolName: string, input: unknown): { segments: DiffSegment[]; truncated: boolean } | null {
  const inp = readRecord(input)
  if (!inp) return null
  let sources: { segments: DiffSegment[]; truncated: boolean } | null = null
  if (toolName === 'Edit') sources = { segments: [{ key: 'edit:0', oldString: text(inp.old_string), newString: text(inp.new_string) }], truncated: false }
  if (toolName === 'Write') sources = { segments: [{ key: 'write:0', oldString: '', newString: text(inp.content) }], truncated: false }
  if (toolName === 'MultiEdit') {
    const edits = Array.isArray(inp.edits) ? inp.edits : []
    sources = {
      segments: edits.slice(0, DIFF_MAX_SEGMENTS).map((raw, index) => {
        const edit = readRecord(raw)
        return { key: `edit:${index}`, oldString: text(edit?.old_string), newString: text(edit?.new_string) }
      }),
      truncated: edits.length > DIFF_MAX_SEGMENTS,
    }
  }
  return sources && sources.segments.some((segment) => segment.oldString || segment.newString) ? sources : null
}

const totalChars = (segments: readonly DiffSegment[]) => segments.reduce((sum, segment) => sum + segment.oldString.length + segment.newString.length, 0)

function aggregate(segments: ToolDiff['segments'], truncated: boolean): ToolDiff {
  return {
    stats: segments.reduce((total, { details }) => ({ add: total.add + details.stats.add, del: total.del + details.stats.del }), { add: 0, del: 0 }),
    segments,
    truncated: truncated || segments.some(({ details }) => details.truncated === true),
  }
}

/** 同步结果：小输入当场算，大输入（还没算过时）返回 null，由 `requestToolDiff` 交给 Worker。 */
export function toolDiffSync(toolName: string, input: unknown): ToolDiff | null {
  const key = readRecord(input)
  const hit = key && cache.get(key)
  if (hit) return hit
  const sources = diffSegmentsForTool(toolName, input)
  if (!key || !sources || totalChars(sources.segments) > DIFF_MAIN_THREAD_MAX_CHARS) return null
  const diff = aggregate(sources.segments.map((segment) => ({ key: segment.key, details: computeDiffDetails(segment.oldString, segment.newString) })), sources.truncated)
  cache.set(key, diff)
  return diff
}

let worker: Worker | null = null
let nextRequest = 0

function diffWorker() {
  if (worker || typeof Worker === 'undefined') return worker
  try {
    worker = new Worker(new URL('./diff.worker.ts', import.meta.url), { type: 'module' })
    worker.addEventListener('error', () => { worker?.terminate(); worker = null })
  } catch {
    worker = null
  }
  return worker
}

/** 首尾预览：Worker 不可用、出错或超时时的兜底，统计不可信，标成 truncated。 */
function fallback(segments: readonly DiffSegment[]) {
  return aggregate(segments.map((segment) => ({ key: segment.key, details: boundedDetails(segment.oldString, segment.newString) })), true)
}

/**
 * 异步结果：大输入在 Worker 里逐行比较；Worker 不可用、出错或超时时退回首尾预览。
 * 结果（包括兜底预览）都进缓存，之后 `toolDiffSync` 直接命中。
 */
export async function requestToolDiff(toolName: string, input: unknown): Promise<ToolDiff | null> {
  const sync = toolDiffSync(toolName, input)
  if (sync) return sync
  const key = readRecord(input)
  const sources = diffSegmentsForTool(toolName, input)
  if (!key || !sources) return null
  const target = totalChars(sources.segments) <= DIFF_MAX_INPUT_CHARS ? diffWorker() : null
  const result = target ? await inWorker(target, sources.segments, sources.truncated) : fallback(sources.segments)
  cache.set(key, result)
  return result
}

function inWorker(target: Worker, segments: DiffSegment[], truncated: boolean) {
  const id = `diff-${nextRequest++}`
  return new Promise<ToolDiff>((resolve) => {
    const done = (result: ToolDiff) => {
      target.removeEventListener('message', onMessage)
      window.clearTimeout(timer)
      resolve(result)
    }
    const onMessage = (event: MessageEvent<{ id: string; segments?: ToolDiff['segments'] }>) => {
      if (event.data?.id !== id) return
      done(event.data.segments ? aggregate(event.data.segments, truncated) : fallback(segments))
    }
    const timer = window.setTimeout(() => done(fallback(segments)), DIFF_WORKER_TIMEOUT_MS)
    target.addEventListener('message', onMessage)
    target.postMessage({ id, segments })
  })
}
