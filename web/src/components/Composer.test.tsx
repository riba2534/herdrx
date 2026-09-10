import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { api, invalidateAuthentication } from '../lib/api'
import { clearComposerDrafts, composerInFlight, readComposerDraft, readComposerSend } from '../lib/composerDrafts'
import { Composer } from './Composer'

const user = { id: 'user', email: 'user@example.test', role: 'user', display_name: 'User' }

async function login(sessionID = 'composer-session') {
  vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response(JSON.stringify({ user, csrf_token: 'csrf', session_id: sessionID }))))
  await api.login({ email: 'user@example.test', password: 'test' })
}

function typeLocal(text: string) {
  fireEvent.change(screen.getByRole('textbox', { name: '本地输入内容' }), { target: { value: text } })
}

const props = {
  visible: true,
  directInput: false,
  onDirectInput: () => {},
  onLocalInput: () => {},
}

beforeEach(async () => {
  invalidateAuthentication()
  clearComposerDrafts()
  sessionStorage.clear()
  await login()
})
afterEach(() => {
  clearComposerDrafts()
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
})

describe('Composer', () => {
  it('keeps compact input modes in a keyboard accessible menu without losing the local draft', () => {
    const onDirectInput = vi.fn()
    const onLocalInput = vi.fn()
    const submit = vi.fn()
    const { rerender } = render(<Composer hostID="host" paneID="p1" {...props} compact onDirectInput={onDirectInput} onLocalInput={onLocalInput} submit={submit}/>)
    typeLocal('保留的多行草稿\n第二行')
    expect(screen.queryByRole('status')).not.toBeInTheDocument()
    expect(screen.queryByRole('button', { name: '直接输入终端' })).not.toBeInTheDocument()
    const trigger = screen.getByRole('button', { name: '输入方式：本地输入' })
    fireEvent.keyDown(trigger, { key: 'ArrowDown' })
    expect(trigger).toHaveAttribute('aria-expanded', 'true')
    const local = screen.getByRole('menuitemradio', { name: /^本地输入/ })
    const direct = screen.getByRole('menuitemradio', { name: /^直接输入终端/ })
    expect(local).toHaveAttribute('aria-checked', 'true')
    expect(local).toHaveFocus()
    fireEvent.keyDown(local, { key: 'ArrowDown' })
    expect(direct).toHaveFocus()
    fireEvent.click(direct)
    expect(onDirectInput).toHaveBeenCalledOnce()
    expect(screen.queryByRole('menu')).not.toBeInTheDocument()
    rerender(<Composer hostID="host" paneID="p1" {...props} directInput compact onDirectInput={onDirectInput} onLocalInput={onLocalInput} submit={submit}/>)
    fireEvent.click(screen.getByRole('button', { name: '输入方式：直接输入终端' }))
    expect(screen.getByRole('menuitemradio', { name: /^直接输入终端/ })).toHaveAttribute('aria-checked', 'true')
    fireEvent.click(screen.getByRole('menuitemradio', { name: /^本地输入/ }))
    expect(onLocalInput).toHaveBeenCalled()
    expect(screen.getByRole('textbox', { name: '本地输入内容' })).toHaveFocus()
    expect(screen.getByRole('textbox', { name: '本地输入内容' })).toHaveValue('保留的多行草稿\n第二行')
    expect(readComposerDraft('host', 'p1')).toBe('保留的多行草稿\n第二行')
    expect(submit).not.toHaveBeenCalled()
  })

  it('dismisses the compact mode menu with Escape, outside taps, and focus leaving the menu', () => {
    render(<Composer hostID="host" paneID="p1" {...props} compact submit={vi.fn()}/>)
    const trigger = screen.getByRole('button', { name: '输入方式：本地输入' })
    fireEvent.click(trigger)
    fireEvent.keyDown(screen.getByRole('menu'), { key: 'Escape' })
    expect(screen.queryByRole('menu')).not.toBeInTheDocument()
    expect(trigger).toHaveFocus()
    fireEvent.click(trigger)
    fireEvent.pointerDown(document.body)
    expect(screen.queryByRole('menu')).not.toBeInTheDocument()
    fireEvent.click(trigger)
    act(() => screen.getByRole('textbox', { name: '本地输入内容' }).focus())
    expect(screen.queryByRole('menu')).not.toBeInTheDocument()
  })

  it('edits locally, uses Enter as newline, and submits once with the click-time pane', async () => {
    const submit = vi.fn().mockResolvedValue(undefined)
    const { rerender } = render(<Composer hostID="host" paneID="p1" {...props} submit={submit}/>)
    const box = screen.getByRole('textbox', { name: '本地输入内容' })
    typeLocal('第一行')
    fireEvent.keyDown(box, { key: 'Enter' })
    typeLocal('第一行\n中文 🙂')
    expect(submit).not.toHaveBeenCalled()
    fireEvent.click(screen.getByRole('button', { name: '发送' }))
    rerender(<Composer hostID="host" paneID="p2" {...props} submit={submit}/>)
    typeLocal('另一个终端的草稿')
    await waitFor(() => expect(submit).toHaveBeenCalledTimes(1))
    expect(submit).toHaveBeenCalledWith('p1', '第一行\n中文 🙂')
    expect(readComposerDraft('host', 'p2')).toBe('另一个终端的草稿')
    expect(readComposerDraft('host', 'p1')).toBe('')
  })

  it('does not clear another pane that has the same draft text as the message just sent', async () => {
    let finish: () => void = () => {}
    const submit = vi.fn().mockImplementation(() => new Promise<void>((resolve) => { finish = resolve }))
    const { rerender } = render(<Composer hostID="host" paneID="p1" {...props} submit={submit}/>)
    typeLocal('同一段提示词')
    fireEvent.click(screen.getByRole('button', { name: '发送' }))
    rerender(<Composer hostID="host" paneID="p2" {...props} submit={submit}/>)
    typeLocal('同一段提示词')
    await act(async () => finish())
    await waitFor(() => expect(readComposerSend('host', 'p1').status).toBe('delivered'))
    expect(screen.getByRole('textbox', { name: '本地输入内容' })).toHaveValue('同一段提示词')
    expect(readComposerDraft('host', 'p2')).toBe('同一段提示词')
    expect(readComposerDraft('host', 'p1')).toBe('')
  })

  it('keeps later edits on the sending pane and only clears an untouched matching revision', async () => {
    let finish: () => void = () => {}
    const submit = vi.fn().mockImplementation(() => new Promise<void>((resolve) => { finish = resolve }))
    render(<Composer hostID="host" paneID="p1" {...props} submit={submit}/>)
    typeLocal('hello')
    fireEvent.click(screen.getByRole('button', { name: '发送' }))
    typeLocal('hello 后续编辑')
    await act(async () => finish())
    await waitFor(() => expect(screen.getByRole('textbox', { name: '本地输入内容' })).toHaveValue('hello 后续编辑'))
    expect(readComposerDraft('host', 'p1')).toBe('hello 后续编辑')
  })

  it('does not submit during IME composition on a non-empty draft, including Ctrl/Cmd+Enter', async () => {
    const submit = vi.fn().mockResolvedValue(undefined)
    render(<Composer hostID="host" paneID="p1" {...props} submit={submit}/>)
    const box = screen.getByRole('textbox', { name: '本地输入内容' })
    typeLocal('payload')
    fireEvent.keyDown(box, { key: 'Enter', ctrlKey: true, isComposing: true, keyCode: 229 })
    fireEvent.keyDown(box, { key: 'Enter', metaKey: true, isComposing: true, keyCode: 229 })
    expect(submit).not.toHaveBeenCalled()
    fireEvent.click(screen.getByRole('button', { name: '发送' }))
    fireEvent.click(screen.getByRole('button', { name: '发送' }))
    await waitFor(() => expect(submit).toHaveBeenCalledTimes(1))
  })

  it('shows a disconnected placeholder while the host cannot send', () => {
    render(<Composer hostID="host" paneID="p1" sendDisabled placeholder="主机未连接，暂不能发送" {...props} submit={vi.fn()}/>)
    expect(screen.getByRole('textbox', { name: '本地输入内容' })).toHaveAttribute('placeholder', '主机未连接，暂不能发送')
  })

  it('keeps the textarea editable while send is blocked, and refuses drafts without a pane', () => {
    const submit = vi.fn()
    const { rerender } = render(<Composer hostID="host" paneID="p1" sendDisabled {...props} submit={submit}/>)
    const box = screen.getByRole('textbox', { name: '本地输入内容' })
    expect(box).not.toBeDisabled()
    typeLocal('offline draft')
    expect(screen.getByRole('button', { name: '发送' })).toBeDisabled()
    expect(readComposerDraft('host', 'p1')).toBe('offline draft')
    rerender(<Composer hostID="host" paneID="" sendDisabled {...props} submit={submit}/>)
    expect(screen.getByRole('textbox', { name: '本地输入内容' })).toBeDisabled()
    expect(readComposerDraft('host', '')).toBe('')
  })

  it('restores unknown send state after remount instead of looking idle', async () => {
    let finish: (error?: Error) => void = () => {}
    const submit = vi.fn().mockImplementation(() => new Promise<void>((resolve, reject) => { finish = (error) => error ? reject(error) : resolve() }))
    const view = render(<Composer hostID="host" paneID="p1" {...props} compact submit={submit}/>)
    typeLocal('maybe delivered')
    fireEvent.click(screen.getByRole('button', { name: '发送' }))
    await waitFor(() => expect(screen.getByRole('status')).toHaveTextContent('发送中'))
    view.unmount()
    expect(composerInFlight('host', 'p1')).toBe(true)
    render(<Composer hostID="host" paneID="p1" {...props} compact submit={submit}/>)
    expect(screen.getByRole('status')).toHaveTextContent('发送中')
    expect(screen.getByRole('button', { name: '发送' })).toBeDisabled()
    await act(async () => finish(new Error('连接已关闭')))
    await waitFor(() => expect(screen.getByRole('status')).toHaveTextContent('结果未知'))
    expect(screen.getByRole('status')).toHaveTextContent('内容已保留，请先核对终端结果再手动重试，不会自动重发')
    expect(screen.getByRole('textbox', { name: '本地输入内容' })).toHaveValue('maybe delivered')
    expect(submit).toHaveBeenCalledTimes(1)
  })

  it('retains the draft on confirmed failure or timeout without replaying', async () => {
    let finish: (error?: Error) => void = () => {}
    const submit = vi.fn().mockImplementation(() => new Promise<void>((resolve, reject) => { finish = (error) => error ? reject(error) : resolve() }))
    render(<Composer hostID="host" paneID="p1" {...props} submit={submit}/>)
    typeLocal('keep me')
    fireEvent.click(screen.getByRole('button', { name: '发送' }))
    await act(async () => finish(new Error('远端拒绝')))
    await waitFor(() => expect(screen.getByRole('status')).toHaveTextContent('发送失败'))
    expect(screen.getByRole('textbox', { name: '本地输入内容' })).toHaveValue('keep me')
    fireEvent.click(screen.getByRole('button', { name: '发送' }))
    await act(async () => finish(new Error('请求超时')))
    await waitFor(() => expect(screen.getByRole('status')).toHaveTextContent('结果未知'))
    expect(submit).toHaveBeenCalledTimes(2)
  })

  it('treats a post-dispatch Herdr EOF as unknown and still shows it after remount', async () => {
    const submit = vi.fn().mockRejectedValue(new Error('read herdr response: EOF'))
    const view = render(<Composer hostID="host" paneID="p1" {...props} submit={submit}/>)
    typeLocal('already on the wire')
    fireEvent.click(screen.getByRole('button', { name: '发送' }))
    await waitFor(() => expect(screen.getByRole('status')).toHaveTextContent('结果未知'))
    expect(screen.queryByRole('status')).not.toHaveTextContent('发送失败')
    expect(screen.getByRole('textbox', { name: '本地输入内容' })).toHaveValue('already on the wire')
    view.unmount()
    render(<Composer hostID="host" paneID="p1" {...props} submit={submit}/>)
    expect(screen.getByRole('status')).toHaveTextContent('结果未知')
    expect(screen.getByRole('textbox', { name: '本地输入内容' })).toHaveValue('already on the wire')
    expect(submit).toHaveBeenCalledTimes(1)
  })
})
