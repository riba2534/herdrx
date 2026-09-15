import type { Workspace } from '../types'

export type WorkspaceGroup = {
  key: string
  parent: Workspace
  children: Workspace[]
  collapsible: boolean
}

export type VisibleWorkspaceGroup = WorkspaceGroup & { collapsed: boolean }

export type WorktreeListItem = {
  path?: string
  branch?: string | null
  open_workspace_id?: string | null
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return Boolean(value) && typeof value === 'object' && !Array.isArray(value)
}

export function workspaceRepoKey(workspace: Workspace): string {
  const key = workspace.worktree?.repo_key
  return typeof key === 'string' ? key.trim() : ''
}

export function isLinkedWorktree(workspace: Workspace): boolean {
  return Boolean(workspace.worktree?.is_linked_worktree && workspaceRepoKey(workspace))
}

export function workspaceGroupKey(workspace: Workspace): string | undefined {
  const key = workspaceRepoKey(workspace)
  return key || undefined
}

export function workspaceGroupHasChildren(workspaces: Workspace[], workspace: Workspace): boolean {
  const key = workspaceRepoKey(workspace)
  if (!key || workspace.worktree?.is_linked_worktree) return false
  return workspaces.some((item) => item.workspace_id !== workspace.workspace_id
    && workspaceRepoKey(item) === key
    && Boolean(item.worktree?.is_linked_worktree))
}

// Linked worktrees hang off the source checkout of their repo, not off whichever
// non-linked workspace happens to come first in snapshot order. Fall back to the
// first candidate when no checkout reports the repo root, so hosts with sparse
// metadata keep the previous behaviour.
export function workspaceRepoParent(workspaces: Workspace[], key: string): Workspace | undefined {
  if (!key) return undefined
  const candidates = workspaces.filter((item) => workspaceRepoKey(item) === key && !item.worktree?.is_linked_worktree)
  const source = candidates.find((item) => {
    const root = item.worktree?.repo_root
    return Boolean(root) && item.worktree?.checkout_path === root
  })
  return source || candidates[0]
}

export function gitWorkspaceListTargets(workspaces: Workspace[]): string[] {
  const seen = new Set<string>()
  const targets: string[] = []
  for (const workspace of workspaces) {
    const key = workspaceRepoKey(workspace)
    if (!key || seen.has(key)) continue
    seen.add(key)
    const parent = workspaceRepoParent(workspaces, key)
    targets.push((parent || workspace).workspace_id)
  }
  return targets
}

export function gitWorkspaceFetchKey(workspaces: Workspace[]): string {
  return workspaces
    .map((workspace) => JSON.stringify([workspace.workspace_id, workspaceRepoKey(workspace), workspace.worktree?.checkout_path || '', workspace.branch || '']))
    .sort()
    .join('\n')
}

export function groupWorkspaces(workspaces: Workspace[]): WorkspaceGroup[] {
  const assigned = new Set<string>()
  const groups: WorkspaceGroup[] = []

  const standalone = (workspace: Workspace) => {
    assigned.add(workspace.workspace_id)
    groups.push({ key: workspace.workspace_id, parent: workspace, children: [], collapsible: false })
  }

  for (const workspace of workspaces) {
    if (assigned.has(workspace.workspace_id)) continue
    const key = workspaceRepoKey(workspace)
    if (!key) {
      standalone(workspace)
      continue
    }
    if (workspace.worktree?.is_linked_worktree) {
      // A parent that is emitted later collects this child when it is visited.
      const parent = workspaceRepoParent(workspaces, key)
      if (parent && !assigned.has(parent.workspace_id)) continue
      standalone(workspace)
      continue
    }
    // Degenerate metadata: several non-linked workspaces share one repo_key. Only
    // the preferred parent may own the group, otherwise two groups would carry the
    // same key (shared collapse state) and the children would nest under the wrong
    // checkout; the others stay standalone rows.
    if (workspaceRepoParent(workspaces, key)?.workspace_id !== workspace.workspace_id) {
      standalone(workspace)
      continue
    }
    const children: Workspace[] = []
    const seenChildren = new Set<string>()
    for (const item of workspaces) {
      if (item.workspace_id === workspace.workspace_id || assigned.has(item.workspace_id)) continue
      if (workspaceRepoKey(item) !== key || !item.worktree?.is_linked_worktree) continue
      if (seenChildren.has(item.workspace_id)) continue
      seenChildren.add(item.workspace_id)
      children.push(item)
    }
    assigned.add(workspace.workspace_id)
    for (const child of children) assigned.add(child.workspace_id)
    groups.push({ key, parent: workspace, children, collapsible: children.length > 0 })
  }

  for (const workspace of workspaces) {
    if (assigned.has(workspace.workspace_id)) continue
    standalone(workspace)
  }

  return groups
}

// `keepVisibleWorkspaceID` never lets a collapsed group hide the workspace the user
// is looking at: the terminal would keep showing it while the sidebar lost its
// active row.
export function visibleWorkspaceGroups(workspaces: Workspace[], collapsedGroups: Iterable<string>, keepVisibleWorkspaceID?: string): VisibleWorkspaceGroup[] {
  const collapsed = collapsedGroups instanceof Set ? collapsedGroups : new Set(collapsedGroups)
  const groups = groupWorkspaces(workspaces)
  const keepKey = keepVisibleWorkspaceID
    ? groups.find((group) => group.children.some((child) => child.workspace_id === keepVisibleWorkspaceID))?.key
    : undefined
  return groups.map((group) => {
    const isCollapsed = group.collapsible && collapsed.has(group.key) && group.key !== keepKey
    return { ...group, collapsed: isCollapsed, children: isCollapsed ? [] : group.children }
  })
}

export function worktreeListEntries(result: unknown): WorktreeListItem[] {
  if (Array.isArray(result)) return result.filter(isRecord)
  if (!isRecord(result)) return []
  return Array.isArray(result.worktrees) ? result.worktrees.filter(isRecord) : []
}

export function branchesFromWorktreeList(result: unknown, workspaces: Workspace[]): Record<string, string> {
  const branches: Record<string, string> = {}
  for (const item of worktreeListEntries(result)) {
    // null is Git's detached HEAD: preserve it as an explicit empty value so
    // callers can clear an older cached branch instead of keeping it forever.
    const branch = item.branch === null ? '' : typeof item.branch === 'string' ? item.branch.trim() : undefined
    if (branch === undefined || (item.branch !== null && !branch)) continue
    if (typeof item.open_workspace_id === 'string' && item.open_workspace_id) {
      const byID = workspaces.find((workspace) => workspace.workspace_id === item.open_workspace_id)
      if (byID) {
        branches[byID.workspace_id] = branch
        continue
      }
    }
    if (typeof item.path === 'string' && item.path) {
      const byPath = workspaces.find((workspace) => workspace.worktree?.checkout_path === item.path)
      if (byPath) branches[byPath.workspace_id] = branch
    }
  }
  return branches
}

export function replaceWorkspaceBranches(current: Record<string, string>, result: unknown, workspaces: Workspace[], workspaceID: string): Record<string, string> {
  const source = workspaces.find((workspace) => workspace.workspace_id === workspaceID)
  if (!source) return current
  const key = workspaceRepoKey(source)
  const scope = key ? workspaces.filter((workspace) => workspaceRepoKey(workspace) === key) : [source]
  const next = { ...current }
  // worktree.list is a complete repository listing; a missing checkout or a
  // detached HEAD must remove its previous branch without touching other repos.
  for (const workspace of scope) next[workspace.workspace_id] = ''
  return { ...next, ...branchesFromWorktreeList(result, scope) }
}

export function workspaceBranchText(workspace: Workspace, branches?: Record<string, string>): string {
  const listed = branches?.[workspace.workspace_id]
  if (listed !== undefined) return listed
  return typeof workspace.branch === 'string' ? workspace.branch.trim() : ''
}
