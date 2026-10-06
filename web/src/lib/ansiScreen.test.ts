import { describe, expect, it } from 'vitest'
import { applyScreenFrame, parseAnsiLine, plainScreenLine, screenColor, screenStyleCSS } from './ansiScreen'
import { terminalThemes } from './themes'

const theme = terminalThemes.Cobalt2

describe('parseAnsiLine', () => {
  it('reads the SGR runs Herdr emits for a styled line', () => {
    // Captured from Herdr 0.9.1 `pane.read --source recent_unwrapped --format ansi`.
    const segments = parseAnsiLine('\x1b[0m\x1b[1m\x1b[48;5;4mbold-blue-bg\x1b[0m 中文宽字符 end')
    expect(segments).toEqual([
      { text: 'bold-blue-bg', style: { bold: true, bg: { kind: 'palette', index: 4 } } },
      { text: ' 中文宽字符 end', style: {} },
    ])
  })

  it('handles truecolor, bright colors and attribute resets', () => {
    const [first, second, third] = parseAnsiLine('\x1b[38;2;255;128;0;3mA\x1b[23;94mB\x1b[39;7mC')
    expect(first.style).toEqual({ fg: { kind: 'rgb', value: '#ff8000' }, italic: true })
    expect(second.style).toEqual({ fg: { kind: 'palette', index: 12 } })
    expect(third.style).toEqual({ inverse: true })
  })

  it('drops cursor movement, OSC titles and control bytes but keeps link text', () => {
    expect(plainScreenLine('\x1b[2K\x1b[1;1H\x1b]0;title\x07ok\x1b]8;;https://example.test\x1b\\link\x1b]8;;\x1b\\\x07\x00!')).toBe('oklink!')
    expect(plainScreenLine('\x1b(Bplain\ttab')).toBe('plain    tab')
  })

  it('merges adjacent runs with the same style and survives a truncated escape', () => {
    expect(parseAnsiLine('\x1b[31ma\x1b[31mb\x1b[')).toEqual([{ text: 'ab', style: { fg: { kind: 'palette', index: 1 } } }])
  })
})

describe('screen colors', () => {
  it('maps the 16 palette entries to the terminal theme and computes the 256-color cube', () => {
    expect(screenColor({ kind: 'palette', index: 1 }, theme)).toBe(theme.red)
    expect(screenColor({ kind: 'palette', index: 9 }, theme)).toBe(theme.brightRed)
    expect(screenColor({ kind: 'palette', index: 196 }, theme)).toBe('#ff0000')
    expect(screenColor({ kind: 'palette', index: 244 }, theme)).toBe('#808080')
  })

  it('swaps colors for inverse text and hides concealed text', () => {
    expect(screenStyleCSS({ inverse: true }, theme)).toMatchObject({ color: theme.background, backgroundColor: theme.foreground })
    expect(screenStyleCSS({ hidden: true, fg: { kind: 'palette', index: 2 } }, theme).color).toBe('transparent')
    expect(screenStyleCSS({ underline: true, strike: true }, theme).textDecoration).toBe('underline line-through')
  })
})

describe('applyScreenFrame', () => {
  it('replaces on a full frame and applies drop and set on a delta', () => {
    const full = applyScreenFrame(['old'], { pane_id: 'p', gen: 1, seq: 1, full: true, lines: ['a', 'b', 'c', 'd'], total: 4 })
    expect(full).toEqual(['a', 'b', 'c', 'd'])
    expect(applyScreenFrame(full, { pane_id: 'p', gen: 1, seq: 2, drop: 2, total: 3, set: [[2, 'e']] })).toEqual(['c', 'd', 'e'])
    expect(applyScreenFrame(full, { pane_id: 'p', gen: 1, seq: 2, total: 1, set: [[0, 'z']] })).toEqual(['z'])
    expect(applyScreenFrame(full, { pane_id: 'p', gen: 1, seq: 2, full: true, total: 0 })).toEqual([])
  })
})
