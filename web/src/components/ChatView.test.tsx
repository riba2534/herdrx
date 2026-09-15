import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { ChatView } from './ChatView'
import { clearComposerDrafts, readComposerDraft } from '../lib/composerDrafts'
import { api, invalidateAuthentication } from '../lib/api'
import type { WorkbenchClient } from '../lib/workbench'
import type { ChatRecord } from '../lib/structuredChatTypes'
import type { Pane } from '../types'

/**
 * 语音替身：默认「未启用」，只有显式 enable() 的用例才会出现麦克风入口。
 * JSON 事件与 {t:'start'} 帧都不出浏览器，所以这里只需要契约里的 VoiceTransport 形状。
 */
const voiceMock = vi.hoisted(() => {
  const state = {
    capabilities: {
      enabled: false,
      protocol: 'openai-realtime' as const,
      model: '',
      inputSampleRate: 24000,
      outputSampleRate: 24000,
      audioReply: 'off' as const,
      maxSessionSeconds: 0,
    },
    events: [] as Array<(event: unknown) => void>,
    opened: [] as string[],
    closes: [] as string[],
  }
  return {
    state,
    enable() {
      state.capabilities = { ...state.capabilities, enabled: true, model: 'gpt-realtime', maxSessionSeconds: 600 }
    },
    reset() {
      state.capabilities = { ...state.capabilities, enabled: false, model: '', maxSessionSeconds: 0 }
      state.events = []
      state.opened = []
      state.closes = []
    },
    emit(event: unknown) {
      for (const handler of Array.from(state.events)) handler(event)
    },
  }
})

vi.mock('../lib/voiceClient', () => ({
  voiceTransport: {
    capabilities: async () => voiceMock.state.capabilities,
    open: async (input: { output: 'text' | 'audio' }) => {
      voiceMock.state.opened.push(input.output)
      return {
        sampleRate: 24000,
        sendAudio: () => {},
        commit: () => {},
        cancelResponse: () => {},
        close: (reason: string) => { voiceMock.state.closes.push(reason) },
        onEvent: (handler: (event: unknown) => void) => {
          voiceMock.state.events.push(handler)
          return () => { voiceMock.state.events = voiceMock.state.events.filter((item) => item !== handler) }
        },
        onAudio: () => () => {},
      }
    },
  },
}))

vi.mock('../lib/voiceCapture', () => ({
  voiceSupport: () => ({ ok: true }),
  startVoiceCapture: async (options: { sampleRate: number; onFrame: (pcm: ArrayBuffer) => void }) => ({
    sampleRate: options.sampleRate,
    degraded: false,
    stop: () => {},
  }),
  createVoicePlayback: () => ({ push: () => {}, interrupt: () => {}, stop: () => {} }),
}))

function imageFile(name = 'shot.png', type = 'image/png') {
  return new File([new Uint8Array(1)], name, { type })
}

function dropData(files: File[]) {
  return { dataTransfer: { types: ['Files'], items: [], files, dropEffect: 'none' } }
}

const basePane: Pane = {
  pane_id: 'p1', workspace_id: 'w1', tab_id: 't1', terminal_id: 'term1',
  agent_status: 'idle', agent: 'claude', cwd: '/home/user/project', focused: true, revision: 1,
}

const candidatesPage = {
  supported: true,
  agent: 'claude',
  candidates: [
    { id: 'cand-1', agent: 'claude', session_id: 'aaaa1111bbbb', updated_at: '2026-09-13T04:00:00Z' },
    { id: 'cand-2', agent: 'claude', session_id: 'cccc2222dddd', updated_at: '2026-09-12T09:00:00Z' },
  ],
  messages: [],
}

function boundPage(messages: ChatRecord[], extra: Record<string, unknown> = {}) {
  return { supported: true, agent: 'claude', session_id: 'aaaa1111bbbb', binding: 'selected', messages, next_cursor: 'cur-1', ...extra }
}

/** 只返回结构化端点的响应；任何 `pane.read` 之类的调用都会走到兜底分支并被断言抓住。 */
function transcriptServer(handler: (url: string, index: number) => unknown) {
  let index = 0
  const mock = vi.fn(async (input: RequestInfo | URL) => {
    const url = String(input)
    const body = handler(url, index++)
    if (body instanceof Response) return body
    return new Response(JSON.stringify(body), { status: 200 })
  })
  vi.stubGlobal('fetch', mock)
  return mock
}

function sequence(...pages: unknown[]) {
  return (url: string, index: number) => {
    if (url.includes('/transcript') === false) return { supported: false, reason: 'internal_error', messages: [] }
    return pages[Math.min(index, pages.length - 1)]
  }
}

function createClient() {
  return { call: vi.fn().mockRejectedValue(new Error('Chat 视图不应调用任何 RPC')), getHostID: () => 'host' } as unknown as WorkbenchClient
}

function props(overrides: Record<string, unknown> = {}) {
  return {
    hostID: 'host', pane: basePane, connected: true, compact: false,
    client: createClient(),
    submit: vi.fn().mockResolvedValue(undefined),
    onSwitchToTerminal: vi.fn(),
    ...overrides,
  } as Parameters<typeof ChatView>[0]
}

function text(id: string, role: ChatRecord['role'], body: string): ChatRecord {
  return { id, role, blocks: [{ type: 'text', text: body }] }
}

const user = { id: 'user', email: 'user@example.test', role: 'user' as const, display_name: 'User' }

beforeEach(async () => {
  invalidateAuthentication()
  clearComposerDrafts()
  voiceMock.reset()
  localStorage.clear()
  sessionStorage.clear()
  // Composer 的发送事务要求存在 Web 登录会话；结构化取数本身只依赖下面的 fetch 桩件。
  vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response(JSON.stringify({ user, csrf_token: 'csrf', session_id: 'login-session' }))))
  await api.login({ email: 'user@example.test', password: 'test' })
})

afterEach(() => {
  clearComposerDrafts()
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
})

describe('latest-message following', () => {
  function scrollGeometry(log: HTMLElement) {
    const size = { height: 1200, viewport: 400, top: 800 }
    Object.defineProperties(log, {
      scrollHeight: { configurable: true, get: () => size.height },
      clientHeight: { configurable: true, get: () => size.viewport },
      scrollTop: {
        configurable: true, get: () => size.top,
        set: (value: number) => { size.top = Math.max(0, Math.min(value, size.height - size.viewport)) },
      },
    })
    fireEvent.scroll(log)
    return size
  }

  async function openChat() {
    render(<ChatView {...props()}/>)
    fireEvent.click(await screen.findByRole('button', { name: /aaaa1111/ }))
    await screen.findByText('回答中')
    return screen.getByRole('log', { name: '对话记录' })
  }

  it('ignores small bottom gaps and uses separate leave/return thresholds', async () => {
    transcriptServer(sequence(candidatesPage, boundPage([text('a1', 'assistant', '回答中')])))
    const log = await openChat()
    const size = scrollGeometry(log)
    for (const gap of [0, 24, 48, 100, 128]) {
      size.top = 800 - gap
      fireEvent.scroll(log)
      expect(screen.queryByRole('button', { name: '跳到最新' })).not.toBeInTheDocument()
    }
    size.top = 500
    fireEvent.scroll(log)
    expect(screen.getByRole('button', { name: '跳到最新' })).toBeVisible()
    size.top = 710
    fireEvent.scroll(log)
    expect(screen.getByRole('button', { name: '跳到最新' })).toBeVisible()
    size.top = 750
    fireEvent.scroll(log)
    expect(screen.queryByRole('button', { name: '跳到最新' })).not.toBeInTheDocument()
  })

  it('does not resume following a small manual scroll when polling or resizing', async () => {
    transcriptServer(sequence(candidatesPage,
      boundPage([text('a1', 'assistant', '回答中')]),
      boundPage([text('a1', 'assistant', '手动上翻后更新')]),
    ))
    const log = await openChat()
    const size = scrollGeometry(log)
    fireEvent.wheel(log, { deltaY: -48 })
    size.top -= 48
    fireEvent.scroll(log)
    expect(screen.queryByRole('button', { name: '跳到最新' })).not.toBeInTheDocument()
    size.height = 1600
    fireEvent.click(screen.getByRole('button', { name: '刷新会话记录' }))
    await screen.findByText('手动上翻后更新')
    expect(size.top).toBe(752)
    expect(screen.getByRole('button', { name: '跳到最新' })).toBeVisible()
    fireEvent.click(screen.getByRole('button', { name: '跳到最新' }))
    expect(size.top).toBe(1200)
  })

  it('stops following touch and keyboard navigation before a resize event', async () => {
    transcriptServer(sequence(candidatesPage, boundPage([text('a1', 'assistant', '回答中')])))
    const log = await openChat()
    const size = scrollGeometry(log)
    fireEvent.touchStart(log, { touches: [{ clientY: 200 }] })
    fireEvent.touchMove(log, { touches: [{ clientY: 250 }] })
    size.top = 750
    size.height = 1300
    fireEvent.scroll(log)
    expect(size.top).toBe(750)
    fireEvent.click(screen.getByRole('button', { name: '跳到最新' }))
    fireEvent.keyDown(log, { key: 'PageUp' })
    size.top = 850
    size.viewport = 250
    fireEvent.scroll(log)
    expect(size.top).toBe(850)
  })

  it('preserves upward scrollbar movement even when content geometry is stale', async () => {
    transcriptServer(sequence(candidatesPage, boundPage([text('a1', 'assistant', '回答中')])))
    const log = await openChat()
    const size = scrollGeometry(log)
    size.height = 1600
    size.top = 752
    fireEvent.scroll(log)
    expect(size.top).toBe(752)
    expect(log.dataset.following).toBe('false')
    fireEvent.click(screen.getByRole('button', { name: '跳到最新' }))
    expect(size.top).toBe(1200)
    expect(log.dataset.following).toBe('true')
    fireEvent.scroll(log)
    expect(log.dataset.following).toBe('true')
  })

  it('keeps following when document shrink clamps the scroll position', async () => {
    transcriptServer(sequence(candidatesPage, boundPage([text('a1', 'assistant', '回答中')])))
    const log = await openChat()
    const size = scrollGeometry(log)
    size.height = 1000
    size.top = 600
    fireEvent.scroll(log)
    size.height = 1400
    fireEvent.scroll(log)
    expect(size.top).toBe(1000)
  })

  it('measures pane-status commits without waiting for a transcript poll', async () => {
    transcriptServer(sequence(candidatesPage, boundPage([text('a1', 'assistant', '回答中')])))
    const viewProps = props()
    const view = render(<ChatView {...viewProps}/>)
    fireEvent.click(await screen.findByRole('button', { name: /aaaa1111/ }))
    await screen.findByText('回答中')
    const size = scrollGeometry(screen.getByRole('log', { name: '对话记录' }))
    size.height = 1400
    view.rerender(<ChatView {...viewProps} pane={{ ...basePane, agent_status: 'working' }}/>)
    expect(size.top).toBe(1000)
  })

  it('follows same-ID reply growth without relying on message count', async () => {
    transcriptServer(sequence(candidatesPage,
      boundPage([text('a1', 'assistant', '回答中')]),
      boundPage([text('a1', 'assistant', '回答已经变长')]),
    ))
    const log = await openChat()
    const size = scrollGeometry(log)
    size.height = 1700
    fireEvent.click(screen.getByRole('button', { name: '刷新会话记录' }))
    await screen.findByText('回答已经变长')
    expect(size.top).toBe(1300)
    expect(screen.queryByRole('button', { name: '跳到最新' })).not.toBeInTheDocument()
  })

  it('follows layout resizing but does not pull a historical reader to the bottom', async () => {
    const callbacks = new Set<() => void>()
    vi.stubGlobal('ResizeObserver', class {
      constructor(private callback: () => void) { callbacks.add(callback) }
      observe() {}
      unobserve() {}
      disconnect() { callbacks.delete(this.callback) }
    })
    transcriptServer(sequence(candidatesPage, boundPage([text('a1', 'assistant', '回答中')])))
    const log = await openChat()
    const size = scrollGeometry(log)
    size.viewport = 250
    act(() => { for (const callback of callbacks) callback() })
    expect(size.top).toBe(950)
    expect(screen.queryByRole('button', { name: '跳到最新' })).not.toBeInTheDocument()
    size.top = 400
    fireEvent.scroll(log)
    size.height = 1800
    act(() => { for (const callback of callbacks) callback() })
    expect(size.top).toBe(400)
    fireEvent.click(screen.getByRole('button', { name: '跳到最新' }))
    expect(size.top).toBe(1550)
    expect(screen.queryByRole('button', { name: '跳到最新' })).not.toBeInTheDocument()
  })
})

describe('candidate selection', () => {
  it('lists candidates and never claims one automatically', async () => {
    const client = createClient()
    transcriptServer(sequence(candidatesPage))
    render(<ChatView {...props({ client })}/>)
    expect(await screen.findByText('选择此终端的会话记录')).toBeVisible()
    const buttons = await screen.findAllByRole('button', { name: /aaaa1111|cccc2222/ })
    expect(buttons).toHaveLength(2)
    expect(screen.getByText(/输入会发送到/)).toBeVisible()
    // 从未调用任何 Herdr RPC（包括 pane.read）。
    expect(client.call).not.toHaveBeenCalled()
  })

  it('reads only the selected candidate and shows two rounds in order', async () => {
    const records: ChatRecord[] = [
      text('u1', 'user', '帮我跑测试'),
      { id: 'a1', role: 'assistant', blocks: [{ type: 'text', text: '好的：\n\n**已开始**' }, { type: 'tool-call', call_id: 'call-1', name: 'Bash', input: { command: 'pnpm test', cwd: '/home/user/project' } }] },
      { id: 't1', role: 'tool', blocks: [{ type: 'tool-result', call_id: 'call-1', output: '2 passed', is_error: false }] },
      text('u2', 'user', '再跑一次'),
      text('a2', 'assistant', '第二次也通过了'),
    ]
    const client = createClient()
    const mock = transcriptServer(sequence(candidatesPage, boundPage(records)))
    render(<ChatView {...props({ client })}/>)
    fireEvent.click(await screen.findByRole('button', { name: /aaaa1111/ }))

    await waitFor(() => expect(document.querySelectorAll('.chat-message')).toHaveLength(5))
    const roles = Array.from(document.querySelectorAll('.chat-message')).map((node) => node.getAttribute('data-role'))
    expect(roles).toEqual(['user', 'assistant', 'tool', 'user', 'assistant'])
    expect(document.querySelectorAll('.chat-message-user')).toHaveLength(2)
    expect(document.querySelector('.chat-message-assistant strong')).toHaveTextContent('已开始')
    const tool = document.querySelector('.chat-tool-call')
    expect(tool?.querySelector('.chat-tool-name')).toHaveTextContent('Bash')
    expect(tool?.querySelector('.chat-tool-id')).toHaveTextContent('call-1')
    expect(tool?.querySelector('.chat-tool-pre')).toHaveTextContent('pnpm test')

    // 第二次请求必须带上被选中的候选，且客户端永不自行拼路径或 cwd。
    const urls = mock.mock.calls.map((call) => String(call[0]))
    expect(urls[0]).toBe('/api/hosts/host/panes/p1/transcript')
    expect(urls[1]).toBe('/api/hosts/host/panes/p1/transcript?session=cand-1')
    expect(urls.every((url) => !url.includes('cwd'))).toBe(true)
    expect(client.call).not.toHaveBeenCalled()
  })

  it('keeps two identical prompts with different record ids', async () => {
    transcriptServer(sequence(candidatesPage, boundPage([text('u1', 'user', '跑测试'), text('u2', 'user', '跑测试')])))
    render(<ChatView {...props()}/>)
    fireEvent.click(await screen.findByRole('button', { name: /aaaa1111/ }))
    await waitFor(() => expect(document.querySelectorAll('.chat-message-user')).toHaveLength(2))
  })

  it('updates a repeated record id in place instead of appending a second copy', async () => {
    transcriptServer(sequence(
      candidatesPage,
      boundPage([text('u1', 'user', '原问题'), text('a1', 'assistant', '回答中…')]),
      boundPage([text('a1', 'assistant', '回答完成')], { next_cursor: 'cur-2' }),
    ))
    render(<ChatView {...props()}/>)
    fireEvent.click(await screen.findByRole('button', { name: /aaaa1111/ }))
    await waitFor(() => expect(screen.getByText('回答中…')).toBeInTheDocument())
    fireEvent.click(screen.getByRole('button', { name: '刷新会话记录' }))
    await waitFor(() => expect(screen.getByText('回答完成')).toBeInTheDocument())
    expect(document.querySelectorAll('.chat-message-assistant')).toHaveLength(1)
    expect(screen.queryByText('回答中…')).toBeNull()
  })

  it('rebuilds the view when the server reports a reset', async () => {
    transcriptServer(sequence(
      candidatesPage,
      boundPage([text('old', 'user', '旧会话内容')]),
      boundPage([text('new', 'user', '轮转后的内容')], { reset: true, previous_cursor: undefined }),
    ))
    render(<ChatView {...props()}/>)
    fireEvent.click(await screen.findByRole('button', { name: /aaaa1111/ }))
    await waitFor(() => expect(screen.getByText('旧会话内容')).toBeInTheDocument())
    fireEvent.click(screen.getByRole('button', { name: '刷新会话记录' }))
    await waitFor(() => expect(screen.getByText('轮转后的内容')).toBeInTheDocument())
    expect(screen.queryByText('旧会话内容')).toBeNull()
  })

  it('returns to the candidate list when the session expires', async () => {
    transcriptServer(sequence(
      candidatesPage,
      boundPage([text('u1', 'user', '问题')]),
      { supported: true, reason: 'session_unavailable', reset: true, messages: [], candidates: [candidatesPage.candidates[1]] },
    ))
    render(<ChatView {...props()}/>)
    fireEvent.click(await screen.findByRole('button', { name: /aaaa1111/ }))
    await waitFor(() => expect(screen.getByText('问题')).toBeInTheDocument())
    fireEvent.click(screen.getByRole('button', { name: '刷新会话记录' }))
    await waitFor(() => expect(screen.getByText('该会话记录已失效')).toBeVisible())
    expect(await screen.findByRole('button', { name: /cccc2222/ })).toBeVisible()
    expect(screen.queryByText('问题')).toBeNull()
  })
})

describe('explicit DSH read source', () => {
  const dshPane: Pane = { ...basePane, agent: '' }
  const dshCandidate = { id: 'cand-dsh', agent: 'dsh' as const, session_id: 'dsh-aaaa1111', updated_at: '2026-09-13T04:00:00Z' }

  function dshServer(handler: (url: string) => unknown) {
    return transcriptServer((url) => handler(url))
  }

  it('offers the DSH source for an unidentified program and never claims a plain shell', async () => {
    const mock = dshServer((url) => (url.includes('source=dsh')
      ? { supported: true, agent: 'dsh', candidates: [], reason: 'no_session_candidates', messages: [] }
      : { supported: false, reason: 'no_agent', messages: [] }))
    render(<ChatView {...props({ pane: dshPane })}/>)

    expect(await screen.findByText('暂不支持结构化会话记录')).toBeVisible()
    expect(screen.getByText(/未识别出这个终端里运行的程序/)).toBeVisible()
    expect(screen.getByText(/输入仍会发送到当前终端/)).toBeVisible()
    expect(screen.queryByText(/普通 Shell/)).toBeNull()

    // 在用户点击之前，客户端从不主动声明读取源，也从不猜 DSH。
    expect(mock.mock.calls.every(([url]) => !String(url).includes('source='))).toBe(true)
    fireEvent.click(screen.getByRole('button', { name: '读取 DSH 会话记录' }))
    await waitFor(() => expect(mock.mock.calls.some(([url]) => String(url) === '/api/hosts/host/panes/p1/transcript?source=dsh')).toBe(true))
    // 只列候选：一个 session 参数都不许出现。
    expect(mock.mock.calls.every(([url]) => !String(url).includes('session='))).toBe(true)
    expect(await screen.findByText(/在 DSH 的会话目录/)).toBeVisible()
  })

  it('requires an explicit candidate click with exactly one DSH candidate and keeps the binding selected', async () => {
    const mock = dshServer((url) => {
      if (!url.includes('source=dsh')) return { supported: false, reason: 'no_agent', messages: [] }
      if (!url.includes('session=')) return { supported: true, agent: 'dsh', candidates: [dshCandidate], messages: [] }
      return { supported: true, agent: 'dsh', session_id: dshCandidate.session_id, binding: 'selected', messages: [text('u1', 'user', 'DSH 里的问题')] }
    })
    render(<ChatView {...props({ pane: dshPane })}/>)
    fireEvent.click(await screen.findByRole('button', { name: '读取 DSH 会话记录' }))
    const candidate = await screen.findByRole('button', { name: /dsh-aaaa/ })
    expect(document.querySelectorAll('.chat-message')).toHaveLength(0)
    expect(mock.mock.calls.every(([url]) => !String(url).includes('session='))).toBe(true)

    fireEvent.click(candidate)
    await screen.findByText('DSH 里的问题')
    const bound = mock.mock.calls.map(([url]) => String(url)).filter((url) => url.includes('session='))
    expect(bound[0]).toBe('/api/hosts/host/panes/p1/transcript?session=cand-dsh&source=dsh')
    // 绑定来源就是用户显式选择，界面不升级、不暗示服务端已自动确认归属。
    expect(screen.getByRole('button', { name: '重新选择会话记录' })).toBeVisible()
    expect(screen.getByText('DSH')).toBeVisible()
    expect(screen.queryByText(/已自动/)).toBeNull()
  })

  it('rebuilds the view on a source switch and keeps the submit target and count unchanged', async () => {
    const submit = vi.fn().mockResolvedValue(undefined)
    const client = createClient()
    let plain = 0
    const mock = dshServer((url) => {
      if (url.includes('source=dsh')) {
        if (url.includes('session=')) return { supported: true, agent: 'dsh', session_id: dshCandidate.session_id, binding: 'selected', previous_cursor: 'dsh-prev', messages: [text('u1', 'user', 'DSH 里的问题')] }
        return { supported: true, agent: 'dsh', candidates: [dshCandidate], messages: [] }
      }
      // 默认读取源先说"没识别出程序"，用户才会看到 DSH 入口；切回来之后按 claude 列候选。
      return plain++ === 0 ? { supported: false, reason: 'no_agent', messages: [] } : candidatesPage
    })
    render(<ChatView {...props({ submit, client, pane: dshPane })}/>)
    fireEvent.click(await screen.findByRole('button', { name: '读取 DSH 会话记录' }))
    fireEvent.click(await screen.findByRole('button', { name: /dsh-aaaa/ }))
    await screen.findByText('DSH 里的问题')
    expect(screen.getByRole('button', { name: '加载更早的记录' })).toBeVisible()

    fireEvent.click(screen.getByRole('button', { name: '恢复默认读取源' }))
    await waitFor(() => expect(screen.queryByText('DSH 里的问题')).toBeNull())
    // 选择 / 游标 / 候选一起重建：不残留上一个读取源的会话与分页锚点。
    expect(screen.queryByRole('button', { name: '加载更早的记录' })).toBeNull()
    expect(await screen.findByRole('button', { name: /aaaa1111/ })).toBeVisible()

    // 切换读取源不发送、不重放任何输入；发送目标仍是这个 pane。
    expect(submit).not.toHaveBeenCalled()
    const input = screen.getByRole('textbox', { name: '对话输入内容' })
    fireEvent.change(input, { target: { value: '继续' } })
    fireEvent.keyDown(input, { key: 'Enter' })
    // 一次发送 = 两腿，不多不少：正文一腿 + 单独回车一腿，目标固定为这个 pane。
    await waitFor(() => expect(submit).toHaveBeenCalledTimes(2))
    expect(submit).toHaveBeenNthCalledWith(1, 'p1', '继续', [])
    expect(submit).toHaveBeenNthCalledWith(2, 'p1', '', ['Enter'])
    expect(client.call).not.toHaveBeenCalled()
    expect(mock.mock.calls.every(([url]) => !String(url).includes('agent='))).toBe(true)
  })

  it('drops the DSH source when the pane turns into a recognized agent without touching the draft', async () => {
    const submit = vi.fn().mockResolvedValue(undefined)
    const mock = dshServer((url) => (url.includes('source=dsh')
      ? { supported: true, agent: 'dsh', candidates: [dshCandidate], messages: [] }
      : { supported: false, reason: 'no_agent', messages: [] }))
    const view = render(<ChatView {...props({ submit, pane: { ...basePane, pane_id: 'p1', agent: '' } })}/>)
    fireEvent.click(await screen.findByRole('button', { name: '读取 DSH 会话记录' }))
    await screen.findByRole('button', { name: /dsh-aaaa/ })

    fireEvent.change(screen.getByRole('textbox', { name: '对话输入内容' }), { target: { value: '草稿要保留' } })

    // pane 现在被识别成 claude：服务端会拒绝 source=dsh，客户端必须立刻放弃它。
    const before = mock.mock.calls.length
    view.rerender(<ChatView {...props({ submit, pane: { ...basePane, pane_id: 'p1', agent: 'claude' } })}/>)
    await waitFor(() => expect(mock.mock.calls.slice(before).some(([url]) => String(url) === '/api/hosts/host/panes/p1/transcript')).toBe(true))
    expect(mock.mock.calls.slice(before).every(([url]) => !String(url).includes('source='))).toBe(true)
    expect(screen.queryByText('DSH')).toBeNull()
    expect(screen.queryByRole('button', { name: '恢复默认读取源' })).toBeNull()

    // 草稿与发送目标都不受影响。
    const input = screen.getByRole('textbox', { name: '对话输入内容' })
    expect(input).toHaveValue('草稿要保留')
    fireEvent.keyDown(input, { key: 'Enter' })
    await waitFor(() => expect(submit).toHaveBeenCalledTimes(2))
    expect(submit).toHaveBeenNthCalledWith(1, 'p1', '草稿要保留', [])
    expect(submit).toHaveBeenNthCalledWith(2, 'p1', '', ['Enter'])
  })

  it('scopes the remembered source and the send target per pane', async () => {
    const submit = vi.fn().mockResolvedValue(undefined)
    const mock = dshServer((url) => (url.includes('source=dsh')
      ? { supported: true, agent: 'dsh', candidates: [dshCandidate], messages: [] }
      : { supported: false, reason: 'no_agent', messages: [] }))
    const view = render(<ChatView {...props({ submit, pane: { ...basePane, pane_id: 'p1', agent: '' } })}/>)
    fireEvent.click(await screen.findByRole('button', { name: '读取 DSH 会话记录' }))
    await waitFor(() => expect(mock.mock.calls.some(([url]) => String(url) === '/api/hosts/host/panes/p1/transcript?source=dsh')).toBe(true))

    // 另一个 pane 不继承这个选择，也不会把记录读成别人的。
    view.rerender(<ChatView {...props({ submit, pane: { ...basePane, pane_id: 'p2', agent: '' } })}/>)
    await waitFor(() => expect(mock.mock.calls.some(([url]) => String(url) === '/api/hosts/host/panes/p2/transcript')).toBe(true))
    const p2 = mock.mock.calls.map(([url]) => String(url)).filter((url) => url.includes('/panes/p2/'))
    expect(p2.every((url) => !url.includes('source='))).toBe(true)

    // 回到原 pane：这个 pane 上显式选过的读取源仍然生效。
    view.rerender(<ChatView {...props({ submit, pane: { ...basePane, pane_id: 'p1', agent: '' } })}/>)
    await waitFor(() => expect(mock.mock.calls.some(([url]) => String(url) === '/api/hosts/host/panes/p1/transcript?source=dsh')).toBe(true))

    const input = screen.getByRole('textbox', { name: '对话输入内容' })
    fireEvent.change(input, { target: { value: '给 p1 的命令' } })
    fireEvent.keyDown(input, { key: 'Enter' })
    // 两腿都发往 p1：pane 切换既不继承读取源，也不会把回车送到别的终端。
    await waitFor(() => expect(submit).toHaveBeenCalledTimes(2))
    expect(submit).toHaveBeenNthCalledWith(1, 'p1', '给 p1 的命令', [])
    expect(submit).toHaveBeenNthCalledWith(2, 'p1', '', ['Enter'])
  })
})

describe('degraded states', () => {
  it('explains an unsupported agent and offers to switch back to the terminal', async () => {
    const onSwitchToTerminal = vi.fn()
    transcriptServer(sequence({ supported: false, reason: 'unsupported_agent', messages: [] }))
    render(<ChatView {...props({ onSwitchToTerminal, pane: { ...basePane, agent: 'gemini' } })}/>)
    expect(await screen.findByText('暂不支持结构化会话记录')).toBeVisible()
    expect(screen.getByText(/该终端运行的 gemini/)).toBeVisible()
    fireEvent.click(screen.getAllByRole('button', { name: '切回终端' })[0])
    expect(onSwitchToTerminal).toHaveBeenCalled()
  })

  it('shows the empty candidate hint without inventing a conversation', async () => {
    transcriptServer(sequence({ supported: true, agent: 'claude', reason: 'no_session_candidates', candidates: [], messages: [] }))
    render(<ChatView {...props()}/>)
    expect(await screen.findByText(/尚未找到这个终端的会话记录/)).toBeVisible()
    expect(document.querySelectorAll('.chat-message')).toHaveLength(0)
  })

  it('reports a read limit instead of pretending the log is empty or corrupt', async () => {
    transcriptServer(sequence({ supported: false, reason: 'read_limit_exceeded', messages: [] }))
    render(<ChatView {...props()}/>)
    expect(await screen.findByText(/超出受限读取上限/)).toBeVisible()
    expect(screen.queryByText(/格式无法识别/)).toBeNull()
    // 读不下是这次读取的结果，不是"没识别出程序"，因此不提供 DSH 入口。
    expect(screen.queryByRole('button', { name: '读取 DSH 会话记录' })).toBeNull()
  })

  it('keeps the selected session on a read limit and does not call it empty', async () => {
    transcriptServer(sequence(candidatesPage,
      { supported: true, agent: 'claude', session_id: 'aaaa1111bbbb', binding: 'selected', reason: 'read_limit_exceeded', messages: [] }))
    render(<ChatView {...props()}/>)
    fireEvent.click(await screen.findByRole('button', { name: /aaaa1111/ }))
    expect(await screen.findByText(/超出受限读取上限/)).toBeVisible()
    expect(screen.queryByText('这个会话还没有记录')).toBeNull()
  })

  it('reports a transport failure with a retry action', async () => {
    transcriptServer(() => new Response(JSON.stringify({ error: '网关错误' }), { status: 502 }))
    render(<ChatView {...props()}/>)
    expect(await screen.findByText('读取会话记录失败')).toBeVisible()
    expect(screen.getByRole('button', { name: '重试' })).toBeVisible()
  })

  it('treats a 401 as a login problem rather than an empty conversation', async () => {
    transcriptServer(() => new Response(JSON.stringify({ error: 'unauthorized' }), { status: 401 }))
    render(<ChatView {...props()}/>)
    expect(await screen.findByText('登录状态已失效，请重新登录后查看')).toBeVisible()
  })

  it('pauses reading and sending while the host is disconnected', async () => {
    transcriptServer(sequence(candidatesPage))
    render(<ChatView {...props({ connected: false })}/>)
    expect(screen.getByRole('alert')).toHaveTextContent('主机未连接')
    expect(screen.getByRole('button', { name: '发送' })).toBeDisabled()
    expect(screen.getByRole('button', { name: '显示终端' })).toBeVisible()
  })
})

describe('sending', () => {
  it('sends once through the existing submit prop without adding an optimistic bubble', async () => {
    const submit = vi.fn().mockResolvedValue(undefined)
    transcriptServer(sequence(candidatesPage, boundPage([text('u1', 'user', '盘上的问题')])))
    render(<ChatView {...props({ submit })}/>)
    fireEvent.click(await screen.findByRole('button', { name: /aaaa1111/ }))
    await waitFor(() => expect(screen.getByText('盘上的问题')).toBeInTheDocument())
    const before = document.querySelectorAll('.chat-message-user').length

    const input = screen.getByRole('textbox', { name: '对话输入内容' })
    fireEvent.change(input, { target: { value: '继续' } })
    fireEvent.keyDown(input, { key: 'Enter' })
    // 一次逻辑发送只有两腿：正文（不带键）与单独一次回车；正文里绝不含隐式回车。
    await waitFor(() => expect(submit).toHaveBeenCalledTimes(2))
    expect(submit).toHaveBeenNthCalledWith(1, 'p1', '继续', [])
    expect(submit).toHaveBeenNthCalledWith(2, 'p1', '', ['Enter'])
    // 不追加乐观气泡：盘上的权威记录到了才显示。
    expect(document.querySelectorAll('.chat-message-user')).toHaveLength(before)
  })

  it('never sends two copies for a repeated Enter keydown', async () => {
    const submit = vi.fn().mockResolvedValue(undefined)
    transcriptServer(sequence(candidatesPage))
    render(<ChatView {...props({ submit })}/>)
    await screen.findByText('选择此终端的会话记录')
    const input = screen.getByRole('textbox', { name: '对话输入内容' })
    fireEvent.change(input, { target: { value: '命令' } })
    fireEvent.keyDown(input, { key: 'Enter', repeat: true })
    expect(submit).not.toHaveBeenCalled()
  })
})

describe('media and voice wiring', () => {
  function mediaServer(handler: (url: string) => unknown) {
    return transcriptServer((url) => (url.includes('paste-image') ? handler(url) : candidatesPage))
  }

  it('stages a dropped image with inject=false and sends text plus reference in one submission', async () => {
    const submit = vi.fn().mockResolvedValue(undefined)
    const mock = mediaServer(() => ({ ok: true, path: '/remote/shot.png', injected: false }))
    const client = createClient()
    render(<ChatView {...props({ client, submit })}/>)
    await screen.findByText('选择此终端的会话记录')

    const wrapper = document.querySelector('.chat-media-composer') as HTMLElement
    fireEvent.drop(wrapper, dropData([imageFile('shot.png')]))
    // stage-only：上传 URL 必须带 inject=false，且拖拽本身绝不提交、绝不碰终端。
    await waitFor(() => expect(mock.mock.calls.some(([url]) => String(url).includes('paste-image?inject=false'))).toBe(true))
    expect(mock.mock.calls.every(([url]) => !String(url).includes('inject=true'))).toBe(true)
    expect(submit).not.toHaveBeenCalled()
    expect(client.call).not.toHaveBeenCalled()
    await screen.findByText('已就绪')
    expect(readComposerDraft('host', 'p1')).toBe('')

    const input = screen.getByRole('textbox', { name: '对话输入内容' })
    fireEvent.change(input, { target: { value: '看这张图' } })
    fireEvent.keyDown(input, { key: 'Enter' })
    // 正文与图片引用合成为第一腿的正文，回车仍单独走第二腿。
    await waitFor(() => expect(submit).toHaveBeenCalledTimes(2))
    expect(submit).toHaveBeenNthCalledWith(1, 'p1', '看这张图\n/remote/shot.png', [])
    expect(submit).toHaveBeenNthCalledWith(2, 'p1', '', ['Enter'])
    expect(client.call).not.toHaveBeenCalled()
  })

  it('sends an image-only message: the reference line is the whole submission', async () => {
    const submit = vi.fn().mockResolvedValue(undefined)
    mediaServer(() => ({ ok: true, path: '/remote/only.png', injected: false }))
    render(<ChatView {...props({ submit })}/>)
    await screen.findByText('选择此终端的会话记录')

    fireEvent.drop(document.querySelector('.chat-media-composer') as HTMLElement, dropData([imageFile('only.png')]))
    await screen.findByText('已就绪')
    fireEvent.click(screen.getByRole('button', { name: '发送' }))
    // 没有正文时第一腿只送引用行，第二腿照旧只送一次回车。
    await waitFor(() => expect(submit).toHaveBeenCalledTimes(2))
    expect(submit).toHaveBeenNthCalledWith(1, 'p1', '/remote/only.png', [])
    expect(submit).toHaveBeenNthCalledWith(2, 'p1', '', ['Enter'])
  })

  it('appends the final voice transcript to the draft and never sends it', async () => {
    const submit = vi.fn().mockResolvedValue(undefined)
    transcriptServer(sequence(candidatesPage))
    voiceMock.enable()
    render(<ChatView {...props({ submit })}/>)
    await screen.findByText('选择此终端的会话记录')

    fireEvent.click(await screen.findByRole('button', { name: '开始语音输入' }))
    await waitFor(() => expect(voiceMock.state.events).toHaveLength(1))
    act(() => voiceMock.emit({ t: 'ready', protocol: 'openai-realtime', model: 'gpt-realtime', inputSampleRate: 24000, outputSampleRate: 24000, audioReply: false }))
    // 非最终结果只做内联显示，绝不写草稿。
    act(() => voiceMock.emit({ t: 'partial', text: '帮我看看' }))
    expect(readComposerDraft('host', 'p1')).toBe('')

    act(() => voiceMock.emit({ t: 'transcript', text: '帮我看看日志', final: true }))
    await waitFor(() => expect(screen.getByRole('textbox', { name: '对话输入内容' })).toHaveValue('帮我看看日志'))
    // 语音没有任何发送路径：草稿写好了，但一次都没提交。
    expect(submit).not.toHaveBeenCalled()
  })

  it('shows the voice assistant answer separately and never writes it to the draft or terminal', async () => {
    const submit = vi.fn().mockResolvedValue(undefined)
    transcriptServer(sequence(candidatesPage))
    voiceMock.enable()
    render(<ChatView {...props({ submit })}/>)
    await screen.findByText('选择此终端的会话记录')

    fireEvent.click(await screen.findByRole('button', { name: '开始语音输入' }))
    await waitFor(() => expect(voiceMock.state.events).toHaveLength(1))
    act(() => voiceMock.emit({ t: 'ready', protocol: 'openai-realtime', model: 'gpt-realtime', inputSampleRate: 24000, outputSampleRate: 24000, audioReply: false }))
    act(() => voiceMock.emit({ t: 'assistantText', text: '我建议先看报错', final: false }))
    act(() => voiceMock.emit({ t: 'assistantText', text: '我建议先看报错行', final: true }))

    expect(await screen.findByText('我建议先看报错行')).toBeVisible()
    expect(screen.getByText('语音助手（非终端 Agent）')).toBeVisible()
    expect(screen.getByRole('textbox', { name: '对话输入内容' })).toHaveValue('')
    expect(readComposerDraft('host', 'p1')).toBe('')
    expect(submit).not.toHaveBeenCalled()
  })
})
