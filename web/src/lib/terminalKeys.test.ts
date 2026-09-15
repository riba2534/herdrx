import { describe, expect, it } from 'vitest'

import { pasteTextTooLarge, sanitizePasteText, TERMINAL_AUXILIARY_KEYS, TERMINAL_AUXILIARY_EXTRA_KEYS } from './terminalKeys'

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
