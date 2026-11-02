// 从 Claude Code 全屏界面的读屏纯文本里取三样东西：输入框上方的 spinner 行、屏幕底部的模式行，
// 以及 spinner 上方最近的几行正文（给「实时画面」折叠条）。读屏只用于展示状态，不代替对话记录；
// 认不出的一律返回空值，不猜。输入是去掉 ANSI 的行（见 ansiScreen.plainScreenLine）。

export type ConsoleMode = 'bypass' | 'auto' | 'manual' | 'acceptEdits' | 'plan'

export type ConsoleSpinner = {
  /** spinner 动词（Claude 随机挑选），不含省略号，如 `Wibbling`。 */
  verb: string
  /** 本轮已用时（秒）；括号里没有时省略。 */
  seconds?: number
  /** 本轮输出 token 数，`↓ 1.2k tokens` 记为 1200；没有时省略。 */
  tokens?: number
  /** 括号里带 `thinking`（如 `thinking with max effort`）。 */
  thinking: boolean
  /** 去掉首尾空白的原始行。 */
  raw: string
}

type ClaudeScreen = { spinner: ConsoleSpinner | null; mode: ConsoleMode | null; preview: string[] }

/** 「实时画面」最多显示的行数。 */
const PREVIEW_LINES = 12
// spinner 行离输入框最多隔这么多行非空内容（中间可能有 todo 列表、提示行）。
const SPINNER_SEARCH_LINES = 12
// 模式行在屏幕最后几行非空内容里。
const MODE_SEARCH_LINES = 6

// 字形在 · ✻ ✢ ✶ ✳ * 之间轮换；`●` 是消息行（`● Running 1 shell command…`），不算。
// 动词后必须紧跟省略号：完成行 `✻ Cooked for 18s · done 3:11` 没有省略号，不算进行中。
const SPINNER = /^([·✻✢✶✳✽✺*])\s+([^\s(…][^(…]{0,47}?)(?:…|\.\.\.)\s*(?:\((.*)\))?$/u
// 一轮结束后的完成行：`✻ Cooked for 18s · done 3:11`、`✻ Worked for 1m 3s`。
const FINISHED = /^[·✻✢✶✳✽✺*]\s+\S+ for (?:\d+h\s*)?(?:\d+m\s*)?\d+s\b/u
const DURATION = /^(?:(\d+)h)?\s*(?:(\d+)m)?\s*(?:(\d+)s)?$/
const TOKENS = /^(?:[↓↑]\s*)?(\d+(?:[.,]\d+)?)\s*([kKmM])?\s*tokens?$/
const MODE = /(?:⏵⏵|⏵|⏸)\s*(bypass permissions|auto mode|manual mode|accept edits|plan mode)\s+on\b/i
const MODE_NAMES: Record<string, ConsoleMode> = {
  'bypass permissions': 'bypass',
  'auto mode': 'auto',
  'manual mode': 'manual',
  'accept edits': 'acceptEdits',
  'plan mode': 'plan',
}
// 横线：输入框上下边、计划视图的 ▔ 与 ╌ 分隔线；右侧可能带会话名（`──── rename ─`）。
const RULE = /^\s*[─━═▔▁╌┄-]{8,}/
// 输入框的第一行。对话框里光标所在的选项（`❯ 1. pytest`）不算。
const PROMPT = /^\s*❯(?!\s*\d+\.)/

function seconds(text: string) {
  const match = DURATION.exec(text.trim())
  if (!match || (!match[1] && !match[2] && !match[3])) return undefined
  return Number(match[1] || 0) * 3600 + Number(match[2] || 0) * 60 + Number(match[3] || 0)
}

function tokens(text: string) {
  const match = TOKENS.exec(text.trim())
  if (!match) return undefined
  const value = Number(match[1].replace(',', '.'))
  const unit = match[2]?.toLowerCase()
  return Math.round(value * (unit === 'k' ? 1000 : unit === 'm' ? 1_000_000 : 1))
}

/** 解析一行 spinner；不是 spinner（含完成行）时返回 null。 */
export function parseSpinner(line: string): ConsoleSpinner | null {
  const raw = line.trim()
  const match = SPINNER.exec(raw)
  if (!match) return null
  const spinner: ConsoleSpinner = { verb: match[2].trim(), thinking: false, raw }
  for (const part of (match[3] || '').split('·')) {
    const elapsed = seconds(part)
    const count = elapsed === undefined ? tokens(part) : undefined
    if (elapsed !== undefined) spinner.seconds ??= elapsed
    else if (count !== undefined) spinner.tokens ??= count
    else if (/\bthinking\b/i.test(part)) spinner.thinking = true
  }
  return spinner
}

function clean(lines: readonly string[]) {
  return lines.map((line) => line.replace(/\r$/, '').trimEnd())
}

/** 输入框上边线的行号；没有输入框（对话框、非 Claude 程序）时返回 -1。 */
function promptTop(lines: readonly string[]) {
  for (let index = lines.length - 1; index > 0; index--) {
    if (PROMPT.test(lines[index]) && RULE.test(lines[index - 1])) return index - 1
  }
  return -1
}

function spinnerAt(lines: readonly string[], end: number) {
  let seen = 0
  for (let index = end - 1; index >= 0 && seen < SPINNER_SEARCH_LINES; index--) {
    if (!lines[index].trim()) continue
    seen++
    const spinner = parseSpinner(lines[index])
    if (spinner) return { spinner, index }
  }
  return null
}

/** 读屏底部的模式行；屏幕上没有（对话框挡住、不是 Claude）时返回 null。 */
export function parseMode(lines: readonly string[]): ConsoleMode | null {
  let seen = 0
  for (let index = lines.length - 1; index >= 0 && seen < MODE_SEARCH_LINES; index--) {
    if (!lines[index].trim()) continue
    seen++
    const match = MODE.exec(lines[index])
    if (match) return MODE_NAMES[match[1].toLowerCase()]
  }
  return null
}

/**
 * `end` 之前最近的若干行正文：去掉空行、横线、右对齐提示、spinner 与完成行，按共同缩进左移。
 * 右对齐提示（`◈ max · /effort`、计划视图的 `↓`）贴着屏幕右边，前面的空白比内容还长。
 */
export function screenPreview(lines: readonly string[], end = lines.length, max = PREVIEW_LINES): string[] {
  const width = Math.max(0, ...lines.map((line) => line.length))
  const kept: string[] = []
  for (let index = end - 1; index >= 0 && kept.length < max; index--) {
    const line = lines[index]
    const indent = line.length - line.trimStart().length
    if (!line.trim() || RULE.test(line) || (indent * 2 > line.length && line.length >= width - 2)) continue
    if (FINISHED.test(line.trim()) || parseSpinner(line)) continue
    kept.push(line)
  }
  kept.reverse()
  const indent = Math.min(...kept.map((line) => line.length - line.trimStart().length))
  return kept.map((line) => line.slice(indent))
}

export function parseClaudeScreen(input: readonly string[]): ClaudeScreen {
  const lines = clean(input)
  const top = promptTop(lines)
  const end = top < 0 ? lines.length : top
  const found = spinnerAt(lines, end)
  return {
    spinner: found?.spinner || null,
    mode: parseMode(lines),
    preview: screenPreview(lines, found ? found.index : end),
  }
}
