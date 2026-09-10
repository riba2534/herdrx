import { readFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

const bootSource = readFileSync(join(dirname(fileURLToPath(import.meta.url)), '../../public/boot.js'), 'utf8')
const windowListeners: Array<[string, EventListenerOrEventListenerObject, boolean | AddEventListenerOptions | undefined]> = []
const originalAddEventListener = window.addEventListener.bind(window)

function heading() {
  return document.querySelector('#root > main h1')?.textContent || ''
}

function runBoot() {
  new Function(bootSource)()
}

describe('boot.js fallback', () => {
  beforeEach(() => {
    vi.useFakeTimers()
    window.addEventListener = ((type: string, listener: EventListenerOrEventListenerObject, options?: boolean | AddEventListenerOptions) => {
      windowListeners.push([type, listener, options])
      originalAddEventListener(type, listener, options)
    }) as typeof window.addEventListener
    document.documentElement.innerHTML = '<head><meta name="theme-color" content="#f7f8fa"></head><body><div id="root"></div></body>'
    delete window.__herdrxBooted
    Object.defineProperty(document, 'readyState', { configurable: true, get: () => 'loading' })
  })

  afterEach(() => {
    for (const [type, listener, options] of windowListeners.splice(0)) {
      window.removeEventListener(type, listener, options)
    }
    window.addEventListener = originalAddEventListener as typeof window.addEventListener
    vi.useRealTimers()
    delete window.__herdrxBooted
  })

  it('does not offer cache cleanup while scripts are still downloading', () => {
    runBoot()
    vi.advanceTimersByTime(3000)
    expect(heading()).toBe('网络较慢，仍在加载…')
    expect(document.querySelector('#root button')).toBeNull()
  })

  it('shows a cache cleanup action only after load without a boot signal', () => {
    runBoot()
    vi.advanceTimersByTime(3000)
    Object.defineProperty(document, 'readyState', { configurable: true, get: () => 'complete' })
    window.dispatchEvent(new Event('load'))
    vi.advanceTimersByTime(7999)
    expect(heading()).toBe('网络较慢，仍在加载…')
    expect(document.querySelector('#root button')).toBeNull()
    vi.advanceTimersByTime(1)
    expect(heading()).toBe('资源加载失败')
    expect(document.querySelector('#root button')?.textContent).toBe('清理缓存并重新加载')
  })

  it('stays quiet when main.tsx marks the app as booted', () => {
    window.__herdrxBooted = true
    runBoot()
    Object.defineProperty(document, 'readyState', { configurable: true, get: () => 'complete' })
    window.dispatchEvent(new Event('load'))
    vi.advanceTimersByTime(20_000)
    expect(heading()).toBe('')
    expect(document.querySelector('#root button')).toBeNull()
  })

  it('shows the failure state as soon as a module script errors', () => {
    runBoot()
    const script = document.createElement('script')
    script.src = '/assets/index.js'
    document.body.append(script)
    const event = new Event('error', { bubbles: false, cancelable: false })
    Object.defineProperty(event, 'target', { value: script })
    window.dispatchEvent(event)
    expect(heading()).toBe('资源加载失败')
    expect(document.querySelector('#root button')).toBeTruthy()
  })
})
