import { act, renderHook } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { Pane, PaneDialog } from '../../types'
import { api, APIError } from '../../lib/api'
import { clearComposerDrafts, readComposerDraft } from '../../lib/composerDrafts'
import { cancelFirstMessage, FIRST_MESSAGE_POLL_MS, FIRST_MESSAGE_WAIT_MS, firstMessageState, queueFirstMessage, resetFirstMessagesForTest, useFirstMessages } from './firstMessage'

vi.mock('../../lib/api', () => ({
  api: { paneDialog: vi.fn(), panePrompt: vi.fn() },
  APIError: class APIError extends Error {
    constructor(public status: number, public code: string, message: string, public details?: unknown) { super(message) }
  },
  currentSessionID: () => 'first-message-session',
  onAuthEvent: () => () => {},
}))

const pane = (patch: Partial<Pane> = {}): Pane => ({ pane_id: 'p1', workspace_id: 'w1', tab_id: 't1', terminal_id: 'term1', focused: true, revision: 1, agent_status: 'unknown', ...patch })
const trust: PaneDialog = { fingerprint: 'f', title: 'Do you trust this folder?', lines: [], options: [], kind: 'trust' }

async function flush() {
  await act(async () => { for (let i = 0; i < 5; i++) await Promise.resolve() })
}

function mount(initial: Pane[], connected = true) {
  const notify = vi.fn()
  const hook = renderHook(({ panes, connected }) => useFirstMessages('host', panes, connected, notify), { initialProps: { panes: initial, connected } })
  return { notify, update: (panes: Pane[], connected = true) => hook.rerender({ panes, connected }) }
}

beforeEach(() => {
  vi.useFakeTimers()
  resetFirstMessagesForTest()
  clearComposerDrafts()
  vi.mocked(api.paneDialog).mockReset().mockResolvedValue({ dialog: null })
  vi.mocked(api.panePrompt).mockReset().mockResolvedValue({ delivered: 'sent' })
})
afterEach(() => vi.useRealTimers())

describe('first message after starting a Claude session', () => {
  it('waits for an idle Claude with no dialog, then sends exactly once with the fixed message id', async () => {
    queueFirstMessage('host', 'p1', '先读一遍 README', 'abc12345')
    const { notify, update } = mount([pane()])
    await flush()
    expect(api.panePrompt).not.toHaveBeenCalled()

    update([pane({ agent: 'claude', agent_status: 'working' })])
    await flush()
    expect(api.panePrompt).not.toHaveBeenCalled()

    update([pane({ agent: 'claude', agent_status: 'idle' })])
    await flush()
    expect(api.paneDialog).toHaveBeenCalledWith('host', 'p1')
    expect(api.panePrompt).toHaveBeenCalledTimes(1)
    expect(api.panePrompt).toHaveBeenCalledWith('host', 'p1', { text: '先读一遍 README', submit: true, client_msg_id: 'first-abc12345' })
    expect(notify).toHaveBeenCalledWith({ tone: 'ok', text: '首条消息已发送给 Claude。' })
    expect(firstMessageState('host', 'p1')).toBeNull()

    update([pane({ agent: 'claude', agent_status: 'idle', revision: 2 })])
    await act(async () => { vi.advanceTimersByTime(FIRST_MESSAGE_POLL_MS * 3) })
    await flush()
    expect(api.panePrompt).toHaveBeenCalledTimes(1)
  })

  it('holds the message while the trust dialog is open and sends after the user answers it', async () => {
    vi.mocked(api.paneDialog).mockResolvedValue({ dialog: trust })
    queueFirstMessage('host', 'p1', 'hello', 'trust001')
    mount([pane({ agent: 'claude', agent_status: 'idle' })])
    await flush()
    await act(async () => { vi.advanceTimersByTime(FIRST_MESSAGE_POLL_MS) })
    await flush()
    expect(api.paneDialog).toHaveBeenCalledTimes(2)
    expect(api.panePrompt).not.toHaveBeenCalled()
    expect(firstMessageState('host', 'p1')).toBe('waiting')

    // 答完对话框后 Herdr 状态不一定变化：靠轮询发现。
    vi.mocked(api.paneDialog).mockResolvedValue({ dialog: null })
    await act(async () => { vi.advanceTimersByTime(FIRST_MESSAGE_POLL_MS) })
    await flush()
    expect(api.panePrompt).toHaveBeenCalledTimes(1)
  })

  it('treats a pane_dialog refusal as nothing sent and retries with the same id', async () => {
    vi.mocked(api.panePrompt).mockRejectedValueOnce(new APIError(409, 'pane_dialog', '终端正弹着一个选择对话框'))
    queueFirstMessage('host', 'p1', 'hello', 'retry0001')
    mount([pane({ agent: 'claude', agent_status: 'idle' })])
    await flush()
    expect(api.panePrompt).toHaveBeenCalledTimes(1)
    expect(firstMessageState('host', 'p1')).toBe('waiting')
    await act(async () => { vi.advanceTimersByTime(FIRST_MESSAGE_POLL_MS) })
    await flush()
    expect(api.panePrompt).toHaveBeenCalledTimes(2)
    expect(vi.mocked(api.panePrompt).mock.calls.map((call) => call[2].client_msg_id)).toEqual(['first-retry0001', 'first-retry0001'])
  })

  it('never resends an uncertain result: the text goes back to the pane draft with a notice', async () => {
    vi.mocked(api.panePrompt).mockRejectedValueOnce(new TypeError('Failed to fetch'))
    queueFirstMessage('host', 'p1', '整理一下变更', 'unknown1')
    const { notify } = mount([pane({ agent: 'claude', agent_status: 'idle' })])
    await flush()
    await act(async () => { vi.advanceTimersByTime(FIRST_MESSAGE_POLL_MS * 4) })
    await flush()
    expect(api.panePrompt).toHaveBeenCalledTimes(1)
    expect(readComposerDraft('host', 'p1')).toBe('整理一下变更')
    expect(notify).toHaveBeenCalledWith(expect.objectContaining({ tone: 'error', text: expect.stringContaining('不会自动重发') }))
  })

  it('also keeps a server-side failure in the draft without retrying', async () => {
    vi.mocked(api.panePrompt).mockRejectedValueOnce(new APIError(502, 'delivery_unknown', '正文已送入终端，但提交回车未确认'))
    queueFirstMessage('host', 'p1', 'go', 'unknown2')
    const { notify } = mount([pane({ agent: 'claude', agent_status: 'idle' })])
    await flush()
    await act(async () => { vi.advanceTimersByTime(FIRST_MESSAGE_POLL_MS * 2) })
    await flush()
    expect(api.panePrompt).toHaveBeenCalledTimes(1)
    expect(readComposerDraft('host', 'p1')).toBe('go')
    expect(notify.mock.calls[0][0].text).toContain('正文已送入终端')
  })

  it('gives up after the wait limit when Claude never becomes ready, without typing into a shell', async () => {
    queueFirstMessage('host', 'p1', 'never sent', 'timeout1')
    const { notify } = mount([pane({ agent_status: 'idle' })])
    await act(async () => { vi.advanceTimersByTime(FIRST_MESSAGE_WAIT_MS + FIRST_MESSAGE_POLL_MS) })
    await flush()
    expect(api.panePrompt).not.toHaveBeenCalled()
    expect(readComposerDraft('host', 'p1')).toBe('never sent')
    expect(notify).toHaveBeenCalledWith(expect.objectContaining({ tone: 'warn' }))
    expect(firstMessageState('host', 'p1')).toBeNull()
  })

  it('does not try while the host is disconnected', async () => {
    queueFirstMessage('host', 'p1', 'later', 'offline1')
    const { update } = mount([pane({ agent: 'claude', agent_status: 'idle' })], false)
    await act(async () => { vi.advanceTimersByTime(FIRST_MESSAGE_POLL_MS * 2) })
    await flush()
    expect(api.paneDialog).not.toHaveBeenCalled()
    expect(firstMessageState('host', 'p1')).toBe('waiting')
    update([pane({ agent: 'claude', agent_status: 'idle' })], true)
    await flush()
    expect(api.panePrompt).toHaveBeenCalledTimes(1)
  })

  it('puts the text back into the draft when the user cancels', () => {
    queueFirstMessage('host', 'p1', 'cancel me', 'cancel01')
    expect(cancelFirstMessage('host', 'p1')).toBe(true)
    expect(readComposerDraft('host', 'p1')).toBe('cancel me')
    expect(firstMessageState('host', 'p1')).toBeNull()
  })

  it('ignores an empty first message', () => {
    queueFirstMessage('host', 'p1', '   ', 'empty001')
    expect(firstMessageState('host', 'p1')).toBeNull()
  })
})
