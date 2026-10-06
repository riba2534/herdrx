import type { ITheme } from '@xterm/xterm'

// 画面视图的 ANSI 行解析。输入是 Herdr pane.read（format=ansi）的一行：Herdr 在每个
// 带样式的行首先输出 SGR 重置，所以每行可以独立解析，不需要跨行携带状态。
// 只解释 SGR（颜色与字形），其余 CSI / OSC / 控制字符一律丢弃——画面视图只读，
// 光标移动之类的序列在这里没有意义。

export type ScreenColor = { kind: 'palette'; index: number } | { kind: 'rgb'; value: string }

export type ScreenStyle = {
  fg?: ScreenColor
  bg?: ScreenColor
  bold?: boolean
  dim?: boolean
  italic?: boolean
  underline?: boolean
  inverse?: boolean
  hidden?: boolean
  strike?: boolean
}

export type ScreenSegment = { text: string; style: ScreenStyle }

const ESC = '\x1b'

function cloneStyle(style: ScreenStyle): ScreenStyle {
  return { ...style }
}

function sameStyle(left: ScreenStyle, right: ScreenStyle) {
  const keys = new Set([...Object.keys(left), ...Object.keys(right)]) as Set<keyof ScreenStyle>
  for (const key of keys) {
    const a = left[key], b = right[key]
    if (typeof a === 'object' || typeof b === 'object') {
      if (JSON.stringify(a) !== JSON.stringify(b)) return false
    } else if (Boolean(a) !== Boolean(b)) return false
  }
  return true
}

function hex(value: number) {
  return Math.max(0, Math.min(255, value)).toString(16).padStart(2, '0')
}

function extendedColor(params: number[], index: number): { color?: ScreenColor; next: number } {
  const mode = params[index + 1]
  if (mode === 5 && index + 2 < params.length) return { color: { kind: 'palette', index: params[index + 2] & 0xff }, next: index + 2 }
  if (mode === 2 && index + 4 < params.length) {
    return { color: { kind: 'rgb', value: `#${hex(params[index + 2])}${hex(params[index + 3])}${hex(params[index + 4])}` }, next: index + 4 }
  }
  return { next: params.length }
}

function applySGR(style: ScreenStyle, raw: string): ScreenStyle {
  // Colon sub-parameters (38:2::r:g:b) are folded into the same list.
  const params = raw === '' ? [0] : raw.split(/[;:]/).map((part) => (part === '' ? 0 : Number(part)))
  let next = cloneStyle(style)
  for (let index = 0; index < params.length; index++) {
    const code = params[index]
    if (!Number.isFinite(code)) continue
    if (code === 0) next = {}
    else if (code === 1) next.bold = true
    else if (code === 2) next.dim = true
    else if (code === 3) next.italic = true
    else if (code === 4) next.underline = true
    else if (code === 7) next.inverse = true
    else if (code === 8) next.hidden = true
    else if (code === 9) next.strike = true
    else if (code === 21 || code === 22) { delete next.bold; delete next.dim }
    else if (code === 23) delete next.italic
    else if (code === 24) delete next.underline
    else if (code === 27) delete next.inverse
    else if (code === 28) delete next.hidden
    else if (code === 29) delete next.strike
    else if (code >= 30 && code <= 37) next.fg = { kind: 'palette', index: code - 30 }
    else if (code === 38) { const result = extendedColor(params, index); if (result.color) next.fg = result.color; index = result.next }
    else if (code === 39) delete next.fg
    else if (code >= 40 && code <= 47) next.bg = { kind: 'palette', index: code - 40 }
    else if (code === 48) { const result = extendedColor(params, index); if (result.color) next.bg = result.color; index = result.next }
    else if (code === 49) delete next.bg
    else if (code >= 90 && code <= 97) next.fg = { kind: 'palette', index: code - 90 + 8 }
    else if (code >= 100 && code <= 107) next.bg = { kind: 'palette', index: code - 100 + 8 }
  }
  return next
}

/** Parse one ANSI line into styled text runs. Unknown escapes are dropped. */
export function parseAnsiLine(line: string): ScreenSegment[] {
  const segments: ScreenSegment[] = []
  let style: ScreenStyle = {}
  let text = ''
  const flush = () => {
    if (!text) return
    const last = segments[segments.length - 1]
    if (last && sameStyle(last.style, style)) last.text += text
    else segments.push({ text, style })
    text = ''
  }
  for (let index = 0; index < line.length; index++) {
    const char = line[index]
    if (char === ESC) {
      const kind = line[index + 1]
      if (kind === '[') {
        let end = index + 2
        while (end < line.length && !/[\x40-\x7e]/.test(line[end])) end++
        if (end >= line.length) break
        if (line[end] === 'm') {
          flush()
          style = applySGR(style, line.slice(index + 2, end))
        }
        index = end
        continue
      }
      if (kind === ']' || kind === 'P' || kind === '_' || kind === '^') {
        // OSC/DCS/APC/PM end at BEL or ST (ESC \); hyperlinks keep their visible text.
        let end = index + 2
        while (end < line.length && line[end] !== '\x07' && !(line[end] === ESC && line[end + 1] === '\\')) end++
        index = end < line.length && line[end] === ESC ? end + 1 : end
        continue
      }
      // Other escapes (charset selection and the like): skip intermediates and the final byte.
      let end = index + 1
      while (end < line.length && line.charCodeAt(end) >= 0x20 && line.charCodeAt(end) <= 0x2f) end++
      index = end
      continue
    }
    if (char === '\t') { text += '    '; continue }
    const code = char.charCodeAt(0)
    if (code < 0x20 || code === 0x7f) continue
    text += char
  }
  flush()
  return segments
}

const PALETTE_KEYS = ['black', 'red', 'green', 'yellow', 'blue', 'magenta', 'cyan', 'white', 'brightBlack', 'brightRed', 'brightGreen', 'brightYellow', 'brightBlue', 'brightMagenta', 'brightCyan', 'brightWhite'] as const
const XTERM_DEFAULTS = ['#000000', '#cd0000', '#00cd00', '#cdcd00', '#0000ee', '#cd00cd', '#00cdcd', '#e5e5e5', '#7f7f7f', '#ff0000', '#00ff00', '#ffff00', '#5c5cff', '#ff00ff', '#00ffff', '#ffffff']

/** Resolve a palette or truecolor value against the terminal theme. */
export function screenColor(color: ScreenColor, theme: ITheme): string {
  if (color.kind === 'rgb') return color.value
  const index = color.index
  if (index < 16) return (theme[PALETTE_KEYS[index]] as string | undefined) || XTERM_DEFAULTS[index]
  if (index < 232) {
    const cube = index - 16
    const level = (value: number) => (value === 0 ? 0 : 55 + value * 40)
    return `#${hex(level(Math.floor(cube / 36)))}${hex(level(Math.floor(cube / 6) % 6))}${hex(level(cube % 6))}`
  }
  const gray = 8 + (index - 232) * 10
  return `#${hex(gray)}${hex(gray)}${hex(gray)}`
}

/** CSS for one run; the default colors come from the surrounding element. */
export function screenStyleCSS(style: ScreenStyle, theme: ITheme): Record<string, string> {
  let fg = style.fg ? screenColor(style.fg, theme) : undefined
  let bg = style.bg ? screenColor(style.bg, theme) : undefined
  if (style.inverse) {
    const swapFg = bg || theme.background || '#000000'
    const swapBg = fg || theme.foreground || '#ffffff'
    fg = swapFg
    bg = swapBg
  }
  const css: Record<string, string> = {}
  if (fg) css.color = fg
  if (bg) css.backgroundColor = bg
  if (style.bold) css.fontWeight = '700'
  if (style.dim) css.opacity = '0.65'
  if (style.italic) css.fontStyle = 'italic'
  const decorations = [style.underline ? 'underline' : '', style.strike ? 'line-through' : ''].filter(Boolean)
  if (decorations.length) css.textDecoration = decorations.join(' ')
  if (style.hidden) css.color = 'transparent'
  return css
}

/** Plain text of a line, for copy and search. */
export function plainScreenLine(line: string) {
  return parseAnsiLine(line).map((segment) => segment.text).join('')
}

export type ScreenFrame = {
  pane_id: string
  gen: number
  seq: number
  full?: boolean
  lines?: string[]
  drop?: number
  total: number
  set?: Array<[number, string]>
  truncated?: boolean
  requested?: number
}

/** Apply a server frame to the previous lines; full frames replace them. */
export function applyScreenFrame(previous: readonly string[], frame: ScreenFrame): string[] {
  if (frame.full) return [...(frame.lines || [])]
  const next = previous.slice(frame.drop || 0)
  if (next.length > frame.total) next.length = frame.total
  while (next.length < frame.total) next.push('')
  for (const [index, line] of frame.set || []) {
    if (index >= 0 && index < next.length) next[index] = line
  }
  return next
}
