/// <reference types="node" />
import { readFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'
import { readAppearance } from './appearance'

const bootSource = readFileSync(join(dirname(fileURLToPath(import.meta.url)), '../../public/boot.js'), 'utf8')

// boot.js 只读外观设置并画首帧；这里只关心外观部分，替换掉它挂的计时与监听。
function bootAt(path: string) {
  window.history.replaceState(null, '', path)
  const timers = window.setTimeout
  const listen = window.addEventListener
  window.setTimeout = (() => 0) as unknown as typeof window.setTimeout
  window.addEventListener = (() => {}) as typeof window.addEventListener
  try { new Function(bootSource)() } finally {
    window.setTimeout = timers
    window.addEventListener = listen
  }
  return document.documentElement
}

beforeEach(() => {
  localStorage.clear()
  document.head.innerHTML = '<meta name="theme-color" content="#f7f8fa">'
})
afterEach(() => window.history.replaceState(null, '', '/'))

describe('appearance defaults', () => {
  it('starts both the site and the workbench light until the user picks something', () => {
    expect(readAppearance('site')).toBe('light')
    expect(readAppearance('workbench')).toBe('light')
    const root = bootAt('/h/host')
    expect(root.dataset.appearanceScope).toBe('workbench')
    expect(root.dataset.appearance).toBe('light')
    expect(root.style.backgroundColor).toBe('rgb(247, 248, 250)')
    expect(document.querySelector('meta[name="theme-color"]')).toHaveAttribute('content', '#f7f8fa')
  })

  it('keeps a saved workbench choice, including dark', () => {
    localStorage.setItem('herdrx.workbench-appearance.v1', 'dark')
    expect(readAppearance('workbench')).toBe('dark')
    const root = bootAt('/h/host')
    expect(root.dataset.appearance).toBe('dark')
    expect(document.querySelector('meta[name="theme-color"]')).toHaveAttribute('content', '#142c3c')
    localStorage.setItem('herdrx.workbench-appearance.v1', 'solarized-light')
    expect(bootAt('/h/host').dataset.appearance).toBe('solarized-light')
  })
})
