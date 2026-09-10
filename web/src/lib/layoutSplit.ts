import type { CSSProperties } from 'react'
import type { Layout, Rect } from '../types'

export type LayoutSplit = Layout['splits'][number]

export function splitPathFromId(id: string): boolean[] {
  const parts = id.split('_')
  const token = parts.slice(2).join('_')
  if (!token || token === 'root') return []
  return [...token].map((ch) => ch === '1')
}

export function clampSplitRatio(ratio: number): number {
  if (!Number.isFinite(ratio)) return 0.5
  return Math.min(0.9, Math.max(0.1, ratio))
}

export function splitHandleStyle(layout: Layout, split: LayoutSplit, previewRatio?: number): CSSProperties {
  const area = layout.area
  if (!area.width || !area.height) return { display: 'none' }
  const x0 = ((split.rect.x - area.x) / area.width) * 100
  const y0 = ((split.rect.y - area.y) / area.height) * 100
  const width = (split.rect.width / area.width) * 100
  const height = (split.rect.height / area.height) * 100
  const ratio = clampSplitRatio(previewRatio ?? split.ratio)
  if (split.direction === 'right') {
    return { left: `${x0 + width * ratio}%`, top: `${y0}%`, height: `${height}%`, width: 6 }
  }
  return { left: `${x0}%`, top: `${y0 + height * ratio}%`, width: `${width}%`, height: 6 }
}

export function ratioFromPointer(split: LayoutSplit, area: Rect, surface: DOMRect, clientX: number, clientY: number): number {
  const scaleX = surface.width / area.width
  const scaleY = surface.height / area.height
  if (split.direction === 'right') {
    const origin = surface.left + (split.rect.x - area.x) * scaleX
    const size = split.rect.width * scaleX
    if (size <= 0) return clampSplitRatio(split.ratio)
    return clampSplitRatio((clientX - origin) / size)
  }
  const origin = surface.top + (split.rect.y - area.y) * scaleY
  const size = split.rect.height * scaleY
  if (size <= 0) return clampSplitRatio(split.ratio)
  return clampSplitRatio((clientY - origin) / size)
}

export const RESIZE_DIRECTIONS: Record<string, 'left' | 'down' | 'up' | 'right'> = {
  h: 'left',
  j: 'down',
  k: 'up',
  l: 'right',
}

export function resizeModeBarItems(): string[] {
  return ['hjkl 调整', 'esc/enter 退出']
}
