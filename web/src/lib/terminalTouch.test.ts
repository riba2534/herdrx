import { afterEach, describe, expect, it, vi } from 'vitest'
import { attachTerminalTouch } from './terminalTouch'

const cleanups: Array<() => void> = []
afterEach(() => { cleanups.splice(0).forEach((cleanup) => cleanup()); document.body.replaceChildren(); vi.restoreAllMocks() })

function fixture(height = 200, contentHeight = height) {
  const viewport = document.createElement('div')
  document.body.appendChild(viewport)
  Object.defineProperties(viewport, { clientHeight: { value: height }, scrollHeight: { value: contentHeight } })
  const onScrollPixels = vi.fn()
  const onGestureCancel = vi.fn()
  const state = { generation: 1, selection: false }
  cleanups.push(attachTerminalTouch(viewport, {
    onScrollPixels, onGestureCancel, getGeneration: () => state.generation, hasSelection: () => state.selection,
  }))
  const touch = (type: string, points: Array<[number, number, number?]>, cancelable = true) => {
    const event = new Event(type, { bubbles: true, cancelable })
    Object.defineProperty(event, 'touches', { value: points.map(([x, y, identifier = 1]) => ({ clientX: x, clientY: y, identifier })) })
    viewport.dispatchEvent(event)
    return event
  }
  return { viewport, onScrollPixels, onGestureCancel, touch, state }
}

describe('terminal finger scrolling', () => {
  it('tracks both finger directions with wheel-compatible pixels and cell coordinates', () => {
    const { onScrollPixels, touch } = fixture()
    expect(touch('touchstart', [[50, 100]]).defaultPrevented).toBe(false)
    expect(touch('touchmove', [[50, 103]]).defaultPrevented).toBe(false)
    expect(touch('touchmove', [[50, 120]]).defaultPrevented).toBe(true)
    touch('touchmove', [[52, 140]])
    touch('touchmove', [[53, 110]])
    expect(onScrollPixels.mock.calls).toEqual([[-14, 50, 120], [-20, 52, 140], [30, 53, 110]])
    touch('touchend', [])
    touch('touchmove', [[50, 10]])
    expect(onScrollPixels).toHaveBeenCalledTimes(3)
  })

  it('pans the enlarged frame and transfers only overflow to remote scrolling at either edge', () => {
    const { viewport, onScrollPixels, touch } = fixture(200, 400)
    viewport.scrollTop = 20
    touch('touchstart', [[50, 100]])
    touch('touchmove', [[50, 120]])
    expect(viewport.scrollTop).toBe(6)
    expect(onScrollPixels).not.toHaveBeenCalled()
    touch('touchmove', [[50, 150]])
    expect(viewport.scrollTop).toBe(0)
    expect(onScrollPixels).toHaveBeenLastCalledWith(-24, 50, 150)
    touch('touchmove', [[50, -80]])
    expect(viewport.scrollTop).toBe(200)
    expect(onScrollPixels).toHaveBeenLastCalledWith(30, 50, -80)
  })

  it('preserves native horizontal movement, taps and both fingers of a pinch', () => {
    const { viewport, onScrollPixels, touch } = fixture()
    expect(viewport.style.touchAction).toBe('pan-x pinch-zoom')
    touch('touchstart', [[100, 100]])
    expect(touch('touchmove', [[70, 105]]).defaultPrevented).toBe(false)
    expect(touch('touchmove', [[70, 160]]).defaultPrevented).toBe(false)
    touch('touchend', [])
    touch('touchstart', [[100, 100]])
    expect(touch('touchstart', [[100, 100], [120, 100, 2]]).defaultPrevented).toBe(false)
    expect(touch('touchmove', [[90, 100], [130, 100, 2]]).defaultPrevented).toBe(false)
    touch('touchend', [[90, 100]])
    touch('touchmove', [[90, 150]])
    expect(onScrollPixels).not.toHaveBeenCalled()
    touch('touchend', [])
    touch('touchstart', [[100, 100]])
    touch('touchmove', [[100, 120]])
    expect(onScrollPixels).toHaveBeenCalledTimes(1)
  })

  it('cancels on stream replacement, touchcancel and non-cancelable browser gestures', () => {
    const { onScrollPixels, onGestureCancel, touch, state } = fixture()
    touch('touchstart', [[50, 100]])
    state.generation++
    touch('touchmove', [[50, 120]])
    touch('touchend', [])
    touch('touchstart', [[50, 100]])
    touch('touchcancel', [])
    touch('touchmove', [[50, 120]])
    touch('touchstart', [[50, 100]])
    touch('touchmove', [[50, 120]], false)
    touch('touchmove', [[50, 140]])
    expect(onScrollPixels).not.toHaveBeenCalled()
    expect(onGestureCancel).toHaveBeenCalledTimes(3)
  })

  it('drops unsent movement when a second finger takes over, but keeps the last frame on normal lift', () => {
    const { onGestureCancel, touch } = fixture()
    touch('touchstart', [[50, 100]])
    touch('touchmove', [[50, 120]])
    touch('touchend', [])
    expect(onGestureCancel).not.toHaveBeenCalled()
    touch('touchstart', [[50, 100]])
    touch('touchmove', [[50, 120]])
    touch('touchstart', [[50, 120], [100, 120, 2]])
    expect(onGestureCancel).toHaveBeenCalledTimes(1)
  })

  it('keeps fractional pan targets when the browser rounds scrollTop', () => {
    const { viewport, onScrollPixels, touch } = fixture(200, 400)
    let top = 0
    Object.defineProperty(viewport, 'scrollTop', { get: () => top, set: (value: number) => { top = Math.round(value) } })
    touch('touchstart', [[50, 100]])
    for (let i = 1; i <= 8; i++) touch('touchmove', [[50, 100 - i * 12.5]])
    expect(viewport.scrollTop).toBe(94)
    expect(onScrollPixels).not.toHaveBeenCalled()
  })

  it('accepts the first gesture after returning from another app', () => {
    const { onScrollPixels, touch } = fixture()
    window.dispatchEvent(new Event('blur'))
    touch('touchstart', [[50, 100]])
    touch('touchmove', [[50, 120]])
    expect(onScrollPixels).toHaveBeenCalledTimes(1)
    window.dispatchEvent(new Event('blur'))
    window.dispatchEvent(new Event('focus'))
    touch('touchstart', [[50, 100]])
    touch('touchmove', [[50, 120]])
    expect(onScrollPixels).toHaveBeenCalledTimes(2)
  })

  it('cancels when the second finger lands outside the terminal and resumes after the final outside lift', () => {
    const { onScrollPixels, onGestureCancel, touch } = fixture()
    const outside = (type: string, count: number) => {
      const event = new Event(type, { bubbles: true, cancelable: true })
      Object.defineProperty(event, 'touches', { value: Array.from({ length: count }, (_, i) => ({ identifier: i + 1, clientX: 100, clientY: 100 })) })
      document.body.dispatchEvent(event)
      return event
    }
    touch('touchstart', [[50, 100]])
    expect(outside('touchstart', 2).defaultPrevented).toBe(false)
    touch('touchend', [[100, 100, 2]])
    outside('touchend', 0)
    expect(onGestureCancel).toHaveBeenCalledTimes(1)
    touch('touchstart', [[50, 100]])
    touch('touchmove', [[50, 120]])
    expect(onScrollPixels).toHaveBeenCalledTimes(1)
  })

  it('leaves an existing terminal selection and long-press menu alone', () => {
    const { viewport, onScrollPixels, touch, state } = fixture()
    state.selection = true
    touch('touchstart', [[50, 100]])
    expect(touch('touchmove', [[50, 120]]).defaultPrevented).toBe(false)
    touch('touchend', [])
    state.selection = false
    touch('touchstart', [[50, 100]])
    viewport.dispatchEvent(new Event('contextmenu', { bubbles: true }))
    expect(touch('touchmove', [[50, 120]]).defaultPrevented).toBe(false)
    expect(onScrollPixels).not.toHaveBeenCalled()
  })
})
