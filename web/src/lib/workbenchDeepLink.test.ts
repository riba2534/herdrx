import { act, renderHook } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import type { Snapshot } from '../types'
import { findPaneTarget, focusPaneMessage, readPaneParam, usePaneDeepLink, workbenchPaneURL } from './workbenchDeepLink'

const snapshot = {
  workspaces: [], tabs: [], layouts: [], agents: [],
  panes: [{ pane_id: 'w2:p3', workspace_id: 'w2', tab_id: 't5' }],
} as unknown as Snapshot

afterEach(() => window.history.replaceState(null, '', '/'))

describe('workbench deep links', () => {
  it('builds and parses /h/<host>?pane=<pane>', () => {
    expect(workbenchPaneURL('host 1', 'w1:p1')).toBe('/h/host%201?pane=w1%3Ap1')
    expect(readPaneParam('?pane=w1%3Ap1')).toBe('w1:p1')
    expect(readPaneParam('')).toBe('')
    expect(readPaneParam(`?pane=${'p'.repeat(257)}`)).toBe('')
  })

  it('finds the workspace and tab of a pane', () => {
    expect(findPaneTarget(snapshot, 'w2:p3')).toEqual({ workspaceID: 'w2', tabID: 't5', paneID: 'w2:p3' })
    expect(findPaneTarget(snapshot, 'gone')).toBeNull()
  })

  it('accepts only well-formed FOCUS_PANE messages', () => {
    expect(focusPaneMessage({ type: 'FOCUS_PANE', pane: 'w1:p1' })).toBe('w1:p1')
    expect(focusPaneMessage({ type: 'OTHER', pane: 'w1:p1' })).toBe('')
    expect(focusPaneMessage({ type: 'FOCUS_PANE', pane: 3 })).toBe('')
    expect(focusPaneMessage(null)).toBe('')
  })

  it('focuses the pane after the first snapshot and removes only the pane parameter', () => {
    window.history.replaceState({ keep: 1 }, '', '/h/host-1?pane=w2%3Ap3&view=x#top')
    const onFocus = vi.fn()
    const onMissing = vi.fn()
    const { rerender } = renderHook(({ current }) => usePaneDeepLink(current, onFocus, onMissing), { initialProps: { current: null as Snapshot | null } })
    expect(onFocus).not.toHaveBeenCalled()
    expect(window.location.search).toContain('pane=')
    rerender({ current: snapshot })
    expect(onFocus).toHaveBeenCalledExactlyOnceWith({ workspaceID: 'w2', tabID: 't5', paneID: 'w2:p3' })
    expect(window.location.pathname + window.location.search + window.location.hash).toBe('/h/host-1?view=x#top')
    expect(window.history.state).toEqual({ keep: 1 })
    rerender({ current: { ...snapshot } })
    expect(onFocus).toHaveBeenCalledTimes(1)
    expect(onMissing).not.toHaveBeenCalled()
  })

  it('reports a pane that has been closed', () => {
    window.history.replaceState(null, '', '/h/host-1?pane=gone')
    const onFocus = vi.fn()
    const onMissing = vi.fn()
    renderHook(() => usePaneDeepLink(snapshot, onFocus, onMissing))
    expect(onMissing).toHaveBeenCalledTimes(1)
    expect(onFocus).not.toHaveBeenCalled()
    expect(window.location.search).toBe('')
  })

  it('follows FOCUS_PANE from the service worker and in-page navigation', () => {
    const worker = new EventTarget()
    Object.defineProperty(navigator, 'serviceWorker', { configurable: true, value: worker })
    try {
      const onFocus = vi.fn()
      renderHook(() => usePaneDeepLink(snapshot, onFocus, vi.fn()))
      expect(onFocus).not.toHaveBeenCalled()
      act(() => { worker.dispatchEvent(new MessageEvent('message', { data: { type: 'FOCUS_PANE', pane: 'w2:p3' } })) })
      act(() => { worker.dispatchEvent(new MessageEvent('message', { data: { type: 'FOCUS_PANE', pane: 'w2:p3' } })) })
      expect(onFocus).toHaveBeenCalledTimes(2)
      act(() => {
        window.history.pushState({}, '', '/h/host-1?pane=w2%3Ap3')
        window.dispatchEvent(new PopStateEvent('popstate'))
      })
      expect(onFocus).toHaveBeenCalledTimes(3)
    } finally {
      Reflect.deleteProperty(navigator, 'serviceWorker')
    }
  })
})
