import { describe, expect, it } from 'vitest'
import type { Layout } from '../types'
import { clampSplitRatio, ratioFromPointer, splitHandleStyle, splitPathFromId } from './layoutSplit'

const layout: Layout = {
  workspace_id: 'w1',
  tab_id: 'w1:t1',
  focused_pane_id: 'w1:p1',
  zoomed: false,
  area: { x: 0, y: 0, width: 100, height: 40 },
  panes: [],
  splits: [
    { id: 'split_0_root', direction: 'right', ratio: 0.4, rect: { x: 0, y: 0, width: 100, height: 40 } },
    { id: 'split_1_0', direction: 'down', ratio: 0.5, rect: { x: 0, y: 0, width: 40, height: 40 } },
  ],
}

describe('layout split helpers', () => {
  it('decodes Herdr split ids into boolean paths', () => {
    expect(splitPathFromId('split_0_root')).toEqual([])
    expect(splitPathFromId('split_1_0')).toEqual([false])
    expect(splitPathFromId('split_2_01')).toEqual([false, true])
  })

  it('places a 6px handle on the split boundary', () => {
    const vertical = splitHandleStyle(layout, layout.splits[0])
    expect(vertical.left).toBe('40%')
    expect(vertical.width).toBe(6)
    const horizontal = splitHandleStyle(layout, layout.splits[1])
    expect(horizontal.top).toBe('50%')
    expect(horizontal.height).toBe(6)
  })

  it('computes a clamped ratio from pointer position', () => {
    const surface = { left: 0, top: 0, width: 200, height: 80 } as DOMRect
    expect(ratioFromPointer(layout.splits[0], layout.area, surface, 120, 0)).toBe(0.6)
    expect(clampSplitRatio(2)).toBe(0.9)
    expect(clampSplitRatio(-1)).toBe(0.1)
  })
})
