import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { Snapshot } from '../types'
import { WorkbenchPage } from './WorkbenchPage'
import { api } from '../lib/api'

const { call } = vi.hoisted(() => ({ call: vi.fn().mockResolvedValue({}) }))
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
    onState(handler: (state: string) => void) { handler('ready'); return () => {} }
    onEpoch() { return () => {} }
    connect() {}
    dispose() {}
  },
}))
vi.mock('../lib/api', () => ({ api: {
  host: vi.fn().mockResolvedValue({ host: { id: 'host', name: 'Local', transport: 'local' } }),
  hosts: vi.fn().mockResolvedValue({ hosts: [{ id: 'host', name: 'Local', transport: 'local' }, { id: 'host-other', name: 'Office', transport: 'ssh' }] }),
  renameHost: vi.fn().mockImplementation(async (id: string, name: string) => ({ host: { id, name, transport: 'local' } })),
} }))
vi.mock('../components/TerminalPane', () => ({ TerminalPane: () => null }))

beforeEach(() => {
  localStorage.clear()
  call.mockClear()
  vi.stubGlobal('matchMedia', () => ({ matches: false, addEventListener() {}, removeEventListener() {} }))
})
afterEach(() => vi.unstubAllGlobals())

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
