import { describe, expect, it } from 'vitest'
import { builtinLaunchers, launchInput, launcherGuide, launcherShortcut, parseLauncherDocument } from './launchers'

describe('launcher catalog', () => {
  it('types a command as one line plus Enter and leaves a shell alone', () => {
    expect(launchInput('')).toBe('')
    expect(launchInput('claude')).toBe('claude\r')
    expect(launchInput('codex review')).toBe('codex review\r')
  })

  it('numbers only the first nine rows', () => {
    expect(launcherShortcut(0)).toBe('1')
    expect(launcherShortcut(8)).toBe('9')
    expect(launcherShortcut(9)).toBe('')
    expect(builtinLaunchers.map((item) => item.command)).toEqual(['', 'claude', 'codex', 'gemini', 'cursor-agent', 'pi', 'grok', 'agy'])
  })

  it('accepts the JSON an agent is asked to write', () => {
    expect(parseLauncherDocument('{"launchers":[{"id":"review","name":" 审查 ","command":" codex review "}]}')).toEqual([
      { id: 'review', name: '审查', command: 'codex review' },
    ])
    expect(launcherGuide([])).toContain('"launchers"')
  })

  it('rejects a second line, a reserved id, and a duplicate', () => {
    expect(() => parseLauncherDocument('{"launchers":[{"id":"a","name":"X","command":"echo\\nhi"}]}')).toThrow(/一行/)
    expect(() => parseLauncherDocument('{"launchers":[{"id":"claude","name":"X","command":"claude"}]}')).toThrow(/内置/)
    expect(() => parseLauncherDocument('{"launchers":[{"id":"a","name":"X","command":"true"},{"id":"a","name":"Y","command":"true"}]}')).toThrow(/重复/)
  })
})
