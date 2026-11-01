// AskUserQuestion / ExitPlanMode 的结果解读。只读工具结果原文，不读屏、不猜测：
// 没有结果就是还在等待；结果是错误就是用户取消或拒绝。

import type { AskQuestion } from './toolDescriptor'
import type { ToolResult } from './workRuns'

type AskState = 'pending' | 'answered' | 'cancelled'

/**
 * Claude 把答案写成 `"问题"="答案", "问题"="答案".`。按题面逐题取答案；
 * 写法变了取不到时该题显示为空，不编造答案。
 */
export function askAnswers(questions: readonly AskQuestion[], result?: ToolResult): { state: AskState; answers: Map<string, string> } {
  const answers = new Map<string, string>()
  if (!result) return { state: 'pending', answers }
  if (result.isError) return { state: 'cancelled', answers }
  for (const { question } of questions) {
    const marker = `"${question}"="`
    const start = result.output.indexOf(marker)
    if (start < 0) continue
    const rest = result.output.slice(start + marker.length)
    const end = rest.search(/"(?:,\s*"|\.(?:\s|$)|$)/)
    answers.set(question, (end < 0 ? rest : rest.slice(0, end)).trim())
  }
  return { state: 'answered', answers }
}

type PlanState = 'pending' | 'approved' | 'rejected'

/** 批准时结果是普通输出；拒绝时是错误输出，用户写了修改意见时跟在 `the user said:` 之后。 */
export function planOutcome(result?: ToolResult): { state: PlanState; feedback?: string } {
  if (!result) return { state: 'pending' }
  if (!result.isError) return { state: 'approved' }
  const marker = 'the user said:'
  const index = result.output.indexOf(marker)
  const feedback = index < 0 ? '' : result.output.slice(index + marker.length).trim()
  return feedback ? { state: 'rejected', feedback } : { state: 'rejected' }
}
