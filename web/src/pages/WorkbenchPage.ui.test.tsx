import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { Pane, Snapshot } from '../types'
import { WorkbenchPage } from './WorkbenchPage'
import { api } from '../lib/api'
import { clearComposerDrafts } from '../lib/composerDrafts'
import { clearPaneViewModes, readPaneViewMode, writePaneViewMode } from '../lib/paneViewMode'
import { resetWorkbenchModeForTest, WORKBENCH_MODE_STORAGE_KEY } from '../lib/workbenchMode'
import { resetWorkbenchSessionCache } from '../lib/workbenchSession'
import { resetFirstMessagesForTest } from '../components/workbench-ui/firstMessage'

const { call, snapshotListeners } = vi.hoisted(() => ({ call: vi.fn().mockResolvedValue({}), snapshotListeners: [] as Array<(value: Snapshot) => void> }))
let snapshot: Snapshot

vi.mock('../lib/workbench', () => ({
  createClientID: () => crypto.randomUUID(),
  WorkbenchClient: class {
    call = call
    onSnapshot(handler: (value: Snapshot) => void) {
      snapshotListeners.push(handler)
      handler(snapshot)
      return () => { snapshotListeners.splice(snapshotListeners.indexOf(handler), 1) }
    }
    onState(handler: (state: string) => void) { handler('ready'); return () => {} }
    onEpoch() { return () => {} }
    connect() {}
    retryNow() {}
    dispose() {}
    hasOpenTerminals() { return false }
  },
}))
vi.mock('../lib/api', () => ({
  api: {
    host: vi.fn().mockResolvedValue({ host: { id: 'host', name: 'Local', transport: 'local' } }),
    hosts: vi.fn().mockResolvedValue({ hosts: [{ id: 'host', name: 'Local', transport: 'local' }] }),
    workbenchSession: vi.fn().mockResolvedValue({ session: null }),
    saveWorkbenchSession: vi.fn().mockResolvedValue({ session: { host_id: 'host' } }),
    pushSubscriptionStatus: vi.fn().mockResolvedValue({ subscribed: false }),
    panePrompt: vi.fn(),
    paneDialog: vi.fn(),
    launchers: vi.fn(),
    startSession: vi.fn(),
  },
  APIError: class APIError extends Error {
    constructor(public status: number, public code: string, message: string, public details?: unknown) { super(message) }
  },
  currentSessionID: () => 'ui-session',
  onAuthEvent: () => () => {},
}))
vi.mock('../components/TerminalPane', () => ({
  TerminalPane: (props: { pane: Pane; active: boolean; viewMode: string }) => <div data-testid={`terminal-pane-${props.pane.pane_id}`} data-active={String(props.active)} data-view-mode={props.viewMode}/>,
}))
vi.mock('../components/ChatView', () => ({
  ChatView: (props: { pane: Pane; variant?: string; onSwitchToTerminal: () => void }) => <div role="region" aria-label="对话视图" data-variant={props.variant}>
    {props.pane.pane_id}<textarea aria-label="对话输入内容" className="composer-input"/><button onClick={props.onSwitchToTerminal}>对话里的切到终端</button>
  </div>,
}))
vi.mock('../components/ScreenView', () => ({
  ScreenView: (props: { pane: Pane }) => <div role="region" aria-label="画面视图">{props.pane.pane_id}</div>,
}))

const pane = (id: string, tab: string, patch: Partial<Pane> = {}): Pane => ({ pane_id: id, workspace_id: 'w1', tab_id: tab, terminal_id: id, focused: false, revision: 1, agent_status: 'idle', ...patch })
const layout = (tab: string, ids: string[]) => ({ workspace_id: 'w1', tab_id: tab, focused_pane_id: ids[0], splits: [], zoomed: false, area: { x: 0, y: 0, width: 80, height: 40 }, panes: ids.map((pane_id, index) => ({ pane_id, focused: index === 0, rect: { x: index * 40, y: 0, width: 40, height: 40 } })) })

function publish(next: Snapshot) {
  snapshot = next
  act(() => snapshotListeners.forEach((listener) => listener(next)))
}

beforeEach(() => {
  localStorage.clear()
  sessionStorage.clear()
  resetWorkbenchModeForTest()
  resetWorkbenchSessionCache()
  resetFirstMessagesForTest()
  clearComposerDrafts()
  clearPaneViewModes()
  snapshotListeners.length = 0
  call.mockReset().mockResolvedValue({})
  vi.mocked(api.panePrompt).mockReset().mockResolvedValue({ delivered: 'sent' })
  vi.mocked(api.paneDialog).mockReset().mockResolvedValue({ dialog: null })
  vi.mocked(api.launchers).mockReset().mockResolvedValue({ supported: true, launchers: [{ name: 'xcc', bypass: true, fullscreen: true, teammate: true }] })
  vi.mocked(api.startSession).mockReset()
  snapshot = {
    version: 'test', protocol: 1, focused_workspace_id: 'w1', focused_tab_id: 't1', focused_pane_id: 'p1',
    workspaces: [{ workspace_id: 'w1', label: 'alpha', number: 1, active_tab_id: 't1', agent_status: 'idle', focused: true, pane_count: 2, tab_count: 1 }],
    tabs: [{ tab_id: 't1', workspace_id: 'w1', label: 'main', number: 1, pane_count: 2, agent_status: 'idle', focused: true }],
    panes: [pane('p1', 't1', { agent: 'claude', label: 'Claude 会话', cwd: '/srv/app' }), pane('p2', 't1', { label: '构建', cwd: '/srv/app' })],
    layouts: [layout('t1', ['p1', 'p2'])],
    agents: [{ name: 'claude', agent: 'claude', agent_status: 'idle', pane_id: 'p1', workspace_id: 'w1', tab_id: 't1', focused: true }],
  }
  vi.stubGlobal('matchMedia', () => ({ matches: false, addEventListener() {}, removeEventListener() {} }))
})
afterEach(() => vi.unstubAllGlobals())

describe('Web UI mode', () => {
  it('opens in Web UI mode by default, with the console instead of terminals and no terminal-only tools', async () => {
    render(<WorkbenchPage hostID="host"/>)
    expect(await screen.findByRole('region', { name: '对话视图' })).toHaveAttribute('data-variant', 'console')
    expect(screen.getByRole('radio', { name: /Web UI 模式/ })).toHaveAttribute('aria-checked', 'true')
    expect(screen.queryByTestId('terminal-pane-p1')).not.toBeInTheDocument()
    expect(screen.queryByRole('button', { name: '本地输入框' })).not.toBeInTheDocument()
    expect(screen.queryByRole('region', { name: '本地输入' })).not.toBeInTheDocument()
    // 标签栏和侧栏工作区区域各有一个入口。
    expect(screen.getAllByRole('button', { name: '新建 Claude 会话' })).toHaveLength(2)
  })

  it('gives the phone top bar the mode switch and a new-session button instead of the terminal tools', async () => {
    vi.stubGlobal('matchMedia', () => ({ matches: true, addEventListener() {}, removeEventListener() {} }))
    render(<WorkbenchPage hostID="host"/>)
    const topbar = await screen.findByRole('banner', { name: '工作台导航' })
    expect(within(topbar).getByRole('radiogroup', { name: '工作台模式' })).toBeInTheDocument()
    expect(within(topbar).getByRole('button', { name: '新建 Claude 会话' })).toBeInTheDocument()
    for (const name of ['终端辅助键', '终端工具']) expect(within(topbar).queryByRole('button', { name })).not.toBeInTheDocument()
    expect(within(topbar).queryByRole('switch', { name: '对话视图' })).not.toBeInTheDocument()
    // 手机沿用自己的 pane 切换条，控制台不再画一份。
    expect(await screen.findByRole('navigation', { name: '切换终端' })).toBeInTheDocument()
    expect(screen.queryByRole('navigation', { name: '选择终端' })).not.toBeInTheDocument()
    expect(screen.queryByRole('region', { name: '本地输入' })).not.toBeInTheDocument()

    expect(within(topbar).getByRole('link', { name: 'Agent 总览' })).not.toHaveClass('mobile-shell-hidden')

    fireEvent.click(within(topbar).getByRole('radio', { name: /Web Shell 模式/ }))
    expect(await within(topbar).findByRole('button', { name: '终端工具' })).toBeInTheDocument()
    expect(within(topbar).getByRole('switch', { name: '对话视图' })).toBeInTheDocument()
    // Shell 模式的顶栏放不下 Agent 总览：入口移到切换面板（链接保持挂载以同步应用角标）。
    expect(within(topbar).getByRole('link', { name: 'Agent 总览', hidden: true })).toHaveClass('mobile-shell-hidden')
    fireEvent.click(within(topbar).getByRole('button', { name: '切换工作区或终端' }))
    expect(within(await screen.findByRole('dialog', { name: '切换工作区或终端' })).getByRole('link', { name: 'Agent 总览' })).toHaveAttribute('href', '/agents')
  })

  it('switches to Shell and back on the same pane without calling Herdr, and remembers the choice', async () => {
    render(<WorkbenchPage hostID="host"/>)
    fireEvent.click(await screen.findByRole('button', { name: /构建/ }))
    expect(screen.getByRole('region', { name: '画面视图' })).toHaveTextContent('p2')
    call.mockClear()

    fireEvent.click(screen.getByRole('radio', { name: /Web Shell 模式/ }))
    await waitFor(() => expect(screen.getByTestId('terminal-pane-p2')).toHaveAttribute('data-active', 'true'))
    expect(screen.queryByRole('region', { name: '画面视图' })).not.toBeInTheDocument()
    expect(screen.getByRole('button', { name: '本地输入框' })).toBeInTheDocument()
    expect(localStorage.getItem(WORKBENCH_MODE_STORAGE_KEY)).toBe('shell')

    fireEvent.click(screen.getByRole('radio', { name: /Web UI 模式/ }))
    expect(await screen.findByRole('region', { name: '画面视图' })).toHaveTextContent('p2')
    expect(localStorage.getItem(WORKBENCH_MODE_STORAGE_KEY)).toBe('ui')
    expect(call).not.toHaveBeenCalled()
  })

  it('opens a Claude pane in Shell on its terminal view when the chat asks for the terminal', async () => {
    writePaneViewMode('host', 'p1', 'chat')
    render(<WorkbenchPage hostID="host"/>)
    fireEvent.click(await screen.findByRole('button', { name: '对话里的切到终端' }))
    await waitFor(() => expect(screen.getByTestId('terminal-pane-p1')).toHaveAttribute('data-active', 'true'))
    expect(screen.getByTestId('terminal-pane-p1')).toHaveAttribute('data-view-mode', 'terminal')
    expect(readPaneViewMode('host', 'p1')).toBe('terminal')
  })

  it('keeps the Herdr prefix keys out of the console inputs', async () => {
    render(<WorkbenchPage hostID="host"/>)
    const chatInput = await screen.findByRole('textbox', { name: '对话输入内容' })
    fireEvent.keyDown(chatInput, { key: 'b', ctrlKey: true })
    fireEvent.keyDown(chatInput, { key: 'x' })
    expect(screen.queryByText('前缀模式 PREFIX')).not.toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: /构建/ }))
    const command = screen.getByRole('textbox', { name: '发送到终端的命令' })
    fireEvent.keyDown(command, { key: 'b', ctrlKey: true })
    fireEvent.keyDown(command, { key: 'v' })
    expect(screen.queryByText('前缀模式 PREFIX')).not.toBeInTheDocument()
    expect(call).not.toHaveBeenCalledWith('pane.split', expect.anything())
  })

  it('opens a screen-view pane in Shell on its terminal view too', async () => {
    writePaneViewMode('host', 'p2', 'screen')
    render(<WorkbenchPage hostID="host"/>)
    fireEvent.click(await screen.findByRole('button', { name: /构建/ }))
    fireEvent.click(screen.getByRole('button', { name: '在 Shell 中打开' }))
    await waitFor(() => expect(screen.getByTestId('terminal-pane-p2')).toHaveAttribute('data-view-mode', 'terminal'))
    expect(readPaneViewMode('host', 'p2')).toBe('terminal')
  })

  it('ignores the prefix keys that act on the hidden split layout, but keeps the others', async () => {
    render(<WorkbenchPage hostID="host"/>)
    await screen.findByRole('region', { name: '对话视图' })
    for (const key of ['z', 'r']) {
      fireEvent.keyDown(document.body, { key: 'b', ctrlKey: true })
      fireEvent.keyDown(document.body, { key })
    }
    expect(screen.queryByText('调整分屏 RESIZE')).not.toBeInTheDocument()
    expect(call).not.toHaveBeenCalledWith('pane.zoom', expect.anything())
    fireEvent.keyDown(document.body, { key: 'b', ctrlKey: true })
    fireEvent.keyDown(document.body, { key: 'w' })
    expect(await screen.findByRole('dialog', { name: '切换工作区或终端' })).toBeInTheDocument()
  })

  it('offers Open in Shell instead of terminal-only items in the pane menu', async () => {
    render(<WorkbenchPage hostID="host"/>)
    const agent = await screen.findByRole('button', { name: /claude/ })
    fireEvent.contextMenu(agent)
    const menu = screen.getByRole('menu')
    expect(within(menu).queryByRole('menuitem', { name: '复制' })).not.toBeInTheDocument()
    expect(within(menu).queryByRole('menuitem', { name: '显示对话视图' })).not.toBeInTheDocument()
    expect(within(menu).getByRole('menuitem', { name: '关闭终端' })).toBeInTheDocument()
    fireEvent.click(within(menu).getByRole('menuitem', { name: '在 Shell 中打开' }))
    await waitFor(() => expect(screen.getByTestId('terminal-pane-p1')).toBeInTheDocument())
  })

  it('leaves the dialog card of a blocked Claude pane to the chat view but keeps it for other panes', async () => {
    snapshot.panes = snapshot.panes.map((item) => ({ ...item, agent_status: 'blocked' }))
    vi.mocked(api.paneDialog).mockResolvedValue({ dialog: { fingerprint: 'f1', title: 'Proceed?', lines: [], options: [{ index: 1, label: 'Yes', selected: true }] } })
    render(<WorkbenchPage hostID="host"/>)
    await screen.findByRole('region', { name: '对话视图' })
    expect(document.querySelector('.workbench-dialog-card')).toBeNull()
    fireEvent.click(screen.getByRole('button', { name: /构建/ }))
    await waitFor(() => expect(document.querySelector('.workbench-dialog-card')).not.toBeNull())
  })

  it('starts a Claude session, moves to its pane once it appears, and sends the first message once', async () => {
    vi.mocked(api.startSession).mockResolvedValue({ workspace_id: 'w2', tab_id: 't2', pane_id: 'p9', session_id: 's', launcher: 'xcc' })
    render(<WorkbenchPage hostID="host"/>)
    fireEvent.click((await screen.findAllByRole('button', { name: '新建 Claude 会话' }))[0])
    const dialog = await screen.findByRole('dialog', { name: '新建 Claude 会话' })
    await within(dialog).findByRole('radio', { name: /xcc/ })
    fireEvent.change(within(dialog).getByLabelText('首条消息可选'), { target: { value: '先读 README' } })
    fireEvent.click(within(dialog).getByRole('button', { name: '新建会话' }))
    await waitFor(() => expect(screen.queryByRole('dialog', { name: '新建 Claude 会话' })).not.toBeInTheDocument())
    const requestID = vi.mocked(api.startSession).mock.calls[0][1].request_id

    // 接口结果先到，快照随后才带上新 pane（此时还是 Shell，Claude 未就绪）。
    const created = pane('p9', 't2', { workspace_id: 'w2', cwd: '/srv/app', agent_status: 'unknown' })
    publish({
      ...snapshot,
      workspaces: [...snapshot.workspaces, { workspace_id: 'w2', label: 'app', number: 2, active_tab_id: 't2', agent_status: 'idle', focused: false, pane_count: 1, tab_count: 1 }],
      tabs: [...snapshot.tabs, { tab_id: 't2', workspace_id: 'w2', label: '1', number: 1, pane_count: 1, agent_status: 'idle', focused: false }],
      panes: [...snapshot.panes, created],
      layouts: [...snapshot.layouts, { ...layout('t2', ['p9']), workspace_id: 'w2' }],
    })
    expect(await screen.findByRole('region', { name: '画面视图' })).toHaveTextContent('p9')
    expect(screen.getByText(/首条消息会在 Claude 就绪/)).toBeInTheDocument()
    expect(api.panePrompt).not.toHaveBeenCalled()

    publish({ ...snapshot, panes: snapshot.panes.map((item) => item.pane_id === 'p9' ? { ...item, agent: 'claude', agent_status: 'idle' } : item) })
    expect(await screen.findByRole('region', { name: '对话视图' })).toHaveTextContent('p9')
    await waitFor(() => expect(api.panePrompt).toHaveBeenCalledTimes(1))
    expect(api.panePrompt).toHaveBeenCalledWith('host', 'p9', { text: '先读 README', submit: true, client_msg_id: `first-${requestID}` })
    publish({ ...snapshot, panes: snapshot.panes.map((item) => item.pane_id === 'p9' ? { ...item, revision: 2 } : item) })
    await new Promise((resolve) => setTimeout(resolve, 20))
    expect(api.panePrompt).toHaveBeenCalledTimes(1)
  })
})
