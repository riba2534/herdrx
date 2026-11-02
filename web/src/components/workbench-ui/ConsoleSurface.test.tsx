import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import type { Pane } from '../../types'
import type { WorkbenchClient } from '../../lib/workbench'
import { api } from '../../lib/api'
import { clearComposerDrafts, readComposerDraft } from '../../lib/composerDrafts'
import { ConsoleSurface, type ConsoleSurfaceProps } from './ConsoleSurface'
import { queueFirstMessage, resetFirstMessagesForTest } from './firstMessage'

vi.mock('../../lib/api', () => ({
  api: { panePrompt: vi.fn(), paneDialog: vi.fn() },
  APIError: class APIError extends Error {
    constructor(public status: number, public code: string, message: string, public details?: unknown) { super(message) }
  },
  currentSessionID: () => 'console-session',
  onAuthEvent: () => () => {},
}))
// 对话视图和画面视图各有自己的测试；这里只确认控制台把哪个 pane 交给谁、带什么参数。
vi.mock('../ChatView', () => ({
  ChatView: (props: { pane: Pane; variant?: string; compact: boolean; onSwitchToTerminal: () => void }) => <div role="region" aria-label="对话视图" data-variant={props.variant} data-compact={String(props.compact)}>
    {props.pane.pane_id}<button onClick={props.onSwitchToTerminal}>对话里的切到终端</button>
  </div>,
}))
vi.mock('../ScreenView', () => ({
  ScreenView: (props: { pane: Pane; onSwitchToTerminal?: () => void }) => <div role="region" aria-label="画面视图" data-switch={String(Boolean(props.onSwitchToTerminal))}>{props.pane.pane_id}</div>,
}))

const pane = (id: string, patch: Partial<Pane> = {}): Pane => ({ pane_id: id, workspace_id: 'w1', tab_id: 't1', terminal_id: id, focused: false, revision: 1, agent_status: 'idle', ...patch })
const claude = pane('p1', { agent: 'claude', label: 'Claude 会话' })
const shell = pane('p2', { label: '构建', agent_status: 'working' })

function show(props: Partial<ConsoleSurfaceProps> = {}) {
  const handlers = { onSelectPane: vi.fn(), onOpenInShell: vi.fn(), onShell: vi.fn(), onNewSession: vi.fn() }
  const view = render(<ConsoleSurface hostID="host" client={{} as WorkbenchClient} panes={[claude, shell]} paneID="p1" compact={false} connected connectionEpoch={0} loading={false} showPanePicker {...handlers} {...props}/>)
  return { ...handlers, ...view }
}

beforeEach(() => {
  clearComposerDrafts()
  resetFirstMessagesForTest()
  vi.mocked(api.panePrompt).mockReset().mockResolvedValue({ delivered: 'sent' })
  vi.mocked(api.paneDialog).mockReset().mockResolvedValue({ dialog: null })
})

describe('ConsoleSurface', () => {
  it('shows a Claude pane as the console chat view, whose terminal switch opens it in Shell', () => {
    const { onOpenInShell } = show()
    const chat = screen.getByRole('region', { name: '对话视图' })
    expect(chat).toHaveAttribute('data-variant', 'console')
    expect(chat).toHaveTextContent('p1')
    expect(screen.queryByRole('region', { name: '画面视图' })).not.toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: '对话里的切到终端' }))
    expect(onOpenInShell).toHaveBeenCalledWith('p1')
  })

  it('shows another pane as a read-only screen with a command line and an Open in Shell button', () => {
    const { onOpenInShell } = show({ paneID: 'p2' })
    expect(screen.getByRole('region', { name: '画面视图' })).toHaveTextContent('p2')
    expect(screen.queryByRole('region', { name: '对话视图' })).not.toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: '在 Shell 中打开' }))
    expect(onOpenInShell).toHaveBeenCalledWith('p2')
  })

  it('sends the command line once through the guarded prompt endpoint', async () => {
    show({ paneID: 'p2' })
    const input = screen.getByRole('textbox', { name: '发送到终端的命令' })
    fireEvent.change(input, { target: { value: 'pnpm test' } })
    fireEvent.keyDown(input, { key: 'Enter' })
    fireEvent.keyDown(input, { key: 'Enter' })
    await waitFor(() => expect(api.panePrompt).toHaveBeenCalledTimes(1))
    expect(api.panePrompt).toHaveBeenCalledWith('host', 'p2', expect.objectContaining({ text: 'pnpm test', submit: true, client_msg_id: expect.any(String) }))
    await waitFor(() => expect(input).toHaveValue(''))
    // Shift+Enter 只换行，不发送。
    fireEvent.change(input, { target: { value: 'a' } })
    fireEvent.keyDown(input, { key: 'Enter', shiftKey: true })
    expect(api.panePrompt).toHaveBeenCalledTimes(1)
  })

  it('does not send while the host is disconnected', () => {
    show({ paneID: 'p2', connected: false })
    const input = screen.getByRole('textbox', { name: '发送到终端的命令' })
    fireEvent.change(input, { target: { value: 'ls' } })
    fireEvent.keyDown(input, { key: 'Enter' })
    expect(api.panePrompt).not.toHaveBeenCalled()
    expect(screen.getByRole('button', { name: '发送' })).toBeDisabled()
  })

  it('offers a pane picker with status dots when the tab has several panes', () => {
    const { onSelectPane, rerender } = show()
    const picker = screen.getByRole('navigation', { name: '选择终端' })
    expect(screen.getByRole('button', { name: /Claude 会话/ })).toHaveAttribute('aria-pressed', 'true')
    fireEvent.click(screen.getByRole('button', { name: /构建/ }))
    expect(onSelectPane).toHaveBeenCalledWith('p2')
    expect(picker.querySelectorAll('.status-dot')).toHaveLength(2)
    rerender(<ConsoleSurface hostID="host" client={{} as WorkbenchClient} panes={[claude]} paneID="p1" compact={false} connected connectionEpoch={0} loading={false} showPanePicker onSelectPane={onSelectPane} onOpenInShell={vi.fn()} onShell={vi.fn()} onNewSession={vi.fn()}/>)
    expect(screen.queryByRole('navigation', { name: '选择终端' })).not.toBeInTheDocument()
  })

  it('leaves the pane picker to the phone switcher', () => {
    show({ showPanePicker: false, compact: true })
    expect(screen.queryByRole('navigation', { name: '选择终端' })).not.toBeInTheDocument()
    expect(screen.getByRole('region', { name: '对话视图' })).toHaveAttribute('data-compact', 'true')
  })

  it('offers a new Claude session and Shell mode when there is no pane', () => {
    const { onNewSession, onShell } = show({ panes: [], paneID: '' })
    fireEvent.click(screen.getByRole('button', { name: '新建 Claude 会话' }))
    expect(onNewSession).toHaveBeenCalled()
    fireEvent.click(screen.getByRole('button', { name: '切换到 Shell 模式' }))
    expect(onShell).toHaveBeenCalled()
  })

  it('says a first message is waiting and lets the user take it back into the draft', () => {
    queueFirstMessage('host', 'p1', '先读 README', 'banner01')
    show()
    expect(screen.getByText(/首条消息会在 Claude 就绪/)).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: '取消自动发送' }))
    expect(screen.queryByText(/首条消息会在 Claude 就绪/)).not.toBeInTheDocument()
    expect(readComposerDraft('host', 'p1')).toBe('先读 README')
  })
})
