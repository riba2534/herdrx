// Adapted from Cindy (https://github.com/makecindy/cindy, Apache-2.0) apps/desktop/src/renderer/components/chat/AgentActionRow.tsx@19dad9c225
// Adapted from Cindy (https://github.com/makecindy/cindy, Apache-2.0) apps/desktop/src/renderer/lib/agent-actions/actionPresentation.ts@19dad9c225
//
// 工作记录里的一行工具调用：状态图标 + 动词 + 文件 chip / 参数 + `+增 -删`，点开就地展开
// 输入与输出。Bash 有 description 时用它做主文案，原始命令放在下面一行；命中高风险规则的
// 命令标红（免授权模式下它已经执行过了，这里只是事后提示）。

import { useEffect, useReducer, useState } from 'react'
import { Bot, Check, ChevronRight, CircleDashed, FileText, LoaderCircle, X } from 'lucide-react'
import { diffSegmentsForTool, requestToolDiff, toolDiffSync, type ToolDiff } from '../../lib/chat/diffStats'
import { HIGH_RISK_HINT, isHighRiskShellCommand } from '../../lib/chat/riskyCommand'
import { toolInputText } from '../../lib/structuredChatMarkdown'
import { readRecord, truncateToolText, type ToolDescriptor } from '../../lib/chat/toolDescriptor'
import type { ToolEntry } from '../../lib/chat/workRuns'
import { ChatCopyButton } from './ChatCopyButton'
import { ChatMarkdown } from './ChatMarkdown'
import { DiffView } from './DiffView'
import { ElisionNote } from './ElisionNote'
import { TodoList, todoProgress } from './TodoList'

type ActionStatus = 'running' | 'done' | 'error' | 'pending'

function actionStatus(entry: ToolEntry, live: boolean): ActionStatus {
  if (entry.result) return entry.result.isError ? 'error' : 'done'
  return live ? 'running' : 'pending'
}

const STATUS_LABEL: Record<ActionStatus, string> = { running: '运行中', done: '已完成', error: '出错', pending: '未完成' }

export function isRiskyAction(entry: ToolEntry) {
  return entry.descriptor.kind === 'command' && isHighRiskShellCommand(entry.descriptor.command)
}

type Presentation = { verb?: string; param?: string; title?: string; file?: boolean; mono?: boolean; raw?: string; chip?: string }

const FILE_VERB = { read: '读取', edit: '编辑', create: '创建' }

/** 一行的文案：动词 + 最有用的那个参数。 */
function presentAction(descriptor: ToolDescriptor): Presentation {
  switch (descriptor.kind) {
    case 'command':
      return {
        ...(descriptor.description ? { param: descriptor.description, title: descriptor.command } : { verb: '运行命令' }),
        ...(descriptor.command ? { raw: descriptor.command } : {}),
        ...(descriptor.background ? { chip: '后台' } : {}),
      }
    case 'file':
      return { verb: FILE_VERB[descriptor.action], param: descriptor.fileName, title: descriptor.filePath, file: true }
    case 'search':
      return { verb: '搜索', param: descriptor.pattern, title: descriptor.path ? `${descriptor.pattern}\n${descriptor.path}` : descriptor.pattern, mono: true }
    case 'web':
      return { verb: descriptor.mode === 'fetch' ? '访问' : '搜索网页', param: descriptor.target, title: descriptor.target }
    case 'todo':
      return { verb: '更新待办', ...(descriptor.todos.length ? { param: todoProgress(descriptor.todos) } : {}) }
    case 'task':
      return { verb: '子代理', param: descriptor.description ?? '子任务', ...(descriptor.subagentType ? { chip: descriptor.subagentType } : {}) }
    case 'mcp':
      return { verb: '调用', param: `${descriptor.server} · ${descriptor.toolLabel}`, title: descriptor.detail ? `${descriptor.toolName}\n${descriptor.detail}` : descriptor.toolName }
    case 'question':
    case 'plan':
    case 'generic':
      return descriptor.toolName
        ? { verb: '调用', param: descriptor.toolName, ...('detail' in descriptor && descriptor.detail ? { title: descriptor.detail } : {}) }
        : { verb: '工具结果' }
  }
}

/** 状态图标占固定 16px 槽位；子代理完成后用机器人图标代替对勾。 */
function StatusIcon({ status, task = false }: { status: ActionStatus; task?: boolean }) {
  const Icon = status === 'running' ? LoaderCircle : task ? Bot : status === 'done' ? Check : status === 'error' ? X : CircleDashed
  return <span className="chat-row-icon" data-status={status} role="img" aria-label={STATUS_LABEL[status]}>
    <Icon size={13} className={status === 'running' ? 'spin' : undefined}/>
  </span>
}

const OUTPUT_PREVIEW_LINES = 12
const OUTPUT_PREVIEW_CHARS = 2000

/** 工具输出：长输出默认只露开头，可展开全部；错误输出用危险色。 */
function ToolOutput({ text, error = false, elided }: { text: string; error?: boolean; elided?: string }) {
  const [all, setAll] = useState(false)
  const lines = text.split('\n')
  const long = lines.length > OUTPUT_PREVIEW_LINES || text.length > OUTPUT_PREVIEW_CHARS
  const preview = long && !all ? lines.slice(0, OUTPUT_PREVIEW_LINES).join('\n').slice(0, OUTPUT_PREVIEW_CHARS) : text
  return <div className="chat-output" data-error={error || undefined}>
    {text ? <pre className="chat-output-pre" tabIndex={0} data-all={all || undefined}>{preview}{long && !all ? '\n…' : ''}</pre> : <p className="chat-detail-empty">（没有输出）</p>}
    <div className="chat-output-actions">
      {long && <button type="button" className="chat-pill chat-output-more" aria-expanded={all} onClick={() => setAll((value) => !value)}>{all ? '收起输出' : `展开全部（共 ${lines.length} 行）`}</button>}
      {text && <ChatCopyButton label="复制输出" text={text}/>}
    </div>
    <ElisionNote text={elided}/>
  </div>
}

/** 小 diff 当场同步算；大 diff 交给 Worker，算完进缓存后重新渲染一次即命中。 */
function useToolDiff(entry: ToolEntry) {
  const diff = toolDiffSync(entry.name, entry.input)
  const pending = !diff && diffSegmentsForTool(entry.name, entry.input) !== null
  const [, settled] = useReducer((count: number) => count + 1, 0)
  useEffect(() => {
    if (!pending) return
    let alive = true
    void requestToolDiff(entry.name, entry.input).then(() => { if (alive) settled() })
    return () => { alive = false }
  }, [pending, entry.name, entry.input])
  return diff
}

function inputLines(descriptor: ToolDescriptor, input: unknown): string {
  const record = readRecord(input) ?? {}
  const field = (key: string) => typeof record[key] === 'string' || typeof record[key] === 'number' ? String(record[key]) : ''
  switch (descriptor.kind) {
    case 'command':
      return `$ ${descriptor.command}`
    case 'file':
      return [descriptor.filePath, field('offset') && `起始行：${field('offset')}`, field('limit') && `行数：${field('limit')}`].filter(Boolean).join('\n')
    case 'search':
      return [`pattern: ${descriptor.pattern}`, ...['path', 'glob', 'type', 'output_mode'].flatMap((key) => field(key) ? [`${key}: ${field(key)}`] : [])].join('\n')
    case 'web':
      return descriptor.prompt ? `${descriptor.target}\n---\n${descriptor.prompt}` : descriptor.target
    default:
      return input === undefined ? '' : toolInputText(input)
  }
}

function ActionDetail({ entry, diff }: { entry: ToolEntry; diff: ToolDiff | null }) {
  const { descriptor, result } = entry
  const output = result && <ToolOutput text={result.output} error={result.isError} elided={result.elided}/>
  if (descriptor.kind === 'todo') return <TodoList todos={descriptor.todos}/>
  if (descriptor.kind === 'task') {
    return <>
      {descriptor.prompt && <details className="chat-task-prompt"><summary>任务说明</summary><p>{descriptor.prompt}</p></details>}
      {result
        ? result.isError ? output : <div className="chat-task-result"><ChatMarkdown text={result.output}/></div>
        : <p className="chat-detail-empty">子代理还没有返回结果。</p>}
    </>
  }
  if (diff) {
    return <>
      <p className="chat-detail-path" data-tooltip={descriptor.kind === 'file' ? descriptor.filePath : undefined}>{descriptor.kind === 'file' ? descriptor.filePath : entry.name}</p>
      {diff.segments.map(({ key, details }) => <DiffView key={key} details={details}/>)}
      <ElisionNote text={entry.elided}/>
      {result?.isError && output}
    </>
  }
  const input = inputLines(descriptor, entry.input)
  return <>
    {input && <pre className="chat-detail-input" tabIndex={0}>{input}</pre>}
    <ElisionNote text={entry.elided}/>
    {output}
  </>
}

export function ActionRow({ entry, live }: { entry: ToolEntry; live: boolean }) {
  const [expanded, setExpanded] = useState(false)
  const status = actionStatus(entry, live)
  const view = presentAction(entry.descriptor)
  const diff = useToolDiff(entry)
  const stats = diff && !diff.truncated ? diff.stats : null
  const risky = isRiskyAction(entry)
  const label = [view.verb, view.param].filter(Boolean).join(' ')
  return <div className="chat-action" data-tool={entry.name || undefined} data-status={status} data-risk={risky ? 'high' : undefined} data-expanded={expanded || undefined}>
    <button type="button" className="chat-action-head" aria-expanded={expanded} aria-label={`${label}（${STATUS_LABEL[status]}）`} onClick={() => setExpanded((value) => !value)}>
      <StatusIcon status={status} task={entry.descriptor.kind === 'task'}/>
      {view.verb && <span className="chat-action-verb">{view.verb}</span>}
      {view.param && (view.file
        ? <span className="chat-file-chip" data-tooltip={view.title}><FileText size={12} aria-hidden="true"/><span>{view.param}</span></span>
        : <span className={`chat-action-param${view.mono ? ' chat-mono' : ''}`} data-tooltip={view.title ?? view.param}>{truncateToolText(view.param, 160)}</span>)}
      {view.chip && <span className="chat-action-chip">{view.chip}</span>}
      <span className="chat-row-spacer"/>
      {stats && <span className="chat-diff-stat" aria-label={`新增 ${stats.add} 行，删除 ${stats.del} 行`}><span data-type="add">+{stats.add}</span><span data-type="del">-{stats.del}</span></span>}
      {risky && <span className="chat-risk" data-tooltip={HIGH_RISK_HINT}>高风险</span>}
      <span className="chat-row-chevron" aria-hidden="true"><ChevronRight size={13}/></span>
    </button>
    {view.raw && !expanded && <div className="chat-action-raw chat-mono" data-tooltip={view.raw}>{view.raw}</div>}
    {expanded && <div className="chat-action-detail"><ActionDetail entry={entry} diff={diff}/></div>}
  </div>
}
