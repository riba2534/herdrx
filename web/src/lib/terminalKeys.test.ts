import { describe, expect, it } from 'vitest'

import { applyModifiers, hasModifiers, isModifiableKey, NO_MODIFIERS, pasteTextTooLarge, sanitizePasteText, STICKY_MODIFIER_KEYS, TERMINAL_AUXILIARY_KEYS, TERMINAL_AUXILIARY_EXTRA_KEYS, type StickyModifiers } from './terminalKeys'

describe('terminal auxiliary keys aligned with the orca mobile bar', () => {
  it('keeps orca ordering: editing keys first, control keys last', () => {
    const ids = TERMINAL_AUXILIARY_KEYS.map((key) => key.id)
    // 大拇指最常按的 Enter 必须在很短的一段内，不能埋在控制键后面。
    expect(ids.slice(0, 3)).toEqual(['escape', 'tab', 'enter'])
    expect(ids.indexOf('space')).toBeLessThan(ids.indexOf('backspace'))
    expect(ids.indexOf('space')).toBeLessThan(ids.indexOf('arrowUp'))
    expect(ids.indexOf('arrowRight')).toBeLessThan(ids.indexOf('ctrlC'))
    expect(ids.at(-1)).toBe('ctrlU')
  })

  it('sends the exact bytes each key stands for', () => {
    const bytes = new Map(TERMINAL_AUXILIARY_KEYS.map((key) => [key.id, key.bytes]))
    expect(bytes.get('escape')).toBe('\x1b')
    expect(bytes.get('tab')).toBe('\t')
    expect(bytes.get('enter')).toBe('\r')
    // 终端程序把 ESC [ Z 识别成反向 Tab。
    expect(bytes.get('shiftTab')).toBe('\x1b[Z')
    expect(bytes.get('space')).toBe(' ')
    expect(bytes.get('backspace')).toBe('\x7f')
    expect(bytes.get('delete')).toBe('\x1b[3~')
    expect(bytes.get('arrowUp')).toBe('\x1b[A')
    expect(bytes.get('ctrlC')).toBe('\x03')
    expect(bytes.get('ctrlD')).toBe('\x04')
  })

  it('marks only the keys a terminal actually auto-repeats as repeatable', () => {
    const repeatable = TERMINAL_AUXILIARY_KEYS.filter((key) => key.repeatable).map((key) => key.id)
    expect(repeatable).toEqual(['backspace', 'delete', 'arrowUp', 'arrowDown', 'arrowLeft', 'arrowRight'])
  })

  it('gives every key a unique id, a non-empty byte sequence and a screen-reader name', () => {
    const all = [...TERMINAL_AUXILIARY_KEYS, ...TERMINAL_AUXILIARY_EXTRA_KEYS]
    const ids = all.map((key) => key.id)
    expect(new Set(ids).size).toBe(ids.length)
    for (const key of all) {
      expect(key.bytes.length).toBeGreaterThan(0)
      // 按钮文字是 Enter、⌫ 这类符号，读屏只靠 aria 说明用途。
      expect(key.aria.length).toBeGreaterThan(0)
      expect(key.aria).not.toBe(key.label)
    }
  })

  it('keeps the CJK-typing punctuation keys out of the orca ordering', () => {
    expect(TERMINAL_AUXILIARY_EXTRA_KEYS.map((key) => key.label)).toEqual(['-', '/', '|', '~'])
  })
})

describe('sticky modifiers', () => {
  const combos: [string, StickyModifiers, number][] = [
    ['Shift', { ctrl: false, alt: false, shift: true }, 2],
    ['Alt', { ctrl: false, alt: true, shift: false }, 3],
    ['Alt+Shift', { ctrl: false, alt: true, shift: true }, 4],
    ['Ctrl', { ctrl: true, alt: false, shift: false }, 5],
    ['Ctrl+Shift', { ctrl: true, alt: false, shift: true }, 6],
    ['Ctrl+Alt', { ctrl: true, alt: true, shift: false }, 7],
    ['Ctrl+Alt+Shift', { ctrl: true, alt: true, shift: true }, 8],
  ]

  it('leaves input untouched without modifiers', () => {
    for (const key of [...TERMINAL_AUXILIARY_KEYS, ...TERMINAL_AUXILIARY_EXTRA_KEYS]) expect(applyModifiers(key.bytes, NO_MODIFIERS)).toBe(key.bytes)
    expect(applyModifiers('a', NO_MODIFIERS)).toBe('a')
  })

  it.each(combos)('encodes arrows and Del with %s as CSI parameters', (_name, modifiers, parameter) => {
    for (const [plain, final] of [['\x1b[A', 'A'], ['\x1b[B', 'B'], ['\x1b[C', 'C'], ['\x1b[D', 'D'], ['\x1bOA', 'A'], ['\x1bOD', 'D']]) {
      expect(applyModifiers(plain, modifiers)).toBe(`\x1b[1;${parameter}${final}`)
    }
    expect(applyModifiers('\x1b[3~', modifiers)).toBe(`\x1b[3;${parameter}~`)
  })

  it('matches the documented examples', () => {
    expect(applyModifiers('\x1b[D', { ctrl: false, alt: true, shift: false })).toBe('\x1b[1;3D')
    expect(applyModifiers('\x1b[C', { ctrl: true, alt: false, shift: false })).toBe('\x1b[1;5C')
  })

  it.each(combos)('encodes letters, space and punctuation with %s', (_name, modifiers) => {
    const meta = modifiers.alt ? '\x1b' : ''
    for (const letter of 'azAZ') {
      const expected = modifiers.ctrl ? String.fromCharCode(letter.toUpperCase().charCodeAt(0) & 0x1f) : modifiers.shift ? letter.toUpperCase() : letter
      expect(applyModifiers(letter, modifiers)).toBe(meta + expected)
    }
    expect(applyModifiers(' ', modifiers)).toBe(meta + (modifiers.ctrl ? '\x00' : ' '))
    // 数字和 ~ 等没有 Ctrl 映射，原样发出；Alt 仍是前缀 ESC。
    expect(applyModifiers('7', modifiers)).toBe(meta + '7')
    expect(applyModifiers('~', modifiers)).toBe(meta + '~')
    expect(applyModifiers('中', modifiers)).toBe(meta + '中')
  })

  it.each(combos)('encodes Tab, Enter, Backspace and Esc with %s', (_name, modifiers) => {
    const meta = modifiers.alt ? '\x1b' : ''
    expect(applyModifiers('\t', modifiers)).toBe(meta + (modifiers.shift ? '\x1b[Z' : '\t'))
    expect(applyModifiers('\r', modifiers)).toBe(meta + '\r')
    expect(applyModifiers('\x7f', modifiers)).toBe(meta + (modifiers.ctrl ? '\x08' : '\x7f'))
    expect(applyModifiers('\x1b', modifiers)).toBe(meta + '\x1b')
    expect(applyModifiers('\x1b[Z', modifiers)).toBe(meta + '\x1b[Z')
    // 键栏上的固定组合（如 Ctrl+C）本身是控制字符，只有 Alt 会叠加。
    expect(applyModifiers('\x03', modifiers)).toBe(meta + '\x03')
  })

  it('maps the remaining Ctrl punctuation like xterm', () => {
    const ctrl = { ctrl: true, alt: false, shift: false }
    expect(applyModifiers('[', ctrl)).toBe('\x1b')
    expect(applyModifiers('_', ctrl)).toBe('\x1f')
    expect(applyModifiers('@', ctrl)).toBe('\x00')
    expect(applyModifiers('?', ctrl)).toBe('\x7f')
  })

  it('does not treat IME commits or pastes as one key', () => {
    const all = { ctrl: true, alt: true, shift: true }
    expect(applyModifiers('你好', all)).toBe('你好')
    expect(applyModifiers('ls -la', all)).toBe('ls -la')
    // xterm 自己发出的焦点回报不是按键，不能消耗修饰键。
    expect(isModifiableKey('\x1b[I')).toBe(false)
    expect(isModifiableKey('你好')).toBe(false)
    for (const key of [...TERMINAL_AUXILIARY_KEYS, ...TERMINAL_AUXILIARY_EXTRA_KEYS]) expect(isModifiableKey(key.bytes)).toBe(true)
  })

  it('exposes Ctrl, Alt and Shift with screen-reader names', () => {
    expect(STICKY_MODIFIER_KEYS.map((key) => key.id)).toEqual(['ctrl', 'alt', 'shift'])
    expect(hasModifiers(NO_MODIFIERS)).toBe(false)
    expect(hasModifiers({ ctrl: false, alt: false, shift: true })).toBe(true)
  })
})

describe('paste text guards', () => {
  it('replaces ESC with a visible marker so pasted text cannot close the paste frame', () => {
    expect(sanitizePasteText('safe\x1b[201~\rrm -rf /')).toBe('safe␛[201~\rrm -rf /')
    expect(sanitizePasteText('no escapes')).toBe('no escapes')
  })

  it('flags only text over the 256 KiB budget as too large', () => {
    expect(pasteTextTooLarge('a'.repeat(256 * 1024))).toBe(false)
    expect(pasteTextTooLarge('a'.repeat(256 * 1024 + 1))).toBe(true)
    // 上限按字节算，中文一个字三字节，不能按字符数判断。
    expect(pasteTextTooLarge('中'.repeat(90_000))).toBe(true)
  })
})
