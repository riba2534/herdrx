import '@testing-library/jest-dom/vitest'
import { cleanup } from '@testing-library/react'
import { afterEach } from 'vitest'

if (typeof window !== 'undefined') {
  if (!window.ResizeObserver) {
    window.ResizeObserver = class ResizeObserver {
      observe() {}
      unobserve() {}
      disconnect() {}
    } as unknown as typeof window.ResizeObserver
  }

  const createMemoryStorage = () => {
    const store = new Map<string, string>()
    return {
      getItem: (key: string) => store.get(key) ?? null,
      setItem: (key: string, value: string) => store.set(key, String(value)),
      removeItem: (key: string) => store.delete(key),
      clear: () => store.clear(),
      key: (index: number) => Array.from(store.keys())[index] ?? null,
      get length() {
        return store.size
      },
    }
  }

  if (!window.localStorage) {
    Object.defineProperty(window, 'localStorage', { value: createMemoryStorage() })
  }
  if (!window.sessionStorage) {
    Object.defineProperty(window, 'sessionStorage', { value: createMemoryStorage() })
  }
}

afterEach(cleanup)

HTMLElement.prototype.scrollIntoView ||= () => {}
HTMLElement.prototype.hasPointerCapture ||= () => false
HTMLElement.prototype.setPointerCapture ||= () => {}
HTMLElement.prototype.releasePointerCapture ||= () => {}

// jsdom has no top layer. nwsapi 2.2.27 delegates these native states back to
// Element.matches and recurses; Floating UI probes them when positioning portals.
// Keep real positioning/keyboard checks in the three-engine browser suite.
const elementMatches = Element.prototype.matches
Element.prototype.matches = function (selector: string) {
  if ([':modal', ':fullscreen', ':popover-open'].includes(selector)) return false
  return elementMatches.call(this, selector)
}
