export interface TerminalTouchOptions {
  /** Positive pixels move toward newer content, matching WheelEvent.deltaY. */
  onScrollPixels: (deltaY: number, clientX: number, clientY: number) => void
  /** Discard unsent movement when a pinch, selection or stream change takes over. */
  onGestureCancel?: () => void
  getGeneration: () => number | string | null
  hasSelection?: () => boolean
  /** When false, native selection/zoom owns the pointer and capture handlers stand down. */
  enabled?: () => boolean
  /** Two-finger pinch inside the terminal. Without it the browser keeps its page zoom. */
  pinch?: TerminalPinchHandlers
}

export interface TerminalPinchHandlers {
  /** Return false to ignore this pinch; the page still does not zoom over the terminal. */
  start: (centerX: number, centerY: number) => boolean
  /** Scale relative to the finger distance when the pinch was recognized. */
  update: (scale: number) => void
  end: (scale: number) => void
  cancel: () => void
}

// Fingers must spread or close this far before a pinch counts, so a two-finger
// tap or a slightly uneven scroll never changes the font.
const PINCH_THRESHOLD = 18

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
  let pinch: { startDistance: number; distance: number; active: boolean } | null = null
  const previousTouchAction = viewport.style.touchAction
  const zoomed = () => (window.visualViewport?.scale || 1) > 1.01
  const active = () => options.enabled?.() !== false
  const updateTouchAction = () => {
    // Native horizontal panning and pinch remain available. When the page is
    // zoomed, the browser also owns vertical panning of the visual viewport.
    // A terminal that handles its own pinch keeps the browser from zooming the
    // page under the fingers; everything outside the terminal still zooms.
    viewport.style.touchAction = !active() || zoomed() ? 'auto' : options.pinch ? 'pan-x' : 'pan-x pinch-zoom'
  }
  const distance = (touches: TouchList) => Math.hypot(touches[0].clientX - touches[1].clientX, touches[0].clientY - touches[1].clientY)
  const endPinch = (commit: boolean) => {
    const current = pinch
    pinch = null
    if (!current?.active) return
    if (commit) options.pinch!.end(current.distance / current.startDistance)
    else options.pinch!.cancel()
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
    if (!active()) return
    // xterm's local touch handler cannot scroll Herdr's observe-only frames.
    // Stopping propagation does not cancel taps, selection, or native zoom.
    event.stopImmediatePropagation()
    if (event.touches.length !== 1) {
      cancel()
      blocked = true
      if (event.touches.length === 2 && options.pinch && !pinch && !zoomed() && !selected()) {
        const startDistance = distance(event.touches)
        if (startDistance > 0) pinch = { startDistance, distance: startDistance, active: false }
      } else if (event.touches.length > 2) endPinch(false)
      return
    }
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
    if (!active()) return
    event.stopImmediatePropagation()
    if (pinch && event.touches.length === 2) {
      pinch.distance = distance(event.touches)
      if (!pinch.active) {
        if (Math.abs(pinch.distance - pinch.startDistance) < PINCH_THRESHOLD) return
        const centerX = (event.touches[0].clientX + event.touches[1].clientX) / 2
        const centerY = (event.touches[0].clientY + event.touches[1].clientY) / 2
        if (!options.pinch!.start(centerX, centerY)) { pinch = null; return }
        // Measure the scale from here so the font does not jump by the threshold.
        pinch.active = true
        pinch.startDistance = pinch.distance
      }
      if (event.cancelable) event.preventDefault()
      options.pinch!.update(pinch.distance / pinch.startDistance)
      return
    }
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
    if (!active()) return
    event.stopImmediatePropagation()
    if (pinch && event.touches.length < 2) endPinch(event.type !== 'touchcancel')
    if (event.type === 'touchcancel') cancel()
    gesture = null
    // After a pinch, the remaining finger does not become a new scroll gesture.
    if (!event.touches.length) blocked = false
  }
  const contextMenu = () => { cancel(); endPinch(false) }
  // Safari's proprietary gesture events would otherwise zoom the page while
  // the terminal is already scaling its own font.
  const blockSafariZoom = (event: Event) => { if (options.pinch && active() && !zoomed()) event.preventDefault() }
  const resume = () => { gesture = null; blocked = false }
  const visibility = () => { if (document.hidden) { cancel(); endPinch(false) } else resume() }
  // The second finger can land on a toolbar outside this viewport. Observe
  // the document without canceling its events, so the whole pinch stays native
  // and its final lift cannot leave the next single-finger gesture blocked.
  const otherStart = (event: TouchEvent) => {
    if (event.touches.length > 1 && gesture) { cancel(); blocked = true }
  }
  const otherEnd = (event: TouchEvent) => { if (!event.touches.length) blocked = false }
  updateTouchAction()
  const classObserver = typeof MutationObserver === 'undefined' ? null : new MutationObserver(updateTouchAction)
  classObserver?.observe(viewport, { attributes: true, attributeFilter: ['class'] })
  viewport.addEventListener('touchstart', start, { capture: true, passive: true })
  viewport.addEventListener('touchmove', move, { capture: true, passive: false })
  viewport.addEventListener('touchend', end, { capture: true, passive: true })
  viewport.addEventListener('touchcancel', end, { capture: true, passive: true })
  viewport.addEventListener('contextmenu', contextMenu, true)
  viewport.addEventListener('gesturestart', blockSafariZoom)
  viewport.addEventListener('gesturechange', blockSafariZoom)
  window.visualViewport?.addEventListener('resize', updateTouchAction)
  window.addEventListener('blur', cancel)
  window.addEventListener('focus', resume)
  document.addEventListener('visibilitychange', visibility)
  document.addEventListener('touchstart', otherStart, { capture: true, passive: true })
  document.addEventListener('touchend', otherEnd, { capture: true, passive: true })
  document.addEventListener('touchcancel', otherEnd, { capture: true, passive: true })
  return () => {
    cancel()
    endPinch(false)
    classObserver?.disconnect()
    viewport.style.touchAction = previousTouchAction
    viewport.removeEventListener('touchstart', start, true)
    viewport.removeEventListener('touchmove', move, true)
    viewport.removeEventListener('touchend', end, true)
    viewport.removeEventListener('touchcancel', end, true)
    viewport.removeEventListener('contextmenu', contextMenu, true)
    viewport.removeEventListener('gesturestart', blockSafariZoom)
    viewport.removeEventListener('gesturechange', blockSafariZoom)
    window.visualViewport?.removeEventListener('resize', updateTouchAction)
    window.removeEventListener('blur', cancel)
    window.removeEventListener('focus', resume)
    document.removeEventListener('visibilitychange', visibility)
    document.removeEventListener('touchstart', otherStart, true)
    document.removeEventListener('touchend', otherEnd, true)
    document.removeEventListener('touchcancel', otherEnd, true)
  }
}
