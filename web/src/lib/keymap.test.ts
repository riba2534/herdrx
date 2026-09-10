import { describe, expect, it } from 'vitest'
import { isLocalInputTarget, isModifierKey, isPrefixChord, keymapHelpGroups, matchPrefixAction, paneDisplayName, prefixKeyId, prefixModeBarItems } from './keymap'

function key(partial: Partial<KeyboardEvent> & { key: string }): KeyboardEvent {
  return {
    key: partial.key,
    ctrlKey: Boolean(partial.ctrlKey),
    altKey: Boolean(partial.altKey),
    metaKey: Boolean(partial.metaKey),
    shiftKey: Boolean(partial.shiftKey),
    target: partial.target ?? document.body,
  } as KeyboardEvent
}

describe('prefix keymap', () => {
  it('treats a second Ctrl+B as send-prefix so the pane can receive 0x02', () => {
    const event = key({ key: 'b', ctrlKey: true })
    expect(isPrefixChord(event)).toBe(true)
    expect(matchPrefixAction(event)?.binding.action).toBe('send-prefix')
  })

  it('does not cancel prefix when only Shift is pressed', () => {
    expect(isModifierKey(key({ key: 'Shift', shiftKey: true }))).toBe(true)
    expect(matchPrefixAction(key({ key: 'Shift', shiftKey: true }))).toBeUndefined()
    expect(matchPrefixAction(key({ key: 'n', shiftKey: true }))?.binding.action).toBe('new-workspace')
  })

  it('maps Ctrl+B 2 to switch-tab index 2', () => {
    const match = matchPrefixAction(key({ key: '2' }))
    expect(match?.binding.action).toBe('switch-tab')
    expect(match?.tabIndex).toBe(2)
    expect(prefixKeyId(key({ key: '2' }))).toBe('2')
  })

  it('ignores composer and search fields as prefix targets', () => {
    const composer = document.createElement('textarea')
    composer.className = 'input composer-input'
    const search = document.createElement('input')
    search.setAttribute('aria-label', '搜索内容')
    const wrap = document.createElement('form')
    wrap.className = 'terminal-search'
    wrap.append(search)
    const terminal = document.createElement('textarea')
    terminal.className = 'xterm-helper-textarea'
    expect(isLocalInputTarget(composer)).toBe(true)
    expect(isLocalInputTarget(search)).toBe(true)
    expect(isLocalInputTarget(terminal)).toBe(false)
  })

  it('lists help groups and marks resize-mode as implemented', () => {
    const groups = keymapHelpGroups()
    expect(groups.map((group) => group.title)).toEqual(['全局', '导航', '标签', '终端'])
    const resize = groups.find((group) => group.id === 'pane')?.entries.find((entry) => entry.chord === 'Ctrl+B R')
    expect(resize).toEqual({ chord: 'Ctrl+B R', label: '调整分屏比例', implemented: true })
    expect(prefixModeBarItems()).toContain('? 帮助')
    expect(prefixModeBarItems()).toContain('r 比例')
    expect(prefixModeBarItems()[0]).toBe('esc 取消')
  })

  it('prefers label, then agent, then title, then id', () => {
    expect(paneDisplayName({ pane_id: 'p3', agent: 'codex', terminal_title_stripped: 'zsh' })).toBe('codex')
    expect(paneDisplayName({ pane_id: 'p3', terminal_title_stripped: 'zsh' })).toBe('zsh')
    expect(paneDisplayName({ pane_id: 'p3' })).toBe('p3')
  })
})
