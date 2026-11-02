import { act, renderHook } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import type { WorkbenchClient } from '../workbench'
import type { ConsoleMode } from './claudeScreen'
import { MODE_CHANGE_TIMEOUT_MS, useConsoleControls } from './useConsoleControls'

function mount(call = vi.fn().mockResolvedValue({})) {
  const client = { call } as unknown as WorkbenchClient
  const view = renderHook((props: { mode: ConsoleMode | null }) => useConsoleControls({ client, paneID: 'p1', ...props }), { initialProps: { mode: 'bypass' } })
  return { call, view }
}

afterEach(() => {
  vi.useRealTimers()
})

describe('useConsoleControls', () => {
  it('interrupts with one Esc and explains an uncertain delivery without resending', async () => {
    const { call, view } = mount(vi.fn().mockResolvedValueOnce({}).mockRejectedValueOnce(new Error('请求超时')))
    await act(() => view.result.current.interrupt())
    expect(call).toHaveBeenCalledWith('pane.send_keys', { pane_id: 'p1', keys: ['esc'] })
    await expect(view.result.current.interrupt()).rejects.toThrow('没有确认 Esc 已送达（请求超时），不会自动重发')
    expect(call).toHaveBeenCalledTimes(2)
  })

  it('cycles the mode with one Shift+Tab and resolves once the mode line changes', async () => {
    vi.useFakeTimers()
    const { call, view } = mount()
    let settled = false
    let cycling: Promise<void> = Promise.resolve()
    await act(async () => { cycling = view.result.current.cycleMode().then(() => { settled = true }) })
    expect(call).toHaveBeenCalledWith('pane.send_keys', { pane_id: 'p1', keys: ['shift+tab'] })
    expect(settled).toBe(false)
    view.rerender({ mode: 'auto' })
    await act(() => cycling)
    expect(settled).toBe(true)
    expect(call).toHaveBeenCalledTimes(1)
  })

  it('accepts a mode change that arrives before the key press is acknowledged', async () => {
    let acknowledge: () => void = () => {}
    const { view } = mount(vi.fn(() => new Promise((resolve) => { acknowledge = () => resolve({}) })))
    let cycling: Promise<void> = Promise.resolve()
    act(() => { cycling = view.result.current.cycleMode() })
    view.rerender({ mode: 'auto' })
    acknowledge()
    await act(() => cycling)
  })

  it('reports no change after three seconds and does not press again', async () => {
    vi.useFakeTimers()
    const { call, view } = mount()
    let failure: unknown
    await act(async () => { void view.result.current.cycleMode().catch((error) => { failure = error }) })
    await act(async () => { await vi.advanceTimersByTimeAsync(MODE_CHANGE_TIMEOUT_MS - 1) })
    expect(failure).toBeUndefined()
    await act(async () => { await vi.advanceTimersByTimeAsync(1) })
    expect(String(failure)).toContain('已按 Shift+Tab，但 3 秒内没看到模式变化，不会再按')
    // 超时之后模式行才变：不算数，也不再按。
    view.rerender({ mode: 'auto' })
    await act(async () => { await vi.advanceTimersByTimeAsync(MODE_CHANGE_TIMEOUT_MS * 3) })
    expect(call).toHaveBeenCalledTimes(1)
  })
})
