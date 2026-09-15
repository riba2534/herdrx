import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { Snapshot } from '../types'
import { WorkbenchPage } from './WorkbenchPage'
import { api } from '../lib/api'
import { clearComposerDrafts } from '../lib/composerDrafts'
import { KEY_REPEAT_DELAY_MS, KEY_REPEAT_INTERVAL_MS } from '../lib/terminalKeys'
import { clearPaneViewModes, readPaneViewMode } from '../lib/paneViewMode'
import { resetWorkbenchSessionCache } from '../lib/workbenchSession'

const { call, connection, retryNow, snapshotListeners, paneInput } = vi.hoisted(() => ({
  call: vi.fn().mockResolvedValue({}),
  connection: { state: 'ready' as string, message: '', retryAt: 0 },
  retryNow: vi.fn(),
  snapshotListeners: [] as Array<(value: Snapshot) => void>,
  // 辅助键栏把字节交给终端面板注册进来的发送函数；这里替身记录收到的原始字节。
  paneInput: [] as string[],
}))
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
    onSnapshot(handler: (value: Snapshot) => void) {
      snapshotListeners.push(handler)
      handler(snapshot)
      return () => {
        const index = snapshotListeners.indexOf(handler)
        if (index >= 0) snapshotListeners.splice(index, 1)
      }
    }
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
    workbenchSession: vi.fn().mockResolvedValue({ session: null }),
    saveWorkbenchSession: vi.fn().mockResolvedValue({ session: { host_id: 'host' } }),
  },
  currentSessionID: () => 'workbench-session',
  onAuthEvent: () => () => {},
}))
// 用可观察的替身替代真实 xterm 面板：暴露收到的 viewMode / connected，
// 并提供和真实工具栏一致的“终端 / 对话”切换入口。
vi.mock('../components/TerminalPane', () => ({
  TerminalPane: (props: { pane: { pane_id: string }; viewMode: string; connected: boolean; active: boolean; onViewModeChange: (mode: string) => void; onFocus: () => void; onControlReady?: (send: ((data: string) => void) | null) => void }) => <div
    data-testid={`terminal-pane-${props.pane.pane_id}`}
    data-view-mode={props.viewMode}
    data-connected={props.connected ? 'true' : 'false'}
    data-active={props.active ? 'true' : 'false'}
  >
    <button aria-label={`聚焦 ${props.pane.pane_id}`} onClick={() => props.onFocus()}>聚焦</button>
    <button aria-label={`${props.pane.pane_id} 接通终端输入`} onClick={() => props.onControlReady?.((data) => { paneInput.push(data) })}>接通</button>
    <button aria-label={`${props.pane.pane_id} 切到对话视图`} aria-pressed={props.viewMode === 'chat'} onClick={() => props.onViewModeChange('chat')}>对话</button>
    <button aria-label={`${props.pane.pane_id} 切回终端视图`} aria-pressed={props.viewMode === 'terminal'} onClick={() => props.onViewModeChange('terminal')}>终端</button>
  </div>,
}))

beforeEach(() => {
  localStorage.clear()
  sessionStorage.clear()
  clearComposerDrafts()
  clearPaneViewModes()
  resetWorkbenchSessionCache()
  snapshotListeners.length = 0
  retryNow.mockClear()
  connection.state = 'ready'
  connection.message = ''
  connection.retryAt = 0
  snapshot.focused_workspace_id = 'w1'
  snapshot.focused_tab_id = 'w1:t1'
  snapshot.focused_pane_id = 'w1:p1'
  snapshot.workspaces = ['alpha', 'beta'].map((label, i) => ({ workspace_id: `w${i + 1}`, label, number: i + 1, active_tab_id: `w${i + 1}:t1`, agent_status: 'idle' as const, focused: i === 0, pane_count: 1, tab_count: 1 }))
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
  snapshot.agents = [1, 2].map((i) => ({ name: `agent${i}`, agent: 'codex', agent_status: 'idle', pane_id: `w${i}:p1`, workspace_id: `w${i}`, tab_id: `w${i}:t1`, focused: i === 1 }))
  snapshot.layouts = []
  vi.mocked(api.workbenchSession).mockReset()
  vi.mocked(api.saveWorkbenchSession).mockReset()
  vi.mocked(api.workbenchSession).mockResolvedValue({ session: null })
  vi.mocked(api.saveWorkbenchSession).mockResolvedValue({ session: { host_id: 'host' } })
  call.mockReset()
  call.mockResolvedValue({})
  vi.stubGlobal('matchMedia', () => ({ matches: false, addEventListener() {}, removeEventListener() {} }))
})
afterEach(() => {
  vi.useRealTimers()
  vi.unstubAllGlobals()
})

async function waitForRestoredWorkbench() {
  await waitFor(() => {
    expect(document.querySelector('.workspace-row[aria-current="true"]')).toBeTruthy()
  })
}

function emitSnapshot(value: Snapshot = snapshot) {
  for (const handler of snapshotListeners) handler(value)
}

const savedW2 = { host_id: 'host', workspace_id: 'w2', tab_id: 'w2:t1', pane_id: 'w2:p1', device_id: 'phone-1', client_class: 'mobile' as const }

describe('workbench session restore', () => {
  it('lands on the last workspace after a PC ↔ phone handoff', async () => {
    vi.mocked(api.workbenchSession).mockResolvedValue({ session: savedW2 })
    render(<WorkbenchPage hostID="host"/>)
    await waitFor(() => expect(screen.getByRole('button', { name: /agent2/ })).toHaveAttribute('aria-current', 'true'))
    expect(screen.getByRole('button', { name: /agent1/ })).not.toHaveAttribute('aria-current')
    expect(document.querySelector('.workspace-row.sidebar-row-active')?.textContent).toContain('beta')
    await waitFor(() => expect(api.saveWorkbenchSession).toHaveBeenCalledWith(expect.objectContaining({
      host_id: 'host', workspace_id: 'w2', tab_id: 'w2:t1', pane_id: 'w2:p1', client_class: 'desktop',
    })))
    expect(api.saveWorkbenchSession).not.toHaveBeenCalledWith(expect.objectContaining({ workspace_id: 'w1' }))
  })

  it('uses the large desktop shell after a mobile-written session, not leftover compact chrome', async () => {
    localStorage.setItem('herdrx.sidebar-open', 'false')
    vi.mocked(api.workbenchSession).mockResolvedValue({ session: savedW2 })
    render(<WorkbenchPage hostID="host"/>)
    await waitFor(() => expect(document.querySelector('.workspace-row.sidebar-row-active')?.textContent).toContain('beta'))
    const bench = document.querySelector('.workbench')
    expect(bench).toHaveAttribute('data-window-form', 'large')
    expect(bench).not.toHaveClass('workbench-compact')
    expect(bench).not.toHaveClass('workbench-sidebar-closed')
    expect(screen.getByRole('button', { name: '收起侧边栏' })).toBeInTheDocument()
    expect(document.querySelector('.hostbar')).toBeTruthy()
    expect(document.querySelector('.mobile-topbar')).toBeNull()
  })

  it('uses compact chrome when the attaching client is a phone, even if the snapshot was written on desktop', async () => {
    vi.stubGlobal('matchMedia', () => ({ matches: true, addEventListener() {}, removeEventListener() {} }))
    vi.mocked(api.workbenchSession).mockResolvedValue({
      session: { host_id: 'host', workspace_id: 'w2', tab_id: 'w2:t1', pane_id: 'w2:p1', device_id: 'pc-1', client_class: 'desktop' as const },
    })
    render(<WorkbenchPage hostID="host"/>)
    await waitFor(() => expect(screen.getByRole('button', { name: '切换工作区或终端' })).toHaveTextContent('beta'))
    const bench = document.querySelector('.workbench')
    expect(bench).toHaveAttribute('data-window-form', 'compact')
    expect(bench).toHaveClass('workbench-compact')
    expect(screen.queryByRole('button', { name: '收起侧边栏' })).not.toBeInTheDocument()
  })

  it('applies the saved workspace after a later snapshot and never persists Herdr focused_* first', async () => {
    snapshot.workspaces = []
    snapshot.tabs = []
    snapshot.panes = []
    snapshot.agents = []
    vi.mocked(api.workbenchSession).mockResolvedValue({ session: savedW2 })
    render(<WorkbenchPage hostID="host"/>)
    await waitFor(() => expect(api.workbenchSession).toHaveBeenCalled())
    expect(document.querySelector('.workspace-row[aria-current="true"]')).toBeNull()
    expect(api.saveWorkbenchSession).not.toHaveBeenCalled()

    emitSnapshot({
      ...snapshot,
      focused_workspace_id: 'w1',
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
      agents: [1, 2].map((i) => ({ name: `agent${i}`, agent: 'codex', agent_status: 'idle', pane_id: `w${i}:p1`, workspace_id: `w${i}`, tab_id: `w${i}:t1`, focused: i === 1 })),
    })

    await waitFor(() => expect(document.querySelector('.workspace-row.sidebar-row-active')?.textContent).toContain('beta'))
    expect(screen.getByRole('button', { name: /agent2/ })).toHaveAttribute('aria-current', 'true')
    await waitFor(() => expect(api.saveWorkbenchSession).toHaveBeenCalledWith(expect.objectContaining({
      workspace_id: 'w2', tab_id: 'w2:t1', pane_id: 'w2:p1',
    })))
    expect(api.saveWorkbenchSession).not.toHaveBeenCalledWith(expect.objectContaining({ workspace_id: 'w1' }))
  })

  it('does not adopt Herdr focused_* while the session fetch is in flight', async () => {
    let finish!: (value: { session: typeof savedW2 }) => void
    vi.mocked(api.workbenchSession).mockReturnValue(new Promise((resolve) => { finish = resolve }))
    render(<WorkbenchPage hostID="host"/>)
    await screen.findByRole('button', { name: /agent1/ })
    expect(document.querySelector('.workspace-row[aria-current="true"]')).toBeNull()
    expect(api.saveWorkbenchSession).not.toHaveBeenCalled()
    finish({ session: savedW2 })
    await waitFor(() => expect(document.querySelector('.workspace-row.sidebar-row-active')?.textContent).toContain('beta'))
    expect(vi.mocked(api.saveWorkbenchSession).mock.calls.every(([payload]) => payload.workspace_id !== 'w1')).toBe(true)
  })
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
    // 分两腿：先整段正文（无按键），再一次单独回车，都发给点击时的 pane。
    await waitFor(() => expect(call).toHaveBeenCalledWith('pane.send_input', { pane_id: 'w1:p1', text: '整段提交', keys: [] }))
    await waitFor(() => expect(call).toHaveBeenCalledWith('pane.send_input', { pane_id: 'w1:p1', text: '', keys: ['Enter'] }))
    expect(call.mock.calls.filter((item) => item[0] === 'pane.send_input')).toHaveLength(2)
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
    await waitForRestoredWorkbench()
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
    await waitForRestoredWorkbench()
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
    await waitForRestoredWorkbench()
    const agent2 = await screen.findByRole('button', { name: /agent2/ })
    fireEvent.click(agent2)
    expect(agent2).toHaveAttribute('aria-current', 'true')
  })
})

describe('workbench workspace groups', () => {
  const exampleWorktree = {
    repo_key: '/workspace/example/.git',
    repo_name: 'example',
    repo_root: '/workspace/example',
    checkout_path: '/workspace/example',
    is_linked_worktree: false,
  }

  beforeEach(() => {
    snapshot.workspaces = [
      { workspace_id: 'w1', label: '~', number: 1, active_tab_id: 'w1:t1', agent_status: 'idle', focused: true, pane_count: 1, tab_count: 1 },
      { workspace_id: 'w2', label: 'astergate', number: 2, active_tab_id: 'w2:t1', agent_status: 'working', focused: false, pane_count: 1, tab_count: 1, worktree: exampleWorktree },
      { workspace_id: 'w3', label: 'codex-reset-cards', number: 3, active_tab_id: 'w3:t1', agent_status: 'idle', focused: false, pane_count: 1, tab_count: 1, worktree: { ...exampleWorktree, checkout_path: '/workspace/example/.herdr/worktrees/example/feat-codex-reset', is_linked_worktree: true } },
      { workspace_id: 'w4', label: 'herdrx', number: 4, active_tab_id: 'w4:t1', agent_status: 'idle', focused: false, pane_count: 1, tab_count: 1, worktree: { repo_key: '/workspace/herdrx/.git', repo_name: 'herdrx', repo_root: '/workspace/herdrx', checkout_path: '/workspace/herdrx', is_linked_worktree: false } },
    ]
    snapshot.tabs = snapshot.workspaces.map((workspace) => ({ tab_id: workspace.active_tab_id, workspace_id: workspace.workspace_id, label: '1', number: 1, pane_count: 1, agent_status: 'idle' as const, focused: workspace.workspace_id === 'w1' }))
    snapshot.panes = snapshot.workspaces.map((workspace) => ({ pane_id: `${workspace.workspace_id}:p1`, workspace_id: workspace.workspace_id, tab_id: workspace.active_tab_id, terminal_id: `term-${workspace.workspace_id}`, agent_status: 'idle' as const, focused: workspace.workspace_id === 'w1', revision: 1 }))
    snapshot.agents = [{ name: 'agent1', agent: 'codex', agent_status: 'idle', pane_id: 'w1:p1', workspace_id: 'w1', tab_id: 'w1:t1', focused: true }]
    call.mockImplementation(async (method: string) => {
      if (method === 'worktree.list') {
        return {
          type: 'worktree_list',
          worktrees: [
            { path: '/workspace/example', open_workspace_id: 'w2', branch: 'feat/overview-tps-m' },
            { path: '/workspace/example/.herdr/worktrees/example/feat-codex-reset', open_workspace_id: 'w3', branch: 'feat/codex-reset-credits' },
            { path: '/workspace/herdrx', open_workspace_id: 'w4', branch: 'main' },
          ],
        }
      }
      return {}
    })
  })

  it('indents linked worktrees and shows branch text', async () => {
    const { container } = render(<WorkbenchPage hostID="host"/>)
    await waitFor(() => expect(container.querySelector('.workspace-branch')?.textContent).toBe('feat/overview-tps-m'))
    expect([...container.querySelectorAll('.workspace-row strong')].map((item) => item.textContent)).toEqual(['~', 'astergate', 'codex-reset-cards', 'herdrx'])
    expect(container.querySelector('.workspace-row-child strong')).toHaveTextContent('codex-reset-cards')
    expect(container.querySelector('.workspace-item-child')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: '收起 astergate 的 Worktree 组' })).toHaveAttribute('aria-expanded', 'true')
  })

  it('collapses a worktree group from the in-row toggle and persists the key', async () => {
    render(<WorkbenchPage hostID="host"/>)
    await waitFor(() => expect(screen.getByText('codex-reset-cards')).toBeInTheDocument())
    fireEvent.click(screen.getByRole('button', { name: '收起 astergate 的 Worktree 组' }))
    expect(screen.queryByText('codex-reset-cards')).not.toBeInTheDocument()
    expect(screen.getByRole('button', { name: '展开 astergate 的 Worktree 组' })).toHaveAttribute('aria-expanded', 'false')
    expect(JSON.parse(localStorage.getItem('herdrx.collapsed-spaces.host') || '[]')).toEqual(['/workspace/example/.git'])
  })

  it('does not collapse the group that holds the active workspace', async () => {
    snapshot.focused_workspace_id = 'w3'
    snapshot.focused_tab_id = 'w3:t1'
    snapshot.focused_pane_id = 'w3:p1'
    render(<WorkbenchPage hostID="host"/>)
    const toggle = await screen.findByRole('button', { name: '收起 astergate 的 Worktree 组' })
    expect(toggle).toHaveAttribute('aria-disabled', 'true')
    fireEvent.click(toggle)
    expect(screen.getByText('codex-reset-cards')).toBeInTheDocument()
    expect(screen.getByText('codex-reset-cards').closest('button')).toHaveAttribute('aria-current', 'true')
    // The context menu may not offer the collapse either.
    fireEvent.contextMenu(screen.getByText('astergate').closest('button')!)
    expect(await screen.findByRole('menuitem', { name: '重命名工作区' })).toBeInTheDocument()
    expect(screen.queryByRole('menuitem', { name: /Worktree 组/ })).not.toBeInTheDocument()
  })

  it('opens the workspace menu from the empty group gutter', async () => {
    render(<WorkbenchPage hostID="host"/>)
    await waitFor(() => expect(screen.getByText('codex-reset-cards')).toBeInTheDocument())
    const spacer = document.querySelector('.workspace-group-spacer')
    expect(spacer).not.toBeNull()
    fireEvent.contextMenu(spacer as Element)
    expect(await screen.findByRole('menuitem', { name: /重命名工作区/ })).toBeInTheDocument()
  })

  it('retries a failed background worktree.list from the workspace menu', async () => {
    call.mockImplementation(async (method: string) => {
      if (method === 'worktree.list') throw new Error('offline')
      return {}
    })
    render(<WorkbenchPage hostID="host"/>)
    await waitFor(() => expect(call).toHaveBeenCalledWith('worktree.list', { workspace_id: 'w2' }))
    call.mockClear()
    fireEvent.contextMenu(screen.getByText('astergate').closest('button')!)
    await waitFor(() => expect(call).toHaveBeenCalledWith('worktree.list', { workspace_id: 'w2' }))
  })

  it('selects an indented child workspace', async () => {
    render(<WorkbenchPage hostID="host"/>)
    const child = await screen.findByRole('button', { name: /codex-reset-cards/ })
    fireEvent.click(child)
    expect(child).toHaveAttribute('aria-current', 'true')
  })

  it('shows branch text in the mobile switcher', async () => {
    vi.stubGlobal('matchMedia', () => ({ matches: true, addEventListener() {}, removeEventListener() {} }))
    render(<WorkbenchPage hostID="host"/>)
    fireEvent.click(await screen.findByRole('button', { name: '切换工作区或终端' }))
    const switcher = screen.getByRole('dialog', { name: '切换工作区或终端' })
    await waitFor(() => expect(within(switcher).getByText(/feat\/overview-tps-m/)).toBeInTheDocument())
    expect(within(switcher).getByText(/codex-reset-cards/)).toBeInTheDocument()
    expect(within(switcher).getByText(/^main · /)).toBeInTheDocument()
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

describe('mobile auxiliary keybar paste', () => {
  beforeEach(() => {
    // 辅助键栏只在紧凑布局渲染；用非 short 的高度，保证切 pane 后输入框仍在，
    // 便于断言粘贴目标没有跟着选择改变。
    vi.stubGlobal('innerHeight', 800)
    vi.stubGlobal('visualViewport', undefined)
    vi.stubGlobal('matchMedia', () => ({ matches: true, addEventListener() {}, removeEventListener() {} }))
    // 辅助键要交给终端面板注册的发送函数，所以这里必须真的渲染 pane 与布局。
    snapshot.layouts = [{
      workspace_id: 'w1', tab_id: 'w1:t1', focused_pane_id: 'w1:p1', zoomed: false,
      area: { x: 0, y: 0, width: 100, height: 40 },
      panes: [{ pane_id: 'w1:p1', focused: true, rect: { x: 0, y: 0, width: 100, height: 40 } }],
      splits: [],
    }]
  })

  // 只替换 navigator.clipboard 与安全上下文判定，避免整体替换 navigator 影响渲染。
  function installClipboard(readText: () => Promise<string>) {
    Object.defineProperty(window, 'isSecureContext', { configurable: true, value: true })
    Object.defineProperty(navigator, 'clipboard', { configurable: true, value: { readText } })
  }

  function readText(value: string | Error) {
    const stub = vi.fn(() => (value instanceof Error ? Promise.reject(value) : Promise.resolve(value)))
    installClipboard(stub)
    return stub
  }

  // 紧凑布局下侧边栏不渲染，等底部输入框出现再操作辅助键栏。
  async function openKeybar() {
    render(<WorkbenchPage hostID="host"/>)
    await screen.findByRole('region', { name: '本地输入' })
    fireEvent.click(screen.getByRole('button', { name: '终端辅助键' }))
    await screen.findByRole('toolbar', { name: '终端辅助键' })
  }

  it('pastes the clipboard into the selected pane once with no Enter key', async () => {
    const read = readText('第一行\n第二行 🙂')
    await openKeybar()
    call.mockClear()
    fireEvent.click(screen.getByRole('button', { name: '粘贴到终端，不自动回车' }))
    await waitFor(() => expect(call).toHaveBeenCalledTimes(1))
    expect(read).toHaveBeenCalledTimes(1)
    // 只送正文：keys 为空，回车由用户自己按，远端才是唯一一次提交。
    expect(call).toHaveBeenCalledWith('pane.send_input', { pane_id: 'w1:p1', text: '第一行\n第二行 🙂', keys: [] })
  })

  it('strips escape bytes so pasted text cannot break out of the remote paste frame', async () => {
    readText('safe\u001b[201~\rrm -rf /')
    await openKeybar()
    call.mockClear()
    fireEvent.click(screen.getByRole('button', { name: '粘贴到终端，不自动回车' }))
    await waitFor(() => expect(call).toHaveBeenCalledTimes(1))
    const params = call.mock.calls[0][1] as { pane_id: string; text: string; keys: string[] }
    expect(params.text).not.toContain('\u001b')
    expect(params.text).toBe('safe\u241b[201~\rrm -rf /')
    expect(params.keys).toEqual([])
  })

  it('reports a refused clipboard read instead of pretending the paste succeeded', async () => {
    readText(new Error('NotAllowedError'))
    await openKeybar()
    call.mockClear()
    fireEvent.click(screen.getByRole('button', { name: '粘贴到终端，不自动回车' }))
    expect(await screen.findByRole('alert')).toHaveTextContent('粘贴失败')
    expect(call).not.toHaveBeenCalled()
  })

  it('refuses an oversized paste instead of silently sending half a command', async () => {
    readText('a'.repeat(256 * 1024 + 1))
    await openKeybar()
    call.mockClear()
    fireEvent.click(screen.getByRole('button', { name: '粘贴到终端，不自动回车' }))
    expect(await screen.findByRole('alert')).toHaveTextContent('256 KiB')
    expect(call).not.toHaveBeenCalled()
  })

  it('offers the orca key set in the orca order so muscle memory carries over', async () => {
    readText('unused')
    await openKeybar()
    const labels = within(screen.getByRole('toolbar', { name: '终端辅助键' })).getAllByRole('button').map((button) => button.textContent || '')
    // 常用编辑键在前、控制键在后；herdrx 额外的符号键留在最后。
    expect(labels.slice(0, 5)).toEqual(['Esc', 'Tab', 'Enter', 'Shift+Tab', 'Space'])
    expect(labels.indexOf('⌫')).toBeLessThan(labels.indexOf('↑'))
    expect(labels.indexOf('↑')).toBeLessThan(labels.indexOf('Ctrl+C'))
    expect(labels.indexOf('Ctrl+C')).toBeLessThan(labels.indexOf('-'))
  })

  it('sends each key byte on tap and keeps repeats off the non-repeatable keys', async () => {
    readText('unused')
    await openKeybar()
    paneInput.length = 0
    fireEvent.click(screen.getByRole('button', { name: 'w1:p1 接通终端输入' }))
    const toolbar = screen.getByRole('toolbar', { name: '终端辅助键' })
    // Shift+Tab 在一次性按键里：点一下发一次 ESC [ Z。
    fireEvent.click(within(toolbar).getByRole('button', { name: 'Shift 加 Tab 反向切换' }))
    fireEvent.click(within(toolbar).getByRole('button', { name: '中断 Ctrl+C' }))
    expect(paneInput).toEqual(['\x1b[Z', '\x03'])
  })

  it('sends a repeatable key on keyboard activation too, since it has no click handler', async () => {
    readText('unused')
    await openKeybar()
    paneInput.length = 0
    fireEvent.click(screen.getByRole('button', { name: 'w1:p1 接通终端输入' }))
    const backspace = within(screen.getByRole('toolbar', { name: '终端辅助键' })).getByRole('button', { name: '退格键' })
    // 长按键走 onPointerDown，没有 onClick；键盘和读屏必须另有一条通路。
    fireEvent.keyDown(backspace, { key: 'Enter' })
    fireEvent.keyDown(backspace, { key: ' ' })
    fireEvent.keyDown(backspace, { key: 'a' })
    expect(paneInput).toEqual(['\x7f', '\x7f'])
  })

  it('repeats a held backspace but stops on release, and does not repeat Enter', async () => {
    readText('unused')
    await openKeybar()
    paneInput.length = 0
    fireEvent.click(screen.getByRole('button', { name: 'w1:p1 接通终端输入' }))
    const toolbar = screen.getByRole('toolbar', { name: '终端辅助键' })
    vi.useFakeTimers()
    try {
      // 长按退格：按下先发一次，停住一段时间后开始连发。
      fireEvent.pointerDown(within(toolbar).getByRole('button', { name: '退格键' }))
      expect(paneInput).toEqual(['\x7f'])
      await act(async () => { await vi.advanceTimersByTimeAsync(KEY_REPEAT_DELAY_MS + KEY_REPEAT_INTERVAL_MS * 2) })
      expect(paneInput.length).toBeGreaterThan(1)
      const held = paneInput.length
      fireEvent.pointerUp(within(toolbar).getByRole('button', { name: '退格键' }))
      await act(async () => { await vi.advanceTimersByTimeAsync(KEY_REPEAT_INTERVAL_MS * 4) })
      expect(paneInput.length).toBe(held)
      // 回车不是可重复键：按住也只发一次。
      const enter = within(toolbar).getByRole('button', { name: '回车键' })
      fireEvent.pointerDown(enter)
      fireEvent.click(enter)
      await act(async () => { await vi.advanceTimersByTimeAsync(KEY_REPEAT_DELAY_MS + KEY_REPEAT_INTERVAL_MS * 2) })
      // 按一下只发一次：pointerdown 对不可重复键没有副作用，click 才是那一次。
      expect(paneInput.slice(held)).toEqual(['\r'])
    } finally {
      vi.useRealTimers()
    }
  })

  it('sends nothing from the keybar until the terminal registers its input channel', async () => {
    readText('unused')
    await openKeybar()
    paneInput.length = 0
    const toolbar = screen.getByRole('toolbar', { name: '终端辅助键' })
    expect(within(toolbar).getByRole('button', { name: '回车键' })).toBeDisabled()
    fireEvent.click(within(toolbar).getByRole('button', { name: '回车键' }))
    expect(paneInput).toEqual([])
  })

  it('keeps the pane captured at click time when the selection changes during the read', async () => {
    let release: (value: string) => void = () => {}
    const stub = vi.fn(() => new Promise<string>((resolve) => { release = resolve }))
    installClipboard(stub)
    await openKeybar()
    call.mockClear()
    fireEvent.click(screen.getByRole('button', { name: '粘贴到终端，不自动回车' }))
    // 剪贴板还没读完就切到另一个 pane；投递目标必须仍是点击那一刻的 w1:p1。
    fireEvent.click(screen.getByRole('button', { name: '切换工作区或终端' }))
    const switcher = screen.getByRole('dialog', { name: '切换工作区或终端' })
    fireEvent.click(within(switcher).getByRole('button', { name: /2 1 个终端/ }))
    // 切完后 Composer 的草稿键应换成新的 pane，证明选择确实变了。
    const box = screen.getByRole('textbox', { name: '本地输入内容' })
    fireEvent.change(box, { target: { value: 'probe' } })
    await waitFor(() => expect(Object.keys(localStorage).some((key) => key.endsWith('w1%3Ap2'))).toBe(true))
    await act(async () => release('切换后读出的内容'))
    await waitFor(() => expect(call).toHaveBeenCalledTimes(1))
    expect(call).toHaveBeenCalledWith('pane.send_input', { pane_id: 'w1:p1', text: '切换后读出的内容', keys: [] })
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

describe('workbench pane view mode', () => {
  beforeEach(() => {
    snapshot.panes = [
      { pane_id: 'w1:p1', workspace_id: 'w1', tab_id: 'w1:t1', terminal_id: 'term1', agent_status: 'idle', focused: true, revision: 1 },
      { pane_id: 'w1:p2', workspace_id: 'w1', tab_id: 'w1:t1', terminal_id: 'term2', agent_status: 'idle', focused: false, revision: 1 },
    ]
    snapshot.layouts = [{
      workspace_id: 'w1', tab_id: 'w1:t1', focused_pane_id: 'w1:p1', splits: [], zoomed: false,
      area: { x: 0, y: 0, width: 80, height: 40 },
      panes: [
        { pane_id: 'w1:p1', focused: true, rect: { x: 0, y: 0, width: 40, height: 40 } },
        { pane_id: 'w1:p2', focused: false, rect: { x: 40, y: 0, width: 40, height: 40 } },
      ],
    }]
  })

  it('keeps the chat view per pane and hides the workbench input box for the pane that is in chat', async () => {
    render(<WorkbenchPage hostID="host"/>)
    await screen.findByRole('button', { name: '本地输入框' })
    fireEvent.click(screen.getByRole('button', { name: '本地输入框' }))
    expect(screen.getByTestId('terminal-pane-w1:p1')).toHaveAttribute('data-view-mode', 'terminal')
    expect(screen.getByTestId('terminal-pane-w1:p2')).toHaveAttribute('data-view-mode', 'terminal')
    expect(screen.getByRole('region', { name: '本地输入' })).toBeInTheDocument()
    expect(screen.getByTestId('terminal-pane-w1:p1')).toHaveAttribute('data-connected', 'true')

    fireEvent.click(screen.getByRole('button', { name: 'w1:p1 切到对话视图' }))
    await waitFor(() => expect(screen.getByTestId('terminal-pane-w1:p1')).toHaveAttribute('data-view-mode', 'chat'))
    // 对话视图自带底部输入框，工作台不再渲染第二个输入框。
    await waitFor(() => expect(screen.queryByRole('region', { name: '本地输入' })).not.toBeInTheDocument())
    // 另一个 pane 和另一个主机都不受这次切换影响。
    expect(screen.getByTestId('terminal-pane-w1:p2')).toHaveAttribute('data-view-mode', 'terminal')
    expect(readPaneViewMode('host', 'w1:p1')).toBe('chat')
    expect(readPaneViewMode('host', 'w1:p2')).toBe('terminal')
    expect(readPaneViewMode('host-other', 'w1:p1')).toBe('terminal')

    // 切到另一个 pane：它仍是终端视图，输入框回来。
    fireEvent.click(screen.getByRole('button', { name: '聚焦 w1:p2' }))
    await waitFor(() => expect(screen.getByRole('region', { name: '本地输入' })).toBeInTheDocument())
    expect(screen.getByTestId('terminal-pane-w1:p2')).toHaveAttribute('data-view-mode', 'terminal')
    expect(screen.getByTestId('terminal-pane-w1:p2')).toHaveAttribute('data-active', 'true')

    // 回到原 pane：对话视图按 pane 记忆，不需要重新选择。
    fireEvent.click(screen.getByRole('button', { name: '聚焦 w1:p1' }))
    await waitFor(() => expect(screen.queryByRole('region', { name: '本地输入' })).not.toBeInTheDocument())
    expect(screen.getByTestId('terminal-pane-w1:p1')).toHaveAttribute('data-view-mode', 'chat')

    // 切回终端视图：模式写回终端，输入框恢复。
    fireEvent.click(screen.getByRole('button', { name: 'w1:p1 切回终端视图' }))
    await waitFor(() => expect(screen.getByTestId('terminal-pane-w1:p1')).toHaveAttribute('data-view-mode', 'terminal'))
    expect(screen.getByRole('region', { name: '本地输入' })).toBeInTheDocument()
    expect(readPaneViewMode('host', 'w1:p1')).toBe('terminal')
  })

  it('offers the same view switch from the switcher actions and follows the connection state', async () => {
    connection.state = 'reconnecting'
    render(<WorkbenchPage hostID="host"/>)
    await waitForRestoredWorkbench()
    await screen.findByRole('button', { name: '切换工作区或终端' })
    // 断线时 pane 收到 connected=false，对话视图据此暂停刷新并禁用发送。
    expect(screen.getByTestId('terminal-pane-w1:p1')).toHaveAttribute('data-connected', 'false')
    expect(screen.getByTestId('terminal-pane-w1:p1')).toHaveAttribute('data-view-mode', 'terminal')

    fireEvent.click(screen.getByRole('button', { name: '切换工作区或终端' }))
    fireEvent.click(within(screen.getByRole('dialog', { name: '切换工作区或终端' })).getByRole('button', { name: /切换到对话视图/ }))
    await waitFor(() => expect(readPaneViewMode('host', 'w1:p1')).toBe('chat'))
    expect(screen.queryByRole('region', { name: '本地输入' })).not.toBeInTheDocument()

    fireEvent.click(screen.getByRole('button', { name: '切换工作区或终端' }))
    expect(within(screen.getByRole('dialog', { name: '切换工作区或终端' })).getByRole('button', { name: /切换到终端视图/ })).toHaveAttribute('aria-pressed', 'true')
  })
})
