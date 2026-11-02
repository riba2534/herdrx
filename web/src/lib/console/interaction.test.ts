import { describe, expect, it } from 'vitest'
import type { ChatRecord } from '../structuredChatTypes'
import type { PaneDialog } from '../../types'
import { plainScreenLine } from '../ansiScreen'
import { parseClaudeScreen } from './claudeScreen'
import { findPendingInteraction, matchQuestion, parseQuestions, planActions, planShownOnScreen, questionOptions, reviewAnswers } from './interaction'
import planDialogScreen from './__fixtures__/plan-dialog.text.txt?raw'

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
  it('needs the exact question and the same option labels in order', () => {
    expect(matchQuestion(dialog('要不要加类型注解？', ['要', '不要', 'Type something.', 'Chat about this']), questions)).toBe(1)
    // 空白不同照样算；用户在自由填写项里打过字不影响前面的选项。
    expect(matchQuestion(dialog('  测试框架用 pytest\n还是 unittest？ ', ['pytest', 'unittest', 'nose2', 'Chat about this']), questions)).toBe(0)
    // 选项集合对得上但题干不同、题干对得上但选项不同或顺序不同、单题也不兜底。
    expect(matchQuestion(dialog('另一个标题', ['要', '不要', 'Type something.']), questions)).toBe(-1)
    expect(matchQuestion(dialog('要不要加类型注解？', ['不要', '要']), questions)).toBe(-1)
    expect(matchQuestion(dialog('要不要加类型注解？', ['要', '不加']), questions)).toBe(-1)
    expect(matchQuestion(dialog('另一个标题', ['x']), questions.slice(0, 1))).toBe(-1)
    // 题干只是开头相同也不算。
    expect(matchQuestion(dialog('测试框架用哪个？', ['pytest', 'unittest']), questions)).toBe(-1)
  })
})

describe('questionOptions', () => {
  it('takes labels and descriptions from the transcript and indexes from the screen', () => {
    const options = questionOptions(dialog('测试框架用 pytest 还是 unittest？', ['pytest', 'unittest', 'Type something.', 'Chat about this']), questions[0])
    expect(options).toEqual([
      { index: 1, label: 'pytest', description: '使用 pytest 作为测试框架', role: 'answer' },
      { index: 2, label: 'unittest', description: '使用 unittest 作为测试框架', role: 'answer' },
      { index: 3, label: 'Type something.', role: 'free' },
      { index: 4, label: 'Chat about this', role: 'chat' },
    ])
  })

  it('recognizes the free-text item after the user typed into it on the computer', () => {
    const options = questionOptions(dialog('输出格式', ['json', 'yaml', ['toml', true], 'Chat about this']), { question: '输出格式', multiSelect: false, options: [{ label: 'json' }, { label: 'yaml' }] })
    expect(options.map((option) => option.role)).toEqual(['answer', 'answer', 'free', 'chat'])
    // 打的字恰好和某个选项一样时，按位置仍是自由填写。
    expect(questionOptions(dialog('输出格式', ['json', 'yaml', ['json', true], 'Chat about this']), { question: '输出格式', multiSelect: false, options: [{ label: 'json' }, { label: 'yaml' }] })[2].role).toBe('free')
    // 不知道题目时只认 Type something。
    expect(questionOptions(dialog('?', ['a', 'toml', 'Type something.'])).map((option) => option.role)).toEqual(['answer', 'answer', 'free'])
  })

  it('uses only the screen text when the question does not match', () => {
    const screen: PaneDialog = { ...dialog('q', ['pytest', 'Type something.', 'Chat about this']), options: [{ index: 1, label: 'pytest', selected: true, description: '屏幕上的说明' }, { index: 2, label: 'Type something.', selected: false }, { index: 3, label: 'Chat about this', selected: false }] }
    expect(questionOptions(screen)).toEqual([
      { index: 1, label: 'pytest', description: '屏幕上的说明', role: 'answer' },
      { index: 2, label: 'Type something.', role: 'free' },
      { index: 3, label: 'Chat about this', role: 'chat' },
    ])
  })
})

describe('planActions', () => {
  const plan = (third: string): PaneDialog => ({ ...dialog('Would you like to proceed?', ['Yes, and switch to BYPASS PERMISSIONS (no further prompts) for this session', 'Yes, manually approve edits', [third, true]]), kind: 'plan' })

  it('names the three plan actions in Chinese', () => {
    expect(planActions(plan('Tell Claude what to change'))).toEqual([
      { index: 1, label: '批准并切换到免授权', screenLabel: 'Yes, and switch to BYPASS PERMISSIONS (no further prompts) for this session', tone: 'primary', free: false },
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

describe('reviewAnswers', () => {
  it('pairs every question with its answer', () => {
    const review: PaneDialog = { ...dialog('Ready to submit your answers?', ['Submit answers', 'Cancel']), kind: 'review', lines: ['Review your answers', ' ● 测试框架用 pytest 还是 unittest？', '   → pytest', ' ● 要不要加类型注解？', 'Ready to submit your answers?'] }
    expect(reviewAnswers(review)).toEqual([{ question: '测试框架用 pytest 还是 unittest？', answer: 'pytest' }, { question: '要不要加类型注解？', answer: '' }])
  })
})

describe('planShownOnScreen', () => {
  // plan-dialog 夹具上的计划（会话记录里第一次 ExitPlanMode 的正文）。
  const shownPlan = '# 将 calc.py 中的 add 函数改名为 plus\n\n## Context\n\n用户要求把 `calc.py` 中的 `add` 函数改名为 `plus`。\n\n## 改动\n\n修改 `/tmp/herdrx-spike/proj/calc.py:1`：\n\n```python\ndef plus(a, b):\n    return a + b\n```\n\n仅改函数定义名，函数体与参数不变。\n\n## 验证\n\n```bash\npython3 -c "import sys; sys.path.insert(0, \'/tmp/herdrx-spike/proj\'); import calc; assert calc.plus(2, 3) == 5; assert not hasattr(calc, \'add\')"\n```\n\n验证 `plus` 可用且 `add` 已不存在。\n'
  const otherPlan = '# 给 calc.py 添加 sub 函数\n\n新增 `sub`，并补 pytest 用例。\n'
  const planDialog: PaneDialog = { fingerprint: 'p', kind: 'plan', title: 'Claude has written up a plan and is ready to execute. Would you like to proceed?', lines: [], options: [] }

  it('checks the plan text visible above the question on the live screen', () => {
    const preview = parseClaudeScreen(planDialogScreen.split('\n').map(plainScreenLine)).preview
    expect(planShownOnScreen(shownPlan, planDialog, preview)).toBe(true)
    expect(planShownOnScreen(otherPlan, planDialog, preview)).toBe(false)
  })

  it('checks the first plan line in the dialog lines, skipping the fixed wording', () => {
    const withLines = { ...planDialog, lines: ['Ready to code?', "Here is Claude's plan:", '给 calc.py 添加 sub 函数', '新增 sub，并补 pytest 用例。'] }
    expect(planShownOnScreen(otherPlan, withLines)).toBe(true)
    expect(planShownOnScreen(shownPlan, withLines)).toBe(false)
  })

  it('ignores table borders and Markdown marks on screen', () => {
    const table = '| 文件 | 改动 |\n|---|---|\n| `calc.py` | 新增 **sub** |\n'
    expect(planShownOnScreen(table, { ...planDialog, lines: ['│ calc.py │ 新增 sub │'] })).toBe(true)
  })

  it('cannot compare when no plan text is visible', () => {
    expect(planShownOnScreen(otherPlan, planDialog, ['1. Yes', '2. No'])).toBe(true)
  })
})
