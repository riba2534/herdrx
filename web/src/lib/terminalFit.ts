type FontMetrics = { width: number; height: number }
export type FontMeasure = (fontSize: number) => FontMetrics | null

type FitBounds = {
  width: number
  height: number
  cols: number
  rows: number
  dpr: number
  lineHeight: number
  letterSpacing: number
}

// Match the pinned xterm 5.5 DOM renderer's device-pixel cell rounding. Search
// against offscreen measurements, never by changing the visible terminal.
export function fittedTerminalFont(bounds: FitBounds, measure: FontMeasure, maxFontSize = 14): number | null {
  if (bounds.width <= 0 || bounds.height <= 0 || bounds.cols <= 0 || bounds.rows <= 0) return null
  let low = 100
  let high = Math.round(maxFontSize * 100)
  let best: number | null = null
  while (low <= high) {
    const candidate = Math.floor((low + high) / 2)
    const metrics = measure(candidate / 100)
    if (!metrics || metrics.width <= 0 || metrics.height <= 0) return null
    const width = Math.round((metrics.width * bounds.dpr + Math.round(bounds.letterSpacing)) * bounds.cols / bounds.dpr)
    const height = Math.round(Math.floor(Math.ceil(metrics.height * bounds.dpr) * bounds.lineHeight) * bounds.rows / bounds.dpr)
    if (width <= bounds.width && height <= bounds.height) {
      best = candidate / 100
      low = candidate + 1
    } else {
      high = candidate - 1
    }
  }
  return best
}

export function createFontMeasure(host: HTMLElement, fontFamily: string): { measure: FontMeasure; dispose: () => void } {
  let context: OffscreenCanvasRenderingContext2D | null = null
  try {
    const canvas = new OffscreenCanvas(100, 100)
    const candidate = canvas.getContext('2d')
    const metrics = candidate?.measureText('W')
    if (metrics && 'fontBoundingBoxAscent' in metrics && 'fontBoundingBoxDescent' in metrics) context = candidate
  } catch { /* Use the same hidden-DOM fallback as xterm on older browsers. */ }

  let probe: HTMLSpanElement | null = null
  if (!context) {
    probe = document.createElement('span')
    probe.textContent = 'W'.repeat(32)
    probe.setAttribute('aria-hidden', 'true')
    Object.assign(probe.style, { position: 'fixed', left: '-10000px', top: '0', visibility: 'hidden', pointerEvents: 'none', whiteSpace: 'pre', fontKerning: 'none', fontWeight: 'normal', lineHeight: 'normal', fontFamily })
    host.appendChild(probe)
  }
  const cache = new Map<number, FontMetrics>()
  return {
    measure: (fontSize) => {
      const cached = cache.get(fontSize)
      if (cached) return cached
      let metrics: FontMetrics
      if (context) {
        context.font = `${fontSize}px ${fontFamily}`
        const measured = context.measureText('W')
        metrics = { width: measured.width, height: measured.fontBoundingBoxAscent + measured.fontBoundingBoxDescent }
      } else {
        probe!.style.fontSize = `${fontSize}px`
        metrics = { width: probe!.offsetWidth / 32, height: probe!.offsetHeight }
      }
      if (metrics.width <= 0 || metrics.height <= 0) return null
      cache.set(fontSize, metrics)
      return metrics
    },
    dispose: () => { probe?.remove(); cache.clear() },
  }
}
