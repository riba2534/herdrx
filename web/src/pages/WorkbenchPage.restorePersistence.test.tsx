import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, expect, it, vi } from 'vitest'
import { WorkbenchPage } from './WorkbenchPage'
import { api } from '../lib/api'
import { resetWorkbenchSessionCache } from '../lib/workbenchSession'
import type { Snapshot } from '../types'

const snapshot: Snapshot = {
  protocol: 1, version: 'test', focused_workspace_id: 'w1', focused_tab_id: 't1', focused_pane_id: 'p1',
  workspaces: [1, 2].map((id) => ({ workspace_id: `w${id}`, active_tab_id: `t${id}`, label: `Workspace ${id}`, number: id, pane_count: 1, tab_count: 1, agent_status: 'idle', focused: id === 1 })),
  tabs: [1, 2].map((id) => ({ workspace_id: `w${id}`, tab_id: `t${id}`, label: `Tab ${id}`, number: 1, pane_count: 1, agent_status: 'idle', focused: id === 1 })),
  panes: [1, 2].map((id) => ({ workspace_id: `w${id}`, tab_id: `t${id}`, pane_id: `p${id}`, terminal_id: `term${id}`, revision: 1, agent_status: 'idle', focused: id === 1 })),
  agents: [1, 2].map((id) => ({ workspace_id: `w${id}`, tab_id: `t${id}`, pane_id: `p${id}`, name: `agent${id}`, agent: 'codex', agent_status: 'idle', focused: id === 1 })),
  layouts: [],
}
vi.mock('../lib/workbench', () => ({ WorkbenchClient: class {
  onSnapshot(handler: (snapshot: Snapshot) => void) { handler(snapshot); return () => {} }
  onState(handler: (state: string) => void) { handler('ready'); return () => {} }
  onEpoch() { return () => {} }
  connect() {}
  dispose() {}
  hasOpenTerminals() { return false }
  call = vi.fn().mockResolvedValue({})
} }))
vi.mock('../lib/api', () => ({
  api: {
    host: vi.fn().mockResolvedValue({ host: { id: 'host', name: 'Test', transport: 'local' } }),
    hosts: vi.fn().mockResolvedValue({ hosts: [{ id: 'host', name: 'Test', transport: 'local' }] }),
    workbenchSession: vi.fn(),
    saveWorkbenchSession: vi.fn().mockResolvedValue({ session: { host_id: 'host' } }),
  },
  currentSessionID: () => 'restore-login', onAuthEvent: () => () => {},
}))
vi.mock('../components/TerminalPane', () => ({ TerminalPane: () => null }))
afterEach(() => vi.unstubAllGlobals())

it('keeps saving manual selections after a slow restore response arrives', async () => {
  resetWorkbenchSessionCache()
  vi.stubGlobal('matchMedia', () => ({ matches: false, addEventListener() {}, removeEventListener() {} }))
  let restored!: (value: { session: { host_id: string; workspace_id: string; tab_id: string; pane_id: string } }) => void
  vi.mocked(api.workbenchSession).mockImplementationOnce(() => new Promise((resolve) => { restored = resolve }))
  render(<WorkbenchPage hostID="host"/>)
  fireEvent.click(await screen.findByRole('button', { name: /agent1/ }))
  await act(async () => restored({ session: { host_id: 'host', workspace_id: 'w2', tab_id: 't2', pane_id: 'p2' } }))
  await waitFor(() => expect(api.saveWorkbenchSession).toHaveBeenCalledWith(expect.objectContaining({ workspace_id: 'w1' })))
  expect(screen.getByRole('button', { name: /agent1/ })).toHaveAttribute('aria-current', 'true')
  fireEvent.click(screen.getByRole('button', { name: /agent2/ }))
  await waitFor(() => expect(api.saveWorkbenchSession).toHaveBeenCalledWith(expect.objectContaining({ workspace_id: 'w2' })))
})
