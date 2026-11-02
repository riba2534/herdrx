import { fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import type { LaunchersResult, Snapshot, StartSessionInput } from '../../types'
import { api, APIError } from '../../lib/api'
import { launcherStorageKey, NewSessionDialog } from './NewSessionDialog'

vi.mock('../../lib/api', () => ({
  api: { launchers: vi.fn(), startSession: vi.fn() },
  APIError: class APIError extends Error {
    constructor(public status: number, public code: string, message: string, public details?: unknown) { super(message) }
  },
}))

const snapshot = {
  protocol: 1, version: 'test', focused_workspace_id: 'w1', focused_tab_id: 't1', focused_pane_id: 'p1',
  workspaces: [{ workspace_id: 'w1', label: 'alpha', number: 1, active_tab_id: 't1', agent_status: 'idle', focused: true, pane_count: 2, tab_count: 1 }],
  tabs: [], layouts: [], agents: [],
  panes: [
    { pane_id: 'p1', workspace_id: 'w1', tab_id: 't1', terminal_id: 'a', cwd: '/srv/app', agent_status: 'idle', focused: true, revision: 1 },
    { pane_id: 'p2', workspace_id: 'w1', tab_id: 't1', terminal_id: 'b', cwd: '/srv/app', agent_status: 'idle', focused: false, revision: 1 },
    { pane_id: 'p3', workspace_id: 'w1', tab_id: 't1', terminal_id: 'c', cwd: '/home/user/notes', agent_status: 'idle', focused: false, revision: 1 },
  ],
} as Snapshot
const launchers: LaunchersResult = { supported: true, launchers: [
  { name: 'ccplain', bypass: false, fullscreen: false, teammate: false },
  { name: 'xcc', bypass: true, fullscreen: true, teammate: true },
] }
const result = { workspace_id: 'w9', tab_id: 't9', pane_id: 'p9', session_id: 'session-uuid', launcher: 'xcc' }

function open(props: Partial<Parameters<typeof NewSessionDialog>[0]> = {}) {
  const handlers = { onClose: vi.fn(), onStarted: vi.fn(), onOpenInShell: vi.fn() }
  render(<NewSessionDialog hostID="host" snapshot={snapshot} workspaceID="w1" defaultCwd="/srv/app" {...handlers} {...props}/>)
  return handlers
}
const submit = () => fireEvent.click(screen.getByRole('button', { name: '新建会话' }))
const sentInput = (call = 0) => vi.mocked(api.startSession).mock.calls[call][1] as StartSessionInput

beforeEach(() => {
  localStorage.clear()
  vi.mocked(api.launchers).mockReset().mockResolvedValue(launchers)
  vi.mocked(api.startSession).mockReset().mockResolvedValue(result)
})

describe('NewSessionDialog', () => {
  it('lists the host launchers with their flags and defaults to the one used last on this host', async () => {
    localStorage.setItem(launcherStorageKey('host'), 'ccplain')
    open()
    const list = await screen.findByRole('radiogroup', { name: '启动器' })
    const xcc = within(list).getByRole('radio', { name: /xcc/ })
    expect(xcc).toHaveTextContent('免授权')
    expect(xcc).toHaveTextContent('全屏')
    expect(xcc).toHaveTextContent('队友')
    expect(within(list).getByRole('radio', { name: /ccplain/ })).toHaveAttribute('aria-checked', 'true')
    expect(within(list).getByRole('radio', { name: /ccplain/ })).not.toHaveTextContent('免授权')
  })

  it('prefers a bypass launcher when this host has no remembered choice, and remembers the started one', async () => {
    const handlers = open()
    expect(await screen.findByRole('radio', { name: /xcc/ })).toHaveAttribute('aria-checked', 'true')
    fireEvent.change(screen.getByLabelText('首条消息可选'), { target: { value: '先读 README' } })
    submit()
    await waitFor(() => expect(handlers.onStarted).toHaveBeenCalledTimes(1))
    const input = sentInput()
    expect(input).toEqual({ request_id: expect.stringMatching(/^[A-Za-z0-9_-]{8,64}$/), cwd: '/srv/app', launcher: 'xcc', target: { kind: 'workspace' }, label: undefined })
    expect(handlers.onStarted).toHaveBeenCalledWith(result, '先读 README', input.request_id)
    expect(localStorage.getItem(launcherStorageKey('host'))).toBe('xcc')
  })

  it('keeps the suggestions closed until asked, then offers the other directories', async () => {
    open()
    const cwd = screen.getByRole('combobox')
    expect(cwd).toHaveValue('/srv/app')
    expect(cwd).toHaveFocus()
    expect(screen.queryByRole('listbox')).not.toBeInTheDocument()
    fireEvent.click(cwd)
    expect(within(screen.getByRole('listbox')).getAllByRole('option').map((option) => option.textContent)).toEqual(['/home/user/notes'])
    fireEvent.change(cwd, { target: { value: '/srv' } })
    expect(within(screen.getByRole('listbox')).getAllByRole('option').map((option) => option.textContent)).toEqual(['/srv/app'])
  })

  it('suggests the snapshot directories once each and picks one with the keyboard', async () => {
    open({ defaultCwd: undefined })
    const cwd = screen.getByRole('combobox')
    fireEvent.change(cwd, { target: { value: '' } })
    const options = within(screen.getByRole('listbox', { name: '最近用过的目录' })).getAllByRole('option')
    expect(options.map((option) => option.textContent)).toEqual(['/srv/app', '/home/user/notes'])
    fireEvent.keyDown(cwd, { key: 'ArrowDown' })
    fireEvent.keyDown(cwd, { key: 'ArrowDown' })
    fireEvent.keyDown(cwd, { key: 'Enter' })
    expect(cwd).toHaveValue('/home/user/notes')
    expect(api.startSession).not.toHaveBeenCalled()
  })

  it('opens a tab in the current workspace when asked to', async () => {
    open()
    await screen.findByRole('radio', { name: /xcc/ })
    fireEvent.click(screen.getByRole('radio', { name: '在“alpha”里新建标签' }))
    fireEvent.change(screen.getByLabelText('标签可选'), { target: { value: 'review' } })
    submit()
    await waitFor(() => expect(api.startSession).toHaveBeenCalledTimes(1))
    expect(sentInput()).toMatchObject({ target: { kind: 'tab', workspace_id: 'w1' }, label: 'review' })
  })

  it('refuses a relative directory before calling the server', async () => {
    open()
    await screen.findByRole('radio', { name: /xcc/ })
    fireEvent.change(screen.getByRole('combobox'), { target: { value: 'srv/app' } })
    submit()
    expect(await screen.findByText('请填写远程主机上的绝对路径，以 / 开头。')).toBeInTheDocument()
    expect(api.startSession).not.toHaveBeenCalled()
  })

  it('falls back to an editable claude launcher on an old controlled host and says how to upgrade', async () => {
    vi.mocked(api.launchers).mockResolvedValue({ supported: false, reason: 'unsupported_agent', launchers: [] })
    open()
    const field = await screen.findByLabelText('启动器')
    expect(field).toHaveValue('claude')
    expect(screen.getByText(/herdrx update/)).toBeInTheDocument()
    expect(screen.queryByRole('radiogroup', { name: '启动器' })).not.toBeInTheDocument()
    submit()
    await waitFor(() => expect(sentInput().launcher).toBe('claude'))
  })

  it('explains an empty launcher list and a failed read, and reads again on request', async () => {
    vi.mocked(api.launchers).mockResolvedValueOnce({ supported: true, launchers: [] })
    open()
    expect(await screen.findByText(/没有在 zsh 里找到调用 claude 的 alias/)).toBeInTheDocument()
    vi.mocked(api.launchers).mockRejectedValueOnce(new Error('网络错误'))
    fireEvent.click(screen.getByRole('button', { name: '重新读取' }))
    expect(await screen.findByText(/读取启动器失败：网络错误/)).toBeInTheDocument()
    expect(screen.getByLabelText('启动器')).toHaveValue('claude')
    fireEvent.click(screen.getByRole('button', { name: '重新读取' }))
    expect(await screen.findByRole('radiogroup', { name: '启动器' })).toBeInTheDocument()
  })

  it('keeps the same request id when the network drops, so a retry cannot start a second session', async () => {
    vi.mocked(api.startSession).mockRejectedValueOnce(new TypeError('Failed to fetch'))
    const handlers = open()
    await screen.findByRole('radio', { name: /xcc/ })
    submit()
    expect(await screen.findByText(/不确定会话是否已经创建/)).toBeInTheDocument()
    submit()
    await waitFor(() => expect(handlers.onStarted).toHaveBeenCalled())
    expect(sentInput(1).request_id).toBe(sentInput(0).request_id)
  })

  it('uses a fresh request id after a definite rejection and names the unknown launcher', async () => {
    vi.mocked(api.startSession).mockRejectedValueOnce(new APIError(409, 'launcher_unknown', 'launcher not found'))
    open()
    await screen.findByRole('radio', { name: /xcc/ })
    submit()
    expect(await screen.findByText(/没有名为“xcc”的启动器/)).toBeInTheDocument()
    submit()
    await waitFor(() => expect(api.startSession).toHaveBeenCalledTimes(2))
    expect(sentInput(1).request_id).not.toBe(sentInput(0).request_id)
  })

  it.each([
    ['herdr_unavailable', /Herdr 没有响应/],
    ['workspace_not_found', /当前工作区已经不存在/],
    ['invalid_cwd', /目录必须是远程主机上的绝对路径/],
  ])('shows a Chinese next step for %s', async (code, text) => {
    vi.mocked(api.startSession).mockRejectedValueOnce(new APIError(400, code, 'x'))
    open()
    await screen.findByRole('radio', { name: /xcc/ })
    submit()
    expect(await screen.findByRole('alert')).toHaveTextContent(text)
  })

  it('offers to view the half-started pane in Shell when Claude could not be launched', async () => {
    vi.mocked(api.startSession).mockRejectedValueOnce(new APIError(502, 'start_failed', 'x', { code: 'start_failed', details: { workspace_id: 'w9', tab_id: 't9', pane_id: 'p9' } }))
    const handlers = open()
    await screen.findByRole('radio', { name: /xcc/ })
    submit()
    expect(await screen.findByText(/终端不会被关闭/)).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: '在 Shell 中查看' }))
    expect(handlers.onOpenInShell).toHaveBeenCalledWith({ workspace_id: 'w9', tab_id: 't9', pane_id: 'p9' })
    expect(handlers.onStarted).not.toHaveBeenCalled()
  })
})
