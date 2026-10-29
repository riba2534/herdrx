import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { APIError, api } from '../lib/api'
import type { PaneDialog } from '../types'
import { ANSWERED_HIDE_MS, DIALOG_POLL_MS, DialogCard, usePrimaryDialogCard } from './DialogCard'

const dialog: PaneDialog = {
  fingerprint: '0123456789abcdef',
  title: 'Do you want to proceed?',
  lines: ['Bash command', 'rm -rf build'],
  options: [
    { index: 1, label: 'Yes', selected: true },
    { index: 2, label: "Yes, and don't ask again for rm commands in /workspace/example", selected: false },
    { index: 3, label: 'No, and tell Claude what to do differently (esc)', selected: false },
  ],
}

afterEach(() => {
  vi.useRealTimers()
  vi.restoreAllMocks()
})

function setVisibility(state: DocumentVisibilityState) {
  Object.defineProperty(document, 'visibilityState', { configurable: true, get: () => state })
}

describe('DialogCard', () => {
  it('renders the recognized dialog and answers with the fingerprint and option index', async () => {
    const read = vi.spyOn(api, 'paneDialog').mockResolvedValueOnce({ dialog }).mockResolvedValue({ dialog: null })
    const answer = vi.spyOn(api, 'answerPaneDialog').mockResolvedValue({ ok: true })
    render(<DialogCard hostID="host" paneID="w1:p1"/>)
    const card = await screen.findByRole('group', { name: 'Do you want to proceed?' })
    expect(card).toHaveTextContent('Bash command')
    expect(card).toHaveTextContent('rm -rf build')
    expect(read).toHaveBeenCalledWith('host', 'w1:p1', expect.objectContaining({ signal: expect.any(AbortSignal) }))
    const options = screen.getAllByRole('listitem')
    expect(options).toHaveLength(3)
    const long = screen.getByRole('button', { name: /don't ask again/ })
    // 单行显示，完整文字放在自绘提示里。
    expect(long).toHaveAttribute('data-tooltip', dialog.options[1].label)
    expect(screen.getByRole('button', { name: 'Yes' })).toHaveAccessibleDescription('终端光标当前所在')
    fireEvent.click(screen.getByRole('button', { name: /No, and tell Claude/ }))
    await waitFor(() => expect(answer).toHaveBeenCalledWith('host', 'w1:p1', dialog.fingerprint, 3))
    expect(await screen.findByText('已选择：No, and tell Claude what to do differently (esc)')).toBeInTheDocument()
    expect(screen.queryByRole('group')).not.toBeInTheDocument()
  })

  it('refreshes the card and explains in Chinese when the dialog changed before the answer', async () => {
    const changed = { ...dialog, fingerprint: 'fedcba9876543210', title: 'Would you like to proceed?' }
    vi.spyOn(api, 'paneDialog').mockResolvedValueOnce({ dialog }).mockResolvedValue({ dialog: changed })
    vi.spyOn(api, 'answerPaneDialog').mockRejectedValue(new APIError(409, 'dialog_changed', '对话框已变化，请重新选择'))
    render(<DialogCard hostID="host" paneID="w1:p1"/>)
    fireEvent.click(await screen.findByRole('button', { name: 'Yes' }))
    expect(await screen.findByText('对话框已变化，请重新选择')).toBeInTheDocument()
    expect(await screen.findByRole('group', { name: 'Would you like to proceed?' })).toBeInTheDocument()
  })

  it('shows uncertain key presses as an error without retrying', async () => {
    vi.spyOn(api, 'paneDialog').mockResolvedValue({ dialog })
    const answer = vi.spyOn(api, 'answerPaneDialog').mockRejectedValue(new APIError(502, 'answer_unknown', '按键结果不确定，不会自动重试。请先查看终端再决定下一步'))
    render(<DialogCard hostID="host" paneID="w1:p1"/>)
    fireEvent.click(await screen.findByRole('button', { name: 'Yes' }))
    expect(await screen.findByText('按键结果不确定，不会自动重试。请先查看终端再决定下一步')).toBeInTheDocument()
    expect(answer).toHaveBeenCalledTimes(1)
  })

  it('renders nothing for an unrecognized screen and reports it to the owner', async () => {
    vi.spyOn(api, 'paneDialog').mockResolvedValue({ dialog: null })
    const onClose = vi.fn()
    const view = render(<DialogCard hostID="host" paneID="w1:p1" onClose={onClose}/>)
    await waitFor(() => expect(onClose).toHaveBeenCalled())
    expect(view.container).toBeEmptyDOMElement()
  })

  it('shows the initial dialog at once and polls every two seconds only while the page is visible', async () => {
    vi.useFakeTimers()
    setVisibility('visible')
    const read = vi.spyOn(api, 'paneDialog').mockResolvedValue({ dialog })
    render(<DialogCard hostID="host" paneID="w1:p1" initial={dialog}/>)
    expect(screen.getByRole('group', { name: 'Do you want to proceed?' })).toBeInTheDocument()
    expect(read).not.toHaveBeenCalled()
    await act(async () => { vi.advanceTimersByTime(DIALOG_POLL_MS) })
    expect(read).toHaveBeenCalledTimes(1)
    setVisibility('hidden')
    await act(async () => { vi.advanceTimersByTime(DIALOG_POLL_MS * 3) })
    expect(read).toHaveBeenCalledTimes(1)
    setVisibility('visible')
    await act(async () => { document.dispatchEvent(new Event('visibilitychange')) })
    expect(read).toHaveBeenCalledTimes(2)
  })

  it('moves focus between options with the arrow keys', async () => {
    vi.spyOn(api, 'paneDialog').mockResolvedValue({ dialog })
    render(<DialogCard hostID="host" paneID="w1:p1"/>)
    const first = await screen.findByRole('button', { name: 'Yes' })
    const buttons = screen.getAllByRole('listitem').map((item) => item.querySelector('button')!)
    first.focus()
    fireEvent.keyDown(first, { key: 'ArrowDown' })
    expect(buttons[1]).toHaveFocus()
    fireEvent.keyDown(buttons[1], { key: 'End' })
    expect(buttons[2]).toHaveFocus()
    fireEvent.keyDown(buttons[2], { key: 'ArrowDown' })
    expect(buttons[0]).toHaveFocus()
  })

  it('collapses until a different dialog appears', async () => {
    vi.useFakeTimers()
    setVisibility('visible')
    const next = { ...dialog, fingerprint: 'fedcba9876543210', title: 'Next question?' }
    vi.spyOn(api, 'paneDialog').mockResolvedValueOnce({ dialog }).mockResolvedValueOnce({ dialog }).mockResolvedValue({ dialog: next })
    render(<DialogCard hostID="host" paneID="w1:p1"/>)
    await act(async () => {})
    fireEvent.click(screen.getByRole('button', { name: '收起对话框卡片' }))
    expect(screen.queryByRole('group')).not.toBeInTheDocument()
    await act(async () => { vi.advanceTimersByTime(DIALOG_POLL_MS) })
    expect(screen.queryByRole('group')).not.toBeInTheDocument()
    await act(async () => { vi.advanceTimersByTime(DIALOG_POLL_MS) })
    expect(screen.getByRole('group', { name: 'Next question?' })).toBeInTheDocument()
  })

  it('registers a primary card so other surfaces do not show a second one', async () => {
    vi.spyOn(api, 'paneDialog').mockResolvedValue({ dialog: null })
    function Probe() {
      return <span>{usePrimaryDialogCard('host', 'w1:p1') ? 'primary' : 'none'}</span>
    }
    const probe = render(<Probe/>)
    expect(probe.getByText('none')).toBeInTheDocument()
    const card = render(<DialogCard hostID="host" paneID="w1:p1" primary/>)
    expect(probe.getByText('primary')).toBeInTheDocument()
    card.unmount()
    expect(probe.getByText('none')).toBeInTheDocument()
  })

  it('does not flash an answered card back from a poll that read the old screen', async () => {
    vi.useFakeTimers()
    setVisibility('visible')
    let finishPoll: (value: { dialog: PaneDialog | null }) => void = () => {}
    const read = vi.spyOn(api, 'paneDialog')
      .mockResolvedValueOnce({ dialog })
      .mockImplementationOnce(() => new Promise((resolve) => { finishPoll = resolve }))
      .mockResolvedValue({ dialog })
    vi.spyOn(api, 'answerPaneDialog').mockResolvedValue({ ok: true })
    render(<DialogCard hostID="host" paneID="w1:p1"/>)
    await act(async () => {})
    await act(async () => { vi.advanceTimersByTime(DIALOG_POLL_MS) })
    expect(read).toHaveBeenCalledTimes(2)
    await act(async () => { fireEvent.click(screen.getByRole('button', { name: 'Yes' })) })
    // 旧轮询带着答前的画面回来，以及终端尚未重绘时的补读：都不让卡片回闪。
    await act(async () => finishPoll({ dialog }))
    await act(async () => { vi.advanceTimersByTime(1000) })
    expect(screen.queryByRole('group')).not.toBeInTheDocument()
  })

  it('shows the same dialog again once the screen cleared or the short hide expired', async () => {
    vi.useFakeTimers()
    setVisibility('visible')
    // 同一命令再次请求权限：回答后先读到“没有对话框”，再出现同指纹的对话框。
    const read = vi.spyOn(api, 'paneDialog').mockResolvedValueOnce({ dialog }).mockResolvedValueOnce({ dialog: null }).mockResolvedValue({ dialog })
    vi.spyOn(api, 'answerPaneDialog').mockResolvedValue({ ok: true })
    const view = render(<DialogCard hostID="host" paneID="w1:p1"/>)
    await act(async () => {})
    await act(async () => { fireEvent.click(screen.getByRole('button', { name: 'Yes' })) })
    await act(async () => { vi.advanceTimersByTime(600) })
    expect(read).toHaveBeenCalledTimes(2)
    await act(async () => { vi.advanceTimersByTime(DIALOG_POLL_MS) })
    expect(screen.getByRole('group', { name: 'Do you want to proceed?' })).toBeInTheDocument()
    view.unmount()

    // 终端一直没重绘（读到的始终是同一个对话框）：最多隐藏 ANSWERED_HIDE_MS。
    read.mockReset()
    read.mockResolvedValue({ dialog })
    render(<DialogCard hostID="host" paneID="w1:p1"/>)
    await act(async () => {})
    await act(async () => { fireEvent.click(screen.getByRole('button', { name: 'Yes' })) })
    await act(async () => { vi.advanceTimersByTime(ANSWERED_HIDE_MS - 500) })
    expect(screen.queryByRole('group')).not.toBeInTheDocument()
    await act(async () => { vi.advanceTimersByTime(1000) })
    expect(screen.getByRole('group', { name: 'Do you want to proceed?' })).toBeInTheDocument()
  })

  it('cancels the follow-up read on unmount so it cannot close another pane card', async () => {
    vi.useFakeTimers()
    setVisibility('visible')
    const read = vi.spyOn(api, 'paneDialog').mockResolvedValueOnce({ dialog }).mockResolvedValue({ dialog: null })
    vi.spyOn(api, 'answerPaneDialog').mockResolvedValue({ ok: true })
    const onClose = vi.fn()
    const view = render(<DialogCard hostID="host" paneID="w1:p1" onClose={onClose}/>)
    await act(async () => {})
    await act(async () => { fireEvent.click(screen.getByRole('button', { name: 'Yes' })) })
    view.unmount()
    const calls = read.mock.calls.length
    await act(async () => { vi.advanceTimersByTime(DIALOG_POLL_MS * 2) })
    expect(read).toHaveBeenCalledTimes(calls)
    expect(onClose).not.toHaveBeenCalled()
  })
})
