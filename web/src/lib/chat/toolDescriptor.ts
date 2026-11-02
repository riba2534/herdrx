// Adapted from Cindy (https://github.com/makecindy/cindy, Apache-2.0) packages/maker-shared/src/toolUseDescriptor.ts@19dad9c225
//
// 把一次工具调用 (name, input) 解析成结构化描述，供工作记录行、动词汇总与交互卡片共用。
// 这里只输出结构化数据，不拼接面向用户的句子；input 来自会话记录，形状不可信，
// 任何分支都不抛异常，拿不到关键参数时降级成 generic。只覆盖 Claude Code 的工具名。

type TodoStatus = 'pending' | 'in_progress' | 'completed'
export type TodoItem = { content: string; status: TodoStatus; activeForm?: string }

type AskOption = { label: string; description?: string }
export type AskQuestion = { question: string; header?: string; options: AskOption[]; multiSelect: boolean }

export type ToolDescriptor =
  | {
      kind: 'command'
      toolName: string
      /** 模型填写的一句话描述（Claude Code Bash 的 `description`），trim 后非空才认。 */
      description?: string
      command: string
      background: boolean
    }
  | { kind: 'file'; toolName: string; action: 'read' | 'edit' | 'create'; filePath: string; fileName: string }
  | { kind: 'search'; toolName: string; pattern: string; path?: string }
  | { kind: 'web'; toolName: string; mode: 'fetch' | 'search'; target: string; prompt?: string }
  | { kind: 'todo'; toolName: string; todos: TodoItem[] }
  | { kind: 'task'; toolName: string; description?: string; subagentType?: string; prompt?: string }
  | { kind: 'question'; toolName: string; questions: AskQuestion[] }
  | { kind: 'plan'; toolName: string; plan: string }
  | { kind: 'mcp'; toolName: string; server: string; toolLabel: string; detail?: string }
  | { kind: 'generic'; toolName: string; detail?: string }

/** 需要用户作答的工具：在对话流里单独成卡，不收进工作记录。 */
export const INTERACTION_TOOLS: ReadonlySet<string> = new Set(['AskUserQuestion', 'ExitPlanMode'])

export function readRecord(value: unknown): Record<string, unknown> | null {
  return value && typeof value === 'object' && !Array.isArray(value) ? (value as Record<string, unknown>) : null
}

function readText(value: unknown): string | undefined {
  if (typeof value !== 'string') return undefined
  const trimmed = value.trim()
  return trimmed ? trimmed : undefined
}

/** 远端路径的文件名部分；同时认 `/` 与 `\`。 */
function basename(path: string) {
  const parts = path.split(/[\\/]+/).filter(Boolean)
  return parts[parts.length - 1] || path
}

/** 下划线转空格并收敛空白，得到可读的 token。 */
function humanizeToolToken(token: string) {
  return token.replace(/_+/g, ' ').replace(/\s+/g, ' ').trim()
}

/** 展示用截断：超出 max 时截到 max-1 并补省略号。 */
export function truncateToolText(text: string, max: number) {
  const normalized = text.trim()
  return normalized.length <= max ? normalized : `${normalized.slice(0, Math.max(0, max - 1)).trimEnd()}…`
}

const DETAIL_KEYS = ['description', 'url', 'query', 'path', 'file_path', 'title', 'name', 'prompt'] as const

function detailOf(input: Record<string, unknown> | null) {
  if (!input) return {}
  for (const key of DETAIL_KEYS) {
    const value = readText(input[key])
    if (value) return { detail: truncateToolText(value, 80) }
  }
  return {}
}

function generic(toolName: string, input: Record<string, unknown> | null): ToolDescriptor {
  return { kind: 'generic', toolName, ...detailOf(input) }
}

function fileDescriptor(toolName: string, action: 'read' | 'edit' | 'create', input: Record<string, unknown> | null): ToolDescriptor {
  const filePath = readText(input?.file_path) ?? readText(input?.notebook_path) ?? readText(input?.path)
  if (!filePath) return generic(toolName, input)
  return { kind: 'file', toolName, action, filePath, fileName: basename(filePath) }
}

function todos(value: unknown): TodoItem[] {
  if (!Array.isArray(value)) return []
  return value.flatMap((entry) => {
    const item = readRecord(entry)
    const content = readText(item?.content)
    if (!item || !content) return []
    const status = item.status === 'completed' || item.status === 'in_progress' ? item.status : 'pending'
    const activeForm = readText(item.activeForm)
    return [activeForm ? { content, status, activeForm } : { content, status }]
  })
}

export function normalizeAskQuestions(value: unknown): AskQuestion[] {
  if (!Array.isArray(value)) return []
  return value.flatMap((entry) => {
    const item = readRecord(entry)
    const question = readText(item?.question)
    if (!item || !question) return []
    const options = Array.isArray(item.options)
      ? item.options.flatMap((raw) => {
        const option = readRecord(raw)
        const label = readText(option?.label)
        if (!label) return []
        const description = readText(option?.description)
        return [description ? { label, description } : { label }]
      })
      : []
    const header = readText(item.header)
    return [{ question, ...(header ? { header } : {}), options, multiSelect: item.multiSelect === true }]
  })
}

/** 解析一次工具调用。input 可能是 null / 字符串 / 残缺对象，任何分支都不抛异常。 */
export function describeToolUse(toolName: string, rawInput: unknown): ToolDescriptor {
  const input = readRecord(rawInput)
  if (toolName.startsWith('mcp__')) {
    const segments = toolName.slice('mcp__'.length).split('__')
    const tool = segments.slice(1).join('__')
    if (segments[0] && tool) return { kind: 'mcp', toolName, server: segments[0], toolLabel: humanizeToolToken(tool), ...detailOf(input) }
    return generic(toolName, input)
  }
  switch (toolName) {
    case 'Bash': {
      const description = readText(input?.description)
      return {
        kind: 'command',
        toolName,
        ...(description ? { description } : {}),
        command: readText(input?.command) ?? '',
        background: input?.run_in_background === true,
      }
    }
    case 'Read':
      return fileDescriptor(toolName, 'read', input)
    case 'Edit':
    case 'MultiEdit':
    case 'NotebookEdit':
      return fileDescriptor(toolName, 'edit', input)
    case 'Write':
      return fileDescriptor(toolName, 'create', input)
    case 'Grep':
    case 'Glob': {
      const pattern = readText(input?.pattern)
      if (!pattern) return generic(toolName, input)
      const path = readText(input?.path)
      return { kind: 'search', toolName, pattern, ...(path ? { path } : {}) }
    }
    case 'WebFetch': {
      const url = readText(input?.url)
      if (!url) return generic(toolName, input)
      const prompt = readText(input?.prompt)
      return { kind: 'web', toolName, mode: 'fetch', target: url, ...(prompt ? { prompt } : {}) }
    }
    case 'WebSearch': {
      const query = readText(input?.query)
      return query ? { kind: 'web', toolName, mode: 'search', target: query } : generic(toolName, input)
    }
    case 'TodoWrite':
      return { kind: 'todo', toolName, todos: todos(input?.todos) }
    case 'Task':
    case 'Agent': {
      const description = readText(input?.description)
      const subagentType = readText(input?.subagent_type)
      const prompt = readText(input?.prompt)
      return { kind: 'task', toolName, ...(description ? { description } : {}), ...(subagentType ? { subagentType } : {}), ...(prompt ? { prompt } : {}) }
    }
    case 'AskUserQuestion':
      return { kind: 'question', toolName, questions: normalizeAskQuestions(input?.questions) }
    case 'ExitPlanMode':
      return { kind: 'plan', toolName, plan: typeof input?.plan === 'string' ? input.plan : '' }
    default:
      return generic(toolName, input)
  }
}
