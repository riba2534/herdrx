import { act, renderHook } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { api } from './api'
import { useAgentSeen } from './useAgentSeen'

vi.mock('./api', () => ({ api: { markAgentSeen: vi.fn() } }))

let visibility: DocumentVisibilityState = 'visible'
function setVisibility(value: DocumentVisibilityState) {
  visibility = value
  document.dispatchEvent(new Event('visibilitychange'))
}

beforeEach(() => {
  vi.useFakeTimers()
  visibility = 'visible'
  vi.spyOn(document, 'visibilityState', 'get').mockImplementation(() => visibility)
  vi.mocked(api.markAgentSeen).mockResolvedValue({ ok: true })
})
afterEach(() => { vi.useRealTimers(); vi.restoreAllMocks(); vi.mocked(api.markAgentSeen).mockReset() })

type Props = { panes: string[]; agents: { pane_id: string; state_change_seq?: number }[] }

describe('useAgentSeen', () => {
  it('marks only on-screen agents after a 1s debounce and never resends the same round', async () => {
    const { rerender } = renderHook<void, Props>(({ panes, agents }: Props) => useAgentSeen('h1', panes, agents), {
      initialProps: { panes: ['p1'], agents: [{ pane_id: 'p1', state_change_seq: 4 }, { pane_id: 'p2', state_change_seq: 9 }] },
    })
    await act(async () => { vi.advanceTimersByTime(999) })
    expect(api.markAgentSeen).not.toHaveBeenCalled()
    await act(async () => { vi.advanceTimersByTime(1) })
    expect(api.markAgentSeen).toHaveBeenCalledTimes(1)
    expect(api.markAgentSeen).toHaveBeenCalledWith('h1', 'p1', 4)

    // Same round again, from a new snapshot object: nothing is resent.
    rerender({ panes: ['p1'], agents: [{ pane_id: 'p1', state_change_seq: 4 }] })
    await act(async () => { vi.advanceTimersByTime(2000) })
    expect(api.markAgentSeen).toHaveBeenCalledTimes(1)

    // Another pane comes on screen: only that pane is marked, not p1 again.
    rerender({ panes: ['p1', 'p2'], agents: [{ pane_id: 'p1', state_change_seq: 4 }, { pane_id: 'p2', state_change_seq: 9 }] })
    await act(async () => { vi.advanceTimersByTime(1000) })
    expect(api.markAgentSeen).toHaveBeenCalledTimes(2)
    expect(api.markAgentSeen).toHaveBeenLastCalledWith('h1', 'p2', 9)

    // A new round of the visible agent is marked; a missing seq counts as 0.
    rerender({ panes: ['p1', 'p3'], agents: [{ pane_id: 'p1', state_change_seq: 5 }, { pane_id: 'p3' }] })
    await act(async () => { vi.advanceTimersByTime(1000) })
    expect(vi.mocked(api.markAgentSeen).mock.calls.slice(2)).toEqual([['h1', 'p1', 5], ['h1', 'p3', 0]])
  })

  it('waits while the page is hidden and marks once it is visible again', async () => {
    visibility = 'hidden'
    renderHook(() => useAgentSeen('h1', ['p1'], [{ pane_id: 'p1', state_change_seq: 2 }]))
    await act(async () => { vi.advanceTimersByTime(5000) })
    expect(api.markAgentSeen).not.toHaveBeenCalled()
    await act(async () => { setVisibility('visible') })
    await act(async () => { vi.advanceTimersByTime(1000) })
    expect(api.markAgentSeen).toHaveBeenCalledWith('h1', 'p1', 2)
  })

  it('cancels a pending mark when the page is hidden or the hook unmounts', async () => {
    const { unmount } = renderHook(() => useAgentSeen('h1', ['p1'], [{ pane_id: 'p1', state_change_seq: 2 }]))
    await act(async () => { vi.advanceTimersByTime(500); setVisibility('hidden') })
    await act(async () => { vi.advanceTimersByTime(2000) })
    unmount()
    await act(async () => { vi.advanceTimersByTime(2000) })
    expect(api.markAgentSeen).not.toHaveBeenCalled()
  })

  it('swallows failures and retries the round when the pane is seen again', async () => {
    vi.mocked(api.markAgentSeen).mockRejectedValueOnce(new Error('offline'))
    renderHook(() => useAgentSeen('h1', ['p1'], [{ pane_id: 'p1', state_change_seq: 3 }]))
    await act(async () => { vi.advanceTimersByTime(1000) })
    expect(api.markAgentSeen).toHaveBeenCalledTimes(1)
    await act(async () => { setVisibility('hidden') })
    await act(async () => { setVisibility('visible') })
    await act(async () => { vi.advanceTimersByTime(1000) })
    expect(api.markAgentSeen).toHaveBeenCalledTimes(2)
  })

  it('does nothing without an on-screen agent', async () => {
    renderHook(() => useAgentSeen('h1', ['p1'], [{ pane_id: 'p2', state_change_seq: 3 }]))
    await act(async () => { vi.advanceTimersByTime(3000) })
    expect(api.markAgentSeen).not.toHaveBeenCalled()
  })
})
