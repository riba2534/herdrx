import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { Snapshot } from '../types'
import { WorkbenchPage } from './WorkbenchPage'
import { api } from '../lib/api'
import { clearComposerDrafts } from '../lib/composerDrafts'

const { call, connection } = vi.hoisted(() => ({ call: vi.fn().mockResolvedValue({}), connection: { state: 'ready' as string } }))
const snapshot: Snapshot = {
  version: 'test', protocol: 1,
  focused_workspace_id: 'w1', focused_tab_id: 'w1:t1', focused_pane_id: 'w1:p1',
  workspaces: ['alpha', 'beta'].map((label, i) => ({ workspace_id: `w${i + 1}`, label, number: i + 1, active_tab_id: `w${i + 1}:t1`, agent_status: 'idle', focused: i === 0, pane_count: 1, tab_count: 1 })),
  tabs: [1, 2].map((i) => ({ tab_id: `w${i}:t1`, workspace_id: `w${i}`, label: '1', number: 1, pane_count: 1, agent_status: 'idle', focused: i === 1 })),
  panes: [1, 2].map((i) => ({ pane_id: `w${i}:p1`, workspace_id: `w${i}`, tab_id: `w${i}:t1`, terminal_id: `term${i}`, agent_status: 'idle', focused: i === 1, revision: 1 })),
  layouts: [],
  agents: [1, 2].map((i) => ({ name: `agent${i}`, agent: 'codex', agent_status: 'idle', pane_id: `w${i}:p1`, workspace_id: `w${i}`, tab_id: `w${i}:t1`, focused: i === 1 })),
}

vi.mock('../lib/workbench', () => ({
  WorkbenchClient: class {
    call = call
    onSnapshot(handler: (value: Snapshot) => void) { handler(snapshot); return () => {} }
    onState(handler: (state: string) => void) { handler(connection.state); return () => {} }
    onEpoch() { return () => {} }
    connect() {}
    dispose() {}
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
  connection.state = 'ready'
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
    expect(screen.getByRole('menu', { name: 'sidebar 右键菜单' })).toBeInTheDocument()
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
    expect(screen.getByRole('menu', { name: 'workspace 右键菜单' })).toBeInTheDocument()
    expect(screen.getByRole('menuitem', { name: '重命名工作区' })).toBeInTheDocument()
    expect(screen.queryByRole('menuitem', { name: '新建工作区' })).not.toBeInTheDocument()
  })

  it('targets the right-clicked Agent pane, not the previously selected pane', async () => {
    render(<WorkbenchPage hostID="host"/>)
    const agent = await screen.findByRole('button', { name: /agent2/ })
    expect(fireEvent.contextMenu(agent.querySelector('strong')!)).toBe(false)
    expect(screen.getByRole('menu', { name: 'pane 右键菜单' })).toBeInTheDocument()
    expect(screen.queryByRole('menuitem', { name: '与当前 Pane 互换' })).not.toBeInTheDocument()
    fireEvent.click(screen.getByRole('menuitem', { name: '重命名 Pane' }))
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
    const switcher = screen.getByRole('dialog', { name: '切换 Herdr 位置' })
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
