export type KeymapGroup = 'global' | 'navigation' | 'tab' | 'pane'

export type PrefixAction =
  | 'send-prefix'
  | 'split-right'
  | 'split-down'
  | 'zoom'
  | 'close-pane'
  | 'focus-left'
  | 'focus-down'
  | 'focus-up'
  | 'focus-right'
  | 'swap-left'
  | 'swap-down'
  | 'swap-up'
  | 'swap-right'
  | 'cycle-pane-next'
  | 'cycle-pane-previous'
  | 'new-tab'
  | 'close-tab'
  | 'next-tab'
  | 'previous-tab'
  | 'switch-tab'
  | 'toggle-sidebar'
  | 'switcher'
  | 'new-workspace'
  | 'switch-workspace'
  | 'close-workspace'
  | 'rename-tab'
  | 'rename-pane'
  | 'help'
  | 'settings'
  | 'resize-mode'

export type PrefixBinding = {
  action: PrefixAction
  key: string
  group: KeymapGroup
  label: string
  chord: string
  implemented: boolean
  modeBar?: string
}

export const PREFIX_BINDINGS: PrefixBinding[] = [
  { action: 'send-prefix', key: 'ctrl+b', group: 'global', label: '向终端发送 Ctrl+B', chord: 'Ctrl+B Ctrl+B', implemented: true },
  { action: 'help', key: '?', group: 'global', label: '快捷键帮助', chord: 'Ctrl+B ?', implemented: true },
  { action: 'settings', key: 's', group: 'global', label: '工作台设置', chord: 'Ctrl+B S', implemented: true, modeBar: 's 设置' },
  { action: 'switcher', key: 'w', group: 'navigation', label: '切换工作区或终端', chord: 'Ctrl+B W', implemented: true, modeBar: 'w 切换' },
  { action: 'switcher', key: 'g', group: 'navigation', label: '打开位置切换器', chord: 'Ctrl+B G', implemented: true },
  { action: 'toggle-sidebar', key: 'b', group: 'navigation', label: '切换侧边栏', chord: 'Ctrl+B B', implemented: true, modeBar: 'b 侧栏' },
  { action: 'focus-left', key: 'h', group: 'navigation', label: '焦点移到左侧 Pane', chord: 'Ctrl+B H', implemented: true, modeBar: 'hjkl 焦点' },
  { action: 'focus-down', key: 'j', group: 'navigation', label: '焦点移到下方 Pane', chord: 'Ctrl+B J', implemented: true },
  { action: 'focus-up', key: 'k', group: 'navigation', label: '焦点移到上方 Pane', chord: 'Ctrl+B K', implemented: true },
  { action: 'focus-right', key: 'l', group: 'navigation', label: '焦点移到右侧 Pane', chord: 'Ctrl+B L', implemented: true },
  { action: 'cycle-pane-next', key: 'tab', group: 'navigation', label: '轮换到下一个 Pane', chord: 'Ctrl+B Tab', implemented: true, modeBar: '⇥ Pane' },
  { action: 'cycle-pane-previous', key: 'shift+tab', group: 'navigation', label: '轮换到上一个 Pane', chord: 'Ctrl+B Shift+Tab', implemented: true },
  { action: 'switch-workspace', key: 'shift+w', group: 'navigation', label: '切换到下一个工作区', chord: 'Ctrl+B Shift+W', implemented: true },
  { action: 'new-workspace', key: 'shift+n', group: 'navigation', label: '新建工作区', chord: 'Ctrl+B Shift+N', implemented: true },
  { action: 'close-workspace', key: 'shift+d', group: 'navigation', label: '关闭当前工作区', chord: 'Ctrl+B Shift+D', implemented: true },
  { action: 'new-tab', key: 'c', group: 'tab', label: '新建标签页', chord: 'Ctrl+B C', implemented: true, modeBar: 'c 新标签' },
  { action: 'next-tab', key: 'n', group: 'tab', label: '下一个标签页', chord: 'Ctrl+B N', implemented: true, modeBar: 'n/p 标签' },
  { action: 'previous-tab', key: 'p', group: 'tab', label: '上一个标签页', chord: 'Ctrl+B P', implemented: true },
  { action: 'switch-tab', key: '1..9', group: 'tab', label: '切换到标签 1–9', chord: 'Ctrl+B 1..9', implemented: true, modeBar: '1..9 标签' },
  { action: 'close-tab', key: 'shift+x', group: 'tab', label: '关闭当前标签页', chord: 'Ctrl+B Shift+X', implemented: true },
  { action: 'rename-tab', key: 'shift+t', group: 'tab', label: '重命名当前标签页', chord: 'Ctrl+B Shift+T', implemented: true },
  { action: 'split-right', key: 'v', group: 'pane', label: '向右分屏', chord: 'Ctrl+B V', implemented: true, modeBar: 'v 向右分屏' },
  { action: 'split-down', key: '-', group: 'pane', label: '向下分屏', chord: 'Ctrl+B -', implemented: true, modeBar: '− 向下分屏' },
  { action: 'zoom', key: 'z', group: 'pane', label: '切换 Pane 缩放', chord: 'Ctrl+B Z', implemented: true, modeBar: 'z 缩放' },
  { action: 'close-pane', key: 'x', group: 'pane', label: '关闭当前 Pane', chord: 'Ctrl+B X', implemented: true, modeBar: 'x 关闭' },
  { action: 'swap-left', key: 'shift+h', group: 'pane', label: '与左侧 Pane 互换', chord: 'Ctrl+B Shift+H', implemented: true },
  { action: 'swap-down', key: 'shift+j', group: 'pane', label: '与下方 Pane 互换', chord: 'Ctrl+B Shift+J', implemented: true },
  { action: 'swap-up', key: 'shift+k', group: 'pane', label: '与上方 Pane 互换', chord: 'Ctrl+B Shift+K', implemented: true },
  { action: 'swap-right', key: 'shift+l', group: 'pane', label: '与右侧 Pane 互换', chord: 'Ctrl+B Shift+L', implemented: true },
  { action: 'rename-pane', key: 'shift+p', group: 'pane', label: '重命名当前 Pane', chord: 'Ctrl+B Shift+P', implemented: true },
  { action: 'resize-mode', key: 'r', group: 'pane', label: '调整分屏比例（未实现）', chord: 'Ctrl+B R', implemented: false },
]

const GROUP_TITLES: Record<KeymapGroup, string> = {
  global: '全局',
  navigation: '导航',
  tab: '标签',
  pane: 'Pane',
}

const GROUP_ORDER: KeymapGroup[] = ['global', 'navigation', 'tab', 'pane']

export function isModifierKey(event: Pick<KeyboardEvent, 'key'>): boolean {
  return event.key === 'Shift' || event.key === 'Control' || event.key === 'Alt' || event.key === 'Meta'
}

export function isPrefixChord(event: Pick<KeyboardEvent, 'key' | 'ctrlKey' | 'altKey' | 'metaKey'>): boolean {
  return event.ctrlKey && !event.altKey && !event.metaKey && event.key.toLowerCase() === 'b'
}

export function isLocalInputTarget(target: EventTarget | null): boolean {
  const el = target as HTMLElement | null
  if (!el || typeof el.closest !== 'function') return false
  if (el.closest('[role="dialog"], [role="alertdialog"], [data-ui-overlay], .terminal-search')) return true
  if (el.classList?.contains('composer-input') || el.closest('.composer-input')) return true
  const tag = el.tagName
  if ((tag === 'INPUT' || tag === 'TEXTAREA' || tag === 'SELECT') && !el.classList.contains('xterm-helper-textarea')) return true
  return Boolean(el.isContentEditable)
}

export function prefixKeyId(event: Pick<KeyboardEvent, 'key' | 'shiftKey' | 'ctrlKey' | 'altKey' | 'metaKey'>): string {
  if (isPrefixChord(event)) return 'ctrl+b'
  const key = event.key
  if (key === 'Tab') return event.shiftKey ? 'shift+tab' : 'tab'
  if (key === 'Escape') return 'escape'
  if (key === '?' || (event.shiftKey && key === '/')) return '?'
  if (key === '_') return '-'
  if (key.length === 1) {
    const lower = key.toLowerCase()
    if (event.shiftKey && lower >= 'a' && lower <= 'z') return `shift+${lower}`
    return lower
  }
  return key.toLowerCase()
}

export type PrefixMatch = { binding: PrefixBinding; tabIndex?: number }

export function matchPrefixAction(event: Pick<KeyboardEvent, 'key' | 'shiftKey' | 'ctrlKey' | 'altKey' | 'metaKey'>): PrefixMatch | undefined {
  if (event.altKey || event.metaKey) return undefined
  if (event.ctrlKey && !isPrefixChord(event)) return undefined
  const id = prefixKeyId(event)
  if (/^[1-9]$/.test(id)) {
    const binding = PREFIX_BINDINGS.find((item) => item.action === 'switch-tab')
    return binding ? { binding, tabIndex: Number(id) } : undefined
  }
  const binding = PREFIX_BINDINGS.find((item) => item.key === id)
  return binding ? { binding } : undefined
}

export function prefixModeBarItems(): string[] {
  const items = ['esc 取消']
  const seen = new Set<string>()
  for (const binding of PREFIX_BINDINGS) {
    if (!binding.implemented || !binding.modeBar || seen.has(binding.modeBar)) continue
    seen.add(binding.modeBar)
    items.push(binding.modeBar)
  }
  items.push('? 帮助')
  return items
}

export type KeymapHelpGroup = { id: KeymapGroup; title: string; entries: Array<{ chord: string; label: string; implemented: boolean }> }

export function keymapHelpGroups(): KeymapHelpGroup[] {
  return GROUP_ORDER.map((id) => ({
    id,
    title: GROUP_TITLES[id],
    entries: PREFIX_BINDINGS.filter((binding) => binding.group === id).map((binding) => ({
      chord: binding.chord,
      label: binding.label,
      implemented: binding.implemented,
    })),
  }))
}

export function paneDisplayName(pane: { label?: string; agent?: string; terminal_title_stripped?: string; pane_id: string }): string {
  return pane.label || pane.agent || pane.terminal_title_stripped || pane.pane_id
}
