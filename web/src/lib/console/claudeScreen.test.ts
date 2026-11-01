import { describe, expect, it } from 'vitest'
import { plainScreenLine } from '../ansiScreen'
import { CONSOLE_MODES, nextConsoleMode, parseClaudeScreen, parseMode, parseSpinner, screenPreview } from './claudeScreen'
import idle from './__fixtures__/idle.ansi.txt?raw'
import working1 from './__fixtures__/working-1.ansi.txt?raw'
import working2 from './__fixtures__/working-2.ansi.txt?raw'
import working3 from './__fixtures__/working-3.ansi.txt?raw'
import working4 from './__fixtures__/working-4.ansi.txt?raw'
import working5 from './__fixtures__/working-5.ansi.txt?raw'
import working6 from './__fixtures__/working-6.ansi.txt?raw'
import planDialog from './__fixtures__/plan-dialog.text.txt?raw'
import planFeedback from './__fixtures__/plan-feedback.text.txt?raw'
import planFeedbackTyped from './__fixtures__/plan-feedback-typed.text.txt?raw'
import questionMulti from './__fixtures__/question-multi.text.txt?raw'
import questionMultiStep2 from './__fixtures__/question-multi-step2.text.txt?raw'
import questionMultiSubmit from './__fixtures__/question-multi-submit.text.txt?raw'
import questionTyped from './__fixtures__/question-typed.text.txt?raw'
import questionTypeSomething from './__fixtures__/question-typesomething.text.txt?raw'
import trust from './__fixtures__/trust.text.txt?raw'

// 读屏帧里是 ANSI 行；夹具按行拆开后和工作台一样先去掉样式。
const ansi = (text: string) => text.split('\n').map(plainScreenLine)
const plain = (text: string) => text.split('\n')

describe('parseClaudeScreen on recorded screens', () => {
  it.each([
    ['working-1', working1, { verb: 'Wibbling', seconds: 15, tokens: 1200, thinking: true }, '·'],
    ['working-2', working2, { verb: 'Wibbling', seconds: 16, tokens: 1300, thinking: true }, '✻'],
    ['working-3', working3, { verb: 'Wibbling', seconds: 17, tokens: 1300, thinking: true }, '*'],
    ['working-4', working4, { verb: 'Wibbling', seconds: 17, tokens: 1400, thinking: true }, '✢'],
  ])('reads the spinner, the mode and the lines above it on %s', (_name, text, spinner, glyph) => {
    const screen = parseClaudeScreen(ansi(text))
    expect(screen.spinner).toMatchObject(spinner)
    expect(screen.spinner?.raw.startsWith(`${glyph} Wibbling…`)).toBe(true)
    expect(screen.mode).toBe('bypass')
    expect(screen.preview.length).toBeGreaterThan(0)
    expect(screen.preview.length).toBeLessThanOrEqual(12)
    // 正文到 spinner 为止：spinner、输入框、状态栏、模式行都不进画面。
    expect(screen.preview.join('\n')).not.toMatch(/Wibbling|❯|bypass permissions|deepseek|\/effort|─{8}/)
  })

  it('keeps the latest content right above the spinner, without the shared indentation', () => {
    const screen = parseClaudeScreen(ansi(working4))
    expect(screen.preview).toContain('● Update(calc.py)')
    expect(screen.preview.at(-2)).toBe('  ⎿  $ python3 -c "import sys; sys.path.insert(0, \'/tmp/herdrx-spike/proj\'); import calc; assert calc.plus(2, 3) == 5;')
    expect(screen.preview.at(-1)).toBe('     assert not hasattr(calc, \'add\')" && echo "VERIFY OK"')
    expect(screen.preview).toContain('  Running 1 shell command…')
  })

  it('treats a frame without a spinner and the finished line as not working', () => {
    for (const text of [working5, working6, idle]) {
      const screen = parseClaudeScreen(ansi(text))
      expect(screen.spinner).toBeNull()
      expect(screen.mode).toBe('bypass')
    }
    const done = parseClaudeScreen(ansi(idle))
    expect(done.preview.at(-1)).toBe('✻ Cooked for 18s · done 3:11')
    expect(done.preview.join('\n')).not.toContain('/effort')
    expect(parseClaudeScreen(ansi(working5)).preview.at(-1)).toBe('  -')
  })

  it.each([
    ['plan-dialog', planDialog, 'ctrl+g to edit in Vim · ~/.claude/plans/calc-py-add-jolly-hamming.md'],
    ['plan-feedback', planFeedback, 'ctrl+g to edit in Vim · ~/.claude/plans/calc-py-add-jolly-hamming.md'],
    ['plan-feedback-typed', planFeedbackTyped, 'ctrl+g to edit in Vim · ~/.claude/plans/calc-py-add-jolly-hamming.md'],
    ['question-multi', questionMulti, 'Enter to select · Tab/Arrow keys to navigate · Esc to cancel'],
    ['question-multi-step2', questionMultiStep2, 'Enter to select · Tab/Arrow keys to navigate · Esc to cancel'],
    ['question-multi-submit', questionMultiSubmit, '2. Cancel'],
    ['question-typed', questionTyped, 'Enter to select · ↑/↓ to navigate · ctrl+g to edit in Vim · Esc to cancel'],
    ['question-typesomething', questionTypeSomething, 'Enter to select · ↑/↓ to navigate · ctrl+g to edit in Vim · Esc to cancel'],
    ['trust', trust, 'Enter to confirm · Esc to cancel', 11],
  ])('shows the dialog itself as the live preview on %s', (_name, text, last, count = 12) => {
    const screen = parseClaudeScreen(plain(text))
    // 对话框挡住了输入框和模式行；历史里的完成行、选项里的 ❯ 都不是 spinner 或输入框。
    expect(screen.spinner).toBeNull()
    expect(screen.mode).toBeNull()
    expect(screen.preview).toHaveLength(count)
    expect(screen.preview.at(-1)?.trim()).toBe(last)
  })
})

describe('parseSpinner', () => {
  it('accepts every glyph and any missing segment', () => {
    expect(parseSpinner('✶ Herding…')).toEqual({ verb: 'Herding', thinking: false, raw: '✶ Herding…' })
    expect(parseSpinner('✳ Compacting conversation… (esc to interrupt)')).toMatchObject({ verb: 'Compacting conversation', thinking: false })
    expect(parseSpinner('  · Pondering… (1m 5s · ↑ 300 tokens)  ')).toMatchObject({ verb: 'Pondering', seconds: 65, tokens: 300, thinking: false, raw: '· Pondering… (1m 5s · ↑ 300 tokens)' })
    expect(parseSpinner('✻ Clauding… (1h 2m 3s · 2.5m tokens · thinking)')).toMatchObject({ seconds: 3723, tokens: 2_500_000, thinking: true })
    expect(parseSpinner('✢ Wibbling... (9s)')).toMatchObject({ verb: 'Wibbling', seconds: 9 })
  })

  it('rejects finished lines, message bullets and ordinary text', () => {
    expect(parseSpinner('✻ Cooked for 18s · done 3:11')).toBeNull()
    expect(parseSpinner('✻ Sautéed for 3s · done 3:14')).toBeNull()
    expect(parseSpinner('● Running 1 shell command…')).toBeNull()
    expect(parseSpinner('● Thinking for 1s…')).toBeNull()
    expect(parseSpinner('     · 要不要加类型注解？ → 要')).toBeNull()
    expect(parseSpinner('Wibbling… (15s)')).toBeNull()
  })

  it('only looks for the spinner near the input box', () => {
    const lines = ['* loading...', ...Array.from({ length: 13 }, (_, index) => `line ${index}`), '─'.repeat(20), '❯ ', '─'.repeat(20)]
    expect(parseClaudeScreen(lines).spinner).toBeNull()
    lines.splice(1, 0, '✻ Herding… (3s)')
    expect(parseClaudeScreen(lines).spinner).toBeNull()
    lines.splice(15, 0, '✻ Herding… (4s)')
    expect(parseClaudeScreen(lines).spinner).toMatchObject({ seconds: 4 })
  })
})

describe('parseMode', () => {
  it.each([
    ['⏵⏵ bypass permissions on (shift+tab to cycle) · ← 2 agents', 'bypass'],
    ['⏵⏵ auto mode on (shift+tab to cycle)', 'auto'],
    ['⏸ manual mode on', 'manual'],
    ['⏵⏵ accept edits on (shift+tab to cycle)', 'acceptEdits'],
    ['⏸ plan mode on (shift+tab to cycle)', 'plan'],
  ])('reads %s', (line, mode) => {
    expect(parseMode(['content', '─'.repeat(20), '❯ ', '─'.repeat(20), `  ${line}`, ''])).toBe(mode)
  })

  it('ignores other bottom lines and mode text far above the bottom', () => {
    expect(parseMode(['? for shortcuts'])).toBeNull()
    expect(parseMode(['⏸ plan mode on', 'a', 'b', 'c', 'd', 'e', 'f'])).toBeNull()
  })

  it('cycles in the Shift+Tab order', () => {
    expect(CONSOLE_MODES.map(nextConsoleMode)).toEqual(['auto', 'manual', 'acceptEdits', 'plan', 'bypass'])
  })
})

describe('screenPreview', () => {
  it('limits the line count and drops rules and right-aligned hints', () => {
    const lines = ['    a', '', '    b', '──────────', `${' '.repeat(60)}↓`, '      c']
    expect(screenPreview(lines)).toEqual(['a', 'b', '  c'])
    expect(screenPreview(lines, lines.length, 2)).toEqual(['b', '  c'])
    expect(screenPreview([])).toEqual([])
  })
})
