import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { AgentLauncher } from './LauncherMenu'
import { api } from '../lib/api'

vi.mock('../lib/api', () => ({
  api: {
    launchers: vi.fn().mockResolvedValue({ launchers: [] }),
    saveLaunchers: vi.fn(async (launchers: unknown) => ({ launchers })),
  },
}))

const launch = vi.fn()

describe('agent launcher', () => {
  beforeEach(() => {
    launch.mockClear()
    vi.mocked(api.launchers).mockReset()
    vi.mocked(api.launchers).mockResolvedValue({ launchers: [] })
    vi.mocked(api.saveLaunchers).mockClear()
  })

  it('starts Claude from the menu and from the 2 key', async () => {
    const view = render(<AgentLauncher x={10} y={10} onClose={() => {}} onLaunch={launch}/>)
    fireEvent.click(await screen.findByRole('menuitem', { name: 'Claude Code' }))
    expect(launch).toHaveBeenCalledWith({ id: 'claude', name: 'Claude Code', command: 'claude' })
    view.unmount()
    launch.mockClear()
    render(<AgentLauncher x={10} y={40} onClose={() => {}} onLaunch={launch}/>)
    fireEvent.keyDown(window, { key: '2' })
    expect(launch).toHaveBeenCalledTimes(1)
    expect(launch).toHaveBeenCalledWith({ id: 'claude', name: 'Claude Code', command: 'claude' })
  })

  it('starts a shell without a command and lists a saved command', async () => {
    vi.mocked(api.launchers).mockResolvedValue({ launchers: [{ id: 'review', name: '审查', command: 'codex review' }] })
    render(<AgentLauncher x={10} y={10} onClose={() => {}} onLaunch={launch}/>)
    fireEvent.click(await screen.findByRole('menuitem', { name: '空终端' }))
    expect(launch).toHaveBeenCalledWith({ id: 'shell', name: '空终端', command: '' })
    expect(await screen.findByRole('menuitem', { name: '审查' })).toBeInTheDocument()
  })

  it('saves a custom row and refuses a command with a newline', async () => {
    render(<AgentLauncher x={10} y={10} onClose={() => {}} onLaunch={launch}/>)
    fireEvent.click(await screen.findByRole('menuitem', { name: '编辑启动项…' }))
    fireEvent.change(screen.getByRole('textbox', { name: '启动项名称' }), { target: { value: '审查' } })
    fireEvent.change(screen.getByRole('textbox', { name: '启动命令' }), { target: { value: 'codex review' } })
    fireEvent.click(screen.getByRole('button', { name: '添加' }))
    await waitFor(() => expect(api.saveLaunchers).toHaveBeenCalled())
    const saved = vi.mocked(api.saveLaunchers).mock.calls[0][0]
    expect(saved).toMatchObject([{ name: '审查', command: 'codex review' }])

    fireEvent.change(screen.getByRole('textbox', { name: '启动项 JSON' }), { target: { value: '{"launchers":[{"id":"bad","name":"坏","command":"echo\\nhi"}]}' } })
    fireEvent.click(screen.getByRole('button', { name: '导入 JSON' }))
    expect(await screen.findByRole('alert')).toHaveTextContent('一行')
    expect(api.saveLaunchers).toHaveBeenCalledTimes(1)
  })
})
