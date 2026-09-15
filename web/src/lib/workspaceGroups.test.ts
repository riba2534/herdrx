import { describe, expect, it } from 'vitest'
import type { Workspace } from '../types'
import {
  branchesFromWorktreeList,
  gitWorkspaceFetchKey,
  gitWorkspaceListTargets,
  groupWorkspaces,
  isLinkedWorktree,
  replaceWorkspaceBranches,
  visibleWorkspaceGroups,
  workspaceBranchText,
  workspaceGroupHasChildren,
  workspaceGroupKey,
  workspaceRepoParent,
  worktreeListEntries,
} from './workspaceGroups'

function workspace(partial: Partial<Workspace> & Pick<Workspace, 'workspace_id' | 'label' | 'number'>): Workspace {
  return {
    active_tab_id: `${partial.workspace_id}:t1`,
    agent_status: 'idle',
    focused: false,
    pane_count: 1,
    tab_count: 1,
    ...partial,
  }
}

const exampleWorktree = {
  repo_key: '/workspace/example/.git',
  repo_name: 'example',
  repo_root: '/workspace/example',
  checkout_path: '/workspace/example',
  is_linked_worktree: false,
}

describe('workspace grouping', () => {
  it('keeps old hosts without worktree metadata as a flat list', () => {
    const workspaces = [
      workspace({ workspace_id: 'w1', label: '~', number: 1 }),
      workspace({ workspace_id: 'w2', label: 'herdrx', number: 2 }),
    ]
    expect(groupWorkspaces(workspaces)).toEqual([
      { key: 'w1', parent: workspaces[0], children: [], collapsible: false },
      { key: 'w2', parent: workspaces[1], children: [], collapsible: false },
    ])
  })

  it('groups linked worktrees under the parent that shares repo_key', () => {
    const parent = workspace({ workspace_id: 'w3', label: 'astergate', number: 3, worktree: exampleWorktree })
    const child = workspace({
      workspace_id: 'w5',
      label: 'codex-reset-cards',
      number: 5,
      worktree: { ...exampleWorktree, checkout_path: '/workspace/example/.herdr/worktrees/example/feat-codex-reset', is_linked_worktree: true },
    })
    const home = workspace({ workspace_id: 'w2', label: '~', number: 2 })
    const other = workspace({
      workspace_id: 'w6',
      label: 'herdrx',
      number: 6,
      worktree: { repo_key: '/workspace/herdrx/.git', repo_name: 'herdrx', repo_root: '/workspace/herdrx', checkout_path: '/workspace/herdrx', is_linked_worktree: false },
    })
    const groups = groupWorkspaces([home, parent, child, other])
    expect(groups.map((group) => [group.parent.label, group.children.map((item) => item.label), group.collapsible])).toEqual([
      ['~', [], false],
      ['astergate', ['codex-reset-cards'], true],
      ['herdrx', [], false],
    ])
    expect(workspaceGroupKey(parent)).toBe(exampleWorktree.repo_key)
    expect(workspaceGroupHasChildren([home, parent, child, other], parent)).toBe(true)
    expect(isLinkedWorktree(child)).toBe(true)
  })

  it('does not infer hierarchy from display-name prefixes', () => {
    const workspaces = [
      workspace({ workspace_id: 'w1', label: 'astergate', number: 1 }),
      workspace({ workspace_id: 'w2', label: 'astergate-docs', number: 2 }),
    ]
    expect(groupWorkspaces(workspaces).map((group) => group.parent.workspace_id)).toEqual(['w1', 'w2'])
    expect(groupWorkspaces(workspaces).every((group) => group.children.length === 0)).toBe(true)
  })

  it('treats orphaned linked worktrees and empty repo keys as standalone rows', () => {
    const orphan = workspace({
      workspace_id: 'w9',
      label: 'orphan',
      number: 9,
      worktree: { ...exampleWorktree, is_linked_worktree: true },
    })
    const emptyKey = workspace({
      workspace_id: 'w8',
      label: 'empty',
      number: 8,
      worktree: { ...exampleWorktree, repo_key: '   ', is_linked_worktree: true },
    })
    const groups = groupWorkspaces([orphan, emptyKey])
    expect(groups).toHaveLength(2)
    expect(groups.every((group) => !group.collapsible && group.children.length === 0)).toBe(true)
    expect(groups[0].parent).toBe(orphan)
    expect(groups[1].parent).toBe(emptyKey)
  })

  it('keeps a later parent in snapshot order and still collects earlier children', () => {
    const child = workspace({
      workspace_id: 'w5',
      label: 'codex-reset-cards',
      number: 1,
      worktree: { ...exampleWorktree, is_linked_worktree: true },
    })
    const parent = workspace({ workspace_id: 'w3', label: 'astergate', number: 2, worktree: exampleWorktree })
    expect(groupWorkspaces([child, parent])).toEqual([
      { key: exampleWorktree.repo_key, parent, children: [child], collapsible: true },
    ])
  })

  it('does not nest two non-linked workspaces that share a repo_key', () => {
    const first = workspace({ workspace_id: 'w1', label: 'one', number: 1, worktree: exampleWorktree })
    const second = workspace({ workspace_id: 'w2', label: 'two', number: 2, worktree: exampleWorktree })
    const groups = groupWorkspaces([first, second])
    expect(groups.map((group) => [group.parent.workspace_id, group.children, group.collapsible])).toEqual([
      ['w1', [], false],
      ['w2', [], false],
    ])
    // The context menu must not offer a collapse that the sidebar render would ignore.
    expect(workspaceGroupHasChildren([first, second], first)).toBe(false)
    expect(workspaceGroupHasChildren([first, second], second)).toBe(false)
  })

  it('nests linked worktrees under the source checkout even when another workspace shares the repo_key', () => {
    const other = workspace({
      workspace_id: 'w1',
      label: 'other',
      number: 1,
      worktree: { ...exampleWorktree, checkout_path: '/workspace/example/sub' },
    })
    const source = workspace({ workspace_id: 'w2', label: 'source', number: 2, worktree: exampleWorktree })
    const child = workspace({
      workspace_id: 'w3',
      label: 'child',
      number: 3,
      worktree: { ...exampleWorktree, is_linked_worktree: true },
    })
    expect(workspaceRepoParent([other, source], exampleWorktree.repo_key)).toBe(source)
    // The non-source checkout precedes the source one in snapshot order.
    const groups = groupWorkspaces([other, source, child])
    expect(groups.map((group) => group.parent.workspace_id)).toEqual(['w1', 'w2'])
    expect(groups[1].children).toEqual([child])
    expect(groups[0].children).toEqual([])
    // Duplicate group keys would share collapse state and React identity.
    expect(new Set(groups.map((group) => group.key)).size).toBe(groups.length)
  })

  it('falls back to the first non-linked workspace when no checkout reports the repo root', () => {
    const withoutRoot = workspace({
      workspace_id: 'w1',
      label: 'one',
      number: 1,
      worktree: { ...exampleWorktree, repo_root: '' },
    })
    const second = workspace({
      workspace_id: 'w2',
      label: 'two',
      number: 2,
      worktree: { ...exampleWorktree, repo_root: '', checkout_path: '/workspace/example/sub' },
    })
    expect(workspaceRepoParent([withoutRoot, second], exampleWorktree.repo_key)).toBe(withoutRoot)
    expect(workspaceRepoParent([withoutRoot], '')).toBeUndefined()
  })

  it('keeps a collapsed group expanded while it holds the active workspace', () => {
    const parent = workspace({ workspace_id: 'w3', label: 'astergate', number: 3, worktree: exampleWorktree })
    const child = workspace({
      workspace_id: 'w5',
      label: 'codex-reset-cards',
      number: 5,
      worktree: { ...exampleWorktree, is_linked_worktree: true },
    })
    const collapsed = new Set([exampleWorktree.repo_key])
    expect(visibleWorkspaceGroups([parent, child], collapsed).map((group) => group.collapsed)).toEqual([true])
    // The active workspace is the linked child, so hiding it would leave the
    // sidebar without an active row while the terminal still shows it.
    expect(visibleWorkspaceGroups([parent, child], collapsed, 'w5').map((group) => [group.collapsed, group.children.length])).toEqual([[false, 1]])
    // The active workspace is the parent, so collapsing stays available.
    expect(visibleWorkspaceGroups([parent, child], collapsed, 'w3').map((group) => group.collapsed)).toEqual([true])
    expect(visibleWorkspaceGroups([parent, child], collapsed, 'unrelated').map((group) => group.collapsed)).toEqual([true])
  })

  it('breaks cycles by assigning each leftover workspace once', () => {
    const a = workspace({
      workspace_id: 'wa',
      label: 'a',
      number: 1,
      worktree: { ...exampleWorktree, is_linked_worktree: true },
    })
    const b = workspace({
      workspace_id: 'wb',
      label: 'b',
      number: 2,
      worktree: { ...exampleWorktree, repo_key: '/workspace/other/.git', is_linked_worktree: true },
    })
    const groups = groupWorkspaces([a, b])
    expect(groups.map((group) => group.parent.workspace_id).sort()).toEqual(['wa', 'wb'])
    expect(new Set(groups.flatMap((group) => [group.parent, ...group.children].map((item) => item.workspace_id))).size).toBe(2)
  })

  it('hides collapsed children without dropping the parent', () => {
    const parent = workspace({ workspace_id: 'w3', label: 'astergate', number: 3, worktree: exampleWorktree })
    const child = workspace({
      workspace_id: 'w5',
      label: 'codex-reset-cards',
      number: 5,
      worktree: { ...exampleWorktree, is_linked_worktree: true },
    })
    const visible = visibleWorkspaceGroups([parent, child], new Set([exampleWorktree.repo_key]))
    expect(visible).toEqual([{ key: exampleWorktree.repo_key, parent, children: [], collapsible: true, collapsed: true }])
  })

  it('lists one worktree.list target per repo_key', () => {
    const parent = workspace({ workspace_id: 'w3', label: 'astergate', number: 3, worktree: exampleWorktree })
    const child = workspace({
      workspace_id: 'w5',
      label: 'child',
      number: 5,
      worktree: { ...exampleWorktree, is_linked_worktree: true },
    })
    const other = workspace({
      workspace_id: 'w6',
      label: 'herdrx',
      number: 6,
      worktree: { repo_key: '/workspace/herdrx/.git', repo_name: 'herdrx', repo_root: '/workspace/herdrx', checkout_path: '/workspace/herdrx', is_linked_worktree: false },
    })
    expect(gitWorkspaceListTargets([child, parent, other])).toEqual(['w3', 'w6'])
    expect(gitWorkspaceFetchKey([parent, child])).toContain('w3')
    expect(gitWorkspaceFetchKey([parent, child])).toContain(exampleWorktree.repo_key)
  })
})

describe('worktree.list branch join', () => {
  const parent = workspace({ workspace_id: 'w3', label: 'astergate', number: 3, worktree: exampleWorktree })
  const child = workspace({
    workspace_id: 'w5',
    label: 'codex-reset-cards',
    number: 5,
    worktree: { ...exampleWorktree, checkout_path: '/workspace/example/.herdr/worktrees/example/feat-codex-reset', is_linked_worktree: true },
  })

  it('reads worktrees from the protocol wrapper or a bare array', () => {
    expect(worktreeListEntries({ type: 'worktree_list', worktrees: [{ path: '/workspace/example', branch: 'main' }] })).toHaveLength(1)
    expect(worktreeListEntries([{ path: '/workspace/example', branch: 'main' }])).toHaveLength(1)
    expect(worktreeListEntries({})).toEqual([])
    expect(worktreeListEntries(null)).toEqual([])
  })

  it('clears detached and missing checkouts while preserving another repository', () => {
    const other = workspace({ workspace_id: 'other', label: 'other', number: 9 })
    const result = { worktrees: [{ path: parent.worktree!.checkout_path, open_workspace_id: parent.workspace_id, branch: null }] }
    const branches = replaceWorkspaceBranches({ w3: 'main', w5: 'feature/old', other: 'unrelated' }, result, [parent, child, other], 'w3')
    expect(branches).toEqual({ w3: '', w5: '', other: 'unrelated' })
    expect(workspaceBranchText({ ...parent, branch: 'stale-snapshot' }, branches)).toBe('')
  })

  it('joins branch by open_workspace_id then checkout path', () => {
    const branches = branchesFromWorktreeList({
      type: 'worktree_list',
      worktrees: [
        { open_workspace_id: 'w3', path: '/workspace/example', branch: 'feat/overview-tps-m' },
        { open_workspace_id: null, path: '/workspace/example/.herdr/worktrees/example/feat-codex-reset', branch: 'feat/codex-reset-credits' },
        { open_workspace_id: 'missing', path: '/tmp/ignored', branch: 'nope' },
        { open_workspace_id: 'w3', path: '/workspace/example', branch: '   ' },
      ],
    }, [parent, child])
    expect(branches).toEqual({
      w3: 'feat/overview-tps-m',
      w5: 'feat/codex-reset-credits',
    })
    expect(workspaceBranchText(parent, branches)).toBe('feat/overview-tps-m')
    expect(workspaceBranchText(child, branches)).toBe('feat/codex-reset-credits')
    expect(workspaceBranchText(workspace({ workspace_id: 'w2', label: '~', number: 1, branch: ' leftover ' }))).toBe('leftover')
    expect(workspaceBranchText(workspace({ workspace_id: 'w1', label: '~', number: 1 }))).toBe('')
  })
})
