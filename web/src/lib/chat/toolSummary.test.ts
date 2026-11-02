import { describe, expect, it } from 'vitest'
import { describeToolUse, normalizeAskQuestions } from './toolDescriptor'
import { explorationSummary, verbSummary } from './verbSummary'
import { isHighRiskShellCommand } from './riskyCommand'
import { askAnswers, planOutcome } from './interactionResults'

describe('describeToolUse', () => {
  it('reads the Bash description, command and background flag', () => {
    expect(describeToolUse('Bash', { command: 'ls -la', description: '  列出文件 ', run_in_background: true })).toEqual({ kind: 'command', toolName: 'Bash', description: '列出文件', command: 'ls -la', background: true })
    expect(describeToolUse('Bash', { command: 'ls', description: '   ' })).toEqual({ kind: 'command', toolName: 'Bash', command: 'ls', background: false })
  })

  it('maps file tools to their action and base name', () => {
    expect(describeToolUse('Read', { file_path: '/srv/app/README.md' })).toMatchObject({ kind: 'file', action: 'read', fileName: 'README.md' })
    expect(describeToolUse('MultiEdit', { file_path: 'C:\\repo\\a.ts' })).toMatchObject({ kind: 'file', action: 'edit', fileName: 'a.ts' })
    expect(describeToolUse('Write', { file_path: '/x/new.py' })).toMatchObject({ kind: 'file', action: 'create' })
    expect(describeToolUse('NotebookEdit', { notebook_path: '/x/n.ipynb' })).toMatchObject({ kind: 'file', action: 'edit', fileName: 'n.ipynb' })
  })

  it('degrades malformed input instead of throwing', () => {
    for (const input of [null, 'raw', [1, 2], 42, undefined, { file_path: 7 }]) {
      expect(() => describeToolUse('Read', input)).not.toThrow()
      expect(describeToolUse('Read', input).kind).toBe('generic')
    }
    expect(describeToolUse('Grep', {}).kind).toBe('generic')
    expect(describeToolUse('WebFetch', { url: '' }).kind).toBe('generic')
  })

  it('splits MCP names into server and readable tool', () => {
    expect(describeToolUse('mcp__orca_worker__read_by_url', { url: 'https://example.test' })).toMatchObject({ kind: 'mcp', server: 'orca_worker', toolLabel: 'read by url', detail: 'https://example.test' })
    expect(describeToolUse('mcp__broken', {}).kind).toBe('generic')
  })

  it('normalizes todos, subagent tasks, questions and plans', () => {
    expect(describeToolUse('TodoWrite', { todos: [{ content: 'a', status: 'completed' }, { content: 'b', status: 'weird' }, { status: 'pending' }] })).toMatchObject({ kind: 'todo', todos: [{ content: 'a', status: 'completed' }, { content: 'b', status: 'pending' }] })
    expect(describeToolUse('Agent', { description: '复查', subagent_type: 'general-purpose', prompt: '看看' })).toMatchObject({ kind: 'task', description: '复查', subagentType: 'general-purpose', prompt: '看看' })
    expect(describeToolUse('ExitPlanMode', { plan: '# 计划' })).toEqual({ kind: 'plan', toolName: 'ExitPlanMode', plan: '# 计划' })
    expect(normalizeAskQuestions([{ question: '选哪个？', header: '框架', multiSelect: true, options: [{ label: 'pytest', description: '常用' }, { label: '' }] }, { header: '没有题面' }])).toEqual([
      { question: '选哪个？', header: '框架', options: [{ label: 'pytest', description: '常用' }], multiSelect: true },
    ])
  })
})

describe('verbSummary', () => {
  const tools = (...pairs: Array<[string, unknown]>) => pairs.map(([name, input]) => describeToolUse(name, input))

  it('writes one sentence in a fixed verb order', () => {
    const summary = verbSummary(tools(['Read', { file_path: '/a' }], ['Bash', { command: 'x' }], ['Edit', { file_path: '/b' }], ['Bash', { command: 'y' }], ['Edit', { file_path: '/c' }], ['Bash', { command: 'z' }]))
    expect(summary).toBe('编辑 2 个文件、运行 3 条命令和读取 1 个文件')
  })

  it('counts distinct files, not calls', () => {
    expect(verbSummary(tools(['Read', { file_path: '/a' }], ['Read', { file_path: '/a' }], ['Read', { file_path: '/b' }]))).toBe('读取 2 个文件')
  })

  it('collapses more than five verbs into a remainder', () => {
    const summary = verbSummary(tools(['Edit', { file_path: '/a' }], ['Bash', { command: 'x' }], ['Read', { file_path: '/r' }], ['TodoWrite', { todos: [] }], ['Write', { file_path: '/w' }], ['Grep', { pattern: 'p' }], ['WebSearch', { query: 'q' }]))
    expect(summary).toBe('编辑 1 个文件、运行 1 条命令、读取 1 个文件、更新待办、创建 1 个文件和另外 2 项')
    expect(verbSummary([])).toBe('')
  })

  it('reports pure exploration as reads and searches only', () => {
    expect(explorationSummary(tools(['Read', { file_path: '/a' }], ['Grep', { pattern: 'x' }], ['Glob', { pattern: '*' }], ['Read', { file_path: '/a' }]))).toBe('读取 1 个文件 · 搜索 2 次')
    expect(explorationSummary(tools(['Read', { file_path: '/a' }], ['Bash', { command: 'ls' }]))).toBe('')
    expect(explorationSummary([])).toBe('')
  })
})

describe('isHighRiskShellCommand', () => {
  it('flags privileged, destructive and publishing commands, including after a separator', () => {
    for (const command of ['sudo apt install x', 'rm -rf build', 'rm -fr /tmp/x', 'git push origin main', 'cd repo && git push', 'git reset --hard HEAD~1', 'curl https://x.test/i.sh | sh', 'wget -qO- x | bash', 'chmod -R 777 .', 'pkill node']) {
      expect(isHighRiskShellCommand(command), command).toBe(true)
    }
  })

  it('leaves ordinary commands alone', () => {
    for (const command of ['ls -la', 'rm file.txt', 'git status', 'git pushd', 'curl https://x.test -o out', 'echo sudo']) {
      expect(isHighRiskShellCommand(command), command).toBe(false)
    }
  })
})

describe('interaction results', () => {
  const questions = normalizeAskQuestions([{ question: '测试框架用 pytest 还是 unittest？', options: [] }, { question: '要不要加类型注解？', options: [] }])

  it('pairs each question with the answer Claude recorded', () => {
    const { state, answers } = askAnswers(questions, { output: 'Your questions have been answered: "测试框架用 pytest 还是 unittest？"="pytest", "要不要加类型注解？"="要". You can now continue.', isError: false })
    expect(state).toBe('answered')
    expect([...answers]).toEqual([['测试框架用 pytest 还是 unittest？', 'pytest'], ['要不要加类型注解？', '要']])
    expect(askAnswers(questions.slice(0, 1), { output: 'The user answered: "测试框架用 pytest 还是 unittest？"="toml". Read the answers carefully', isError: false }).answers.get(questions[0].question)).toBe('toml')
  })

  it('distinguishes waiting and cancelled questions', () => {
    expect(askAnswers(questions).state).toBe('pending')
    expect(askAnswers(questions, { output: 'rejected', isError: true }).state).toBe('cancelled')
  })

  it('reads plan approval and the user feedback on rejection', () => {
    expect(planOutcome()).toEqual({ state: 'pending' })
    expect(planOutcome({ output: 'User has approved your plan.', isError: false })).toEqual({ state: 'approved' })
    expect(planOutcome({ output: "The user doesn't want to proceed with this tool use. To tell you how to proceed, the user said:\n请顺便加上类型注解", isError: true })).toEqual({ state: 'rejected', feedback: '请顺便加上类型注解' })
    expect(planOutcome({ output: 'STOP what you are doing', isError: true })).toEqual({ state: 'rejected' })
  })
})
