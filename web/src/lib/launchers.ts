// Built-in commands are the `run` argv from the official Herdr app's
// defaults/programs.json (herdrdev/herdr-app). Custom rows are account data
// an agent can rewrite as JSON; the workbench types the line into the new
// tab's shell and does not run it on the workbench host.

export type UserLauncher = { id: string; name: string; command: string }

export type LaunchChoice = { id: string; name: string; command: string }

export const builtinLaunchers: LaunchChoice[] = [
  { id: 'shell', name: '空终端', command: '' },
  { id: 'claude', name: 'Claude Code', command: 'claude' },
  { id: 'codex', name: 'Codex', command: 'codex' },
  { id: 'gemini', name: 'Gemini', command: 'gemini' },
  { id: 'cursor', name: 'Cursor', command: 'cursor-agent' },
  { id: 'pi', name: 'Pi', command: 'pi' },
  { id: 'grok', name: 'Grok', command: 'grok' },
  { id: 'agy', name: 'Antigravity', command: 'agy' },
]

const reservedIDs = new Set(builtinLaunchers.map((item) => item.id))
const idPattern = /^[a-z][a-z0-9-]{0,31}$/
const maxLaunchers = 24

export function launcherShortcut(index: number) {
  return index >= 0 && index < 9 ? String(index + 1) : ''
}

// launchInput is the bytes typed into the new pane. An empty command starts a
// shell and sends nothing. A command is one line plus Enter, and is not retried.
export function launchInput(command: string) {
  return command ? `${command}\r` : ''
}

export function launcherGuide(current: UserLauncher[]) {
  return [
    '请只输出一份 herdrx 启动项 JSON，不要解释，不要用 markdown 代码块。',
    '格式：{"launchers":[{"id":"review","name":"审查","command":"codex review"}]}',
    'id 用小写字母开头，只含小写字母、数字和连字符，不能用 shell、claude、codex、gemini、cursor、pi、grok、agy。',
    'name 是菜单上的名字。command 是一行 shell 命令，会在新建标签页的登录 shell 里执行并回车。',
    '不要包含换行或控制字符。最多 24 项。',
    '当前启动项：',
    JSON.stringify({ launchers: current }),
  ].join('\n')
}

export function parseLauncherDocument(raw: string): UserLauncher[] {
  let parsed: unknown
  try {
    parsed = JSON.parse(raw)
  } catch {
    throw new Error('启动项需要是 {"launchers":[...]} 这样的 JSON')
  }
  if (!parsed || typeof parsed !== 'object' || Array.isArray(parsed)) {
    throw new Error('启动项需要是 {"launchers":[...]} 这样的 JSON')
  }
  const body = parsed as Record<string, unknown>
  const extra = Object.keys(body).filter((key) => key !== 'launchers')
  if (extra.length) throw new Error('启动项 JSON 只需要 launchers 字段')
  const rows = body.launchers
  if (!Array.isArray(rows)) throw new Error('启动项需要是 {"launchers":[...]} 这样的 JSON')
  if (rows.length > maxLaunchers) throw new Error('自定义启动项最多 24 个')
  const seen = new Set<string>()
  return rows.map((row) => normalizeLauncher(row, seen))
}

export function normalizeLauncher(row: unknown, seen = new Set<string>()): UserLauncher {
  if (!row || typeof row !== 'object' || Array.isArray(row)) throw new Error('每一项都要有 id、name 和 command')
  const item = row as Record<string, unknown>
  const extra = Object.keys(item).filter((key) => key !== 'id' && key !== 'name' && key !== 'command')
  if (extra.length) throw new Error('启动项只需要 id、name 和 command')
  if (typeof item.id !== 'string' || !idPattern.test(item.id)) {
    throw new Error('启动项 id 要用小写字母开头，只能含小写字母、数字和连字符')
  }
  if (reservedIDs.has(item.id)) throw new Error(`id “${item.id}” 是内置启动项，请换一个`)
  if (seen.has(item.id)) throw new Error(`启动项 id “${item.id}” 重复了`)
  seen.add(item.id)
  if (typeof item.name !== 'string' || typeof item.command !== 'string') throw new Error('每一项都要有 id、name 和 command')
  const name = item.name.trim()
  const command = item.command.trim()
  if (!name || [...name].length > 32 || hasControl(name)) throw new Error('启动项名称要在 1 到 32 个字符之间，且不能含换行')
  if (!command || [...command].length > 200 || hasControl(command)) throw new Error('启动命令要是一行，1 到 200 个字符，不能含换行或控制字符')
  return { id: item.id, name, command }
}

function hasControl(value: string) {
  for (const char of value) {
    const code = char.codePointAt(0) || 0
    if (code < 0x20 || code === 0x7f) return true
  }
  return false
}
