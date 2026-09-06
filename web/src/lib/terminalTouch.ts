export interface TerminalTouchOptions {
  /** Positive pixels move toward newer content, matching WheelEvent.deltaY. */
  onScrollPixels: (deltaY: number, clientX: number, clientY: number) => void
  /** Discard unsent movement when a pinch, selection or stream change takes over. */
  onGestureCancel?: () => void
  getGeneration: () => number | string | null
  hasSelection?: () => boolean
}

/** Keep single-finger vertical movement in the terminal, including at its edges. */
export function attachTerminalTouch(viewport: HTMLElement, options: TerminalTouchOptions): () => void {
  const threshold = 6
  let gesture: {
    identifier: number
    startX: number
    startY: number
    lastY: number
    panTop: number
    axis: 'pending' | 'vertical' | 'horizontal'
    generation: number | string | null
  } | null = null
  let blocked = false
  const previousTouchAction = viewport.style.touchAction
  const zoomed = () => (window.visualViewport?.scale || 1) > 1.01
  const updateTouchAction = () => {
    // Native horizontal panning and pinch remain available. When the page is
    // zoomed, the browser also owns vertical panning of the visual viewport.
    viewport.style.touchAction = zoomed() ? 'auto' : 'pan-x pinch-zoom'
  }
  const selected = () => {
    if (options.hasSelection?.()) return true
    const selection = window.getSelection()
    return !!selection && !selection.isCollapsed &&
      (viewport.contains(selection.anchorNode) || viewport.contains(selection.focusNode))
  }
  const cancel = () => {
    if (gesture) options.onGestureCancel?.()
    blocked = blocked || !!gesture
    gesture = null
  }
  const start = (event: TouchEvent) => {
    // xterm's local touch handler cannot scroll Herdr's observe-only frames.
    // Stopping propagation does not cancel taps, selection, or native zoom.
    event.stopImmediatePropagation()
    if (event.touches.length !== 1) { cancel(); blocked = true; return }
    if (blocked || zoomed() || selected()) return
    const target = event.target
    if (target instanceof Element && target.closest('a, button, input, textarea, select, [contenteditable="true"]')) return
    const touch = event.touches[0]
    gesture = {
      identifier: touch.identifier, startX: touch.clientX, startY: touch.clientY,
      lastY: touch.clientY, panTop: viewport.scrollTop, axis: 'pending', generation: options.getGeneration(),
    }
  }
  const move = (event: TouchEvent) => {
    event.stopImmediatePropagation()
    if (event.touches.length !== 1) { cancel(); blocked = true; return }
    if (!gesture || blocked) return
    if (gesture.generation !== options.getGeneration() || zoomed() || selected()) { cancel(); return }
    const touch = Array.from(event.touches).find((point) => point.identifier === gesture!.identifier)
    if (!touch) { cancel(); return }
    if (gesture.axis === 'pending') {
      const distanceX = Math.abs(touch.clientX - gesture.startX)
      const distanceY = Math.abs(touch.clientY - gesture.startY)
      if (Math.max(distanceX, distanceY) < threshold) return
      gesture.axis = distanceY >= distanceX ? 'vertical' : 'horizontal'
      // Spend the initial movement recognizing the gesture, without a jump.
      gesture.lastY = gesture.startY + Math.sign(touch.clientY - gesture.startY) * threshold
    }
    if (gesture.axis === 'horizontal') return
    // A browser-owned (non-cancelable) gesture must never also reach Herdr.
    if (!event.cancelable) { cancel(); return }
    event.preventDefault()
    const deltaY = gesture.lastY - touch.clientY
    gesture.lastY = touch.clientY
    if (!deltaY) return
    // Pan an enlarged terminal frame first, then pass only unconsumed pixels
    // to history/the fullscreen application. Direction stays finger-relative.
    const max = Math.max(0, viewport.scrollHeight - viewport.clientHeight)
    // Browsers may round scrollTop to whole device pixels. Keep the fractional
    // target between moves so small finger steps do not accumulate rounding drift.
    const before = Math.max(0, Math.min(max, Math.abs(viewport.scrollTop - gesture.panTop) > 1 ? viewport.scrollTop : gesture.panTop))
    const next = Math.max(0, Math.min(max, before + deltaY))
    gesture.panTop = next
    viewport.scrollTop = next
    const remaining = deltaY - (next - before)
    if (remaining) options.onScrollPixels(remaining, touch.clientX, touch.clientY)
  }
  const end = (event: TouchEvent) => {
    event.stopImmediatePropagation()
    if (event.type === 'touchcancel') cancel()
    gesture = null
    // After a pinch, the remaining finger does not become a new scroll gesture.
    if (!event.touches.length) blocked = false
  }
  const contextMenu = () => cancel()
  const resume = () => { gesture = null; blocked = false }
  const visibility = () => { if (document.hidden) cancel(); else resume() }
  // The second finger can land on a toolbar outside this viewport. Observe
  // the document without canceling its events, so the whole pinch stays native
  // and its final lift cannot leave the next single-finger gesture blocked.
  const otherStart = (event: TouchEvent) => {
    if (event.touches.length > 1 && gesture) { cancel(); blocked = true }
  }
  const otherEnd = (event: TouchEvent) => { if (!event.touches.length) blocked = false }
  updateTouchAction()
  viewport.addEventListener('touchstart', start, { capture: true, passive: true })
  viewport.addEventListener('touchmove', move, { capture: true, passive: false })
  viewport.addEventListener('touchend', end, { capture: true, passive: true })
  viewport.addEventListener('touchcancel', end, { capture: true, passive: true })
  viewport.addEventListener('contextmenu', contextMenu, true)
  window.visualViewport?.addEventListener('resize', updateTouchAction)
  window.addEventListener('blur', cancel)
  window.addEventListener('focus', resume)
  document.addEventListener('visibilitychange', visibility)
  document.addEventListener('touchstart', otherStart, { capture: true, passive: true })
  document.addEventListener('touchend', otherEnd, { capture: true, passive: true })
  document.addEventListener('touchcancel', otherEnd, { capture: true, passive: true })
  return () => {
    cancel()
    viewport.style.touchAction = previousTouchAction
    viewport.removeEventListener('touchstart', start, true)
    viewport.removeEventListener('touchmove', move, true)
    viewport.removeEventListener('touchend', end, true)
    viewport.removeEventListener('touchcancel', end, true)
    viewport.removeEventListener('contextmenu', contextMenu, true)
    window.visualViewport?.removeEventListener('resize', updateTouchAction)
    window.removeEventListener('blur', cancel)
    window.removeEventListener('focus', resume)
    document.removeEventListener('visibilitychange', visibility)
    document.removeEventListener('touchstart', otherStart, true)
    document.removeEventListener('touchend', otherEnd, true)
    document.removeEventListener('touchcancel', otherEnd, true)
  }
}
