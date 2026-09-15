import type { Snapshot } from '../types'
import { COMPACT_WORKBENCH_QUERY } from './displayPreferences'

export const WORKBENCH_DEVICE_KEY = 'herdrx.device.v1'
export const WORKBENCH_VISIT_KEY = 'herdrx.workbench-visit.v1'

export type WorkbenchClientClass = 'desktop' | 'mobile'

export type WorkbenchSession = {
  host_id: string
  workspace_id?: string
  tab_id?: string
  pane_id?: string
  device_id?: string
  client_class?: WorkbenchClientClass
  updated_at?: string
}

export type WorkbenchLocation = {
  host_id: string
  workspace_id?: string
  tab_id?: string
  pane_id?: string
}

export function workbenchDeviceID(storage: Storage | undefined = localStorage) {
  let value: string | null = null
  try {
    value = storage?.getItem(WORKBENCH_DEVICE_KEY) ?? null
  } catch {
    // Private storage may be unavailable.
  }
  if (!value) {
    value = typeof globalThis.crypto?.randomUUID === 'function' ? globalThis.crypto.randomUUID() : `dev-${Date.now().toString(16)}`
    try {
      storage?.setItem(WORKBENCH_DEVICE_KEY, value)
    } catch {
      // Keep a per-visit identifier even when persistence is blocked.
    }
  }
  return value
}

export function workbenchClientClass(mobile = compactWorkbench()): WorkbenchClientClass {
  return mobile ? 'mobile' : 'desktop'
}

export function compactWorkbench() {
  try {
    return window.matchMedia(COMPACT_WORKBENCH_QUERY).matches
  } catch {
    return false
  }
}

export function hasWorkbenchVisit(storage: Storage | undefined = sessionStorage) {
  try {
    return storage?.getItem(WORKBENCH_VISIT_KEY) === '1'
  } catch {
    return false
  }
}

export function markWorkbenchVisit(storage: Storage | undefined = sessionStorage) {
  try {
    storage?.setItem(WORKBENCH_VISIT_KEY, '1')
  } catch {
    // A blocked session store still allows this visit to continue without restore loops.
  }
}

export function shouldRestoreWorkbenchSession(
  session: WorkbenchSession | null | undefined,
  options: { path?: string; deviceID: string; clientClass: WorkbenchClientClass; hasVisit: boolean },
) {
  if (!session?.host_id) return false
  if (options.path && options.path !== '/') return false
  if (!options.hasVisit) return true
  if (session.device_id && session.device_id !== options.deviceID) return true
  if (session.client_class && session.client_class !== options.clientClass) return true
  return false
}

export function matchWorkbenchLocation(snapshot: Snapshot, location: Omit<WorkbenchLocation, 'host_id'> | WorkbenchLocation) {
  const pane = location.pane_id ? snapshot.panes.find((item) => item.pane_id === location.pane_id) : undefined
  const tab = (location.tab_id && snapshot.tabs.find((item) => item.tab_id === location.tab_id))
    || (pane && snapshot.tabs.find((item) => item.tab_id === pane.tab_id))
    || undefined
  const workspace = (location.workspace_id && snapshot.workspaces.find((item) => item.workspace_id === location.workspace_id))
    || (tab && snapshot.workspaces.find((item) => item.workspace_id === tab.workspace_id))
    || (pane && snapshot.workspaces.find((item) => item.workspace_id === pane.workspace_id))
    || undefined
  if (!workspace && !tab && !pane) return null
  const resolvedWorkspace = workspace || snapshot.workspaces[0]
  if (!resolvedWorkspace) return null
  const resolvedTab = tab && tab.workspace_id === resolvedWorkspace.workspace_id
    ? tab
    : snapshot.tabs.find((item) => item.tab_id === resolvedWorkspace.active_tab_id)
      || snapshot.tabs.find((item) => item.workspace_id === resolvedWorkspace.workspace_id)
  if (!resolvedTab) return { workspaceID: resolvedWorkspace.workspace_id, tabID: '', paneID: '' }
  const resolvedPane = pane && pane.tab_id === resolvedTab.tab_id
    ? pane
    : snapshot.panes.find((item) => item.tab_id === resolvedTab.tab_id)
  return {
    workspaceID: resolvedWorkspace.workspace_id,
    tabID: resolvedTab.tab_id,
    paneID: resolvedPane?.pane_id || '',
  }
}

export function workbenchSessionPayload(location: WorkbenchLocation, mobile = compactWorkbench()): WorkbenchSession {
  return {
    host_id: location.host_id,
    workspace_id: location.workspace_id || '',
    tab_id: location.tab_id || '',
    pane_id: location.pane_id || '',
    device_id: workbenchDeviceID(),
    client_class: workbenchClientClass(mobile),
  }
}
