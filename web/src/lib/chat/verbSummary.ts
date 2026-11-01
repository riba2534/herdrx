// Adapted from Cindy (https://github.com/makecindy/cindy, Apache-2.0) apps/desktop/src/renderer/lib/agent-actions/verbAggregator.ts@19dad9c225
// Adapted from Cindy (https://github.com/makecindy/cindy, Apache-2.0) packages/maker-shared/src/workActivityProjection.ts@19dad9c225
//
// 把一段工作记录里的工具调用汇总成一句话：「编辑 2 个文件、运行 3 条命令和读取 1 个文件」。
// 工具名 → 动词是固定映射，排序固定，同一组动作无论到达顺序都得到同一句话；超过 5 种动词时
// 收成「另外 N 项」。纯读取 / 搜索的一组另给「读取 N 个文件 · 搜索 N 次」的探索摘要。

import type { ToolDescriptor } from './toolDescriptor'

export type Verb = 'edited' | 'created' | 'ran' | 'read' | 'updated' | 'searched' | 'fetched' | 'delegated' | 'used'

const TOOL_TO_VERB: Record<string, Verb> = {
  Edit: 'edited',
  MultiEdit: 'edited',
  NotebookEdit: 'edited',
  Write: 'created',
  Bash: 'ran',
  Read: 'read',
  TodoWrite: 'updated',
  Glob: 'searched',
  Grep: 'searched',
  WebFetch: 'fetched',
  WebSearch: 'fetched',
  Task: 'delegated',
  Agent: 'delegated',
}

const ORDER: Verb[] = ['edited', 'ran', 'read', 'updated', 'created', 'searched', 'fetched', 'delegated', 'used']

const PART: Record<Verb, (count: number) => string> = {
  edited: (count) => `编辑 ${count} 个文件`,
  created: (count) => `创建 ${count} 个文件`,
  ran: (count) => `运行 ${count} 条命令`,
  read: (count) => `读取 ${count} 个文件`,
  updated: () => '更新待办',
  searched: () => '执行搜索',
  fetched: () => '访问网络',
  delegated: (count) => `委派 ${count} 个子任务`,
  used: (count) => `调用 ${count} 个工具`,
}

function verbForTool(toolName: string): Verb {
  return TOOL_TO_VERB[toolName] ?? 'used'
}

/** 文件类动词按不同路径计数（同一个文件读两次仍是「读取 1 个文件」），其余按调用次数。 */
function counter(descriptors: readonly ToolDescriptor[]) {
  const files = new Map<Verb, Set<string>>()
  const counts = new Map<Verb, number>()
  for (const descriptor of descriptors) {
    const verb = verbForTool(descriptor.toolName)
    if (descriptor.kind === 'file') {
      const seen = files.get(verb) ?? new Set<string>()
      seen.add(descriptor.filePath)
      files.set(verb, seen)
      counts.set(verb, seen.size)
    } else {
      counts.set(verb, (counts.get(verb) ?? 0) + 1)
    }
  }
  return counts
}

/** 「编辑 2 个文件、运行 3 条命令和读取 1 个文件」；没有工具调用时返回空串。 */
export function verbSummary(descriptors: readonly ToolDescriptor[]) {
  const counts = counter(descriptors)
  const parts = ORDER.flatMap((verb) => {
    const count = counts.get(verb)
    return count === undefined ? [] : [PART[verb](count)]
  })
  if (parts.length > 5) parts.splice(5, parts.length - 5, `另外 ${parts.length - 5} 项`)
  if (parts.length <= 1) return parts[0] ?? ''
  return `${parts.slice(0, -1).join('、')}和${parts[parts.length - 1]}`
}

/**
 * 纯探索（全是 Read / Grep / Glob）时的摘要：「读取 2 个文件 · 搜索 1 次」。
 * 夹着任何一个会改东西或跑命令的工具就不是纯探索，返回空串，由调用方改用动词汇总。
 */
export function explorationSummary(descriptors: readonly ToolDescriptor[]) {
  if (!descriptors.length) return ''
  const reads = new Set<string>()
  let searches = 0
  for (const descriptor of descriptors) {
    if (descriptor.kind === 'file' && descriptor.action === 'read') reads.add(descriptor.filePath)
    else if (descriptor.kind === 'search') searches++
    else return ''
  }
  return [reads.size ? `读取 ${reads.size} 个文件` : '', searches ? `搜索 ${searches} 次` : ''].filter(Boolean).join(' · ')
}
