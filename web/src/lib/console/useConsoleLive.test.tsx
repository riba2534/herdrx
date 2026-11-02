import { act, renderHook } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import type { ScreenFrame } from '../ansiScreen'
import type { ScreenError, WorkbenchClient } from '../workbench'
import { CONSOLE_SCREEN_LINES } from './screenWatch'
import { EMPTY_CONSOLE_LIVE, useConsoleLive } from './useConsoleLive'
import working1 from './__fixtures__/working-1.ansi.txt?raw'
import questionMulti from './__fixtures__/question-multi.text.txt?raw'

type Watch = { paneID: string; lines: number; onFrame: (frame: ScreenFrame) => void; onError: (error: ScreenError) => void; stopped: boolean }

function fakeClient() {
  const watches: Watch[] = []
  const epochs = new Set<(epoch: number) => void>()
  const client = {
    watchScreen: vi.fn((paneID: string, lines: number, handlers: { onFrame: (frame: ScreenFrame) => void; onError: (error: ScreenError) => void }) => {
      const watch: Watch = { paneID, lines, ...handlers, stopped: false }
      watches.push(watch)
      return () => { watch.stopped = true }
    }),
    onEpoch: vi.fn((handler: (epoch: number) => void) => {
      epochs.add(handler)
      return () => epochs.delete(handler)
    }),
  }
  const live = () => watches.filter((watch) => !watch.stopped)
  const newEpoch = () => act(() => { for (const handler of epochs) handler(2) })
  return { client: client as unknown as WorkbenchClient, watches, live, epochs, newEpoch }
}

const full = (paneID: string, text: string, seq = 1): ScreenFrame => {
  const lines = text.split('\n')
  return { pane_id: paneID, gen: 1, seq, full: true, lines, total: lines.length }
}

function setVisibility(state: DocumentVisibilityState) {
  Object.defineProperty(document, 'visibilityState', { configurable: true, get: () => state })
  document.dispatchEvent(new Event('visibilitychange'))
}

afterEach(() => {
  setVisibility('visible')
  vi.restoreAllMocks()
})

describe('useConsoleLive', () => {
  it('does not watch while disabled', () => {
    const { client, watches } = fakeClient()
    const { result } = renderHook(() => useConsoleLive({ hostID: 'h', paneID: 'p1', client, enabled: false }))
    expect(watches).toHaveLength(0)
    expect(result.current).toBe(EMPTY_CONSOLE_LIVE)
  })

  it('parses the spinner, the mode and the preview from the screen frames', () => {
    const { client, watches } = fakeClient()
    const { result } = renderHook(() => useConsoleLive({ hostID: 'h', paneID: 'p1', client, enabled: true }))
    expect(watches).toHaveLength(1)
    expect(watches[0]).toMatchObject({ paneID: 'p1', lines: CONSOLE_SCREEN_LINES })
    act(() => watches[0].onFrame(full('p1', working1)))
    expect(result.current.spinner).toMatchObject({ verb: 'Wibbling', seconds: 15, tokens: 1200, thinking: true })
    expect(result.current.mode).toBe('bypass')
    expect(result.current.preview.at(-1)).toContain('Plan approved')

    // 增量帧：spinner 行换字形与计时。
    const index = working1.split('\n').findIndex((line) => line.includes('Wibbling'))
    act(() => watches[0].onFrame({ pane_id: 'p1', gen: 1, seq: 2, total: working1.split('\n').length, set: [[index, '✻ Wibbling… (16s · ↓ 1.3k tokens)']] }))
    expect(result.current.spinner).toMatchObject({ seconds: 16, tokens: 1300, thinking: false })
    // 只换了 spinner 字形：状态对象不变，使用方不重渲染。
    const before = result.current
    act(() => watches[0].onFrame({ pane_id: 'p1', gen: 1, seq: 3, total: working1.split('\n').length, set: [[index, '\x1b[0m\x1b[38;5;2m✢\x1b[0m Wibbling… (16s · ↓ 1.3k tokens)']] }))
    expect(result.current).toBe(before)
  })

  it('keeps the last mode while a dialog hides the mode line', () => {
    const { client, watches } = fakeClient()
    const { result } = renderHook(() => useConsoleLive({ hostID: 'h', paneID: 'p1', client, enabled: true }))
    act(() => watches[0].onFrame(full('p1', working1)))
    act(() => watches[0].onFrame(full('p1', questionMulti, 2)))
    expect(result.current.mode).toBe('bypass')
    expect(result.current.spinner).toBeNull()
    expect(result.current.preview.at(-1)).toContain('Esc to cancel')
  })

  it('shares one watch per pane and stops it after the last subscriber leaves', () => {
    const { client, watches } = fakeClient()
    const first = renderHook(() => useConsoleLive({ hostID: 'h', paneID: 'p1', client, enabled: true }))
    act(() => watches[0].onFrame(full('p1', working1)))
    const second = renderHook(() => useConsoleLive({ hostID: 'h', paneID: 'p1', client, enabled: true }))
    expect(watches).toHaveLength(1)
    // 后来的订阅者立刻拿到当前画面，不用等下一帧。
    expect(second.result.current.spinner?.verb).toBe('Wibbling')
    first.unmount()
    expect(watches[0].stopped).toBe(false)
    second.unmount()
    expect(watches[0].stopped).toBe(true)
    renderHook(() => useConsoleLive({ hostID: 'h', paneID: 'p1', client, enabled: true }))
    expect(watches).toHaveLength(2)
  })

  it('stops while the page is hidden and resumes when it is shown again', () => {
    const { client, watches, live } = fakeClient()
    renderHook(() => useConsoleLive({ hostID: 'h', paneID: 'p1', client, enabled: true }))
    expect(live()).toHaveLength(1)
    act(() => setVisibility('hidden'))
    expect(live()).toHaveLength(0)
    act(() => setVisibility('visible'))
    expect(live()).toHaveLength(1)
    expect(watches).toHaveLength(2)
  })

  it('unsubscribes when disabled and switches panes', () => {
    const { client, watches, live } = fakeClient()
    const view = renderHook((props: { paneID: string; enabled: boolean }) => useConsoleLive({ hostID: 'h', client, ...props }), { initialProps: { paneID: 'p1', enabled: true } })
    act(() => watches[0].onFrame(full('p1', working1)))
    view.rerender({ paneID: 'p2', enabled: true })
    expect(view.result.current).toBe(EMPTY_CONSOLE_LIVE)
    expect(live().map((watch) => watch.paneID)).toEqual(['p2'])
    view.rerender({ paneID: 'p2', enabled: false })
    expect(live()).toHaveLength(0)
    expect(view.result.current).toBe(EMPTY_CONSOLE_LIVE)
  })

  it('re-watches on a new connection and reports read errors until the next frame', () => {
    const { client, watches, live, epochs, newEpoch } = fakeClient()
    const view = renderHook(() => useConsoleLive({ hostID: 'h', paneID: 'p1', client, enabled: true }))
    act(() => watches[0].onError({ code: 'screen_unavailable', message: '主机连接尚未就绪' }))
    expect(view.result.current.error).toBe('主机连接尚未就绪')
    newEpoch()
    expect(watches[0].stopped).toBe(true)
    expect(live()).toHaveLength(1)
    act(() => live()[0].onFrame(full('p1', working1)))
    expect(view.result.current.error).toBeUndefined()
    expect(view.result.current.spinner?.verb).toBe('Wibbling')
    view.unmount()
    expect(epochs.size).toBe(0)
    expect(live()).toHaveLength(0)
  })
})
