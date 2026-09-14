import { describe, expect, it, vi } from 'vitest'
import { AUTO_FIT_MIN_FONT_SIZE, autoFitFont, fittedTerminalFont, TERMINAL_FONT_FAMILY, whenFontsReady } from './terminalFit'

describe('offscreen terminal sizing', () => {
  const bounds = { width: 1260, height: 840, cols: 200, rows: 80, lineHeight: 1, letterSpacing: 0, dpr: 1 }
  const measure = (size: number) => ({ width: size * 0.6, height: size })

  it('fits both regular and high-DPI cell boundaries without overflowing a row', () => {
    // 10.5px cells fit at DPR=2, but round up to 11px (880px total) at DPR=1.
    expect(fittedTerminalFont(bounds, measure)).toBe(10)
    expect(fittedTerminalFont({ ...bounds, dpr: 2 }, measure)).toBe(10.5)
  })

  it('computes the final width-limited size independently of previous fits', () => {
    const room = { ...bounds, cols: 150, rows: 40, height: 600 }
    const collapsed = fittedTerminalFont({ ...room, width: 1080 }, measure)!
    const expanded = fittedTerminalFont({ ...room, width: 900 }, measure)!
    expect(collapsed).toBeGreaterThan(expanded)
    expect(fittedTerminalFont({ ...room, width: 1080 }, measure)).toBe(collapsed)
    expect(Math.round(measure(expanded).width * room.cols)).toBeLessThanOrEqual(900)
    expect(Math.round(measure(expanded + 0.01).width * room.cols)).toBeGreaterThan(900)
  })

  it('skips hidden containers and respects the maximum font size', () => {
    const spy = vi.fn(measure)
    expect(fittedTerminalFont({ ...bounds, width: 0 }, spy)).toBeNull()
    expect(spy).not.toHaveBeenCalled()
    expect(fittedTerminalFont({ ...bounds, width: 4000, height: 3000 }, measure)).toBe(14)
    expect(fittedTerminalFont(bounds, () => null)).toBeNull()
  })

  it('prefers JetBrains Mono then ui-monospace, Menlo and Roboto Mono before generic monospace', () => {
    expect(TERMINAL_FONT_FAMILY.startsWith('"JetBrains Mono"')).toBe(true)
    expect(TERMINAL_FONT_FAMILY).toContain('ui-monospace')
    expect(TERMINAL_FONT_FAMILY).toContain('Menlo')
    expect(TERMINAL_FONT_FAMILY).toContain('"Roboto Mono"')
    expect(TERMINAL_FONT_FAMILY.endsWith('monospace')).toBe(true)
  })

  it('keeps auto mode readable and hands off to cropping below the floor', () => {
    const room = { ...bounds, cols: 80, rows: 40 }
    expect(AUTO_FIT_MIN_FONT_SIZE).toBe(10)
    expect(autoFitFont({ ...room, width: 500, height: 400 }, measure)).toBe(10)
    expect(autoFitFont({ ...room, width: 5000, height: 4000 }, measure)).toBe(14)
    // 80 columns in 200 px would need ~2 px text, so auto must crop instead.
    expect(fittedTerminalFont({ ...room, width: 200, height: 80 }, measure)).toBe(2)
    expect(autoFitFont({ ...room, width: 200, height: 80 }, measure)).toBeNull()
    expect(autoFitFont({ ...room, width: 0 }, measure)).toBeNull()
    expect(autoFitFont(room, () => null)).toBeNull()
  })

  it('waits for document.fonts.ready before measuring', async () => {
    await expect(whenFontsReady()).resolves.toBeUndefined()
  })
})
