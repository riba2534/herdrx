import { fireEvent, render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'

const standalone = vi.hoisted(() => ({ value: false }))
vi.mock('../lib/pwa', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../lib/pwa')>()
  return {
    ...actual,
    isStandalone: () => standalone.value,
    usePWA: () => ({ online: true, updateReady: false, error: '' }),
    applyUpdate: vi.fn(),
  }
})

import { PWAStatus } from './PWA'

afterEach(() => { standalone.value = false })

describe('PWA install prompt', () => {
  it('hides add-to-home-screen copy in a standalone window', () => {
    standalone.value = true
    render(<PWAStatus/>)
    fireEvent(window, new Event('beforeinstallprompt'))
    expect(screen.queryByRole('complementary', { name: '添加到主屏幕' })).not.toBeInTheDocument()
  })

  it('shows add-to-home-screen copy in a browser tab', () => {
    render(<PWAStatus/>)
    const event = new Event('beforeinstallprompt', { cancelable: true })
    Object.assign(event, { prompt: vi.fn() })
    fireEvent(window, event)
    expect(screen.getByRole('complementary', { name: '添加到主屏幕' })).toBeInTheDocument()
  })
})
