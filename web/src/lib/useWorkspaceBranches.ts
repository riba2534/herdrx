import { useCallback, useEffect, useRef, useState } from 'react'
import type { Workspace } from '../types'
import type { WorkbenchClient } from './workbench'
import { gitWorkspaceFetchKey, gitWorkspaceListTargets, replaceWorkspaceBranches, workspaceRepoKey, worktreeListEntries } from './workspaceGroups'

export const WORKSPACE_BRANCH_REFRESH_MS = 15_000

// Older Herdr snapshots omit the branch. Refresh while visible as well as on
// snapshot branch changes, so a git switch in a pane reaches the sidebar.
export function useWorkspaceBranches(client: Pick<WorkbenchClient, 'call'>, connection: string, workspaces: Workspace[]) {
  const [gitWorkspaces, setGitWorkspaces] = useState<Record<string, boolean>>({})
  const [workspaceBranches, setWorkspaceBranches] = useState<Record<string, string>>({})
  const refreshRef = useRef<(workspaceID: string) => Promise<void>>(() => Promise.resolve())
  const fetchKey = gitWorkspaceFetchKey(workspaces)

  useEffect(() => {
    setGitWorkspaces({})
    setWorkspaceBranches({})
  }, [client])

  useEffect(() => {
    if (connection !== 'ready') return
    let cancelled = false
    const inFlight = new Map<string, Promise<void>>()
    const refresh = (workspaceID: string): Promise<void> => {
      const workspace = workspaces.find((item) => item.workspace_id === workspaceID)
      if (!workspace) return Promise.resolve()
      const key = workspaceRepoKey(workspace) || workspaceID
      const pending = inFlight.get(key)
      if (pending) return pending
      const request = client.call('worktree.list', { workspace_id: workspaceID }).then((result) => {
        if (cancelled) return
        setGitWorkspaces((current) => {
          const next = { ...current, [workspaceID]: true }
          for (const item of worktreeListEntries(result)) {
            if (typeof item.open_workspace_id === 'string' && item.open_workspace_id) next[item.open_workspace_id] = true
          }
          return next
        })
        setWorkspaceBranches((current) => replaceWorkspaceBranches(current, result, workspaces, workspaceID))
      }).catch(() => {
        // Keep the last known branch; the next visible refresh or context menu
        // retries this repository after a transient host/Git failure.
        if (!cancelled) setGitWorkspaces((current) => ({ ...current, [workspaceID]: false }))
      }).finally(() => { inFlight.delete(key) })
      inFlight.set(key, request)
      return request
    }
    const refreshVisible = () => {
      if (document.visibilityState === 'hidden') return
      for (const workspaceID of gitWorkspaceListTargets(workspaces)) void refresh(workspaceID)
    }
    refreshRef.current = refresh
    refreshVisible()
    const timer = window.setInterval(refreshVisible, WORKSPACE_BRANCH_REFRESH_MS)
    window.addEventListener('focus', refreshVisible)
    document.addEventListener('visibilitychange', refreshVisible)
    return () => {
      cancelled = true
      refreshRef.current = () => Promise.resolve()
      window.clearInterval(timer)
      window.removeEventListener('focus', refreshVisible)
      document.removeEventListener('visibilitychange', refreshVisible)
    }
  }, [client, connection, fetchKey])

  const refreshWorkspaceBranches = useCallback((workspaceID: string) => refreshRef.current(workspaceID), [])
  return { gitWorkspaces, workspaceBranches, refreshWorkspaceBranches }
}
