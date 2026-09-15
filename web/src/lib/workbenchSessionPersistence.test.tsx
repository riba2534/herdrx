import { act, renderHook } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { api } from './api'
import { readWorkbenchSession, useWorkbenchSessionPersistence, waitForWorkbenchSessionWrites } from './workbenchSessionPersistence'

const auth = vi.hoisted(() => ({ sessionID: 'login-a' }))
vi.mock('./api', () => ({
  api: { saveWorkbenchSession: vi.fn().mockResolvedValue({ session: { host_id: 'host' } }), workbenchSession: vi.fn().mockResolvedValue({ session: null }) },
  currentSessionID: () => auth.sessionID,
}))

const location = (workspace: string, host = 'host') => ({ host_id: host, workspace_id: workspace, tab_id: `${workspace}:t1`, pane_id: `${workspace}:p1` })
const tick = async () => { await act(() => vi.advanceTimersByTimeAsync(300)) }

beforeEach(() => {
  vi.useFakeTimers()
  auth.sessionID = 'login-a'
  vi.mocked(api.saveWorkbenchSession).mockReset().mockResolvedValue({ session: { host_id: 'host' } })
})
afterEach(async () => {
  await waitForWorkbenchSessionWrites()
  vi.useRealTimers()
  vi.restoreAllMocks()
})

describe('workbench position persistence', () => {
  it('debounces changes but flushes the latest position when leaving the workbench', async () => {
    const view = renderHook(({ value }) => useWorkbenchSessionPersistence(value, false), { initialProps: { value: location('w1') } })
    view.rerender({ value: location('w2') })
    expect(api.saveWorkbenchSession).not.toHaveBeenCalled()
    view.unmount()
    await waitForWorkbenchSessionWrites()
    expect(api.saveWorkbenchSession).toHaveBeenCalledTimes(1)
    expect(api.saveWorkbenchSession).toHaveBeenCalledWith(expect.objectContaining({ workspace_id: 'w2' }))
    await tick()
    expect(api.saveWorkbenchSession).toHaveBeenCalledTimes(1)
  })

  it('flushes on pagehide without republishing a position that is already saved', async () => {
    const view = renderHook(({ value }) => useWorkbenchSessionPersistence(value, false), { initialProps: { value: location('w1') } })
    act(() => window.dispatchEvent(new Event('pagehide')))
    await waitForWorkbenchSessionWrites()
    await tick()
    act(() => window.dispatchEvent(new Event('pagehide')))
    expect(api.saveWorkbenchSession).toHaveBeenCalledTimes(1)
    view.rerender({ value: location('w2') })
    act(() => window.dispatchEvent(new Event('pagehide')))
    await waitForWorkbenchSessionWrites()
    expect(api.saveWorkbenchSession).toHaveBeenLastCalledWith(expect.objectContaining({ workspace_id: 'w2' }))
    view.unmount()
  })

  it('orders saves across host mounts even when the previous request is slow', async () => {
    let finish!: (value: { session: { host_id: string } }) => void
    vi.mocked(api.saveWorkbenchSession).mockImplementationOnce(() => new Promise((resolve) => { finish = resolve }))
    const first = renderHook(() => useWorkbenchSessionPersistence(location('old', 'host-a'), false))
    await tick()
    first.unmount()
    const second = renderHook(() => useWorkbenchSessionPersistence(location('new', 'host-b'), false))
    await tick()
    expect(api.saveWorkbenchSession).toHaveBeenCalledTimes(1)
    finish({ session: { host_id: 'host-a' } })
    await waitForWorkbenchSessionWrites()
    expect(api.saveWorkbenchSession).toHaveBeenCalledTimes(2)
    expect(api.saveWorkbenchSession).toHaveBeenLastCalledWith(expect.objectContaining({ host_id: 'host-b', workspace_id: 'new' }))
    second.unmount()
  })

  it('starts the final keepalive save during pagehide even if an earlier request is still running', async () => {
    let finish!: (value: { session: { host_id: string } }) => void
    vi.mocked(api.saveWorkbenchSession).mockImplementationOnce(() => new Promise((resolve) => { finish = resolve }))
    const view = renderHook(({ value }) => useWorkbenchSessionPersistence(value, false), { initialProps: { value: location('w1') } })
    await tick()
    view.rerender({ value: location('w2') })
    act(() => window.dispatchEvent(new Event('pagehide')))
    expect(api.saveWorkbenchSession).toHaveBeenCalledTimes(2)
    const [old, latest] = vi.mocked(api.saveWorkbenchSession).mock.calls.map(([payload]) => payload)
    expect(latest).toMatchObject({ workspace_id: 'w2', writer_id: old.writer_id })
    expect(latest.sequence).toBeGreaterThan(old.sequence!)
    finish({ session: { host_id: 'host' } })
    await waitForWorkbenchSessionWrites()
    view.unmount()
    expect(api.saveWorkbenchSession).toHaveBeenCalledTimes(2)
  })

  it('does not send pending or queued positions under a different login', async () => {
    let finish!: (value: { session: { host_id: string } }) => void
    vi.mocked(api.saveWorkbenchSession).mockImplementationOnce(() => new Promise((resolve) => { finish = resolve }))
    const view = renderHook(({ value }) => useWorkbenchSessionPersistence(value, false), { initialProps: { value: location('w1') } })
    await tick()
    view.rerender({ value: location('w2') })
    await tick()
    view.rerender({ value: location('w3') })
    auth.sessionID = 'login-b'
    view.unmount()
    finish({ session: { host_id: 'host' } })
    await waitForWorkbenchSessionWrites()
    expect(api.saveWorkbenchSession).toHaveBeenCalledTimes(1)
  })

  it('continues after a save failure without retrying the failed position', async () => {
    const warning = vi.spyOn(console, 'warn').mockImplementation(() => {})
    vi.mocked(api.saveWorkbenchSession).mockRejectedValueOnce(new Error('offline'))
    const view = renderHook(({ value }) => useWorkbenchSessionPersistence(value, false), { initialProps: { value: location('w1') } })
    await tick()
    expect(warning).toHaveBeenCalledOnce()
    view.rerender({ value: location('w2') })
    view.unmount()
    await waitForWorkbenchSessionWrites()
    expect(api.saveWorkbenchSession).toHaveBeenCalledTimes(2)
    expect(api.saveWorkbenchSession).toHaveBeenLastCalledWith(expect.objectContaining({ workspace_id: 'w2' }))
  })

  it('bounds hung saves, coalesces pending positions, and restores local state without waiting for the network', async () => {
    vi.spyOn(console, 'warn').mockImplementation(() => {})
    vi.mocked(api.saveWorkbenchSession).mockImplementationOnce(() => new Promise(() => {}))
    const view = renderHook(({ value }) => useWorkbenchSessionPersistence(value, false), { initialProps: { value: location('w1') } })
    await tick()
    const first = vi.mocked(api.saveWorkbenchSession).mock.calls[0][0]
    for (const workspace of ['w2', 'w3', 'w4']) {
      view.rerender({ value: location(workspace) })
      await tick()
    }
    const restoring = readWorkbenchSession()
    await act(() => vi.advanceTimersByTimeAsync(1000))
    expect(await restoring).toMatchObject({ session: { workspace_id: 'w4' } })
    expect(api.workbenchSession).not.toHaveBeenCalled()
    await act(() => vi.advanceTimersByTimeAsync(4000))
    await waitForWorkbenchSessionWrites()
    expect(api.saveWorkbenchSession).toHaveBeenCalledTimes(2)
    const last = vi.mocked(api.saveWorkbenchSession).mock.calls[1][0]
    expect(last).toMatchObject({ workspace_id: 'w4', writer_id: first.writer_id })
    expect(last.sequence).toBeGreaterThan(first.sequence!)
    view.unmount()
  })
})
