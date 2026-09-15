import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { api, invalidateAuthentication } from '../../lib/api'
import { clearComposerDrafts, readComposerDraft, readComposerSend, runComposerSend } from '../../lib/composerDrafts'
import { ChatMediaComposer } from './ChatMediaComposer'

const user = { id: 'user', email: 'user@example.test', role: 'user', display_name: 'User' }

async function login(sessionID = 'media-ui-session') {
  vi.stubGlobal('fetch', vi.fn().mockResolvedValue(
    new Response(JSON.stringify({ user, csrf_token: 'csrf', session_id: sessionID })),
  ))
  await api.login({ email: 'user@example.test', password: 'test' })
}

function imageFile(name = 'a.png', size = 1024, type = 'image/png') {
  const file = new File([new Uint8Array(1)], name, { type })
  Object.defineProperty(file, 'size', { value: size })
  return file
}

function dropData(files: File[]) {
  return { dataTransfer: { types: ['Files'], items: [], files, dropEffect: 'none' } }
}

function deferred<T>() {
  let resolve!: (value: T) => void
  let reject!: (error: unknown) => void
  const promise = new Promise<T>((res, rej) => { resolve = res; reject = rej })
  return { promise, resolve, reject }
}

type Overrides = Partial<Parameters<typeof ChatMediaComposer>[0]>

/** 默认 stageImage 的 mock 类型：断言调用时需要 `.mock`。 */
type StageMock = ReturnType<typeof vi.fn<(hostID: string, paneID: string, file: File) => Promise<{ path: string }>>>

function renderComposer(overrides: Overrides = {}) {
  const submit = overrides.submit ?? vi.fn(async () => {})
  const stageMock: StageMock = vi.fn(async (_host: string, _pane: string, file: File) => ({ path: `/remote/${file.name}` }))
  const stageImage = overrides.stageImage ?? stageMock
  const view = render(<ChatMediaComposer
    hostID="host"
    paneID="p1"
    visible
    variant="chat"
    onDirectInput={() => {}}
    onLocalInput={() => {}}
    send={runComposerSend}
    readSend={readComposerSend}
    submit={submit}
    stageImage={stageImage}
    {...overrides}
  />)
  const wrapper = view.container.querySelector('.chat-media-composer') as HTMLElement
  return { ...view, wrapper, submit, stageImage, stageMock }
}

function tray() { return screen.queryByRole('list', { name: /图片附件/ }) }
function sendButton() { return screen.getByRole('button', { name: '发送' }) }
function textarea() { return screen.getByRole('textbox', { name: '对话输入内容' }) }

beforeEach(async () => {
  invalidateAuthentication()
  clearComposerDrafts()
  Object.defineProperty(URL, 'createObjectURL', { configurable: true, value: () => 'blob:ui-preview' })
  Object.defineProperty(URL, 'revokeObjectURL', { configurable: true, value: () => {} })
  await login()
})

afterEach(() => {
  clearComposerDrafts()
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
})

describe('ChatMediaComposer 图片附件', () => {
  it('拖入多张图片只落占位并 stage-only 上传，不向终端打任何字', async () => {
    const { wrapper, stageImage, submit } = renderComposer()
    fireEvent.dragOver(wrapper, dropData([imageFile('a.png')]))
    expect(wrapper).toHaveAttribute('data-drag', 'true')

    fireEvent.drop(wrapper, dropData([imageFile('a.png'), imageFile('b.png')]))
    expect(wrapper).not.toHaveAttribute('data-drag')

    expect(stageImage).toHaveBeenCalledTimes(2)
    expect(tray()).toBeInTheDocument()
    expect(within(tray() as HTMLElement).getAllByRole('listitem')).toHaveLength(2)
    expect(screen.getByText('a.png')).toBeInTheDocument()

    // 拖拽绝不触发发送，也不改草稿。
    expect(submit).not.toHaveBeenCalled()
    expect(readComposerDraft('host', 'p1')).toBe('')
    expect(readComposerSend('host', 'p1').status).toBe('idle')
    await waitFor(() => expect(screen.getAllByText('已就绪')).toHaveLength(2))
  })

  it('在输入框粘贴图片走同一条占位通道，而不是把文件塞进终端', async () => {
    const { stageImage, submit } = renderComposer()
    fireEvent.paste(textarea(), { clipboardData: { items: [{ kind: 'file', getAsFile: () => imageFile('shot.png') }], files: [] } })
    expect(stageImage).toHaveBeenCalledTimes(1)
    await waitFor(() => expect(screen.getByText('已就绪')).toBeInTheDocument())
    expect(submit).not.toHaveBeenCalled()
    expect(readComposerDraft('host', 'p1')).toBe('')
  })

  it('「添加图片」按钮读取的文件与拖拽同一路径', async () => {
    const { stageMock } = renderComposer()
    const input = document.querySelector('.chat-media-file') as HTMLInputElement
    fireEvent.change(input, { target: { files: [imageFile('picked.png')] } })
    expect(stageMock).toHaveBeenCalledTimes(1)
    expect(stageMock.mock.calls[0][2].name).toBe('picked.png')
    // 选择后清空 input，允许重复选择同一文件。
    expect(input.value).toBe('')
  })

  it('类型不合规的文件显示失败占位与中文原因，且不发请求', async () => {
    const { stageImage } = renderComposer()
    fireEvent.drop(document.querySelector('.chat-media-composer') as HTMLElement, dropData([imageFile('notes.txt', 8, 'text/plain')]))
    expect(stageImage).not.toHaveBeenCalled()
    expect(screen.getByRole('alert')).toHaveTextContent('仅支持 PNG、JPEG、WebP、GIF 图片')
  })

  it('上传中禁用发送；失败后可重试，重试只重传这一张', async () => {
    const gate = deferred<{ path: string }>()
    const stageImage = vi.fn()
      .mockImplementationOnce(() => gate.promise)
      .mockResolvedValueOnce({ path: '/remote/retry.png' })
    const { wrapper } = renderComposer({ stageImage })

    fireEvent.change(textarea(), { target: { value: '看看这张' } })
    fireEvent.drop(wrapper, dropData([imageFile('retry.png')]))
    expect(sendButton()).toBeDisabled()

    gate.reject(new Error('连接已断开'))
    await waitFor(() => expect(screen.getByRole('alert')).toHaveTextContent('连接已断开'))
    expect(sendButton()).toBeEnabled()

    fireEvent.click(screen.getByRole('button', { name: '重试 retry.png' }))
    await waitFor(() => expect(screen.getByText('已就绪')).toBeInTheDocument())
    expect(stageImage).toHaveBeenCalledTimes(2)
  })

  it('发送时把正文与已 staged 的引用合成一次提交，成功后清空附件', async () => {
    const submit = vi.fn(async () => {})
    const { wrapper } = renderComposer({ submit })
    fireEvent.change(textarea(), { target: { value: '看这两张图' } })
    fireEvent.drop(wrapper, dropData([imageFile('a.png'), imageFile('b.png')]))
    await waitFor(() => expect(screen.getAllByText('已就绪')).toHaveLength(2))

    fireEvent.click(sendButton())
    // 一次逻辑发送 = 两腿：第一腿整段正文（含引用行），第二腿只补一次回车。
    await waitFor(() => expect(submit).toHaveBeenCalledTimes(2))
    expect(submit).toHaveBeenNthCalledWith(1, 'p1', '看这两张图\n/remote/a.png\n/remote/b.png', [])
    expect(submit).toHaveBeenNthCalledWith(2, 'p1', '', ['Enter'])
    await waitFor(() => expect(tray()).not.toBeInTheDocument())
    expect(readComposerDraft('host', 'p1')).toBe('')
  })

  it('发送失败保留附件与草稿，且不自动重放', async () => {
    const submit = vi.fn(async () => { throw new Error('连接已断开') })
    const { wrapper } = renderComposer({ submit })
    fireEvent.change(textarea(), { target: { value: '看这张图' } })
    fireEvent.drop(wrapper, dropData([imageFile('a.png')]))
    await waitFor(() => expect(screen.getByText('已就绪')).toBeInTheDocument())

    fireEvent.click(sendButton())
    // Composer 的状态行同时渲染完整/紧凑两份文案，这里按内容匹配。
    await waitFor(() => expect(
      screen.getAllByRole('status').some((node) => node.textContent?.includes('结果未知')),
    ).toBe(true))
    expect(tray()).toBeInTheDocument()
    expect(screen.getByText('已就绪')).toBeInTheDocument()
    // 失败时保留的是**本次提交文本**（正文 + 引用行）：附件没丢，手动重试时合成是幂等的。
    expect(readComposerDraft('host', 'p1')).toBe('看这张图\n/remote/a.png')
    expect(submit).toHaveBeenCalledTimes(1)
  })

  it('失败后手动重试只重发同一条提交文本，引用不会被追加第二遍', async () => {
    const submit = vi.fn()
      .mockRejectedValueOnce(new Error('连接已断开'))
      .mockResolvedValue(undefined)
    const { wrapper } = renderComposer({ submit })
    fireEvent.change(textarea(), { target: { value: '看这张图' } })
    fireEvent.drop(wrapper, dropData([imageFile('a.png')]))
    await waitFor(() => expect(screen.getByText('已就绪')).toBeInTheDocument())

    fireEvent.click(sendButton())
    // 第一腿（正文）被拒：本次发送到此为止，第二腿的回车绝不补发。
    await waitFor(() => expect(submit).toHaveBeenCalledTimes(1))
    expect(submit).toHaveBeenNthCalledWith(1, 'p1', '看这张图\n/remote/a.png', [])
    expect(screen.getByText('已就绪')).toBeInTheDocument()

    fireEvent.click(sendButton())
    // 手动重试重发的是同一条提交文本：第一腿逐字节相同（引用行没有被追加第二遍），
    // 只有第一腿成功之后才补第二腿的回车。
    await waitFor(() => expect(submit).toHaveBeenCalledTimes(3))
    expect(submit).toHaveBeenNthCalledWith(2, 'p1', '看这张图\n/remote/a.png', [])
    expect(submit).toHaveBeenNthCalledWith(3, 'p1', '', ['Enter'])
    await waitFor(() => expect(tray()).not.toBeInTheDocument())
  })

  it('移除占位后列表为空，且迟到的上传结果不会复活它', async () => {
    const gate = deferred<{ path: string }>()
    const { wrapper } = renderComposer({ stageImage: vi.fn(() => gate.promise) })
    fireEvent.drop(wrapper, dropData([imageFile('a.png')]))
    expect(within(tray() as HTMLElement).getAllByRole('listitem')).toHaveLength(1)
    fireEvent.click(screen.getByRole('button', { name: '移除图片 a.png' }))
    expect(tray()).not.toBeInTheDocument()

    await act(async () => { gate.resolve({ path: '/remote/a.png' }) })
    expect(tray()).not.toBeInTheDocument()
  })

  it('切换 pane 时不显示其它 pane 的附件', async () => {
    const stageImage = vi.fn(async (_host: string, _pane: string, file: File) => ({ path: `/remote/${file.name}` }))
    const { wrapper, rerender, submit } = renderComposer({ stageImage })
    fireEvent.drop(wrapper, dropData([imageFile('a.png')]))
    await waitFor(() => expect(screen.getByText('已就绪')).toBeInTheDocument())

    rerender(<ChatMediaComposer
      hostID="host" paneID="p2" visible variant="chat"
      onDirectInput={() => {}} onLocalInput={() => {}}
      send={runComposerSend} readSend={readComposerSend} submit={submit} stageImage={stageImage}
    />)
    expect(tray()).not.toBeInTheDocument()
  })

  it('可见时渲染语音槽位；不可见时整体不渲染', () => {
    const { rerender, submit, stageImage } = renderComposer({ voice: <button type="button">语音输入</button> })
    expect(screen.getByRole('button', { name: '语音输入' })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: '添加图片' })).toBeInTheDocument()

    rerender(<ChatMediaComposer
      hostID="host" paneID="p1" visible={false} variant="chat"
      onDirectInput={() => {}} onLocalInput={() => {}}
      send={runComposerSend} readSend={readComposerSend} submit={submit} stageImage={stageImage}
    />)
    expect(screen.queryByRole('button', { name: '添加图片' })).not.toBeInTheDocument()
    expect(screen.queryByRole('textbox', { name: '对话输入内容' })).not.toBeInTheDocument()
  })

  // 接线后（契约 §7.4「仅图片也能发」）：空正文 + 有 staged 附件时发送按钮可用，
  // 且整条提交事务只跑一次，提交文本就是引用行本身。
  it('仅图片（空正文）也能发送：提交文本就是引用行本身，只提交一次', async () => {
    const submit = vi.fn(async () => {})
    const { wrapper } = renderComposer({ submit })
    fireEvent.drop(wrapper, dropData([imageFile('a.png')]))
    await waitFor(() => expect(screen.getByText('已就绪')).toBeInTheDocument())
    expect(sendButton()).toBeEnabled()

    fireEvent.click(sendButton())
    // 提交文本就是引用行本身，仍然按两腿发：正文腿 + 回车腿。
    await waitFor(() => expect(submit).toHaveBeenCalledTimes(2))
    expect(submit).toHaveBeenNthCalledWith(1, 'p1', '/remote/a.png', [])
    expect(submit).toHaveBeenNthCalledWith(2, 'p1', '', ['Enter'])
    expect(readComposerDraft('host', 'p1')).toBe('')
    await waitFor(() => expect(tray()).not.toBeInTheDocument())
  })

  it('空正文且没有可用附件时仍然不能发送', async () => {
    const submit = vi.fn(async () => {})
    const { wrapper } = renderComposer({ submit })
    fireEvent.drop(wrapper, dropData([imageFile('notes.txt', 8, 'text/plain')]))
    expect(screen.getByRole('alert')).toHaveTextContent('仅支持 PNG、JPEG、WebP、GIF 图片')
    expect(sendButton()).toBeDisabled()
    fireEvent.click(sendButton())
    fireEvent.keyDown(textarea(), { key: 'Enter' })
    expect(submit).not.toHaveBeenCalled()
  })

  it.each([false, true])('发送期间新加的图片不会打断提交或被结算清掉（上传挂起：%s）', async (pendingUpload) => {
    // 两腿各占一个 deferred：放行第一腿后才会发出第二腿，逐腿控制时序。
    const legs: Array<() => void> = []
    const submit = vi.fn(() => new Promise<void>((resolve) => { legs.push(resolve) }))
    let finishUpload!: () => void
    const stageImage = vi.fn(async (_host: string, _pane: string, file: File) => {
      if (pendingUpload && file.name === 'b.png') await new Promise<void>((resolve) => { finishUpload = resolve })
      return { path: `/remote/${file.name}` }
    })
    const { wrapper } = renderComposer({ submit, stageImage })
    fireEvent.change(textarea(), { target: { value: '看这张图' } })
    fireEvent.drop(wrapper, dropData([imageFile('a.png')]))
    await waitFor(() => expect(screen.getByText('已就绪')).toBeInTheDocument())

    fireEvent.click(sendButton())
    await waitFor(() => expect(submit).toHaveBeenCalledTimes(1))
    expect(submit).toHaveBeenNthCalledWith(1, 'p1', '看这张图\n/remote/a.png', [])
    // 发送在飞时又加了一张：它不属于本次提交快照。
    fireEvent.drop(wrapper, dropData([imageFile('b.png')]))
    if (!pendingUpload) await waitFor(() => expect(screen.getAllByText('已就绪')).toHaveLength(2))

    // 两腿都放行才算送达；中间新加的 b.png 不参与本次提交。
    await act(async () => { legs[0]() })
    await waitFor(() => expect(submit).toHaveBeenCalledTimes(2))
    expect(submit).toHaveBeenNthCalledWith(2, 'p1', '', ['Enter'])
    await act(async () => { legs[1]() })
    await waitFor(() => expect(readComposerSend('host', 'p1').status).toBe('delivered'))
    if (pendingUpload) await act(async () => finishUpload())
    // 只清掉本次提交过的那张，发送期间新加的仍然在。
    await waitFor(() => expect(screen.getAllByText('已就绪')).toHaveLength(1))
    expect(screen.getByText('b.png')).toBeInTheDocument()
    expect(screen.queryByText('a.png')).toBeNull()
  })
})
