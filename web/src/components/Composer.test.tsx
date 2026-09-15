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

/**
 * 提交分两腿（先整段正文、后单独回车），所以测试要能手握每一腿的完成时机：
 * `legs[n]` 对应第 n+1 次 `submit` 调用，前一次没有结算时后一腿不会被调用。
 */
function deferredSubmit() {
  const legs: Array<{ resolve: () => void; reject: (error: Error) => void }> = []
  const submit = vi.fn((_paneID: string, _text: string, _keys: string[]) => new Promise<void>((resolve, reject) => {
    legs.push({ resolve: () => resolve(), reject })
  }))
  return { submit, legs }
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
    await waitFor(() => expect(submit).toHaveBeenCalledTimes(2))
    // 两腿都钉在点击那一刻的 pane，切换 pane 不会把回车送到别处。
    expect(submit).toHaveBeenNthCalledWith(1, 'p1', '第一行\n中文 🙂', [])
    expect(submit).toHaveBeenNthCalledWith(2, 'p1', '', ['Enter'])
    expect(readComposerDraft('host', 'p2')).toBe('另一个终端的草稿')
    expect(readComposerDraft('host', 'p1')).toBe('')
  })

  it('does not clear another pane that has the same draft text as the message just sent', async () => {
    const { submit, legs } = deferredSubmit()
    const { rerender } = render(<Composer hostID="host" paneID="p1" {...props} submit={submit}/>)
    typeLocal('同一段提示词')
    fireEvent.click(screen.getByRole('button', { name: '发送' }))
    rerender(<Composer hostID="host" paneID="p2" {...props} submit={submit}/>)
    typeLocal('同一段提示词')
    await act(async () => legs[0].resolve())
    await waitFor(() => expect(submit).toHaveBeenCalledTimes(2))
    await act(async () => legs[1].resolve())
    await waitFor(() => expect(readComposerSend('host', 'p1').status).toBe('delivered'))
    expect(screen.getByRole('textbox', { name: '本地输入内容' })).toHaveValue('同一段提示词')
    expect(readComposerDraft('host', 'p2')).toBe('同一段提示词')
    expect(readComposerDraft('host', 'p1')).toBe('')
  })

  it('keeps later edits on the sending pane and only clears an untouched matching revision', async () => {
    const { submit, legs } = deferredSubmit()
    render(<Composer hostID="host" paneID="p1" {...props} submit={submit}/>)
    typeLocal('hello')
    fireEvent.click(screen.getByRole('button', { name: '发送' }))
    typeLocal('hello 后续编辑')
    await act(async () => legs[0].resolve())
    await waitFor(() => expect(submit).toHaveBeenCalledTimes(2))
    await act(async () => legs[1].resolve())
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
    // 连点两次也只提交一次：正文一腿、回车一腿。
    await waitFor(() => expect(submit).toHaveBeenCalledTimes(2))
    expect(submit.mock.calls.filter((call) => (call[2] as string[]).includes('Enter'))).toHaveLength(1)
  })

  it('sends the text and the Enter as two legs, and reports an unconfirmed Enter without resending the text', async () => {
    const { submit, legs } = deferredSubmit()
    render(<Composer hostID="host" paneID="p1" {...props} submit={submit}/>)
    typeLocal('分两腿发送')
    fireEvent.click(screen.getByRole('button', { name: '发送' }))
    await waitFor(() => expect(submit).toHaveBeenCalledTimes(1))
    // 第一腿只有正文、没有按键：bracketed paste 由 herdr 按 pane 状态决定。
    expect(submit).toHaveBeenNthCalledWith(1, 'p1', '分两腿发送', [])
    await act(async () => legs[0].resolve())
    // 第二腿只有一次回车，正文不再重发。
    await waitFor(() => expect(submit).toHaveBeenCalledTimes(2))
    expect(submit).toHaveBeenNthCalledWith(2, 'p1', '', ['Enter'])
    await act(async () => legs[1].reject(new Error('连接已关闭')))
    await waitFor(() => expect(screen.getByRole('status')).toHaveTextContent('结果未知'))
    expect(screen.getByRole('status')).toHaveTextContent('正文已送入终端，但提交回车未确认')
    expect(screen.getByRole('textbox', { name: '本地输入内容' })).toHaveValue('分两腿发送')
    await new Promise((resolve) => setTimeout(resolve, 25))
    expect(submit).toHaveBeenCalledTimes(2)
  })

  it('never sends the Enter leg when the text leg fails, so a failed send cannot half-submit', async () => {
    const { submit, legs } = deferredSubmit()
    render(<Composer hostID="host" paneID="p1" {...props} submit={submit}/>)
    typeLocal('keep me')
    fireEvent.click(screen.getByRole('button', { name: '发送' }))
    await waitFor(() => expect(submit).toHaveBeenCalledTimes(1))
    await act(async () => legs[0].reject(new Error('远端拒绝')))
    await waitFor(() => expect(screen.getByRole('status')).toHaveTextContent('发送失败'))
    await new Promise((resolve) => setTimeout(resolve, 25))
    expect(submit).toHaveBeenCalledTimes(1)
    expect(screen.getByRole('textbox', { name: '本地输入内容' })).toHaveValue('keep me')
  })

  it('clears the draft only after both legs succeed', async () => {
    const { submit, legs } = deferredSubmit()
    render(<Composer hostID="host" paneID="p1" {...props} submit={submit}/>)
    typeLocal('两腿都成功才清空')
    fireEvent.click(screen.getByRole('button', { name: '发送' }))
    await waitFor(() => expect(submit).toHaveBeenCalledTimes(1))
    await act(async () => legs[0].resolve())
    await waitFor(() => expect(submit).toHaveBeenCalledTimes(2))
    expect(readComposerDraft('host', 'p1')).toBe('两腿都成功才清空')
    await act(async () => legs[1].resolve())
    await waitFor(() => expect(screen.getByRole('status')).toHaveTextContent('已送达'))
    expect(readComposerDraft('host', 'p1')).toBe('')
  })

  it('ignores repeated Enter and repeated Ctrl/Cmd+Enter keydown without sending', async () => {
    const submit = vi.fn().mockResolvedValue(undefined)
    render(<Composer hostID="host" paneID="p1" {...props} submit={submit}/>)
    const box = screen.getByRole('textbox', { name: '本地输入内容' })
    typeLocal('长按产生的内容')
    fireEvent.keyDown(box, { key: 'Enter', repeat: true })
    fireEvent.keyDown(box, { key: 'Enter', ctrlKey: true, repeat: true })
    fireEvent.keyDown(box, { key: 'Enter', metaKey: true, repeat: true })
    expect(submit).not.toHaveBeenCalled()
    // Ctrl+Enter 的单次按键仍然按既有语义发送：一次逻辑发送 = 两腿。
    fireEvent.keyDown(box, { key: 'Enter', ctrlKey: true })
    await waitFor(() => expect(submit).toHaveBeenCalledTimes(2))
    expect(submit).toHaveBeenNthCalledWith(1, 'p1', '长按产生的内容', [])
    expect(submit).toHaveBeenNthCalledWith(2, 'p1', '', ['Enter'])
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

describe('Composer media host', () => {
  /** 最小媒体层替身：只实现冻结的 ComposerMediaHost 接口。 */
  function mediaHost(overrides: Partial<Parameters<typeof Composer>[0]['mediaHost']> = {}) {
    return {
      canSend: vi.fn(() => false),
      compose: vi.fn((draft: string) => draft),
      onSettled: vi.fn(),
      onFiles: vi.fn(),
      ...overrides,
    }
  }

  it('keeps the pre-wiring behavior byte for byte when the media host is absent', async () => {
    const submit = vi.fn().mockResolvedValue(undefined)
    render(<Composer hostID="host" paneID="p1" {...props} variant="chat" submit={submit}/>)
    const box = screen.getByRole('textbox', { name: '对话输入内容' })
    expect(screen.getByRole('button', { name: '发送' })).toBeDisabled()
    fireEvent.keyDown(box, { key: 'Enter' })
    fireEvent.paste(box, { clipboardData: { items: [{ kind: 'file', getAsFile: () => new File(['x'], 'a.png', { type: 'image/png' }) }], files: [] } })
    expect(submit).not.toHaveBeenCalled()
  })

  it('sends an image-only message once: composes with the media host, then settles', async () => {
    const host = mediaHost({ canSend: vi.fn(() => true), compose: vi.fn(() => '/remote/a.png') })
    const submit = vi.fn().mockResolvedValue(undefined)
    render(<Composer hostID="host" paneID="p1" {...props} variant="chat" submit={submit} mediaHost={host}/>)
    const button = screen.getByRole('button', { name: '发送' })
    expect(button).toBeEnabled()

    fireEvent.click(button)
    // 一次逻辑发送 = 两腿：先正文、后一次单独回车。
    await waitFor(() => expect(submit).toHaveBeenCalledTimes(2))
    expect(host.compose).toHaveBeenCalledWith('')
    expect(submit).toHaveBeenNthCalledWith(1, 'p1', '/remote/a.png', [])
    expect(submit).toHaveBeenNthCalledWith(2, 'p1', '', ['Enter'])
    await waitFor(() => expect(host.onSettled).toHaveBeenCalledWith('delivered'))
  })

  it('sends text plus references as one submission and never types at paste time', async () => {
    const host = mediaHost({ compose: vi.fn((draft: string) => `${draft}\n/remote/a.png`) })
    const onPasteImages = vi.fn()
    const submit = vi.fn().mockResolvedValue(undefined)
    render(<Composer hostID="host" paneID="p1" {...props} variant="chat" submit={submit} onPasteImages={onPasteImages} mediaHost={host}/>)
    const box = screen.getByRole('textbox', { name: '对话输入内容' })
    const shot = new File(['x'], 'shot.png', { type: 'image/png' })

    fireEvent.paste(box, { clipboardData: { items: [{ kind: 'file', getAsFile: () => shot }], files: [] } })
    // 粘贴只交给媒体层暂存：不发送、不走旧的整体上传入口。
    expect(host.onFiles).toHaveBeenCalledWith([shot])
    expect(onPasteImages).not.toHaveBeenCalled()
    expect(submit).not.toHaveBeenCalled()

    fireEvent.change(box, { target: { value: '看这张图' } })
    fireEvent.keyDown(box, { key: 'Enter' })
    // 正文与引用合成**一次**提交，这次提交同样分两腿送出。
    await waitFor(() => expect(submit).toHaveBeenCalledTimes(2))
    expect(submit).toHaveBeenNthCalledWith(1, 'p1', '看这张图\n/remote/a.png', [])
    expect(submit).toHaveBeenNthCalledWith(2, 'p1', '', ['Enter'])
  })

  it('abandons the send when the media host refuses to compose', async () => {
    const host = mediaHost({ canSend: vi.fn(() => true), compose: vi.fn(() => null) })
    const submit = vi.fn().mockResolvedValue(undefined)
    render(<Composer hostID="host" paneID="p1" {...props} variant="chat" submit={submit} mediaHost={host}/>)
    fireEvent.click(screen.getByRole('button', { name: '发送' }))
    expect(submit).not.toHaveBeenCalled()
    expect(readComposerSend('host', 'p1').status).toBe('idle')
  })

  it('keeps medium edits blocked while the media host is busy and renders its tray above the input', () => {
    const host = mediaHost({ canSend: vi.fn(() => false) })
    render(<Composer hostID="host" paneID="p1" {...props} variant="chat" submit={vi.fn()} mediaHost={{ ...host, tray: <p>图片附件占位区</p> }}/>)
    const tray = screen.getByText('图片附件占位区')
    const box = screen.getByRole('textbox', { name: '对话输入内容' })
    expect(screen.getByRole('button', { name: '发送' })).toBeDisabled()
    // 占位区必须在 textarea 之前（上方），且不抢走输入焦点。
    expect(tray.compareDocumentPosition(box) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
  })
})

describe('Composer chat variant', () => {
  it('sends exactly once with Enter, keeps Shift+Enter and IME Enter as newlines, and has no second mode switch', async () => {
    const submit = vi.fn().mockResolvedValue(undefined)
    render(<Composer hostID="host" paneID="p1" {...props} variant="chat" submit={submit}/>)
    const box = screen.getByRole('textbox', { name: '对话输入内容' })
    expect(screen.getByRole('region', { name: '对话输入' })).toBeInTheDocument()
    expect(box).toHaveAttribute('enterkeyhint', 'send')
    // 对话视图复用同一套草稿与发送事务，不出现第二个输入方式切换。
    expect(screen.queryByRole('button', { name: /输入方式/ })).not.toBeInTheDocument()
    expect(screen.queryByRole('button', { name: '本地输入' })).not.toBeInTheDocument()
    expect(screen.queryByRole('button', { name: '直接输入终端' })).not.toBeInTheDocument()

    fireEvent.change(box, { target: { value: '第一行' } })
    fireEvent.keyDown(box, { key: 'Enter', shiftKey: true })
    fireEvent.keyDown(box, { key: 'Process' })
    fireEvent.keyDown(box, { key: 'Enter', isComposing: true })
    expect(submit).not.toHaveBeenCalled()

    fireEvent.keyDown(box, { key: 'Enter' })
    // 「发送一次」= 两腿：整段正文、再单独一次回车，且两腿都钉在同一个 pane。
    await waitFor(() => expect(submit).toHaveBeenCalledTimes(2))
    expect(submit).toHaveBeenNthCalledWith(1, 'p1', '第一行', [])
    expect(submit).toHaveBeenNthCalledWith(2, 'p1', '', ['Enter'])
    // 发送后草稿清空，重复按键不会再发一次。
    await waitFor(() => expect(box).toHaveValue(''))
    fireEvent.keyDown(box, { key: 'Enter' })
    expect(submit).toHaveBeenCalledTimes(2)
  })

  it('keeps the chat draft and never sends while the host is disconnected', () => {
    const submit = vi.fn()
    render(<Composer hostID="host" paneID="p1" {...props} variant="chat" sendDisabled submit={submit}/>)
    const box = screen.getByRole('textbox', { name: '对话输入内容' })
    fireEvent.change(box, { target: { value: '断线时输入的内容' } })
    fireEvent.keyDown(box, { key: 'Enter' })
    expect(submit).not.toHaveBeenCalled()
    expect(screen.getByRole('button', { name: '发送' })).toBeDisabled()
    expect(readComposerDraft('host', 'p1')).toBe('断线时输入的内容')
    expect(readComposerSend('host', 'p1').status).toBe('idle')
  })

  it('sends with Ctrl/Cmd+Enter while Shift+Enter keeps newlines even with extra modifiers', async () => {
    const submit = vi.fn().mockResolvedValue(undefined)
    render(<Composer hostID="host" paneID="p1" {...props} variant="chat" submit={submit}/>)
    const box = screen.getByRole('textbox', { name: '对话输入内容' })
    fireEvent.change(box, { target: { value: '第一行' } })
    fireEvent.keyDown(box, { key: 'Enter', shiftKey: true, ctrlKey: true })
    fireEvent.keyDown(box, { key: 'Enter', shiftKey: true, metaKey: true })
    fireEvent.keyDown(box, { key: 'Enter', shiftKey: true, altKey: true })
    expect(submit).not.toHaveBeenCalled()
    fireEvent.keyDown(box, { key: 'Enter', ctrlKey: true })
    await waitFor(() => expect(submit).toHaveBeenCalledTimes(2))
    expect(submit).toHaveBeenNthCalledWith(1, 'p1', '第一行', [])
    expect(submit).toHaveBeenNthCalledWith(2, 'p1', '', ['Enter'])
    await waitFor(() => expect(box).toHaveValue(''))
    fireEvent.change(box, { target: { value: '第二行' } })
    fireEvent.keyDown(box, { key: 'Enter', metaKey: true })
    await waitFor(() => expect(submit).toHaveBeenCalledTimes(4))
    expect(submit).toHaveBeenNthCalledWith(3, 'p1', '第二行', [])
    expect(submit).toHaveBeenNthCalledWith(4, 'p1', '', ['Enter'])
  })

  it('ignores repeated Enter keydown and presses on an empty draft', async () => {
    const submit = vi.fn().mockResolvedValue(undefined)
    render(<Composer hostID="host" paneID="p1" {...props} variant="chat" submit={submit}/>)
    const box = screen.getByRole('textbox', { name: '对话输入内容' })
    // 空内容与纯空白都不能发送。
    fireEvent.keyDown(box, { key: 'Enter' })
    fireEvent.change(box, { target: { value: '   ' } })
    fireEvent.keyDown(box, { key: 'Enter' })
    expect(submit).not.toHaveBeenCalled()
    // 长按 Enter：repeat 的 keydown 不发送，只有单次按键才发送。
    fireEvent.change(box, { target: { value: '长按' } })
    fireEvent.keyDown(box, { key: 'Enter', repeat: true })
    fireEvent.keyDown(box, { key: 'Enter', repeat: true })
    expect(submit).not.toHaveBeenCalled()
    fireEvent.keyDown(box, { key: 'Enter' })
    await waitFor(() => expect(submit).toHaveBeenCalledTimes(2))
    expect(submit).toHaveBeenNthCalledWith(1, 'p1', '长按', [])
    expect(submit).toHaveBeenNthCalledWith(2, 'p1', '', ['Enter'])
  })

  it('does not send twice while a chat message is still in flight', async () => {
    const { submit, legs } = deferredSubmit()
    render(<Composer hostID="host" paneID="p1" {...props} variant="chat" submit={submit}/>)
    const box = screen.getByRole('textbox', { name: '对话输入内容' })
    fireEvent.change(box, { target: { value: '发送中的内容' } })
    fireEvent.keyDown(box, { key: 'Enter' })
    fireEvent.keyDown(box, { key: 'Enter' })
    fireEvent.keyDown(box, { key: 'Enter', ctrlKey: true })
    await waitFor(() => expect(screen.getByRole('status')).toHaveTextContent('发送中'))
    // 第一腿还在飞：后续按键不会开出第二次逻辑发送。
    expect(submit).toHaveBeenCalledTimes(1)
    await act(async () => legs[0].resolve())
    await waitFor(() => expect(submit).toHaveBeenCalledTimes(2))
    // 第二腿还在飞时同样不重入。
    fireEvent.keyDown(box, { key: 'Enter' })
    expect(submit).toHaveBeenCalledTimes(2)
    await act(async () => legs[1].resolve())
    await waitFor(() => expect(readComposerSend('host', 'p1').status).toBe('delivered'))
    expect(submit).toHaveBeenCalledTimes(2)
  })
})
