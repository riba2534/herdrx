import { describe, expect, it } from 'vitest'
import type { ChatRecord } from '../structuredChatTypes'
import type { PaneDialog } from '../../types'
import { findPendingInteraction, latestPlan, matchQuestion, parseQuestions, planActions, questionOptions } from './interaction'

const questions = [
  { header: '测试框架', multiSelect: false, question: '测试框架用 pytest 还是 unittest？', options: [{ label: 'pytest', description: '使用 pytest 作为测试框架' }, { label: 'unittest', description: '使用 unittest 作为测试框架' }] },
  { header: '类型注解', multiSelect: false, question: '要不要加类型注解？', options: [{ label: '要', description: '给函数加上类型注解' }, { label: '不要', description: '不加类型注解' }] },
]
const call = (id: string, callID: string, name: string, input: unknown): ChatRecord => ({ id, role: 'assistant', blocks: [{ type: 'tool-call', call_id: callID, name, input }] })
const result = (id: string, callID: string): ChatRecord => ({ id, role: 'tool', blocks: [{ type: 'tool-result', call_id: callID, output: 'ok', is_error: false }] })
const dialog = (title: string, labels: Array<string | [string, boolean]>): PaneDialog => ({
  fingerprint: title,
  kind: 'question',
  title,
  lines: [],
  options: labels.map((label, index) => (typeof label === 'string' ? { index: index + 1, label, selected: index === 0 } : { index: index + 1, label: label[0], selected: label[1] })),
})

describe('findPendingInteraction', () => {
  it('returns the last AskUserQuestion or ExitPlanMode until its result arrives', () => {
    const asked = [call('1', 'a', 'AskUserQuestion', { questions }), call('2', 'b', 'Bash', { command: 'ls' })]
    expect(findPendingInteraction(asked)).toEqual({ tool: 'AskUserQuestion', callID: 'a', questions })
    expect(findPendingInteraction([...asked, result('3', 'b')])).toMatchObject({ callID: 'a' })
    expect(findPendingInteraction([...asked, result('3', 'a')])).toBeNull()
    expect(findPendingInteraction([])).toBeNull()
    // 只看最后一个：之前的未完成调用不再算数。
    expect(findPendingInteraction([...asked, call('4', 'p', 'ExitPlanMode', { plan: '# 计划' }), result('5', 'p')])).toBeNull()
  })

  it('reads the plan from any copy of the same call and pairs calls without ids by position', () => {
    const records = [call('1', 'p', 'ExitPlanMode', { plan: '# 计划\n\n正文' }), call('2', 'p', 'ExitPlanMode', {})]
    expect(findPendingInteraction(records)).toEqual({ tool: 'ExitPlanMode', callID: 'p', plan: '# 计划\n\n正文' })
    expect(latestPlan([...records, result('3', 'p')])).toBe('# 计划\n\n正文')
    const anonymous = [result('0', ''), call('1', '', 'AskUserQuestion', { questions })]
    expect(findPendingInteraction(anonymous)).toMatchObject({ callID: '' })
    expect(findPendingInteraction([...anonymous, result('2', '')])).toBeNull()
  })
})

describe('parseQuestions', () => {
  it('keeps well-formed questions and drops malformed parts', () => {
    expect(parseQuestions({ questions: [{ question: ' 选哪个？ ', options: [{ label: 'a' }, { label: '' }, 'x', { label: 'b', description: '说明' }], multiSelect: true }, { header: '缺题目' }, null] })).toEqual([
      { question: '选哪个？', options: [{ label: 'a' }, { label: 'b', description: '说明' }], multiSelect: true },
    ])
    expect(parseQuestions(null)).toEqual([])
    expect(parseQuestions({ questions: 'no' })).toEqual([])
  })
})

describe('matchQuestion', () => {
  it('finds the current page by its text, then by its options', () => {
    expect(matchQuestion(dialog('要不要加类型注解？', ['要', '不要']), questions)).toBe(1)
    expect(matchQuestion({ ...dialog('', ['pytest', 'unittest']), lines: ['测试框架用 pytest 还是', 'unittest？'] }, questions)).toBe(0)
    expect(matchQuestion(dialog('另一个标题', ['要', '不要', 'Type something.']), questions)).toBe(1)
    expect(matchQuestion(dialog('另一个标题', ['x']), questions)).toBe(-1)
    expect(matchQuestion(dialog('另一个标题', ['x']), questions.slice(0, 1))).toBe(0)
  })
})

describe('questionOptions', () => {
  it('takes labels and descriptions from the transcript and indexes from the screen', () => {
    const options = questionOptions(dialog('测试框架用 pytest 还是 unittest？', ['pytest', 'unittest', 'Type something.', 'Chat about this']), questions[0])
    expect(options).toEqual([
      { index: 1, label: 'pytest', description: '使用 pytest 作为测试框架', selected: true, role: 'answer' },
      { index: 2, label: 'unittest', description: '使用 unittest 作为测试框架', selected: false, role: 'answer' },
      { index: 3, label: 'Type something.', selected: false, role: 'free' },
      { index: 4, label: 'Chat about this', selected: false, role: 'chat' },
    ])
  })

  it('recognizes the free-text item after the user typed into it on the computer', () => {
    const options = questionOptions(dialog('输出格式', ['json', 'yaml', ['toml', true], 'Chat about this']), { question: '输出格式', multiSelect: false, options: [{ label: 'json' }, { label: 'yaml' }] })
    expect(options.map((option) => option.role)).toEqual(['answer', 'answer', 'free', 'chat'])
    // 不知道题目时只认 Type something。
    expect(questionOptions(dialog('?', ['a', 'toml', 'Type something.'])).map((option) => option.role)).toEqual(['answer', 'answer', 'free'])
  })

  it('matches labels the screen cut short', () => {
    const options = questionOptions(dialog('q', ['A very long option labe…']), { question: 'q', multiSelect: false, options: [{ label: 'A very long option label that wraps', description: 'd' }] })
    expect(options[0]).toMatchObject({ label: 'A very long option label that wraps', description: 'd', role: 'answer' })
  })
})

describe('planActions', () => {
  const plan = (third: string): PaneDialog => ({ ...dialog('Would you like to proceed?', ['Yes, and switch to BYPASS PERMISSIONS (no further prompts) for this session', 'Yes, manually approve edits', [third, true]]), kind: 'plan' })

  it('names the three plan actions in Chinese', () => {
    expect(planActions(plan('Tell Claude what to change'))).toEqual([
      { index: 1, label: '批准并保持免授权', screenLabel: 'Yes, and switch to BYPASS PERMISSIONS (no further prompts) for this session', tone: 'primary', free: false },
      { index: 2, label: '批准，改动逐个确认', screenLabel: 'Yes, manually approve edits', tone: 'secondary', free: false },
      { index: 3, label: '提修改意见', screenLabel: 'Tell Claude what to change', tone: 'secondary', free: true },
    ])
  })

  it('keeps the feedback action after the label was replaced by typed text, and unknown options as they are', () => {
    expect(planActions(plan('请顺便加上类型注解'))[2]).toMatchObject({ label: '提修改意见', free: true })
    const other: PaneDialog = { ...dialog('x', ['Yes, and auto-accept edits', 'No, keep planning']), kind: 'plan' }
    expect(planActions(other).map((action) => [action.label, action.free])).toEqual([['Yes, and auto-accept edits', false], ['No, keep planning', false]])
  })
})
