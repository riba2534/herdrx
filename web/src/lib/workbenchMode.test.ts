import { afterEach, describe, expect, it } from 'vitest'
import { DEFAULT_WORKBENCH_MODE, readWorkbenchMode, resetWorkbenchModeForTest, subscribeWorkbenchMode, WORKBENCH_MODE_STORAGE_KEY, writeWorkbenchMode } from './workbenchMode'

afterEach(() => {
  localStorage.clear()
  resetWorkbenchModeForTest()
  window.history.replaceState(null, '', '/')
})

describe('workbench mode', () => {
  it('defaults to the Web UI mode', () => {
    expect(DEFAULT_WORKBENCH_MODE).toBe('ui')
    expect(readWorkbenchMode()).toBe('ui')
  })

  it('persists an explicit choice per device', () => {
    writeWorkbenchMode('shell')
    expect(localStorage.getItem(WORKBENCH_MODE_STORAGE_KEY)).toBe('shell')
    resetWorkbenchModeForTest()
    expect(readWorkbenchMode()).toBe('shell')
  })

  it('ignores an unknown stored value', () => {
    localStorage.setItem(WORKBENCH_MODE_STORAGE_KEY, 'terminal')
    expect(readWorkbenchMode()).toBe('ui')
  })

  it('lets ?mode= override for this visit without storing it', () => {
    window.history.replaceState(null, '', '/h/demo?mode=shell')
    expect(readWorkbenchMode()).toBe('shell')
    expect(localStorage.getItem(WORKBENCH_MODE_STORAGE_KEY)).toBeNull()
  })

  it('drops the address override once the user picks a mode', () => {
    window.history.replaceState(null, '', '/h/demo?mode=shell&pane=w1%3Ap1')
    writeWorkbenchMode('ui')
    expect(window.location.search).toBe('?pane=w1%3Ap1')
    expect(readWorkbenchMode()).toBe('ui')
  })

  it('notifies subscribers on change', () => {
    let calls = 0
    const off = subscribeWorkbenchMode(() => { calls++ })
    writeWorkbenchMode('shell')
    off()
    writeWorkbenchMode('ui')
    expect(calls).toBe(1)
  })
})
