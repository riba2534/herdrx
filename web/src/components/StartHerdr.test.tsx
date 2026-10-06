import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { api } from '../lib/api'
import { herdrStartFeedback, StartHerdrButton } from './StartHerdr'
import type { Host } from '../types'

const ssh: Host = { id: 'h1', name: 'SSH', transport: 'ssh', username: 'dev' } as Host

afterEach(() => { vi.restoreAllMocks() })

describe('herdrStartFeedback', () => {
  it('explains each outcome and the next step', () => {
    expect(herdrStartFeedback({ status: 'started', method: 'systemd', linger: 'yes' })).toMatchObject({ tone: 'ok', text: expect.stringContaining('独立用户服务') })
    expect(herdrStartFeedback({ status: 'started', method: 'setsid', linger: 'no' }, 'dev').text).toContain('sudo loginctl enable-linger dev')
    expect(herdrStartFeedback({ status: 'started', method: 'setsid', linger: 'unknown' }).tone).toBe('ok')
    expect(herdrStartFeedback({ status: 'running' }).text).toContain('没有重复启动')
    expect(herdrStartFeedback({ status: 'missing' })).toMatchObject({ tone: 'error', text: expect.stringContaining('找不到 herdr') })
    expect(herdrStartFeedback({ status: 'failed', method: 'setsid', linger: 'no' }).text).toContain('herdr status server')
  })
})

describe('StartHerdrButton', () => {
  it('renders only for SSH hosts', () => {
    const { container } = render(<StartHerdrButton host={{ ...ssh, transport: 'tailcat' }}/>)
    expect(container).toBeEmptyDOMElement()
  })

  it('starts Herdr once per click and reconnects after a start', async () => {
    const start = vi.spyOn(api, 'startHerdr').mockResolvedValue({ result: { status: 'started', method: 'systemd', linger: 'yes' } })
    const onStarted = vi.fn()
    render(<StartHerdrButton host={ssh} onStarted={onStarted}/>)
    fireEvent.click(screen.getByRole('button', { name: /在远程主机启动 Herdr/ }))
    await waitFor(() => expect(onStarted).toHaveBeenCalledExactlyOnceWith(expect.objectContaining({ tone: 'ok', text: expect.stringContaining('独立用户服务') })))
    expect(start).toHaveBeenCalledExactlyOnceWith('h1')
    expect(screen.getByRole('status')).toHaveTextContent('已在远程主机启动 Herdr')
  })

  it('shows failures without reconnecting', async () => {
    vi.spyOn(api, 'startHerdr').mockRejectedValue(new Error('无法在远程主机启动 Herdr：ssh: unexpected EOF'))
    const onStarted = vi.fn()
    render(<StartHerdrButton host={ssh} onStarted={onStarted}/>)
    fireEvent.click(screen.getByRole('button', { name: /在远程主机启动 Herdr/ }))
    expect(await screen.findByRole('alert')).toHaveTextContent('ssh: unexpected EOF')
    expect(onStarted).not.toHaveBeenCalled()
  })
})
