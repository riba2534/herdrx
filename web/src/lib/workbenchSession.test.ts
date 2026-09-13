import { describe, expect, it } from 'vitest'
import type { Snapshot } from '../types'
import {
  applyAttachWindowForm,
  matchWorkbenchLocation,
  shouldRestoreWorkbenchSession,
  workbenchWindowForm,
  WORKBENCH_SIDEBAR_KEY,
} from './workbenchSession'

const snapshot: Snapshot = {
  version: 'test', protocol: 1,
  focused_workspace_id: 'w1', focused_tab_id: 'w1:t1', focused_pane_id: 'w1:p1',
  workspaces: [
    { workspace_id: 'w1', label: 'alpha', number: 1, active_tab_id: 'w1:t1', agent_status: 'idle', focused: true, pane_count: 1, tab_count: 1 },
    { workspace_id: 'w2', label: 'beta', number: 2, active_tab_id: 'w2:t1', agent_status: 'idle', focused: false, pane_count: 1, tab_count: 1 },
  ],
  tabs: [
    { tab_id: 'w1:t1', workspace_id: 'w1', label: '1', number: 1, pane_count: 1, agent_status: 'idle', focused: false },
    { tab_id: 'w2:t1', workspace_id: 'w2', label: '1', number: 1, pane_count: 1, agent_status: 'idle', focused: true },
  ],
  panes: [
    { pane_id: 'w1:p1', workspace_id: 'w1', tab_id: 'w1:t1', terminal_id: 'term1', agent_status: 'idle', focused: false, revision: 1 },
    { pane_id: 'w2:p1', workspace_id: 'w2', tab_id: 'w2:t1', terminal_id: 'term2', agent_status: 'idle', focused: true, revision: 1 },
  ],
  layouts: [],
  agents: [],
}

describe('shouldRestoreWorkbenchSession', () => {
  const session = { host_id: 'hst_desk', workspace_id: 'w2', device_id: 'pc-1', client_class: 'desktop' as const }

  it('restores a cold start and a PC ↔ phone device handoff', () => {
    expect(shouldRestoreWorkbenchSession(session, { path: '/', deviceID: 'pc-1', clientClass: 'desktop', hasVisit: false })).toBe(true)
    expect(shouldRestoreWorkbenchSession(session, { path: '/', deviceID: 'phone-1', clientClass: 'mobile', hasVisit: true })).toBe(true)
    expect(shouldRestoreWorkbenchSession(session, { path: '/', deviceID: 'pc-1', clientClass: 'mobile', hasVisit: true })).toBe(true)
  })

  it('keeps an explicit hosts visit on the same client', () => {
    expect(shouldRestoreWorkbenchSession(session, { path: '/', deviceID: 'pc-1', clientClass: 'desktop', hasVisit: true })).toBe(false)
    expect(shouldRestoreWorkbenchSession(session, { path: '/h/hst_desk', deviceID: 'phone-1', clientClass: 'mobile', hasVisit: false })).toBe(false)
    expect(shouldRestoreWorkbenchSession(null, { path: '/', deviceID: 'pc-1', clientClass: 'desktop', hasVisit: false })).toBe(false)
  })
})

describe('workbenchWindowForm', () => {
  it('gives the attaching client large or compact chrome, not the last writer', () => {
    expect(workbenchWindowForm('desktop')).toBe('large')
    expect(workbenchWindowForm('mobile')).toBe('compact')
    expect(applyAttachWindowForm('desktop').sidebarOpen).toBe(true)
    expect(applyAttachWindowForm('mobile')).toEqual({ form: 'compact', compact: true, sidebarOpen: false })
  })

  it('clears a leftover collapsed sidebar so a desktop attach is not a small shell', () => {
    localStorage.setItem(WORKBENCH_SIDEBAR_KEY, 'false')
    expect(applyAttachWindowForm('desktop')).toEqual({ form: 'large', compact: false, sidebarOpen: true })
    expect(localStorage.getItem(WORKBENCH_SIDEBAR_KEY)).toBe('true')
    applyAttachWindowForm('mobile')
    expect(localStorage.getItem(WORKBENCH_SIDEBAR_KEY)).toBeNull()
  })
})

describe('matchWorkbenchLocation', () => {
  it('restores the last pane even when Herdr focus is elsewhere', () => {
    expect(matchWorkbenchLocation(snapshot, { host_id: 'hst_desk', workspace_id: 'w2', tab_id: 'w2:t1', pane_id: 'w2:p1' })).toEqual({
      workspaceID: 'w2', tabID: 'w2:t1', paneID: 'w2:p1',
    })
  })

  it('falls back within the last workspace when a pane was closed', () => {
    expect(matchWorkbenchLocation(snapshot, { host_id: 'hst_desk', workspace_id: 'w2', tab_id: 'w2:t1', pane_id: 'gone' })).toEqual({
      workspaceID: 'w2', tabID: 'w2:t1', paneID: 'w2:p1',
    })
  })

  it('returns null when the snapshot no longer contains the location', () => {
    expect(matchWorkbenchLocation(snapshot, { host_id: 'hst_desk', workspace_id: 'missing', tab_id: 'gone', pane_id: 'gone' })).toBeNull()
  })
})
