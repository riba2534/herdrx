import type { ChatRecord, ChatToolCallBlock } from '../structuredChatTypes'
import type { PaneDialog, PaneDialogOption } from '../../types'

// 交互卡片的数据：哪些 Claude 工具调用还在等用户回答（来自对话记录），以及屏幕上认出的
// 对话框里每个选项该按哪个编号（来自读屏）。题目、选项说明以对话记录为准，作答只按读屏的编号。

export type AskOption = { label: string; description?: string }
export type AskQuestion = { question: string; header?: string; options: AskOption[]; multiSelect: boolean }

/** 对话记录里最后一个还没有结果的 AskUserQuestion / ExitPlanMode。 */
export type PendingInteraction =
  | { tool: 'AskUserQuestion'; callID: string; questions: AskQuestion[] }
  | { tool: 'ExitPlanMode'; callID: string; plan: string }

const INTERACTION_TOOLS = new Set(['AskUserQuestion', 'ExitPlanMode'])

const record = (value: unknown): Record<string, unknown> | null =>
  value && typeof value === 'object' && !Array.isArray(value) ? value as Record<string, unknown> : null
const text = (value: unknown) => (typeof value === 'string' ? value : '')

export function parseQuestions(input: unknown): AskQuestion[] {
  const questions = record(input)?.questions
  if (!Array.isArray(questions)) return []
  return questions.flatMap((value) => {
    const item = record(value)
    const question = text(item?.question).trim()
    if (!item || !question) return []
    const options = (Array.isArray(item.options) ? item.options : []).flatMap((option) => {
      const entry = record(option)
      const label = text(entry?.label).trim()
      if (!label) return []
      const description = text(entry?.description).trim()
      return [description ? { label, description } : { label }]
    })
    const header = text(item.header).trim()
    return [{ question, ...(header ? { header } : {}), options, multiSelect: item.multiSelect === true }]
  })
}

/** 同一个 call_id 的 ExitPlanMode 可能出现多次，取带正文的那次。 */
function planFor(records: readonly ChatRecord[], callID: string, fallback: ChatToolCallBlock) {
  for (let index = records.length - 1; index >= 0; index--) {
    for (const block of records[index].blocks) {
      if (block.type === 'tool-call' && block.name === 'ExitPlanMode' && block.call_id === callID) {
        const plan = text(record(block.input)?.plan)
        if (plan.trim()) return plan
      }
    }
  }
  return text(record(fallback.input)?.plan)
}

export function findPendingInteraction(records: readonly ChatRecord[]): PendingInteraction | null {
  let call: ChatToolCallBlock | null = null
  let position = -1
  for (let index = 0; index < records.length; index++) {
    for (const block of records[index].blocks) {
      if (block.type === 'tool-call' && INTERACTION_TOOLS.has(block.name)) {
        call = block
        position = index
      }
    }
  }
  if (!call) return null
  for (let index = position; index < records.length; index++) {
    // 没有 call_id 时只能认它后面的结果。
    if (records[index].blocks.some((other) => other.type === 'tool-result' && other.call_id === call.call_id && (call.call_id || index > position))) return null
  }
  if (call.name === 'ExitPlanMode') return { tool: 'ExitPlanMode', callID: call.call_id, plan: planFor(records, call.call_id, call) }
  return { tool: 'AskUserQuestion', callID: call.call_id, questions: parseQuestions(call.input) }
}

// 计划批准框里不属于计划正文的固定文案。
const PLAN_CHROME = /^(?:ready to code\?|here is claude's plan:?|claude has written up a plan\b.*|would you like to proceed\?)$/i
// 比较屏幕上的计划行与 Markdown 原文：屏幕已经渲染掉了标题、强调、代码标记和列表符号，表格画成
// 框线，长行也会折开；两边都去掉这些再比。
const squash = (value: string) => value.replace(/[\s#*_`>|~•·⎿\-─━│┃┌┐└┘├┤┬┴┼]/g, '')

/**
 * 屏幕上看得见的计划行（对话框说明行，以及读屏里问句上方的正文）是否出自这份计划：第一行可见
 * 正文去掉空白和 Markdown 符号后要能在计划原文里找到。屏幕上看不到任何正文时无从比对，返回 true。
 */
export function planShownOnScreen(plan: string, dialog: PaneDialog, preview: readonly string[] = []) {
  const title = squash(dialog.title || '')
  const question = title ? preview.findIndex((line) => squash(line).includes(title)) : -1
  const visible = [...dialog.lines, ...(question >= 0 ? preview.slice(0, question) : [])]
  const first = visible.map((line) => line.trim()).find((line) => squash(line) && !PLAN_CHROME.test(line))
  return first === undefined || squash(plan).includes(squash(first))
}

const normalize = (value: string) => value.replace(/\s+/g, ' ').trim()

/**
 * 对话框当前页对应第几题；对不上时返回 -1。题干要完全一致（只规范化空白），题目选项的标签
 * 也要按顺序与屏幕上的前几项完全一致——差一点都不拿对话记录去套，卡片只用屏幕上的文字。
 */
export function matchQuestion(dialog: PaneDialog, questions: readonly AskQuestion[]) {
  const title = normalize(dialog.title || '')
  return questions.findIndex((question) => normalize(question.question) === title
    && question.options.length > 0
    && question.options.every((option, position) => normalize(dialog.options[position]?.label || '') === normalize(option.label)))
}

export type OptionRole = 'answer' | 'free' | 'chat'
export type OptionView = { index: number; label: string; description?: string; role: OptionRole }

const FREE_TEXT = /^type something/i
const CHAT = /^chat about this/i

/**
 * 题目的选项：标签、编号取自屏幕。`question` 只在 matchQuestion 对上时传入，此时前几项与题目
 * 选项逐一相同，补上对话记录里的说明；紧随其后的是自由填写（`Type something.`，用户在电脑上
 * 打过字时会原地变成那段文字）和 `Chat about this`。对不上时只认得出这两个固定项。
 */
export function questionOptions(dialog: PaneDialog, question?: AskQuestion): OptionView[] {
  return dialog.options.map((option, position) => {
    const known = question?.options[position]
    let role: OptionRole = 'answer'
    if (CHAT.test(option.label)) role = 'chat'
    else if (!known && (FREE_TEXT.test(option.label) || position === question?.options.length)) role = 'free'
    const description = known?.description || option.description
    return { index: option.index, label: option.label, ...(description ? { description } : {}), role }
  })
}

/** 多题确认页上每题的回答：`● 问题` 下面跟 `→ 回答`。 */
export function reviewAnswers(dialog: PaneDialog) {
  const pairs: Array<{ question: string; answer: string }> = []
  for (const line of dialog.lines) {
    const value = line.trim()
    if (/^[●•]/.test(value)) pairs.push({ question: value.replace(/^[●•]\s*/, ''), answer: '' })
    else if (value.startsWith('→') && pairs.length) pairs[pairs.length - 1].answer = value.replace(/^→\s*/, '')
  }
  return pairs
}

export function findOption(dialog: PaneDialog, pattern: RegExp): PaneDialogOption | undefined {
  return dialog.options.find((option) => pattern.test(option.label.trim()))
}

export type PlanAction = { index: number; label: string; screenLabel: string; tone: 'primary' | 'secondary'; free: boolean }

const FEEDBACK = { pattern: /^tell claude what to change/i, label: '提修改意见', tone: 'secondary' as const, free: true }
const PLAN_ACTIONS: Array<{ pattern: RegExp; label: string; tone: 'primary' | 'secondary'; free?: boolean }> = [
  { pattern: /^yes,? and switch to bypass/i, label: '批准并切换到免授权', tone: 'primary' },
  { pattern: /^yes,? manually approve edits/i, label: '批准，改动逐个确认', tone: 'secondary' },
  FEEDBACK,
]

/** 计划确认的动作；认不出的选项保留屏幕原文。电脑上打过字后「提修改意见」那项（光标所在的最后一项）会变成那段文字。 */
export function planActions(dialog: PaneDialog): PlanAction[] {
  const hasFree = dialog.options.some((option) => FEEDBACK.pattern.test(option.label.trim()))
  return dialog.options.map((option, position) => {
    const known = PLAN_ACTIONS.find((action) => action.pattern.test(option.label.trim()))
    if (known) return { index: option.index, label: known.label, screenLabel: option.label, tone: known.tone, free: Boolean(known.free) }
    const typed = !hasFree && option.selected && position === dialog.options.length - 1 && !/^yes\b/i.test(option.label.trim())
    return { index: option.index, label: typed ? FEEDBACK.label : option.label, screenLabel: option.label, tone: 'secondary', free: typed }
  })
}
