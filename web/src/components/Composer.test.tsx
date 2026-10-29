import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { APIError, api, invalidateAuthentication } from '../lib/api'
import { clearComposerDrafts, composerInFlight, readComposerDraft, readComposerSend } from '../lib/composerDrafts'
import type { PaneDialog, PanePromptInput, PanePromptResult } from '../types'
import { Composer } from './Composer'

const user = { id: 'user', email: 'user@example.test', role: 'user', display_name: 'User' }

async function login(sessionID = 'composer-session') {
  vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response(JSON.stringify({ user, csrf_token: 'csrf', session_id: sessionID }))))
  await api.login({ email: 'user@example.test', password: 'test' })
}

function typeLocal(text: string) {
  fireEvent.change(screen.getByRole('textbox', { name: '本地输入内容' }), { target: { value: text } })
}

/** 发送统一走 `api.panePrompt`：一次逻辑发送 = 一次请求，服务端负责读屏与两腿送入。 */
function mockPrompt(result: () => Promise<PanePromptResult> = () => Promise.resolve({ delivered: 'sent' })) {
  return vi.spyOn(api, 'panePrompt').mockImplementation(() => result())
}

/** 手握每次请求的结算时机：`calls[n]` 对应第 n+1 次 `panePrompt`。 */
function deferredPrompt() {
  const calls: Array<{ resolve: () => void; reject: (error: unknown) => void }> = []
  const prompt = mockPrompt(() => new Promise<PanePromptResult>((resolve, reject) => {
    calls.push({ resolve: () => resolve({ delivered: 'sent' }), reject })
  }))
  return { prompt, calls }
}

function sent(prompt: ReturnType<typeof mockPrompt>) {
  return prompt.mock.calls.map(([host, pane, input]) => ({ host, pane, ...(input as PanePromptInput) }))
}

const dialog: PaneDialog = {
  fingerprint: '0123456789abcdef',
  title: 'Do you want to proceed?',
  lines: ['Bash command', 'rm -rf build'],
  options: [{ index: 1, label: 'Yes', selected: true }, { index: 2, label: 'No', selected: false }],
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
    const prompt = mockPrompt()
    const { rerender } = render(<Composer hostID="host" paneID="p1" {...props} compact onDirectInput={onDirectInput} onLocalInput={onLocalInput}/>)
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
    rerender(<Composer hostID="host" paneID="p1" {...props} directInput compact onDirectInput={onDirectInput} onLocalInput={onLocalInput}/>)
    fireEvent.click(screen.getByRole('button', { name: '输入方式：直接输入终端' }))
    expect(screen.getByRole('menuitemradio', { name: /^直接输入终端/ })).toHaveAttribute('aria-checked', 'true')
    fireEvent.click(screen.getByRole('menuitemradio', { name: /^本地输入/ }))
    expect(onLocalInput).toHaveBeenCalled()
    expect(screen.getByRole('textbox', { name: '本地输入内容' })).toHaveFocus()
    expect(screen.getByRole('textbox', { name: '本地输入内容' })).toHaveValue('保留的多行草稿\n第二行')
    expect(readComposerDraft('host', 'p1')).toBe('保留的多行草稿\n第二行')
    expect(prompt).not.toHaveBeenCalled()
  })

  it('dismisses the compact mode menu with Escape, outside taps, and focus leaving the menu', () => {
    render(<Composer hostID="host" paneID="p1" {...props} compact/>)
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
    const prompt = mockPrompt()
    const { rerender } = render(<Composer hostID="host" paneID="p1" {...props}/>)
    const box = screen.getByRole('textbox', { name: '本地输入内容' })
    typeLocal('第一行')
    fireEvent.keyDown(box, { key: 'Enter' })
    typeLocal('第一行\n中文 🙂')
    expect(prompt).not.toHaveBeenCalled()
    fireEvent.click(screen.getByRole('button', { name: '发送' }))
    rerender(<Composer hostID="host" paneID="p2" {...props}/>)
    typeLocal('另一个终端的草稿')
    await waitFor(() => expect(readComposerSend('host', 'p1').status).toBe('delivered'))
    // 一次逻辑发送 = 一次请求，钉在点击那一刻的 pane；切换 pane 不会把回车送到别处。
    expect(sent(prompt)).toEqual([{ host: 'host', pane: 'p1', text: '第一行\n中文 🙂', submit: true, client_msg_id: expect.stringMatching(/^[0-9a-f]{32}$/), force: undefined }])
    expect(readComposerDraft('host', 'p2')).toBe('另一个终端的草稿')
    expect(readComposerDraft('host', 'p1')).toBe('')
  })

  it('does not clear another pane that has the same draft text as the message just sent', async () => {
    const { calls } = deferredPrompt()
    const { rerender } = render(<Composer hostID="host" paneID="p1" {...props}/>)
    typeLocal('同一段提示词')
    fireEvent.click(screen.getByRole('button', { name: '发送' }))
    rerender(<Composer hostID="host" paneID="p2" {...props}/>)
    typeLocal('同一段提示词')
    await waitFor(() => expect(calls).toHaveLength(1))
    await act(async () => calls[0].resolve())
    await waitFor(() => expect(readComposerSend('host', 'p1').status).toBe('delivered'))
    expect(screen.getByRole('textbox', { name: '本地输入内容' })).toHaveValue('同一段提示词')
    expect(readComposerDraft('host', 'p2')).toBe('同一段提示词')
    expect(readComposerDraft('host', 'p1')).toBe('')
  })

  it('keeps later edits on the sending pane and only clears an untouched matching revision', async () => {
    const { calls } = deferredPrompt()
    render(<Composer hostID="host" paneID="p1" {...props}/>)
    typeLocal('hello')
    fireEvent.click(screen.getByRole('button', { name: '发送' }))
    typeLocal('hello 后续编辑')
    await waitFor(() => expect(calls).toHaveLength(1))
    await act(async () => calls[0].resolve())
    await waitFor(() => expect(readComposerSend('host', 'p1').status).toBe('delivered'))
    expect(screen.getByRole('textbox', { name: '本地输入内容' })).toHaveValue('hello 后续编辑')
    expect(readComposerDraft('host', 'p1')).toBe('hello 后续编辑')
  })

  it('does not submit during IME composition on a non-empty draft, including Ctrl/Cmd+Enter', async () => {
    const prompt = mockPrompt()
    render(<Composer hostID="host" paneID="p1" {...props}/>)
    const box = screen.getByRole('textbox', { name: '本地输入内容' })
    typeLocal('payload')
    fireEvent.keyDown(box, { key: 'Enter', ctrlKey: true, isComposing: true, keyCode: 229 })
    fireEvent.keyDown(box, { key: 'Enter', metaKey: true, isComposing: true, keyCode: 229 })
    expect(prompt).not.toHaveBeenCalled()
    fireEvent.click(screen.getByRole('button', { name: '发送' }))
    fireEvent.click(screen.getByRole('button', { name: '发送' }))
    // 连点两次也只提交一次。
    await waitFor(() => expect(readComposerSend('host', 'p1').status).toBe('delivered'))
    expect(prompt).toHaveBeenCalledTimes(1)
  })

  it('clears the draft only after the server confirms delivery', async () => {
    const { calls } = deferredPrompt()
    render(<Composer hostID="host" paneID="p1" {...props}/>)
    typeLocal('确认送达才清空')
    fireEvent.click(screen.getByRole('button', { name: '发送' }))
    await waitFor(() => expect(calls).toHaveLength(1))
    expect(readComposerDraft('host', 'p1')).toBe('确认送达才清空')
    await act(async () => calls[0].resolve())
    await waitFor(() => expect(screen.getByRole('status')).toHaveTextContent('已送达'))
    expect(readComposerDraft('host', 'p1')).toBe('')
  })

  it('treats a duplicate acknowledgement as delivered', async () => {
    mockPrompt(() => Promise.resolve({ delivered: 'duplicate' }))
    render(<Composer hostID="host" paneID="p1" {...props}/>)
    typeLocal('已经送过的')
    fireEvent.click(screen.getByRole('button', { name: '发送' }))
    await waitFor(() => expect(readComposerSend('host', 'p1').status).toBe('delivered'))
    expect(readComposerDraft('host', 'p1')).toBe('')
  })

  it('ignores repeated Enter and repeated Ctrl/Cmd+Enter keydown without sending', async () => {
    const prompt = mockPrompt()
    render(<Composer hostID="host" paneID="p1" {...props}/>)
    const box = screen.getByRole('textbox', { name: '本地输入内容' })
    typeLocal('长按产生的内容')
    fireEvent.keyDown(box, { key: 'Enter', repeat: true })
    fireEvent.keyDown(box, { key: 'Enter', ctrlKey: true, repeat: true })
    fireEvent.keyDown(box, { key: 'Enter', metaKey: true, repeat: true })
    expect(prompt).not.toHaveBeenCalled()
    // Ctrl+Enter 的单次按键仍然按既有语义发送。
    fireEvent.keyDown(box, { key: 'Enter', ctrlKey: true })
    await waitFor(() => expect(prompt).toHaveBeenCalledTimes(1))
    expect(sent(prompt)[0]).toMatchObject({ pane: 'p1', text: '长按产生的内容', submit: true })
  })

  it('shows a disconnected placeholder while the host cannot send', () => {
    render(<Composer hostID="host" paneID="p1" sendDisabled placeholder="主机未连接，暂不能发送" {...props}/>)
    expect(screen.getByRole('textbox', { name: '本地输入内容' })).toHaveAttribute('placeholder', '主机未连接，暂不能发送')
  })

  it('keeps the textarea editable while send is blocked, and refuses drafts without a pane', () => {
    const prompt = mockPrompt()
    const { rerender } = render(<Composer hostID="host" paneID="p1" sendDisabled {...props}/>)
    const box = screen.getByRole('textbox', { name: '本地输入内容' })
    expect(box).not.toBeDisabled()
    typeLocal('offline draft')
    expect(screen.getByRole('button', { name: '发送' })).toBeDisabled()
    expect(readComposerDraft('host', 'p1')).toBe('offline draft')
    rerender(<Composer hostID="host" paneID="" sendDisabled {...props}/>)
    expect(screen.getByRole('textbox', { name: '本地输入内容' })).toBeDisabled()
    expect(readComposerDraft('host', '')).toBe('')
    expect(prompt).not.toHaveBeenCalled()
  })

  it('restores unknown send state after remount instead of looking idle', async () => {
    const { prompt, calls } = deferredPrompt()
    const view = render(<Composer hostID="host" paneID="p1" {...props} compact/>)
    typeLocal('maybe delivered')
    fireEvent.click(screen.getByRole('button', { name: '发送' }))
    await waitFor(() => expect(screen.getByRole('status')).toHaveTextContent('发送中'))
    view.unmount()
    expect(composerInFlight('host', 'p1')).toBe(true)
    render(<Composer hostID="host" paneID="p1" {...props} compact/>)
    expect(screen.getByRole('status')).toHaveTextContent('发送中')
    expect(screen.getByRole('button', { name: '发送' })).toBeDisabled()
    // 网络断在半路：请求可能已到达服务端，结果未知。
    await act(async () => calls[0].reject(new TypeError('Failed to fetch')))
    await waitFor(() => expect(screen.getByRole('status')).toHaveTextContent('结果未知'))
    expect(screen.getByRole('status')).toHaveTextContent('网络中断，没有收到服务端的回应。内容已保留，请先核对终端结果再手动重试，不会自动重发')
    expect(screen.getByRole('textbox', { name: '本地输入内容' })).toHaveValue('maybe delivered')
    expect(prompt).toHaveBeenCalledTimes(1)
  })

  it('reuses the message id for a manual retry after a lost response, and rotates it after the server reports an uncertain send', async () => {
    const { prompt, calls } = deferredPrompt()
    render(<Composer hostID="host" paneID="p1" {...props}/>)
    typeLocal('maybe delivered')
    fireEvent.click(screen.getByRole('button', { name: '发送' }))
    await waitFor(() => expect(calls).toHaveLength(1))
    await act(async () => calls[0].reject(new TypeError('Failed to fetch')))
    await waitFor(() => expect(readComposerSend('host', 'p1').status).toBe('unknown'))
    await new Promise((resolve) => setTimeout(resolve, 25))
    expect(prompt).toHaveBeenCalledTimes(1)
    // 用户看过终端后手动重试：同一段正文沿用同一个 id，服务端据此去重。
    fireEvent.click(screen.getByRole('button', { name: '发送' }))
    await waitFor(() => expect(calls).toHaveLength(2))
    const [first, second] = sent(prompt)
    expect(second.client_msg_id).toBe(first.client_msg_id)
    await act(async () => calls[1].reject(new APIError(409, 'delivery_unknown', '上一次发送结果不确定，请先查看终端再决定是否重发')))
    await waitFor(() => expect(screen.getByRole('status')).toHaveTextContent('结果未知'))
    expect(screen.getByRole('textbox', { name: '本地输入内容' })).toHaveValue('maybe delivered')
    // 服务端明确说结果不确定后，再点发送就是一条新消息。
    fireEvent.click(screen.getByRole('button', { name: '发送' }))
    await waitFor(() => expect(calls).toHaveLength(3))
    expect(sent(prompt)[2].client_msg_id).not.toBe(first.client_msg_id)
  })

  it('retains the draft on a confirmed failure without replaying', async () => {
    const { prompt, calls } = deferredPrompt()
    render(<Composer hostID="host" paneID="p1" {...props}/>)
    typeLocal('keep me')
    fireEvent.click(screen.getByRole('button', { name: '发送' }))
    await waitFor(() => expect(calls).toHaveLength(1))
    await act(async () => calls[0].reject(new APIError(502, 'pane_read_failed', '读取终端画面失败，什么都没有发送。请检查主机连接后重试，或直接在终端里操作')))
    await waitFor(() => expect(screen.getByRole('status')).toHaveTextContent('发送失败：读取终端画面失败'))
    expect(screen.getByRole('textbox', { name: '本地输入内容' })).toHaveValue('keep me')
    await new Promise((resolve) => setTimeout(resolve, 25))
    expect(prompt).toHaveBeenCalledTimes(1)
  })

  it('shows a dialog card instead of typing into an open dialog, and answers through it', async () => {
    mockPrompt(() => Promise.reject(new APIError(409, 'pane_dialog', '终端正弹着一个选择对话框', { code: 'pane_dialog', dialog })))
    vi.spyOn(api, 'paneDialog').mockResolvedValue({ dialog })
    const answer = vi.spyOn(api, 'answerPaneDialog').mockResolvedValue({ ok: true })
    render(<Composer hostID="host" paneID="p1" {...props}/>)
    typeLocal('1')
    fireEvent.click(screen.getByRole('button', { name: '发送' }))
    await waitFor(() => expect(screen.getByRole('group', { name: 'Do you want to proceed?' })).toBeInTheDocument())
    expect(readComposerSend('host', 'p1').status).toBe('refused')
    expect(screen.getAllByText(/未发送：终端正弹着一个选择对话框/).length).toBeGreaterThan(0)
    expect(screen.getByRole('textbox', { name: '本地输入内容' })).toHaveValue('1')
    fireEvent.click(screen.getByRole('button', { name: /No/ }))
    await waitFor(() => expect(answer).toHaveBeenCalledWith('host', 'p1', dialog.fingerprint, 2))
  })

  it('asks before sending on top of a draft typed at the computer, and forces only after confirmation', async () => {
    const prompt = mockPrompt()
    prompt.mockImplementationOnce(() => Promise.reject(new APIError(409, 'pane_draft', '电脑上的输入框里有未发送的内容')))
    render(<Composer hostID="host" paneID="p1" {...props}/>)
    typeLocal('接着说')
    fireEvent.click(screen.getByRole('button', { name: '发送' }))
    const ask = await screen.findByRole('alertdialog', { name: '电脑上有未发送的输入，仍要发送吗？' })
    expect(ask).toBeInTheDocument()
    expect(prompt).toHaveBeenCalledTimes(1)
    fireEvent.click(screen.getByRole('button', { name: '取消' }))
    await waitFor(() => expect(screen.queryByRole('alertdialog')).not.toBeInTheDocument())
    expect(prompt).toHaveBeenCalledTimes(1)
    expect(readComposerDraft('host', 'p1')).toBe('接着说')

    prompt.mockImplementationOnce(() => Promise.reject(new APIError(409, 'pane_draft', '电脑上的输入框里有未发送的内容')))
    fireEvent.click(screen.getByRole('button', { name: '发送' }))
    await screen.findByRole('alertdialog')
    fireEvent.click(screen.getByRole('button', { name: '仍要发送' }))
    await waitFor(() => expect(readComposerSend('host', 'p1').status).toBe('delivered'))
    const calls = sent(prompt)
    expect(calls.map((call) => call.force)).toEqual([undefined, undefined, true])
    expect(new Set(calls.map((call) => call.client_msg_id)).size).toBe(1)
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
    const prompt = mockPrompt()
    render(<Composer hostID="host" paneID="p1" {...props} variant="chat"/>)
    const box = screen.getByRole('textbox', { name: '对话输入内容' })
    expect(screen.getByRole('button', { name: '发送' })).toBeDisabled()
    fireEvent.keyDown(box, { key: 'Enter' })
    fireEvent.paste(box, { clipboardData: { items: [{ kind: 'file', getAsFile: () => new File(['x'], 'a.png', { type: 'image/png' }) }], files: [] } })
    expect(prompt).not.toHaveBeenCalled()
  })

  it('sends an image-only message once: composes with the media host, then settles', async () => {
    const host = mediaHost({ canSend: vi.fn(() => true), compose: vi.fn(() => '/remote/a.png') })
    const prompt = mockPrompt()
    render(<Composer hostID="host" paneID="p1" {...props} variant="chat" mediaHost={host}/>)
    const button = screen.getByRole('button', { name: '发送' })
    expect(button).toBeEnabled()

    fireEvent.click(button)
    await waitFor(() => expect(host.onSettled).toHaveBeenCalledWith('delivered'))
    expect(host.compose).toHaveBeenCalledWith('')
    expect(sent(prompt)).toEqual([expect.objectContaining({ pane: 'p1', text: '/remote/a.png', submit: true })])
  })

  it('sends text plus references as one submission and never types at paste time', async () => {
    const host = mediaHost({ compose: vi.fn((draft: string) => `${draft}\n/remote/a.png`) })
    const onPasteImages = vi.fn()
    const prompt = mockPrompt()
    render(<Composer hostID="host" paneID="p1" {...props} variant="chat" onPasteImages={onPasteImages} mediaHost={host}/>)
    const box = screen.getByRole('textbox', { name: '对话输入内容' })
    const shot = new File(['x'], 'shot.png', { type: 'image/png' })

    fireEvent.paste(box, { clipboardData: { items: [{ kind: 'file', getAsFile: () => shot }], files: [] } })
    // 粘贴只交给媒体层暂存：不发送、不走旧的整体上传入口。
    expect(host.onFiles).toHaveBeenCalledWith([shot])
    expect(onPasteImages).not.toHaveBeenCalled()
    expect(prompt).not.toHaveBeenCalled()

    fireEvent.change(box, { target: { value: '看这张图' } })
    fireEvent.keyDown(box, { key: 'Enter' })
    // 正文与引用合成**一次**提交。
    await waitFor(() => expect(prompt).toHaveBeenCalledTimes(1))
    expect(sent(prompt)[0]).toMatchObject({ pane: 'p1', text: '看这张图\n/remote/a.png', submit: true })
  })

  it('settles a refused send as not delivered so attachments stay', async () => {
    const host = mediaHost({ canSend: vi.fn(() => true), compose: vi.fn(() => '/remote/a.png') })
    mockPrompt(() => Promise.reject(new APIError(409, 'pane_dialog', '终端正弹着一个选择对话框', { dialog })))
    vi.spyOn(api, 'paneDialog').mockResolvedValue({ dialog })
    render(<Composer hostID="host" paneID="p1" {...props} variant="chat" mediaHost={host}/>)
    fireEvent.click(screen.getByRole('button', { name: '发送' }))
    await waitFor(() => expect(host.onSettled).toHaveBeenCalledWith('refused'))
  })

  it('abandons the send when the media host refuses to compose', async () => {
    const host = mediaHost({ canSend: vi.fn(() => true), compose: vi.fn(() => null) })
    const prompt = mockPrompt()
    render(<Composer hostID="host" paneID="p1" {...props} variant="chat" mediaHost={host}/>)
    fireEvent.click(screen.getByRole('button', { name: '发送' }))
    expect(prompt).not.toHaveBeenCalled()
    expect(readComposerSend('host', 'p1').status).toBe('idle')
  })

  it('keeps medium edits blocked while the media host is busy and renders its tray above the input', () => {
    const host = mediaHost({ canSend: vi.fn(() => false) })
    render(<Composer hostID="host" paneID="p1" {...props} variant="chat" mediaHost={{ ...host, tray: <p>图片附件占位区</p> }}/>)
    const tray = screen.getByText('图片附件占位区')
    const box = screen.getByRole('textbox', { name: '对话输入内容' })
    expect(screen.getByRole('button', { name: '发送' })).toBeDisabled()
    // 占位区必须在 textarea 之前（上方），且不抢走输入焦点。
    expect(tray.compareDocumentPosition(box) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
  })
})

describe('Composer chat variant', () => {
  it('sends exactly once with Enter, keeps Shift+Enter and IME Enter as newlines, and has no second mode switch', async () => {
    const prompt = mockPrompt()
    render(<Composer hostID="host" paneID="p1" {...props} variant="chat"/>)
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
    expect(prompt).not.toHaveBeenCalled()

    fireEvent.keyDown(box, { key: 'Enter' })
    await waitFor(() => expect(prompt).toHaveBeenCalledTimes(1))
    expect(sent(prompt)[0]).toMatchObject({ pane: 'p1', text: '第一行', submit: true })
    // 发送后草稿清空，重复按键不会再发一次。
    await waitFor(() => expect(box).toHaveValue(''))
    fireEvent.keyDown(box, { key: 'Enter' })
    expect(prompt).toHaveBeenCalledTimes(1)
  })

  it('keeps the chat draft and never sends while the host is disconnected', () => {
    const prompt = mockPrompt()
    render(<Composer hostID="host" paneID="p1" {...props} variant="chat" sendDisabled/>)
    const box = screen.getByRole('textbox', { name: '对话输入内容' })
    fireEvent.change(box, { target: { value: '断线时输入的内容' } })
    fireEvent.keyDown(box, { key: 'Enter' })
    expect(prompt).not.toHaveBeenCalled()
    expect(screen.getByRole('button', { name: '发送' })).toBeDisabled()
    expect(readComposerDraft('host', 'p1')).toBe('断线时输入的内容')
    expect(readComposerSend('host', 'p1').status).toBe('idle')
  })

  it('sends with Ctrl/Cmd+Enter while Shift+Enter keeps newlines even with extra modifiers', async () => {
    const prompt = mockPrompt()
    render(<Composer hostID="host" paneID="p1" {...props} variant="chat"/>)
    const box = screen.getByRole('textbox', { name: '对话输入内容' })
    fireEvent.change(box, { target: { value: '第一行' } })
    fireEvent.keyDown(box, { key: 'Enter', shiftKey: true, ctrlKey: true })
    fireEvent.keyDown(box, { key: 'Enter', shiftKey: true, metaKey: true })
    fireEvent.keyDown(box, { key: 'Enter', shiftKey: true, altKey: true })
    expect(prompt).not.toHaveBeenCalled()
    fireEvent.keyDown(box, { key: 'Enter', ctrlKey: true })
    await waitFor(() => expect(prompt).toHaveBeenCalledTimes(1))
    await waitFor(() => expect(box).toHaveValue(''))
    fireEvent.change(box, { target: { value: '第二行' } })
    fireEvent.keyDown(box, { key: 'Enter', metaKey: true })
    await waitFor(() => expect(prompt).toHaveBeenCalledTimes(2))
    expect(sent(prompt).map((call) => call.text)).toEqual(['第一行', '第二行'])
  })

  it('ignores repeated Enter keydown and presses on an empty draft', async () => {
    const prompt = mockPrompt()
    render(<Composer hostID="host" paneID="p1" {...props} variant="chat"/>)
    const box = screen.getByRole('textbox', { name: '对话输入内容' })
    // 空内容与纯空白都不能发送。
    fireEvent.keyDown(box, { key: 'Enter' })
    fireEvent.change(box, { target: { value: '   ' } })
    fireEvent.keyDown(box, { key: 'Enter' })
    expect(prompt).not.toHaveBeenCalled()
    // 长按 Enter：repeat 的 keydown 不发送，只有单次按键才发送。
    fireEvent.change(box, { target: { value: '长按' } })
    fireEvent.keyDown(box, { key: 'Enter', repeat: true })
    fireEvent.keyDown(box, { key: 'Enter', repeat: true })
    expect(prompt).not.toHaveBeenCalled()
    fireEvent.keyDown(box, { key: 'Enter' })
    await waitFor(() => expect(prompt).toHaveBeenCalledTimes(1))
    expect(sent(prompt)[0]).toMatchObject({ pane: 'p1', text: '长按' })
  })

  it('does not send twice while a chat message is still in flight', async () => {
    const { prompt, calls } = deferredPrompt()
    render(<Composer hostID="host" paneID="p1" {...props} variant="chat"/>)
    const box = screen.getByRole('textbox', { name: '对话输入内容' })
    fireEvent.change(box, { target: { value: '发送中的内容' } })
    fireEvent.keyDown(box, { key: 'Enter' })
    fireEvent.keyDown(box, { key: 'Enter' })
    fireEvent.keyDown(box, { key: 'Enter', ctrlKey: true })
    await waitFor(() => expect(screen.getByRole('status')).toHaveTextContent('发送中'))
    expect(prompt).toHaveBeenCalledTimes(1)
    await act(async () => calls[0].resolve())
    await waitFor(() => expect(readComposerSend('host', 'p1').status).toBe('delivered'))
    expect(prompt).toHaveBeenCalledTimes(1)
  })
})
