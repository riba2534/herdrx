import { act, renderHook } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { Workspace } from '../types'
import { useWorkspaceBranches, WORKSPACE_BRANCH_REFRESH_MS } from './useWorkspaceBranches'

const workspace: Workspace = {
  workspace_id: 'w1', label: 'example', number: 1, active_tab_id: '', agent_status: 'idle', focused: true, pane_count: 0, tab_count: 0,
  worktree: { repo_key: '/workspace/example/.git', repo_name: 'example', repo_root: '/workspace/example', checkout_path: '/workspace/example', is_linked_worktree: false },
}
const listed = (branch: string | null) => ({ worktrees: [{ path: '/workspace/example', open_workspace_id: 'w1', branch }] })
const settle = () => act(async () => { await Promise.resolve() })

beforeEach(() => vi.useFakeTimers())
afterEach(() => { vi.useRealTimers(); vi.restoreAllMocks() })

describe('live workspace branches', () => {
  it('fetches a changed snapshot branch without retaining the initial cache', async () => {
    const call = vi.fn().mockResolvedValue(listed('main'))
    const client = { call }
    const { result, rerender } = renderHook(({ branch }) => useWorkspaceBranches(client, 'ready', [{ ...workspace, branch }]), { initialProps: { branch: 'main' } })
    await settle()
    expect(result.current.workspaceBranches.w1).toBe('main')
    call.mockResolvedValue(listed('feature/new'))
    rerender({ branch: 'feature/new' })
    await settle()
    expect(result.current.workspaceBranches.w1).toBe('feature/new')
    expect(call).toHaveBeenCalledTimes(2)
  })

  it('refreshes branchless snapshots while visible and clears detached HEAD on reconnect', async () => {
    const call = vi.fn().mockResolvedValue(listed('main'))
    const client = { call }
    const { result, rerender } = renderHook(({ connection }) => useWorkspaceBranches(client, connection, [workspace]), { initialProps: { connection: 'ready' } })
    await settle()
    call.mockResolvedValue(listed('feature/new'))
    await act(() => vi.advanceTimersByTimeAsync(WORKSPACE_BRANCH_REFRESH_MS))
    expect(result.current.workspaceBranches.w1).toBe('feature/new')
    rerender({ connection: 'offline' })
    call.mockResolvedValue(listed(null))
    rerender({ connection: 'ready' })
    await settle()
    expect(result.current.workspaceBranches.w1).toBe('')
  })

  it('refreshes on return to a visible page without polling hidden pages', async () => {
    const visibility = vi.spyOn(document, 'visibilityState', 'get').mockReturnValue('visible')
    const call = vi.fn().mockResolvedValue(listed('main'))
    const client = { call }
    const { result } = renderHook(() => useWorkspaceBranches(client, 'ready', [workspace]))
    await settle()
    visibility.mockReturnValue('hidden')
    await act(() => vi.advanceTimersByTimeAsync(WORKSPACE_BRANCH_REFRESH_MS * 2))
    expect(call).toHaveBeenCalledTimes(1)
    call.mockResolvedValue(listed('feature/returned'))
    visibility.mockReturnValue('visible')
    act(() => document.dispatchEvent(new Event('visibilitychange')))
    await settle()
    expect(result.current.workspaceBranches.w1).toBe('feature/returned')
  })

  it('coalesces slow requests and ignores results from a previous client', async () => {
    let resolveOld!: (result: unknown) => void
    const call = vi.fn().mockImplementation(() => new Promise((resolve) => { resolveOld = resolve }))
    const first = { call }
    const second = { call: vi.fn().mockResolvedValue(listed('new-host')) }
    const { result, rerender, unmount } = renderHook(({ client }) => useWorkspaceBranches(client, 'ready', [workspace]), { initialProps: { client: first } })
    act(() => window.dispatchEvent(new Event('focus')))
    await act(() => vi.advanceTimersByTimeAsync(WORKSPACE_BRANCH_REFRESH_MS * 2))
    expect(call).toHaveBeenCalledTimes(1)
    rerender({ client: second })
    await settle()
    await act(async () => { resolveOld(listed('old-host')) })
    expect(result.current.workspaceBranches.w1).toBe('new-host')
    unmount()
    await act(() => vi.advanceTimersByTimeAsync(WORKSPACE_BRANCH_REFRESH_MS * 2))
    expect(second.call).toHaveBeenCalledTimes(1)
  })

  it('lets the context menu look up a newly added workspace without worktree metadata', async () => {
    const call = vi.fn().mockResolvedValue(listed('main'))
    const client = { call }
    const { result, rerender } = renderHook(({ workspaces }) => useWorkspaceBranches(client, 'ready', workspaces), { initialProps: { workspaces: [] as Workspace[] } })
    rerender({ workspaces: [{ ...workspace, worktree: undefined }] })
    await act(() => result.current.refreshWorkspaceBranches('w1'))
    expect(result.current.workspaceBranches.w1).toBe('main')
    expect(call).toHaveBeenCalledTimes(1)
  })

  it('retains the last known branch on failure and retries on the next refresh', async () => {
    const call = vi.fn().mockResolvedValueOnce(listed('main')).mockRejectedValueOnce(new Error('offline')).mockResolvedValue(listed('recovered'))
    const client = { call }
    const { result } = renderHook(() => useWorkspaceBranches(client, 'ready', [workspace]))
    await settle()
    await act(() => vi.advanceTimersByTimeAsync(WORKSPACE_BRANCH_REFRESH_MS))
    expect(result.current.workspaceBranches.w1).toBe('main')
    expect(result.current.gitWorkspaces.w1).toBe(false)
    await act(() => vi.advanceTimersByTimeAsync(WORKSPACE_BRANCH_REFRESH_MS))
    expect(result.current.workspaceBranches.w1).toBe('recovered')
    expect(result.current.gitWorkspaces.w1).toBe(true)
  })
})
