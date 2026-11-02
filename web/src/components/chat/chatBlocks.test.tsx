import { act, fireEvent, render, screen, within } from '@testing-library/react'
import { describe, expect, it, vi } from 'vitest'
import { describeToolUse } from '../../lib/chat/toolDescriptor'
import { computeDiffDetails } from '../../lib/chat/diffStats'
import type { ThinkingEntry, ToolEntry, ToolResult, WorkBlock, WorkEntry } from '../../lib/chat/workRuns'
import { ActionRow } from './ActionRow'
import { WorkGroup, workSummary } from './WorkGroup'
import { InteractionCard } from './InteractionCard'
import { ChatThinking } from './ChatThinking'
import { DiffView } from './DiffView'
import { ChatChips } from './ChatChips'

let sequence = 0
function tool(name: string, input: unknown, result?: Partial<ToolResult> & { output: string }): ToolEntry {
  const key = `k${sequence++}`
  return { kind: 'tool', key, recordID: key, callID: key, name, input, descriptor: describeToolUse(name, input), ...(result ? { result: { isError: false, ...result } } : {}) }
}
const thinking = (text: string, durationMs?: number): ThinkingEntry => ({ kind: 'thinking', key: `k${sequence++}`, recordID: 'r', text, ...(durationMs === undefined ? {} : { durationMs }) })
const work = (entries: WorkEntry[], extra: Partial<WorkBlock> = {}): WorkBlock => ({ kind: 'work', key: `w${sequence++}`, entries, live: false, ...extra })

describe('ActionRow', () => {
  it('leads with the Bash description and keeps the raw command underneath', () => {
    const { container } = render(<ActionRow entry={tool('Bash', { command: 'git status --short', description: '查看 Git 状态' }, { output: 'M a.go' })} live={false}/>)
    expect(screen.getByRole('button', { name: '查看 Git 状态（已完成）' })).toBeVisible()
    expect(container.querySelector('.chat-action-raw')).toHaveTextContent('git status --short')
    fireEvent.click(screen.getByRole('button', { name: /查看 Git 状态/ }))
    // 展开后命令与输出就地显示，下方的原始命令行收起。
    expect(container.querySelector('.chat-detail-input')).toHaveTextContent('$ git status --short')
    expect(container.querySelector('.chat-output-pre')).toHaveTextContent('M a.go')
    expect(container.querySelector('.chat-action-raw')).toBeNull()
  })

  it('marks executed high-risk commands in red after the fact', () => {
    const { container } = render(<ActionRow entry={tool('Bash', { command: 'cd repo && git push origin main', description: '推送' }, { output: '' })} live={false}/>)
    expect(container.querySelector('.chat-action')).toHaveAttribute('data-risk', 'high')
    expect(screen.getByText('高风险')).toHaveAttribute('data-tooltip', expect.stringContaining('已自动执行'))
    const safe = render(<ActionRow entry={tool('Bash', { command: 'git status' }, { output: '' })} live={false}/>)
    expect(safe.container.querySelector('[data-risk]')).toBeNull()
  })

  it('shows running, failed and unfinished states', () => {
    const running = render(<ActionRow entry={tool('Bash', { command: 'sleep 9' })} live/>)
    expect(running.container.querySelector('.chat-action')).toHaveAttribute('data-status', 'running')
    const failed = render(<ActionRow entry={tool('Bash', { command: 'false' }, { output: 'boom', isError: true })} live={false}/>)
    expect(failed.container.querySelector('.chat-action')).toHaveAttribute('data-status', 'error')
    const pending = render(<ActionRow entry={tool('Bash', { command: 'sleep 9' })} live={false}/>)
    expect(within(pending.container).getByRole('img', { name: '未完成' })).toBeInTheDocument()
  })

  it('shows a file chip, +/- stats and the diff for edits', () => {
    const { container } = render(<ActionRow entry={tool('Edit', { file_path: '/srv/app/calc.py', old_string: 'def add(a, b):\n    return a + b\n', new_string: 'def plus(a, b):\n    return a + b\n' }, { output: 'ok' })} live={false}/>)
    expect(container.querySelector('.chat-file-chip')).toHaveTextContent('calc.py')
    expect(container.querySelector('.chat-file-chip')).toHaveAttribute('data-tooltip', '/srv/app/calc.py')
    expect(screen.getByLabelText('新增 1 行，删除 1 行')).toHaveTextContent('+1-1')
    fireEvent.click(screen.getByRole('button', { name: /编辑 calc.py/ }))
    const rows = Array.from(container.querySelectorAll('.chat-diff-row'), (row) => `${row.getAttribute('data-type')}:${row.querySelector('.chat-diff-text')?.textContent}`)
    expect(rows).toEqual(['del:def add(a, b):', 'add:def plus(a, b):', 'ctx:    return a + b'])
  })

  it('truncates long output and expands it in full', () => {
    const output = Array.from({ length: 40 }, (_, index) => `line ${index + 1}`).join('\n')
    const { container } = render(<ActionRow entry={tool('Bash', { command: 'cat big.log' }, { output })} live={false}/>)
    fireEvent.click(screen.getByRole('button', { name: /运行命令/ }))
    const pre = container.querySelector('.chat-output-pre') as HTMLElement
    expect(pre).toHaveTextContent('line 12')
    expect(pre).not.toHaveTextContent('line 13')
    fireEvent.click(screen.getByRole('button', { name: '展开全部（共 40 行）' }))
    expect(pre).toHaveTextContent('line 40')
    expect(screen.getByRole('button', { name: '收起输出' })).toHaveAttribute('aria-expanded', 'true')
  })

  it('shows the elision note for outputs the server cut short', () => {
    render(<ActionRow entry={tool('Bash', { command: 'cat x' }, { output: 'x', elided: '…（已截断 4096 字节）' })} live={false}/>)
    fireEvent.click(screen.getByRole('button', { name: /运行命令/ }))
    expect(screen.getByRole('note')).toHaveTextContent('已截断 4096 字节')
  })

  it('renders TodoWrite as a checklist with progress', () => {
    const { container } = render(<ActionRow entry={tool('TodoWrite', { todos: [{ content: '加函数', status: 'completed' }, { content: '补测试', status: 'in_progress', activeForm: '正在补测试' }, { content: '推送', status: 'pending' }] }, { output: 'ok' })} live={false}/>)
    expect(screen.getByRole('button', { name: /更新待办 1\/3 已完成/ })).toBeVisible()
    fireEvent.click(screen.getByRole('button', { name: /更新待办/ }))
    expect(Array.from(container.querySelectorAll('.chat-todo-item'), (item) => `${item.getAttribute('data-status')}:${item.textContent}`)).toEqual(['completed:加函数', 'in_progress:正在补测试', 'pending:推送'])
  })

  it('shows a subagent task with its prompt and Markdown result', () => {
    const { container } = render(<ActionRow entry={tool('Task', { description: '复查覆盖', subagent_type: 'general-purpose', prompt: '检查边界' }, { output: '## 结论\n\n- **缺** 负数用例' })} live={false}/>)
    expect(screen.getByText('general-purpose')).toBeVisible()
    fireEvent.click(screen.getByRole('button', { name: /子代理 复查覆盖/ }))
    expect(screen.getByText('任务说明')).toBeVisible()
    expect(container.querySelector('.chat-task-result h2')).toHaveTextContent('结论')
    expect(container.querySelector('.chat-task-result strong')).toHaveTextContent('缺')
  })

  it('names MCP tools by server and readable tool, and orphan results as tool results', () => {
    render(<ActionRow entry={tool('mcp__docs__read_page', { url: 'https://example.test' }, { output: 'x' })} live={false}/>)
    expect(screen.getByRole('button', { name: /调用 docs · read page/ })).toBeVisible()
    render(<ActionRow entry={tool('', undefined, { output: 'orphan' })} live={false}/>)
    expect(screen.getByRole('button', { name: /工具结果/ })).toBeVisible()
  })
})

describe('WorkGroup', () => {
  const finished = () => work([
    thinking('先想想', 2000),
    { kind: 'text', key: 'p1', recordID: 'p1', text: '我先看看结构' },
    tool('Read', { file_path: '/a.py' }, { output: 'x' }),
    tool('Grep', { pattern: 'def' }, { output: 'a.py' }),
  ], { durationMs: 18_000 })

  it('collapses a finished group into one summary line with the exploration counts', () => {
    const block = finished()
    render(<WorkGroup block={block}/>)
    const head = screen.getByRole('button', { name: '已工作 18 秒 · 读取 1 个文件 · 搜索 1 次' })
    expect(head).toHaveAttribute('aria-expanded', 'false')
    expect(screen.queryByText('我先看看结构')).toBeNull()
    fireEvent.click(head)
    expect(screen.getByText('我先看看结构')).toBeVisible()
    expect(screen.getByRole('button', { name: /思考了 2 秒/ })).toBeVisible()
    expect(screen.getByRole('button', { name: /读取 a.py/ })).toBeVisible()
  })

  it('uses the verb sentence when the group changes things, and a plain label without timestamps', () => {
    expect(workSummary(work([tool('Edit', { file_path: '/a' }), tool('Bash', { command: 'x' })], { durationMs: 400 }))).toBe('已工作 1 秒 · 编辑 1 个文件和运行 1 条命令')
    expect(workSummary(work([thinking('嗯')]))).toBe('工作过程')
  })

  it('flags a collapsed group that contains high-risk commands', () => {
    render(<WorkGroup block={work([tool('Bash', { command: 'sudo rm -rf /opt/x' }, { output: '' }), tool('Bash', { command: 'ls' }, { output: '' })], { durationMs: 1000 })}/>)
    expect(screen.getByText('高风险 1')).toBeVisible()
  })

  it('shows only the latest five steps while running and toggles the rest', () => {
    const steps = Array.from({ length: 7 }, (_, index) => tool('Read', { file_path: `/f${index}.py` }, { output: '' }))
    const { container } = render(<WorkGroup block={work(steps, { live: true, startAt: Date.now() - 5000 })}/>)
    expect(screen.getByText('正在工作…')).toBeVisible()
    expect(container.querySelector('.chat-work-timer')).toHaveTextContent('5 秒')
    const names = () => Array.from(container.querySelectorAll('.chat-file-chip'), (chip) => chip.textContent)
    expect(names()).toEqual(['f2.py', 'f3.py', 'f4.py', 'f5.py', 'f6.py'])
    fireEvent.click(screen.getByRole('button', { name: /另有 2 步/ }))
    expect(names()).toHaveLength(7)
  })

  it('does not offer a toggle when the live preview already shows everything', () => {
    render(<WorkGroup block={work([tool('Read', { file_path: '/a' })], { live: true, waiting: true })}/>)
    expect(screen.getByText('等待处理…')).toBeVisible()
    expect(screen.queryByRole('button', { name: /等待处理/ })).toBeNull()
  })

  it('ticks the live timer', () => {
    vi.useFakeTimers()
    const start = Date.now()
    const { container } = render(<WorkGroup block={work([], { live: true, startAt: start })}/>)
    expect(container.querySelector('.chat-work-timer')).toHaveTextContent('0 秒')
    act(() => { vi.advanceTimersByTime(3000) })
    expect(container.querySelector('.chat-work-timer')).toHaveTextContent('3 秒')
    vi.useRealTimers()
  })
})

describe('InteractionCard', () => {
  it('shows question → answer once AskUserQuestion is answered', () => {
    const entry = tool('AskUserQuestion', { questions: [{ question: '函数名用 add 还是 plus？', header: '函数名', options: [{ label: 'add' }, { label: 'plus' }] }] }, { output: 'Your questions have been answered: "函数名用 add 还是 plus？"="add". You can now continue.' })
    const { container } = render(<InteractionCard entry={entry}/>)
    expect(screen.getByText('已回答')).toBeVisible()
    expect(screen.getByText('函数名')).toBeVisible()
    expect(container.querySelector('.chat-ask-answer')).toHaveTextContent('→ add')
    expect(container.querySelector('.chat-ask-options')).toBeNull()
  })

  it('collapses a waiting question into one line and opens the options read-only', () => {
    const entry = tool('AskUserQuestion', { questions: [{ question: '选哪个？', options: [{ label: 'json', description: '通用' }, { label: 'yaml' }] }, { question: '要注解吗？', options: [{ label: '要' }] }] })
    const { container } = render(<InteractionCard entry={entry}/>)
    // 作答入口在输入框位置的交互卡片里：流里只占一行，不重复展开题目和选项。
    const head = screen.getByRole('button', { name: '等待你回答 · 2 个问题' })
    expect(head).toHaveAttribute('aria-expanded', 'false')
    expect(screen.queryByText('选哪个？')).toBeNull()
    expect(container.querySelector('.chat-ask-options')).toBeNull()
    fireEvent.click(head)
    expect(screen.getByText('选哪个？')).toBeVisible()
    expect(Array.from(container.querySelectorAll('.chat-ask-options'), (list) => list.textContent)).toEqual(['json通用yaml', '要'])
    expect(container.querySelectorAll('button')).toHaveLength(1)
  })

  it('collapses a plan awaiting approval and opens the plan read-only', () => {
    const { container } = render(<InteractionCard entry={tool('ExitPlanMode', { plan: '# 改名计划\n\n- 改 calc.py' })}/>)
    const head = screen.getByRole('button', { name: '计划等待批准' })
    expect(container.querySelector('h1')).toBeNull()
    expect(screen.queryByText('已批准')).toBeNull()
    fireEvent.click(head)
    expect(head).toHaveAttribute('aria-expanded', 'true')
    expect(container.querySelector('h1')).toHaveTextContent('改名计划')
    expect(container.querySelectorAll('button')).toHaveLength(1)
  })

  it('folds the plan body and reports approval', () => {
    const { container } = render(<InteractionCard entry={tool('ExitPlanMode', { plan: '# 改名计划\n\n- 改 calc.py' }, { output: 'User has approved your plan.' })}/>)
    expect(screen.getByText('已批准')).toBeVisible()
    const body = container.querySelector('.chat-plan-body') as HTMLElement
    expect(body).not.toHaveAttribute('data-open')
    expect(body.querySelector('h1')).toHaveTextContent('改名计划')
    fireEvent.click(screen.getByRole('button', { name: '展开计划' }))
    expect(body).toHaveAttribute('data-open', 'true')
  })

  it('shows the rejection with the user feedback', () => {
    render(<InteractionCard entry={tool('ExitPlanMode', { plan: '# 计划' }, { output: 'The user doesn\'t want to proceed with this tool use. To tell you how to proceed, the user said:\n顺便加上类型注解', isError: true })}/>)
    expect(screen.getByText('未批准')).toBeVisible()
    expect(screen.getByText('顺便加上类型注解')).toBeVisible()
  })
})

describe('ChatThinking', () => {
  it('stays collapsed as “思考了 3 秒” with a one-line preview until opened', () => {
    const { container } = render(<ChatThinking entry={thinking('先读 **calc.py**\n再改 `add`', 2600)}/>)
    const head = screen.getByRole('button', { name: /思考了 3 秒/ })
    expect(head).toHaveAttribute('aria-expanded', 'false')
    expect(container.querySelector('.chat-thinking-body')).toBeNull()
    fireEvent.click(head)
    expect(container.querySelector('.chat-thinking-body strong')).toHaveTextContent('calc.py')
    expect(container.querySelector('.chat-thinking-body code')).toHaveTextContent('add')
  })

  it('shows a duration-only line for thinking without text', () => {
    render(<ChatThinking entry={thinking('', 800)}/>)
    expect(screen.getByText('思考了 1 秒')).toBeVisible()
    expect(screen.queryByRole('button')).toBeNull()
  })
})

describe('DiffView', () => {
  it('mounts only the visible window of a long diff', () => {
    const details = computeDiffDetails('', Array.from({ length: 1000 }, (_, index) => `line ${index}`).join('\n'))
    const { container } = render(<DiffView details={details}/>)
    const view = container.querySelector('.chat-diff') as HTMLElement
    expect(view).toHaveAttribute('data-virtual', 'true')
    const mounted = () => container.querySelectorAll('.chat-diff-row').length
    expect(mounted()).toBeLessThan(100)
    expect(container.querySelector('.chat-diff-row .chat-diff-text')).toHaveTextContent('line 0')
    view.scrollTop = 20 * 500
    fireEvent.scroll(view)
    expect(mounted()).toBeLessThan(100)
    expect(Array.from(container.querySelectorAll('.chat-diff-text'), (node) => node.textContent)).toContain('line 500')
  })

  it('renders short diffs in full and notes previews', () => {
    const { container } = render(<DiffView details={{ ...computeDiffDetails('a', 'b'), truncated: true }}/>)
    expect(container.querySelector('.chat-diff')).not.toHaveAttribute('data-virtual')
    expect(container.querySelectorAll('.chat-diff-row')).toHaveLength(2)
    expect(screen.getByRole('note')).toHaveTextContent('只显示开头和结尾')
  })
})

describe('ChatChips', () => {
  it('shows exactly one of jump-to-bottom or the unread count', () => {
    const onJump = vi.fn()
    const { rerender } = render(<ChatChips visible unread={0} onJump={onJump}/>)
    fireEvent.click(screen.getByRole('button', { name: '跳到底部' }))
    expect(onJump).toHaveBeenCalledTimes(1)
    rerender(<ChatChips visible unread={3} onJump={onJump}/>)
    expect(screen.queryByRole('button', { name: '跳到底部' })).toBeNull()
    expect(screen.getByRole('button', { name: '回到底部，查看 3 条新消息' })).toHaveTextContent('3 条新消息')
    rerender(<ChatChips visible={false} unread={3} onJump={onJump}/>)
    expect(screen.queryByRole('button')).toBeNull()
  })
})
