import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { Snapshot } from '../types'
import { WorkbenchPage } from './WorkbenchPage'
import { api } from '../lib/api'
import { clearComposerDrafts } from '../lib/composerDrafts'

const { call, connection, retryNow } = vi.hoisted(() => ({ call: vi.fn().mockResolvedValue({}), connection: { state: 'ready' as string, message: '', retryAt: 0 }, retryNow: vi.fn() }))
const snapshot: Snapshot = {
  version: 'test', protocol: 1,
  focused_workspace_id: 'w1', focused_tab_id: 'w1:t1', focused_pane_id: 'w1:p1',
  workspaces: ['alpha', 'beta'].map((label, i) => ({ workspace_id: `w${i + 1}`, label, number: i + 1, active_tab_id: `w${i + 1}:t1`, agent_status: 'idle', focused: i === 0, pane_count: 1, tab_count: 1 })),
  tabs: [
    { tab_id: 'w1:t1', workspace_id: 'w1', label: '1', number: 1, pane_count: 1, agent_status: 'idle', focused: true },
    { tab_id: 'w1:t2', workspace_id: 'w1', label: '2', number: 2, pane_count: 1, agent_status: 'idle', focused: false },
    { tab_id: 'w2:t1', workspace_id: 'w2', label: '1', number: 1, pane_count: 1, agent_status: 'idle', focused: false },
  ],
  panes: [
    { pane_id: 'w1:p1', workspace_id: 'w1', tab_id: 'w1:t1', terminal_id: 'term1', agent_status: 'idle', focused: true, revision: 1 },
    { pane_id: 'w1:p2', workspace_id: 'w1', tab_id: 'w1:t2', terminal_id: 'term3', agent_status: 'idle', focused: false, revision: 1 },
    { pane_id: 'w2:p1', workspace_id: 'w2', tab_id: 'w2:t1', terminal_id: 'term2', agent_status: 'idle', focused: false, revision: 1 },
  ],
  layouts: [],
  agents: [1, 2].map((i) => ({ name: `agent${i}`, agent: 'codex', agent_status: 'idle', pane_id: `w${i}:p1`, workspace_id: `w${i}`, tab_id: `w${i}:t1`, focused: i === 1 })),
}

vi.mock('../lib/workbench', () => ({
  WorkbenchClient: class {
    call = call
    onSnapshot(handler: (value: Snapshot) => void) { handler(snapshot); return () => {} }
    onState(handler: (state: string, message?: string, retryAt?: number) => void) { handler(connection.state, connection.message, connection.retryAt); return () => {} }
    onEpoch() { return () => {} }
    connect() {}
    retryNow = retryNow
    dispose() {}
    hasOpenTerminals() { return false }
  },
}))
vi.mock('../lib/api', () => ({
  api: {
    host: vi.fn().mockResolvedValue({ host: { id: 'host', name: 'Local', transport: 'local' } }),
    hosts: vi.fn().mockResolvedValue({ hosts: [{ id: 'host', name: 'Local', transport: 'local' }, { id: 'host-other', name: 'Office', transport: 'ssh' }] }),
    renameHost: vi.fn().mockImplementation(async (id: string, name: string) => ({ host: { id, name, transport: 'local' } })),
  },
  currentSessionID: () => 'workbench-session',
  onAuthEvent: () => () => {},
}))
vi.mock('../components/TerminalPane', () => ({ TerminalPane: () => null }))

beforeEach(() => {
  localStorage.clear()
  sessionStorage.clear()
  clearComposerDrafts()
  call.mockClear()
  retryNow.mockClear()
  connection.state = 'ready'
  connection.message = ''
  connection.retryAt = 0
  snapshot.tabs = [
    { tab_id: 'w1:t1', workspace_id: 'w1', label: '1', number: 1, pane_count: 1, agent_status: 'idle', focused: true },
    { tab_id: 'w1:t2', workspace_id: 'w1', label: '2', number: 2, pane_count: 1, agent_status: 'idle', focused: false },
    { tab_id: 'w2:t1', workspace_id: 'w2', label: '1', number: 1, pane_count: 1, agent_status: 'idle', focused: false },
  ]
  snapshot.panes = [
    { pane_id: 'w1:p1', workspace_id: 'w1', tab_id: 'w1:t1', terminal_id: 'term1', agent_status: 'idle', focused: true, revision: 1 },
    { pane_id: 'w1:p2', workspace_id: 'w1', tab_id: 'w1:t2', terminal_id: 'term3', agent_status: 'idle', focused: false, revision: 1 },
    { pane_id: 'w2:p1', workspace_id: 'w2', tab_id: 'w2:t1', terminal_id: 'term2', agent_status: 'idle', focused: false, revision: 1 },
  ]
  snapshot.layouts = []
  vi.stubGlobal('matchMedia', () => ({ matches: false, addEventListener() {}, removeEventListener() {} }))
})
afterEach(() => {
  vi.useRealTimers()
  vi.unstubAllGlobals()
})

describe('workbench sidebar context menus', () => {
  it('switches the current host from the top navigation', async () => {
    render(<WorkbenchPage hostID="host"/>)
    fireEvent.click(await screen.findByRole('link', { name: 'Office' }))
    expect(window.location.pathname).toBe('/h/host-other')
    window.history.replaceState({}, '', '/')
  })

  it('renames the current host from the top navigation', async () => {
    render(<WorkbenchPage hostID="host"/>)
    expect(await screen.findByRole('link', { name: 'Local' })).toHaveAttribute('aria-current', 'page')
    fireEvent.click(screen.getByRole('button', { name: '重命名当前主机' }))
    fireEvent.change(screen.getByRole('textbox', { name: '主机名称' }), { target: { value: 'Home workstation' } })
    fireEvent.click(screen.getByRole('button', { name: '保存名称' }))
    await screen.findByRole('link', { name: 'Home workstation' })
    expect(api.renameHost).toHaveBeenCalledWith('host', 'Home workstation')
  })

  it('handles blank section space and opens settings from its menu', async () => {
    const { container } = render(<WorkbenchPage hostID="host"/>)
    await screen.findByRole('button', { name: /agent1/ })
    const blank = container.querySelectorAll('.sidebar-section')[1]
    expect(fireEvent.contextMenu(blank, { clientX: 40, clientY: 400 })).toBe(false)
    expect(screen.getByRole('menu', { name: '侧栏右键菜单' })).toBeInTheDocument()
    expect(screen.getByRole('menuitem', { name: '新建工作区' })).toBeEnabled()
    expect(screen.queryByRole('menuitem', { name: '关闭工作区' })).not.toBeInTheDocument()
    fireEvent.click(screen.getByRole('menuitem', { name: '工作台设置' }))
    expect(screen.getByRole('dialog', { name: '工作台设置' })).toBeInTheDocument()
    expect(screen.queryByRole('menu')).not.toBeInTheDocument()
  })

  it('keeps the workspace row menu instead of replacing it with the sidebar fallback', async () => {
    const { container } = render(<WorkbenchPage hostID="host"/>)
    await screen.findByRole('button', { name: /agent1/ })
    const label = container.querySelector('.workspace-row strong')!
    expect(fireEvent.contextMenu(label)).toBe(false)
    expect(screen.getByRole('menu', { name: '工作区右键菜单' })).toBeInTheDocument()
    expect(screen.getByRole('menuitem', { name: '重命名工作区' })).toBeInTheDocument()
    expect(screen.queryByRole('menuitem', { name: '新建工作区' })).not.toBeInTheDocument()
  })

  it('targets the right-clicked Agent pane, not the previously selected pane', async () => {
    render(<WorkbenchPage hostID="host"/>)
    const agent = await screen.findByRole('button', { name: /agent2/ })
    expect(fireEvent.contextMenu(agent.querySelector('strong')!)).toBe(false)
    expect(screen.getByRole('menu', { name: '终端右键菜单' })).toBeInTheDocument()
    expect(screen.queryByRole('menuitem', { name: '与当前终端互换' })).not.toBeInTheDocument()
    fireEvent.click(screen.getByRole('menuitem', { name: '重命名终端' }))
    fireEvent.change(screen.getByRole('textbox', { name: '名称' }), { target: { value: 'renamed-agent' } })
    fireEvent.click(screen.getByRole('button', { name: '保存' }))
    await waitFor(() => expect(call).toHaveBeenCalledWith('pane.rename', { pane_id: 'w2:p1', label: 'renamed-agent' }))
  })
})

describe('workbench composer', () => {
  it('hides the local composer on desktop until it is opened, then submits the current pane once', async () => {
    render(<WorkbenchPage hostID="host"/>)
    await screen.findByRole('button', { name: /agent1/ })
    expect(screen.queryByRole('region', { name: '本地输入' })).not.toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: '本地输入框' }))
    const box = screen.getByRole('textbox', { name: '本地输入内容' })
    fireEvent.change(box, { target: { value: '整段提交' } })
    fireEvent.click(screen.getByRole('button', { name: '发送' }))
    await waitFor(() => expect(call).toHaveBeenCalledWith('pane.send_input', { pane_id: 'w1:p1', text: '整段提交', keys: ['Enter'] }))
  })

  it('shows the local composer by default on a compact workbench', async () => {
    vi.stubGlobal('matchMedia', () => ({ matches: true, addEventListener() {}, removeEventListener() {} }))
    render(<WorkbenchPage hostID="host"/>)
    expect(await screen.findByRole('region', { name: '本地输入' })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: '发送' })).toBeDisabled()
  })

  it('keeps a failed workspace creation visible inside the mobile switcher', async () => {
    vi.stubGlobal('matchMedia', () => ({ matches: true, addEventListener() {}, removeEventListener() {} }))
    call.mockRejectedValueOnce(new Error('没有创建工作区的权限'))
    render(<WorkbenchPage hostID="host"/>)
    fireEvent.click(await screen.findByRole('button', { name: '切换工作区或终端' }))
    const switcher = screen.getByRole('dialog', { name: '切换工作区或终端' })
    fireEvent.click(within(switcher).getByRole('button', { name: '新建工作区' }))
    expect(await within(switcher).findByRole('alert')).toHaveTextContent('没有创建工作区的权限')
    expect(within(switcher).getByRole('button', { name: '新建工作区' })).toBeEnabled()
    expect(call).toHaveBeenCalledWith('workspace.create', expect.anything())
  })

  it('restores desktop direct input after a compact visit and keeps an explicit desktop composer', async () => {
    const media = { matches: false, listeners: [] as Array<() => void>, addEventListener(_type: string, fn: () => void) { this.listeners.push(fn) }, removeEventListener(_type: string, fn: () => void) { this.listeners = this.listeners.filter((item) => item !== fn) }, set(next: boolean) { this.matches = next; this.listeners.forEach((fn) => fn()) } }
    vi.stubGlobal('matchMedia', () => media)
    const { rerender } = render(<WorkbenchPage hostID="host"/>)
    await screen.findByRole('button', { name: /agent1/ })
    expect(screen.queryByRole('region', { name: '本地输入' })).not.toBeInTheDocument()
    await act(async () => { media.set(true); rerender(<WorkbenchPage hostID="host"/>) })
    expect(await screen.findByRole('region', { name: '本地输入' })).toBeInTheDocument()
    await act(async () => { media.set(false); rerender(<WorkbenchPage hostID="host"/>) })
    await screen.findByRole('button', { name: '本地输入框' })
    expect(screen.queryByRole('region', { name: '本地输入' })).not.toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: '本地输入框' }))
    expect(screen.getByRole('region', { name: '本地输入' })).toBeInTheDocument()
    await act(async () => { media.set(true); rerender(<WorkbenchPage hostID="host"/>) })
    expect(await screen.findByRole('region', { name: '本地输入' })).toBeInTheDocument()
    await act(async () => { media.set(false); rerender(<WorkbenchPage hostID="host"/>) })
    expect(await screen.findByRole('region', { name: '本地输入' })).toBeInTheDocument()
  })

  it('shows the iOS home-screen notice on the notification button', async () => {
    vi.spyOn(await import('../lib/pwa'), 'needsHomeScreenForNotifications').mockReturnValue(true)
    render(<WorkbenchPage hostID="host"/>)
    fireEvent.click(await screen.findByRole('button', { name: '工作台设置' }))
    expect(screen.getByRole('button', { name: '请先添加到主屏幕后再开启通知' })).toBeDisabled()
  })

  it('lets the user edit a pane draft while the host is still connecting', async () => {
    connection.state = 'connecting'
    render(<WorkbenchPage hostID="host"/>)
    fireEvent.click(await screen.findByRole('button', { name: '本地输入框' }))
    const box = screen.getByRole('textbox', { name: '本地输入内容' })
    expect(box).not.toBeDisabled()
    fireEvent.change(box, { target: { value: 'while connecting' } })
    expect(screen.getByRole('button', { name: '发送' })).toBeDisabled()
    expect(call).not.toHaveBeenCalledWith('pane.send_input', expect.anything())
  })

  it('maps connection and transport to Chinese and reconnects without reloading', async () => {
    render(<WorkbenchPage hostID="host"/>)
    expect(await screen.findByText('本机')).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: '重新连接' }))
    expect(retryNow).toHaveBeenCalled()
  })

  it('keeps the last frame visible on a degraded connection and offers an immediate retry', async () => {
    connection.state = 'degraded'
    connection.message = '链路不稳定'
    connection.retryAt = Date.now() + 5000
    render(<WorkbenchPage hostID="host"/>)
    await screen.findByRole('button', { name: /agent1/ })
    expect(screen.getByRole('status')).toHaveTextContent('连接受限')
    expect(screen.getByRole('status')).toHaveTextContent('链路不稳定')
    expect(screen.queryByRole('heading', { name: '主机暂时不可用' })).not.toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: '立即重连' }))
    expect(retryNow).toHaveBeenCalled()
    fireEvent.click(screen.getByRole('button', { name: '本地输入框' }))
    expect(screen.getByRole('textbox', { name: '本地输入内容' })).toHaveAttribute('placeholder', '主机未连接，暂不能发送')
  })

  it('uses an offline overlay with a primary retry action', async () => {
    connection.state = 'offline'
    connection.message = '连接已断开'
    render(<WorkbenchPage hostID="host"/>)
    expect(await screen.findByRole('heading', { name: '主机暂时不可用' })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: '立即重连' })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: '返回主机' })).toBeInTheDocument()
  })

  it('dismisses an action toast after five seconds', async () => {
    call.mockRejectedValueOnce(new Error('没有创建工作区的权限'))
    render(<WorkbenchPage hostID="host"/>)
    const create = await screen.findByRole('button', { name: '新建工作区' })
    vi.useFakeTimers()
    fireEvent.click(create)
    await act(async () => { await Promise.resolve() })
    expect(screen.getByRole('alert')).toHaveTextContent('没有创建工作区的权限')
    await act(async () => { vi.advanceTimersByTime(5000) })
    expect(screen.queryByRole('alert')).not.toBeInTheDocument()
  })
})

describe('workbench tab and mobile status', () => {
  it('opens a tab context menu without selecting that tab first', async () => {
    snapshot.tabs = [
      { tab_id: 'w1:t1', workspace_id: 'w1', label: '开发', number: 1, pane_count: 1, agent_status: 'idle', focused: true },
      { tab_id: 'w1:t2', workspace_id: 'w1', label: '日志', number: 2, pane_count: 1, agent_status: 'working', focused: false },
    ]
    render(<WorkbenchPage hostID="host"/>)
    await screen.findByRole('button', { name: /agent1/ })
    const logTab = screen.getByText('日志').closest('.tab')!
    const logSelect = logTab.querySelector('.tab-select')!
    expect(logSelect).toHaveAttribute('aria-pressed', 'false')
    expect(fireEvent.contextMenu(logTab)).toBe(false)
    expect(screen.getByRole('menu', { name: '标签页右键菜单' })).toBeInTheDocument()
    expect(logSelect).toHaveAttribute('aria-pressed', 'false')
    expect(screen.getByText('开发').closest('.tab')!.querySelector('.tab-select')).toHaveAttribute('aria-pressed', 'true')
  })

  it('shows mobile connection and pane chips when multiple panes exist', async () => {
    vi.stubGlobal('matchMedia', () => ({ matches: true, addEventListener() {}, removeEventListener() {} }))
    snapshot.panes = [
      { pane_id: 'w1:p1', workspace_id: 'w1', tab_id: 'w1:t1', terminal_id: 'term1', agent_status: 'working', focused: true, revision: 1, label: '前端' },
      { pane_id: 'w1:p2', workspace_id: 'w1', tab_id: 'w1:t1', terminal_id: 'term2', agent_status: 'blocked', focused: false, revision: 1, label: '审查' },
    ]
    snapshot.layouts = [{
      workspace_id: 'w1', tab_id: 'w1:t1', focused_pane_id: 'w1:p1', splits: [], zoomed: false,
      area: { x: 0, y: 0, width: 80, height: 40 },
      panes: [
        { pane_id: 'w1:p1', focused: true, rect: { x: 0, y: 0, width: 40, height: 40 } },
        { pane_id: 'w1:p2', focused: false, rect: { x: 40, y: 0, width: 40, height: 40 } },
      ],
    }]
    render(<WorkbenchPage hostID="host"/>)
    expect(await screen.findByRole('navigation', { name: '切换终端' })).toBeInTheDocument()
    expect(screen.getByLabelText('已连接')).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: /审查/ }))
    expect(screen.getByRole('button', { name: /审查/ })).toHaveAttribute('aria-pressed', 'true')
  })
})

describe('workbench prefix keymap and focus', () => {
  function press(key: string, init: KeyboardEventInit = {}) {
    fireEvent.keyDown(window, { key, ctrlKey: false, altKey: false, metaKey: false, ...init })
  }

  it('sends 0x02 on Ctrl+B Ctrl+B, ignores Shift cancel, and does not steal composer Ctrl+B', async () => {
    render(<WorkbenchPage hostID="host"/>)
    await screen.findByRole('button', { name: /agent1/ })
    press('b', { ctrlKey: true })
    expect(screen.getByText('前缀模式 PREFIX')).toBeInTheDocument()
    press('Shift', { shiftKey: true })
    expect(screen.getByText('前缀模式 PREFIX')).toBeInTheDocument()
    press('b', { ctrlKey: true })
    expect(screen.queryByText('前缀模式 PREFIX')).not.toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: '本地输入框' }))
    const box = screen.getByRole('textbox', { name: '本地输入内容' })
    box.focus()
    fireEvent.keyDown(box, { key: 'b', ctrlKey: true })
    expect(screen.queryByText('前缀模式 PREFIX')).not.toBeInTheDocument()
  })

  it('leaves prefix mode when focus moves into the composer', async () => {
    render(<WorkbenchPage hostID="host"/>)
    await screen.findByRole('button', { name: /agent1/ })
    press('b', { ctrlKey: true })
    expect(screen.getByText('前缀模式 PREFIX')).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: '本地输入框' }))
    const box = screen.getByRole('textbox', { name: '本地输入内容' })
    box.focus()
    fireEvent.keyDown(box, { key: 'x' })
    expect(screen.queryByText('前缀模式 PREFIX')).not.toBeInTheDocument()
  })

  it('switches to tab 2 with Ctrl+B 2', async () => {
    render(<WorkbenchPage hostID="host"/>)
    await screen.findByRole('button', { name: /agent1/ })
    expect(screen.getByRole('button', { pressed: true, name: '空闲 1' })).toBeInTheDocument()
    press('b', { ctrlKey: true })
    press('2')
    expect(screen.getByRole('button', { pressed: true, name: '空闲 2' })).toBeInTheDocument()
  })

  it('opens shortcut help from the sidebar and settings', async () => {
    render(<WorkbenchPage hostID="host"/>)
    await screen.findByRole('button', { name: /agent1/ })
    fireEvent.click(screen.getByRole('button', { name: /快捷键/ }))
    expect(screen.getByRole('dialog', { name: '快捷键' })).toBeInTheDocument()
    expect(screen.getByText('向终端发送 Ctrl+B')).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: '关闭快捷键' }))
    fireEvent.click(screen.getByRole('button', { name: '工作台设置' }))
    fireEvent.click(within(screen.getByRole('dialog', { name: '工作台设置' })).getByRole('button', { name: /快捷键/ }))
    expect(screen.getByRole('dialog', { name: '快捷键' })).toBeInTheDocument()
  })

  it('keeps the Agent row as the selected pane after a sidebar click', async () => {
    render(<WorkbenchPage hostID="host"/>)
    const agent2 = await screen.findByRole('button', { name: /agent2/ })
    fireEvent.click(agent2)
    expect(agent2).toHaveAttribute('aria-current', 'true')
  })
})

describe('compact short workbench', () => {
  it('hides the composer when auxiliary keys open in a short viewport', async () => {
    vi.stubGlobal('innerHeight', 390)
    vi.stubGlobal('visualViewport', undefined)
    vi.stubGlobal('matchMedia', () => ({ matches: true, addEventListener() {}, removeEventListener() {} }))
    render(<WorkbenchPage hostID="host"/>)
    expect(await screen.findByRole('region', { name: '本地输入' })).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: '终端辅助键' }))
    expect(screen.queryByRole('region', { name: '本地输入' })).not.toBeInTheDocument()
    expect(screen.getByRole('toolbar', { name: '终端辅助键' })).toBeInTheDocument()
  })
})

describe('workbench split resize', () => {
  function press(key: string, init: KeyboardEventInit = {}) {
    fireEvent.keyDown(window, { key, ctrlKey: false, altKey: false, metaKey: false, ...init })
  }

  beforeEach(() => {
    HTMLElement.prototype.setPointerCapture = vi.fn()
    HTMLElement.prototype.releasePointerCapture = vi.fn()
    snapshot.layouts = [{
      workspace_id: 'w1', tab_id: 'w1:t1', focused_pane_id: 'w1:p1', zoomed: false,
      area: { x: 0, y: 0, width: 100, height: 40 },
      panes: [
        { pane_id: 'w1:p1', focused: true, rect: { x: 0, y: 0, width: 50, height: 40 } },
        { pane_id: 'w1:p2', focused: false, rect: { x: 50, y: 0, width: 50, height: 40 } },
      ],
      splits: [{ id: 'split_0_root', direction: 'right', ratio: 0.5, rect: { x: 0, y: 0, width: 100, height: 40 } }],
    }]
  })

  it('enters RESIZE mode from prefix+r and resizes the current pane with hjkl', async () => {
    render(<WorkbenchPage hostID="host"/>)
    await screen.findByRole('button', { name: /agent1/ })
    press('b', { ctrlKey: true })
    press('r')
    expect(screen.getByText('调整分屏 RESIZE')).toBeInTheDocument()
    press('l')
    expect(call).toHaveBeenCalledWith('pane.resize', { pane_id: 'w1:p1', direction: 'right', amount: 0.05 })
    press('Escape')
    expect(screen.queryByText('调整分屏 RESIZE')).not.toBeInTheDocument()
  })

  it('commits a desktop split drag with layout.set_split_ratio', async () => {
    render(<WorkbenchPage hostID="host"/>)
    const handle = await screen.findByRole('separator', { name: '左右调整分屏' })
    const surface = document.querySelector('.terminal-surface') as HTMLElement
    vi.spyOn(surface, 'getBoundingClientRect').mockReturnValue({ x: 0, y: 0, left: 0, top: 0, right: 200, bottom: 80, width: 200, height: 80, toJSON() { return {} } })
    fireEvent.pointerDown(handle, { pointerType: 'mouse', pointerId: 1, clientX: 100, clientY: 20 })
    window.dispatchEvent(new MouseEvent('pointermove', { clientX: 140, clientY: 20, bubbles: true }))
    window.dispatchEvent(new MouseEvent('pointerup', { clientX: 140, clientY: 20, bubbles: true }))
    expect(call).toHaveBeenCalledWith('layout.set_split_ratio', { tab_id: 'w1:t1', path: [], ratio: 0.7 })
  })

  it('does not render split handles on a compact workbench', async () => {
    vi.stubGlobal('matchMedia', () => ({ matches: true, addEventListener() {}, removeEventListener() {} }))
    render(<WorkbenchPage hostID="host"/>)
    await screen.findByRole('button', { name: '切换工作区或终端' })
    expect(screen.queryByRole('separator', { name: '左右调整分屏' })).not.toBeInTheDocument()
  })
})
