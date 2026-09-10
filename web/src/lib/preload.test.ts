import { describe, expect, it } from 'vitest'
import { shouldPreloadWorkbench } from './preload'

describe('shouldPreloadWorkbench', () => {
  it('preloads the workbench chunk on host routes', () => {
    expect(shouldPreloadWorkbench('/h/perf-host')).toBe(true)
    expect(shouldPreloadWorkbench('/h/w27%3Ap1')).toBe(true)
  })

  it('does not preload on site routes', () => {
    expect(shouldPreloadWorkbench('/')).toBe(false)
    expect(shouldPreloadWorkbench('/keys')).toBe(false)
    expect(shouldPreloadWorkbench('/admin')).toBe(false)
    expect(shouldPreloadWorkbench('/hosts')).toBe(false)
  })
})
