import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { Snapshot } from '../types'
import { WorkbenchPage } from './WorkbenchPage'
import { clearComposerDrafts } from '../lib/composerDrafts'
import { clearPaneViewModes, readPaneViewMode } from '../lib/paneViewMode'

const { call, connection, retryNow } = vi.hoisted(() => ({ call: vi.fn().mockResolvedValue({}), connection: { state: 'ready' as string, message: '', retryAt: 0 }, retryNow: vi.fn() }))
const snapshot: Snapshot = {
  version: 'test', protocol: 1,
  focused_workspace_id: 'w1', focused_tab_id: 'w1:t1', focused_pane_id: 'w1:p1',
  workspaces: ['alpha', 'beta'].map((label, i) => ({ workspace_id: `w${i + 1}`, label, number: i + 1, active_tab_id: `w${i + 1}:t1`, agent_status: 'idle', focused: i === 0, pane_count: 1, tab_count: 1 })),
  tabs: ([
    { tab_id: 'w1:t1', workspace_id: 'w1', label: '1', number: 1, pane_count: 1, agent_status: 'idle', focused: true },
    { tab_id: 'w1:t2', workspace_id: 'w1', label: '2', number: 2, pane_count: 1, agent_status: 'idle', focused: false },
    { tab_id: 'w2:t1', workspace_id: 'w2', label: '1', number: 1, pane_count: 1, agent_status: 'idle', focused: false },
  ] satisfies Snapshot['tabs']),
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
  },
  currentSessionID: () => 'workbench-session',
  onAuthEvent: () => () => {},
}))
// TerminalPane 自己的行为（xterm 保活、流不重开、输入禁用）在 TerminalPane.test.tsx 覆盖；
// 这里用桩件确认工作台把 viewMode 正确下发到对应 pane，并只作用于被选中的 pane。
vi.mock('../components/TerminalPane', () => ({
  TerminalPane: ({ pane, viewMode }: { pane: { pane_id: string }; viewMode: string }) =>
    viewMode === 'chat' ? <div role="region" aria-label="对话视图">{pane.pane_id}</div> : <div role="region" aria-label="终端视图">{pane.pane_id}</div>,
}))

beforeEach(() => {
  localStorage.clear()
  sessionStorage.clear()
  clearComposerDrafts()
  clearPaneViewModes()
  call.mockClear()
  retryNow.mockClear()
  connection.state = 'ready'
  connection.message = ''
  connection.retryAt = 0
  snapshot.layouts = [
    { workspace_id: 'w1', tab_id: 'w1:t1', focused_pane_id: 'w1:p1', splits: [], zoomed: false, area: { x: 0, y: 0, width: 80, height: 40 }, panes: [{ pane_id: 'w1:p1', focused: true, rect: { x: 0, y: 0, width: 80, height: 40 } }] },
    { workspace_id: 'w1', tab_id: 'w1:t2', focused_pane_id: 'w1:p2', splits: [], zoomed: false, area: { x: 0, y: 0, width: 80, height: 40 }, panes: [{ pane_id: 'w1:p2', focused: true, rect: { x: 0, y: 0, width: 80, height: 40 } }] },
    { workspace_id: 'w2', tab_id: 'w2:t1', focused_pane_id: 'w2:p1', splits: [], zoomed: false, area: { x: 0, y: 0, width: 80, height: 40 }, panes: [{ pane_id: 'w2:p1', focused: true, rect: { x: 0, y: 0, width: 80, height: 40 } }] },
  ]
  vi.stubGlobal('matchMedia', () => ({ matches: false, addEventListener() {}, removeEventListener() {} }))
})
afterEach(() => {
  clearPaneViewModes()
  vi.unstubAllGlobals()
})

const openSwitcher = async () => {
  fireEvent.click(await screen.findByRole('button', { name: '切换工作区或终端' }))
  return screen.findByRole('dialog', { name: '切换工作区或终端' })
}

describe('workbench pane chat mode', () => {
  it('switches the selected pane to the chat view and back from the switcher actions', async () => {
    render(<WorkbenchPage hostID="host"/>)
    await openSwitcher()
    const chatAction = await screen.findByRole('button', { name: /切换到对话视图/ })
    expect(chatAction).toHaveAttribute('aria-pressed', 'false')
    fireEvent.click(chatAction)
    await waitFor(() => expect(screen.getByRole('region', { name: '对话视图' })).toHaveTextContent('w1:p1'))
    expect(readPaneViewMode('host', 'w1:p1')).toBe('chat')

    await openSwitcher()
    const terminalAction = screen.getByRole('button', { name: /切换到终端视图/ })
    expect(terminalAction).toHaveAttribute('aria-pressed', 'true')
    fireEvent.click(terminalAction)
    await waitFor(() => expect(screen.queryByRole('region', { name: '对话视图' })).not.toBeInTheDocument())
    expect(readPaneViewMode('host', 'w1:p1')).toBe('terminal')
  })

  it('hides the workbench composer while the selected pane shows the chat view and restores its draft', async () => {
    render(<WorkbenchPage hostID="host"/>)
    fireEvent.click(await screen.findByRole('button', { name: '本地输入框' }))
    fireEvent.change(screen.getByRole('textbox', { name: '本地输入内容' }), { target: { value: '草稿保留' } })

    await openSwitcher()
    fireEvent.click(screen.getByRole('button', { name: /切换到对话视图/ }))
    await waitFor(() => expect(screen.queryByRole('region', { name: '本地输入' })).not.toBeInTheDocument())

    await openSwitcher()
    fireEvent.click(screen.getByRole('button', { name: /切换到终端视图/ }))
    await waitFor(() => expect(screen.getByRole('region', { name: '本地输入' })).toBeInTheDocument())
    expect(screen.getByRole('textbox', { name: '本地输入内容' })).toHaveValue('草稿保留')
  })

  it('keeps chat mode per pane so another pane stays on the terminal', async () => {
    render(<WorkbenchPage hostID="host"/>)
    await openSwitcher()
    fireEvent.click(screen.getByRole('button', { name: /切换到对话视图/ }))
    await waitFor(() => expect(readPaneViewMode('host', 'w1:p1')).toBe('chat'))

    fireEvent.click(screen.getByRole('button', { name: /agent2/ }))
    await waitFor(() => expect(screen.queryByRole('region', { name: '对话视图' })).not.toBeInTheDocument())
    expect(readPaneViewMode('host', 'w2:p1')).toBe('terminal')

    fireEvent.click(screen.getByRole('button', { name: /agent1/ }))
    await waitFor(() => expect(screen.getByRole('region', { name: '对话视图' })).toHaveTextContent('w1:p1'))
  })

  it('offers the chat view on the pane context menu and toggles only that pane', async () => {
    render(<WorkbenchPage hostID="host"/>)
    const agent = await screen.findByRole('button', { name: /agent2/ })
    expect(fireEvent.contextMenu(agent.querySelector('strong')!)).toBe(false)
    fireEvent.click(screen.getByRole('menuitem', { name: '显示对话视图' }))
    await waitFor(() => expect(screen.getByRole('region', { name: '对话视图' })).toHaveTextContent('w2:p1'))
    expect(readPaneViewMode('host', 'w1:p1')).toBe('terminal')

    const again = await screen.findByRole('button', { name: /agent2/ })
    expect(fireEvent.contextMenu(again.querySelector('strong')!)).toBe(false)
    fireEvent.click(screen.getByRole('menuitem', { name: '显示终端视图' }))
    await waitFor(() => expect(screen.queryByRole('region', { name: '对话视图' })).not.toBeInTheDocument())
  })
})
