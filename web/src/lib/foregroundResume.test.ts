import { describe, expect, it, vi } from 'vitest'
import { installForegroundResume, type ForegroundResumeEnvironment } from './foregroundResume'

function fakeEnvironment() {
  let now = 0
  let visibility: DocumentVisibilityState = 'visible'
  const doc = new EventTarget(), win = new EventTarget()
  const nav = { onLine: true }
  const timers = new Map<number, () => void>()
  let nextTimer = 1
  const env: ForegroundResumeEnvironment = {
    document: Object.defineProperty(doc, 'visibilityState', { get: () => visibility }) as unknown as ForegroundResumeEnvironment['document'],
    window: win as unknown as ForegroundResumeEnvironment['window'],
    navigator: nav,
    now: () => now,
    setInterval: (callback) => { const id = nextTimer++; timers.set(id, callback); return id },
    clearInterval: (id) => { timers.delete(id) },
  }
  return {
    env, nav, timers,
    advance(ms: number) { now += ms },
    // 模拟定时器按时或迟到地触发一次。
    tick(ms: number) { now += ms; for (const callback of timers.values()) callback() },
    setVisibility(next: DocumentVisibilityState) { visibility = next; doc.dispatchEvent(new Event('visibilitychange')) },
    window: win,
  }
}

describe('installForegroundResume', () => {
  it('fires on visible, pageshow and online, coalescing triggers within one second', () => {
    const fake = fakeEnvironment(), onResume = vi.fn()
    installForegroundResume(onResume, {}, fake.env)
    fake.setVisibility('hidden')
    expect(onResume).not.toHaveBeenCalled()
    fake.advance(10_000)
    fake.setVisibility('visible')
    fake.window.dispatchEvent(new Event('pageshow'))
    fake.window.dispatchEvent(new Event('online'))
    expect(onResume.mock.calls).toEqual([['visible']])
    fake.advance(1000)
    fake.window.dispatchEvent(new Event('pageshow'))
    expect(onResume.mock.calls).toEqual([['visible'], ['pageshow']])
    fake.advance(1000)
    fake.window.dispatchEvent(new Event('online'))
    expect(onResume.mock.calls.at(-1)).toEqual(['online'])
  })

  it('stays silent while the browser reports offline', () => {
    const fake = fakeEnvironment(), onResume = vi.fn()
    installForegroundResume(onResume, {}, fake.env)
    fake.nav.onLine = false
    fake.setVisibility('hidden')
    fake.setVisibility('visible')
    fake.tick(60_000)
    expect(onResume).not.toHaveBeenCalled()
    fake.nav.onLine = true
    fake.window.dispatchEvent(new Event('online'))
    expect(onResume).toHaveBeenCalledExactlyOnceWith('online')
  })

  it('detects a wake from sleep by a late timer tick, only while visible', () => {
    const fake = fakeEnvironment(), onResume = vi.fn()
    installForegroundResume(onResume, { tickMs: 5000, wakeGapMs: 25_000 }, fake.env)
    fake.tick(5000)
    fake.tick(29_000)
    expect(onResume).not.toHaveBeenCalled()
    fake.tick(31_000)
    expect(onResume).toHaveBeenCalledExactlyOnceWith('wake')
    // 后台不计时；回到前台从新的基准开始，不会把后台时长误判为睡眠。
    fake.setVisibility('hidden')
    expect(fake.timers.size).toBe(0)
    fake.advance(120_000)
    fake.setVisibility('visible')
    expect(onResume.mock.calls).toEqual([['wake'], ['visible']])
    fake.tick(5000)
    expect(onResume).toHaveBeenCalledTimes(2)
  })

  it('removes listeners and the timer on uninstall', () => {
    const fake = fakeEnvironment(), onResume = vi.fn()
    const uninstall = installForegroundResume(onResume, {}, fake.env)
    uninstall()
    expect(fake.timers.size).toBe(0)
    fake.setVisibility('hidden')
    fake.setVisibility('visible')
    fake.window.dispatchEvent(new Event('online'))
    expect(onResume).not.toHaveBeenCalled()
  })
})
