import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { APIError, api, currentSessionID, invalidateAuthentication } from './api'
import {
  applyStructuredChatPage,
  formatChatDuration,
  fetchStructuredChat,
  formatChatTime,
  mergeChatRecords,
  normalizeStructuredChatResponse,
  paneAgentRejectsReadSource,
  prependStructuredChatPage,
  readPaneReadSource,
  rememberPaneReadSource,
  shortSessionID,
  structuredChatPath,
  StructuredChatSession,
} from './structuredChat'
import { EMPTY_STRUCTURED_CHAT_STATE, type ChatRecord, type ChatSource, type StructuredChatResponse, type StructuredChatState } from './structuredChatTypes'

const user = { id: 'user', email: 'user@example.test', role: 'user', display_name: 'User' }

function state(overrides: Partial<StructuredChatState> = {}): StructuredChatState {
  return { ...EMPTY_STRUCTURED_CHAT_STATE, ...overrides }
}

function page(overrides: Partial<StructuredChatResponse> = {}): StructuredChatResponse {
  return { supported: true, messages: [], ...overrides }
}

function textRecord(id: string, role: ChatRecord['role'], text: string): ChatRecord {
  return { id, role, blocks: [{ type: 'text', text }] }
}

async function login(sessionID = 'chat-session') {
  vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response(JSON.stringify({ user, csrf_token: 'csrf', session_id: sessionID }))))
  await api.login({ email: 'user@example.test', password: 'test' })
}

beforeEach(() => {
  invalidateAuthentication()
})
afterEach(() => {
  invalidateAuthentication()
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
  vi.useRealTimers()
})

describe('structuredChatPath', () => {
  it('keeps host and pane in the path and only opaque values in the query', () => {
    expect(structuredChatPath('h/1', 'p 2', {})).toBe('/api/hosts/h%2F1/panes/p%202/transcript')
    const query = structuredChatPath('host', 'pane', { session: 'a b', cursor: 'c=1', before: 'b' })
    expect(query).toBe('/api/hosts/host/panes/pane/transcript?session=a+b&cursor=c%3D1&before=b')
  })

  it('never sends cwd or agent from the client', () => {
    const path = structuredChatPath('host', 'pane', {})
    expect(path).not.toContain('cwd')
    expect(path).not.toContain('agent')
  })

  it('carries the explicit DSH read source as source= and never as agent=', () => {
    expect(structuredChatPath('host', 'pane', { source: 'dsh' })).toBe('/api/hosts/host/panes/pane/transcript?source=dsh')
    const bound = structuredChatPath('host', 'pane', { session: 'c1', cursor: 'cur', source: 'dsh' })
    expect(bound).toBe('/api/hosts/host/panes/pane/transcript?session=c1&cursor=cur&source=dsh')
    expect(bound).not.toContain('agent=')
    expect(bound).not.toContain('cwd=')
  })

  it('drops a read source outside the closed set instead of forwarding it', () => {
    const path = structuredChatPath('host', 'pane', { source: 'claude' as ChatSource })
    expect(path).toBe('/api/hosts/host/panes/pane/transcript')
  })
})

describe('paneAgentRejectsReadSource', () => {
  it('rejects an explicit DSH source only for recognized non-DSH agents', () => {
    // 已识别的读取源：服务端不接受客户端覆盖。
    expect(paneAgentRejectsReadSource('claude')).toBe(true)
    expect(paneAgentRejectsReadSource(' codex ')).toBe(true)
    expect(paneAgentRejectsReadSource('cursor')).toBe(true)
    expect(paneAgentRejectsReadSource('gemini')).toBe(true)
    expect(paneAgentRejectsReadSource('agy')).toBe(true)
    expect(paneAgentRejectsReadSource('pi')).toBe(true)
    expect(paneAgentRejectsReadSource('grok')).toBe(true)
    expect(paneAgentRejectsReadSource('traex')).toBe(true)
    // 未识别 / 未知 / 本来就是 dsh：都允许用户显式选择 DSH。
    expect(paneAgentRejectsReadSource('')).toBe(false)
    expect(paneAgentRejectsReadSource(undefined)).toBe(false)
    expect(paneAgentRejectsReadSource('omp')).toBe(false)
    expect(paneAgentRejectsReadSource('dsh')).toBe(false)
  })
})

describe('normalizeStructuredChatResponse', () => {
  it('drops messages entirely when the server reports no support', () => {
    const normalized = normalizeStructuredChatResponse({ supported: false, reason: 'unsupported_agent', messages: [textRecord('1', 'assistant', '假的回答')] })
    expect(normalized.supported).toBe(false)
    expect(normalized.reason).toBe('unsupported_agent')
    expect(normalized.messages).toEqual([])
  })

  it('drops unknown roles and unknown block shapes instead of guessing', () => {
    const normalized = normalizeStructuredChatResponse({
      supported: true,
      messages: [
        textRecord('a', 'user', '问题'),
        { id: 'b', role: 'reasoning', blocks: [{ type: 'text', text: '思想链' }] },
        { id: 'c', role: 'assistant', blocks: [{ type: 'unknown', text: 'x' }, { type: 'text', text: '有效' }] },
        { id: '', role: 'assistant', blocks: [] },
      ],
    })
    expect(normalized.messages.map((item) => item.id)).toEqual(['a', 'c'])
    expect(normalized.messages[1].blocks).toEqual([{ type: 'text', text: '有效' }])
  })

  it('keeps tool blocks with real ids and rejects malformed ones', () => {
    const normalized = normalizeStructuredChatResponse({
      supported: true,
      messages: [{
        id: 'a', role: 'assistant', blocks: [
          { type: 'tool-call', call_id: 'call-1', name: 'Bash', input: { command: 'ls' } },
          { type: 'tool-call', call_id: 'call-2', input: {} },
          { type: 'tool-result', call_id: 'call-1', output: 'ok', is_error: false },
          { type: 'tool-result', call_id: 'call-3', output: 42 },
        ],
      }],
    })
    expect(normalized.messages[0].blocks).toEqual([
      { type: 'tool-call', call_id: 'call-1', name: 'Bash', input: { command: 'ls' } },
      { type: 'tool-result', call_id: 'call-1', output: 'ok', is_error: false },
    ])
  })

  it('only accepts whitelisted agents and reasons for candidates', () => {
    const normalized = normalizeStructuredChatResponse({
      supported: true,
      candidates: [
        { id: 'c1', agent: 'claude', session_id: 's1', updated_at: '2026-09-13T04:00:00Z' },
        { id: 'c2', agent: 'omp', session_id: 's2', updated_at: '' },
        { id: 'c3', agent: 'cursor', session_id: 's3' },
        { id: 'c4', agent: 'gemini', session_id: 's4', updated_at: '' },
        { id: 'c5', agent: 'agy', session_id: 's5', updated_at: '' },
        { id: 'c6', agent: 'pi', session_id: 's6', updated_at: '' },
        { id: 'c7', agent: 'grok', session_id: 's7', updated_at: '' },
        { id: 'c8', agent: 'traex', session_id: 's8', updated_at: '' },
      ],
      reason: 'made_up_reason',
    })
    expect(normalized.candidates?.map((item) => item.id)).toEqual(['c1', 'c3', 'c4', 'c5', 'c6', 'c7', 'c8'])
    expect(normalized.reason).toBeUndefined()
  })

  it('accepts dsh as an agent and read_limit_exceeded as a reason', () => {
    const normalized = normalizeStructuredChatResponse({
      supported: true,
      candidates: [
        { id: 'c1', agent: 'dsh', session_id: 's1', updated_at: '2026-09-13T04:00:00Z' },
        { id: 'c2', agent: 'dsh-shell', session_id: 's2', updated_at: '' },
      ],
      messages: [],
    })
    expect(normalized.candidates?.map((item) => item.agent)).toEqual(['dsh'])

    const limited = normalizeStructuredChatResponse({ supported: false, reason: 'read_limit_exceeded', messages: [] })
    expect(limited.reason).toBe('read_limit_exceeded')
    expect(limited.supported).toBe(false)
    expect(limited.messages).toEqual([])
  })
})

describe('fetchStructuredChat', () => {
  it('uses the declared HTTP conventions and degrades over 200', async () => {
    await login()
    const fetchMock = vi.fn().mockResolvedValue(new Response(JSON.stringify({ supported: true, candidates: [], messages: [] })))
    vi.stubGlobal('fetch', fetchMock)
    const response = await fetchStructuredChat({ hostID: 'host', paneID: 'pane' })
    expect(response.supported).toBe(true)
    const [path, init] = fetchMock.mock.calls[0]
    expect(path).toBe('/api/hosts/host/panes/pane/transcript')
    expect(init).toMatchObject({ method: 'GET', credentials: 'same-origin', cache: 'no-store' })
  })

  it('invalidates the login session on 401 and raises an API error', async () => {
    await login()
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response(JSON.stringify({ error: '未登录' }), { status: 401 })))
    await expect(fetchStructuredChat({ hostID: 'host', paneID: 'pane' })).rejects.toMatchObject({ status: 401 })
    expect(currentSessionID()).toBe('')
  })

  it('reports a network failure as a readable error, not as an empty conversation', async () => {
    await login()
    vi.stubGlobal('fetch', vi.fn().mockRejectedValue(new TypeError('failed to fetch')))
    await expect(fetchStructuredChat({ hostID: 'host', paneID: 'pane' })).rejects.toThrow('无法读取会话记录，请检查网络后重试')
  })
})

describe('page merging', () => {
  it('updates a repeated id in place and keeps the order', () => {
    const first = [textRecord('a', 'user', '问题'), textRecord('b', 'assistant', '回答中')]
    const merged = mergeChatRecords(first, [textRecord('b', 'assistant', '回答完成'), textRecord('c', 'user', '下一问')])
    expect(merged.map((item) => item.id)).toEqual(['a', 'b', 'c'])
    expect((merged[1].blocks[0] as { text: string }).text).toBe('回答完成')
  })

  it('keeps two identical prompts with different ids (no text based dedupe)', () => {
    const merged = mergeChatRecords([textRecord('1', 'user', '跑测试')], [textRecord('2', 'user', '跑测试')])
    expect(merged).toHaveLength(2)
  })

  it('never renders messages without an explicit selection', () => {
    const next = applyStructuredChatPage(state(), page({ messages: [textRecord('a', 'assistant', '未经选择')], binding: 'selected' }))
    expect(next.messages).toEqual([])
    expect(next.status).toBe('idle')
  })

  it('rebuilds the conversation on reset', () => {
    const previous = state({ session: 'c1', binding: 'selected', messages: [textRecord('old', 'user', '旧')], nextCursor: '9', skipped: 3 })
    const next = applyStructuredChatPage(previous, page({ reset: true, messages: [textRecord('new', 'user', '新')], binding: 'selected' }))
    expect(next.messages.map((item) => item.id)).toEqual(['new'])
    expect(next.nextCursor).toBeUndefined()
    expect(next.skipped).toBe(0)
  })

  it('clears the selection and keeps candidates when the session expired', () => {
    const previous = state({ session: 'c1', binding: 'selected', messages: [textRecord('a', 'user', '旧')] })
    const next = applyStructuredChatPage(previous, page({ reason: 'session_unavailable', reset: true, candidates: [{ id: 'c2', agent: 'claude', session_id: 's2', updated_at: '' }] }))
    expect(next.session).toBeUndefined()
    expect(next.messages).toEqual([])
    expect(next.status).toBe('idle')
    expect(next.reason).toBe('session_unavailable')
  })

  it('goes unavailable without inventing records', () => {
    const next = applyStructuredChatPage(state({ session: 'c1', messages: [textRecord('a', 'user', '旧')] }), page({ supported: false, reason: 'cwd_unavailable' }))
    expect(next.status).toBe('unavailable')
    expect(next.messages).toEqual([])
    expect(next.session).toBeUndefined()
  })

  it('prepends earlier pages and updates ids in place', () => {
    const current = state({ session: 'c1', binding: 'selected', messages: [textRecord('c', 'user', '第三'), textRecord('d', 'assistant', '第四')], previousCursor: 'p2' })
    const earlier = page({ messages: [textRecord('a', 'user', '第一'), textRecord('b', 'assistant', '第二'), textRecord('c', 'user', '第三（更新）')], previous_cursor: 'p1' })
    const next = prependStructuredChatPage(current, earlier)
    expect(next.messages.map((item) => item.id)).toEqual(['a', 'b', 'c', 'd'])
    expect((next.messages[2].blocks[0] as { text: string }).text).toBe('第三（更新）')
    expect(next.previousCursor).toBe('p1')
  })

  it('keeps the explicit read source across pages and degradation', () => {
    const bound = applyStructuredChatPage(state({ session: 'c1', source: 'dsh' }), page({
      agent: 'dsh',
      messages: [textRecord('a', 'user', '问')],
      binding: 'selected',
    }))
    expect(bound.source).toBe('dsh')
    const limited = applyStructuredChatPage(state({ session: 'c1', source: 'dsh' }), page({ reason: 'read_limit_exceeded' }))
    expect(limited.reason).toBe('read_limit_exceeded')
    expect(limited.source).toBe('dsh')
    // 绑定会话时的降级原因不能被吞掉，否则"读不下"会显示成"这个会话没有记录"。
    expect(limited.status).toBe('empty')
    const unavailable = applyStructuredChatPage(state({ source: 'dsh' }), page({ supported: false, reason: 'no_agent' }))
    expect(unavailable.reason).toBe('no_agent')
    expect(unavailable.source).toBe('dsh')
  })

  it('accumulates skipped records and bounds status by content', () => {
    const withRecords = applyStructuredChatPage(state({ session: 'c1' }), page({ messages: [textRecord('a', 'user', '问')], binding: 'selected', skipped: 2 }))
    expect(withRecords.status).toBe('ready')
    expect(withRecords.skipped).toBe(2)
    const empty = applyStructuredChatPage(withRecords, page({ messages: [], binding: 'selected', skipped: 1 }))
    expect(empty.status).toBe('ready')
    expect(empty.skipped).toBe(3)
  })

  it('counts truncated records apart from skipped ones and clears them on reset', () => {
    const cut = applyStructuredChatPage(state({ session: 'c1' }), page({ messages: [textRecord('a', 'user', '问')], binding: 'selected', truncated: 40 }))
    expect(cut.truncated).toBe(40)
    expect(cut.skipped).toBe(0)
    const more = applyStructuredChatPage(cut, page({ messages: [], binding: 'selected', truncated: 2 }))
    expect(more.truncated).toBe(42)
    const rebuilt = applyStructuredChatPage(more, page({ messages: [textRecord('b', 'user', '新')], binding: 'selected', reset: true }))
    expect(rebuilt.truncated).toBe(0)
    expect(normalizeStructuredChatResponse({ supported: true, messages: [], truncated: 'x' }).truncated).toBeUndefined()
  })
})

describe('display helpers', () => {
  it('formats timestamps and rejects garbage', () => {
    const now = Date.parse('2026-09-13T10:00:00Z')
    expect(formatChatTime('2026-09-13T09:30:00Z', now)).not.toBe('')
    expect(formatChatTime('not-a-date', now)).toBe('')
    expect(formatChatTime(undefined, now)).toBe('')
  })

  it('shortens session ids for display', () => {
    expect(shortSessionID('abcdefghijkl')).toBe('abcdefgh…')
    expect(shortSessionID('short')).toBe('short')
  })
})

// ── 会话状态机 ────────────────────────────────────────────────────────────────

function deferred<T>() {
  let resolve!: (value: T) => void
  const promise = new Promise<T>((settle) => { resolve = settle })
  return { promise, resolve }
}

type FetchCall = { session?: string; cursor?: string; before?: string; source?: ChatSource; manual?: boolean; signal?: AbortSignal }

function recorder(handler: (call: FetchCall, index: number) => StructuredChatResponse | Promise<StructuredChatResponse>) {
  const calls: FetchCall[] = []
  const fetchPage = vi.fn(async (request: FetchCall) => {
    calls.push(request)
    return handler(request, calls.length - 1)
  })
  return { calls, fetchPage }
}

function candidate(id: string, agent: 'claude' | 'codex' | 'dsh' = 'claude') {
  return { id, agent, session_id: `session-${id}`, updated_at: '2026-09-13T04:00:00Z' }
}

describe('StructuredChatSession', () => {
  beforeEach(() => {
    vi.useFakeTimers()
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response(JSON.stringify({ user, csrf_token: 'csrf', session_id: 'login-session' }))))
  })

  it('lists candidates without a session and never auto-claims the only one', async () => {
    const { calls, fetchPage } = recorder(() => page({ candidates: [candidate('only')] }))
    const session = new StructuredChatSession({ hostID: 'host', paneID: 'pane', fetchPage, pollMs: 1000 })
    session.start()
    await vi.advanceTimersByTimeAsync(0)
    expect(calls[0].session).toBeUndefined()
    expect(calls[0].cursor).toBeUndefined()
    expect(calls[0].before).toBeUndefined()
    expect(session.getSnapshot().candidates.map((item) => item.id)).toEqual(['only'])
    expect(session.getSnapshot().state.session).toBeUndefined()
    expect(session.getSnapshot().state.messages).toEqual([])
    session.stop()
  })

  it('reads, then polls forward only with the latest cursor', async () => {
    const { calls, fetchPage } = recorder((call) => call.session
      ? page({ messages: call.cursor ? [textRecord('2', 'assistant', '增量')] : [textRecord('1', 'user', '第一问')], binding: 'selected', session_id: 's1', next_cursor: call.cursor ? 'c2' : 'c1' })
      : page({ candidates: [candidate('c1')] }))
    const session = new StructuredChatSession({ hostID: 'host', paneID: 'pane', fetchPage, pollMs: 1000 })
    session.start()
    await vi.advanceTimersByTimeAsync(0)
    session.select('c1')
    await vi.advanceTimersByTimeAsync(0)
    expect(session.getSnapshot().state.messages.map((item) => item.id)).toEqual(['1'])
    await vi.advanceTimersByTimeAsync(1000)
    expect(calls.at(-1)).toMatchObject({ session: 'c1', cursor: 'c1' })
    expect(session.getSnapshot().state.messages.map((item) => item.id)).toEqual(['1', '2'])
    session.stop()
  })

  it('never stacks requests while one is in flight', async () => {
    const pending = deferred<StructuredChatResponse>()
    const { fetchPage } = recorder((call) => call.session ? pending.promise : page({ candidates: [candidate('c1')] }))
    const session = new StructuredChatSession({ hostID: 'host', paneID: 'pane', fetchPage, pollMs: 1000 })
    session.start()
    await vi.advanceTimersByTimeAsync(0)
    session.select('c1')
    await vi.advanceTimersByTimeAsync(0)
    const afterSelect = fetchPage.mock.calls.length
    await vi.advanceTimersByTimeAsync(5000)
    expect(fetchPage.mock.calls.length).toBe(afterSelect)
    pending.resolve(page({ messages: [textRecord('1', 'user', '问')], binding: 'selected', session_id: 's1' }))
    await vi.advanceTimersByTimeAsync(0)
    expect(session.getSnapshot().state.messages).toHaveLength(1)
    session.stop()
  })

  it('catches up immediately when the server reports more records', async () => {
    const { calls, fetchPage } = recorder((call) => {
      if (!call.session) return page({ candidates: [candidate('c1')] })
      if (!call.cursor) return page({ messages: [textRecord('1', 'user', '问')], binding: 'selected', session_id: 's1', next_cursor: 'c1', has_more: true })
      return page({ messages: [textRecord('2', 'assistant', '答')], binding: 'selected', session_id: 's1', next_cursor: 'c2' })
    })
    const session = new StructuredChatSession({ hostID: 'host', paneID: 'pane', fetchPage, pollMs: 5000 })
    session.start()
    await vi.advanceTimersByTimeAsync(0)
    session.select('c1')
    await vi.advanceTimersByTimeAsync(0)
    expect(calls.map((call) => call.cursor)).toEqual([undefined, undefined, 'c1'])
    expect(session.getSnapshot().state.messages.map((item) => item.id)).toEqual(['1', '2'])
    session.stop()
  })

  it('drops a late response from the previously selected session', async () => {
    const first = deferred<StructuredChatResponse>()
    const { fetchPage } = recorder((call) => {
      if (!call.session) return page({ candidates: [candidate('c1'), candidate('c2')] })
      if (call.session === 'c1') return first.promise
      return page({ messages: [textRecord('b', 'user', '第二个会话')], binding: 'selected', session_id: 's2' })
    })
    const session = new StructuredChatSession({ hostID: 'host', paneID: 'pane', fetchPage, pollMs: 1000 })
    session.start()
    await vi.advanceTimersByTimeAsync(0)
    session.select('c1')
    await vi.advanceTimersByTimeAsync(0)
    session.select('c2')
    await vi.advanceTimersByTimeAsync(0)
    expect(session.getSnapshot().state.messages.map((item) => item.id)).toEqual(['b'])
    first.resolve(page({ messages: [textRecord('a', 'user', '迟到的第一个会话')], binding: 'selected', session_id: 's1' }))
    await vi.advanceTimersByTimeAsync(0)
    expect(session.getSnapshot().state.messages.map((item) => item.id)).toEqual(['b'])
    session.stop()
  })

  it('pauses while hidden or disconnected and resumes with one immediate poll', async () => {
    const { fetchPage } = recorder((call) => call.session
      ? page({ messages: [textRecord('1', 'user', '问')], binding: 'selected', session_id: 's1', next_cursor: 'c1' })
      : page({ candidates: [candidate('c1')] }))
    const session = new StructuredChatSession({ hostID: 'host', paneID: 'pane', fetchPage, pollMs: 1000 })
    session.start()
    await vi.advanceTimersByTimeAsync(0)
    session.select('c1')
    await vi.advanceTimersByTimeAsync(0)
    const bound = fetchPage.mock.calls.length
    session.setActive(false)
    await vi.advanceTimersByTimeAsync(5000)
    expect(fetchPage.mock.calls.length).toBe(bound)
    session.setActive(true)
    await vi.advanceTimersByTimeAsync(0)
    expect(fetchPage.mock.calls.length).toBe(bound + 1)
    session.stop()
  })

  it('stops all traffic after stop()', async () => {
    const { fetchPage } = recorder((call) => call.session
      ? page({ messages: [textRecord('1', 'user', '问')], binding: 'selected', session_id: 's1', next_cursor: 'c1' })
      : page({ candidates: [candidate('c1')] }))
    const session = new StructuredChatSession({ hostID: 'host', paneID: 'pane', fetchPage, pollMs: 1000 })
    session.start()
    await vi.advanceTimersByTimeAsync(0)
    session.select('c1')
    await vi.advanceTimersByTimeAsync(0)
    const bound = fetchPage.mock.calls.length
    session.stop()
    await vi.advanceTimersByTimeAsync(10000)
    expect(fetchPage.mock.calls.length).toBe(bound)
  })

  it('loads earlier pages with the reverse cursor', async () => {
    const { calls, fetchPage } = recorder((call) => {
      if (!call.session) return page({ candidates: [candidate('c1')] })
      if (call.before) return page({ messages: [textRecord('0', 'user', '更早')], binding: 'selected', session_id: 's1', previous_cursor: 'older' })
      return page({ messages: [textRecord('1', 'user', '最新')], binding: 'selected', session_id: 's1', next_cursor: 'c1', previous_cursor: 'p1' })
    })
    const session = new StructuredChatSession({ hostID: 'host', paneID: 'pane', fetchPage, pollMs: 100000 })
    session.start()
    await vi.advanceTimersByTimeAsync(0)
    session.select('c1')
    await vi.advanceTimersByTimeAsync(0)
    expect(session.getSnapshot().state.previousCursor).toBe('p1')
    session.loadEarlier()
    await vi.advanceTimersByTimeAsync(0)
    expect(calls.at(-1)).toMatchObject({ session: 'c1', before: 'p1' })
    expect(session.getSnapshot().state.messages.map((item) => item.id)).toEqual(['0', '1'])
    expect(session.getSnapshot().state.previousCursor).toBe('older')
    session.stop()
  })

  it('clears the selection when the session expires and keeps the fresh candidates', async () => {
    const { fetchPage } = recorder((call) => call.session
      ? page({ reason: 'session_unavailable', reset: true, candidates: [candidate('c2')] })
      : page({ candidates: [candidate('c1')] }))
    const session = new StructuredChatSession({ hostID: 'host', paneID: 'pane', fetchPage, pollMs: 1000 })
    session.start()
    await vi.advanceTimersByTimeAsync(0)
    session.select('c1')
    await vi.advanceTimersByTimeAsync(0)
    expect(session.getSnapshot().state.session).toBeUndefined()
    expect(session.getSnapshot().state.messages).toEqual([])
    expect(session.getSnapshot().candidates.map((item) => item.id)).toEqual(['c2'])
    session.stop()
  })

  it('surfaces transport failures without clearing what is already on screen', async () => {
    let fail = false
    const { fetchPage } = recorder((call) => {
      if (!call.session) return page({ candidates: [candidate('c1')] })
      if (fail) throw new Error('无法读取会话记录，请检查网络后重试')
      return page({ messages: [textRecord('1', 'user', '问')], binding: 'selected', session_id: 's1', next_cursor: 'c1' })
    })
    const session = new StructuredChatSession({ hostID: 'host', paneID: 'pane', fetchPage, pollMs: 1000 })
    session.start()
    await vi.advanceTimersByTimeAsync(0)
    session.select('c1')
    await vi.advanceTimersByTimeAsync(0)
    fail = true
    await vi.advanceTimersByTimeAsync(1000)
    expect(session.getSnapshot().error).toBe('无法读取会话记录，请检查网络后重试')
    expect(session.getSnapshot().state.messages).toHaveLength(1)
    session.stop()
  })

  it('only sends source=dsh after the user asks for it and still never auto-binds', async () => {
    const { calls, fetchPage } = recorder((call) => call.source === 'dsh'
      ? page({ agent: 'dsh', candidates: [candidate('only', 'dsh')] })
      : page({ candidates: [] }))
    const session = new StructuredChatSession({ hostID: 'host', paneID: 'pane', fetchPage, pollMs: 1000 })
    session.start()
    await vi.advanceTimersByTimeAsync(0)
    expect(calls[0].source).toBeUndefined()

    session.setReadSource('dsh')
    await vi.advanceTimersByTimeAsync(0)
    expect(calls.at(-1)).toMatchObject({ source: 'dsh' })
    expect(calls.at(-1)?.session).toBeUndefined()
    expect(calls.at(-1)?.cursor).toBeUndefined()
    expect(session.getSnapshot().state.source).toBe('dsh')
    // 只有一个候选也不能自动认领。
    expect(session.getSnapshot().state.session).toBeUndefined()
    expect(session.getSnapshot().state.messages).toEqual([])
    expect(session.getSnapshot().candidates.map((item) => item.id)).toEqual(['only'])
    session.stop()
  })

  it('clears selection, cursors and candidates when the read source switches', async () => {
    const { calls, fetchPage } = recorder((call) => {
      if (call.source === 'dsh') return page({ agent: 'dsh', candidates: [candidate('dsh1', 'dsh')] })
      if (!call.session) return page({ candidates: [candidate('c1')] })
      return page({ messages: [textRecord('1', 'user', '问')], binding: 'selected', session_id: 's1', next_cursor: 'cur', previous_cursor: 'prev' })
    })
    const session = new StructuredChatSession({ hostID: 'host', paneID: 'pane', fetchPage, pollMs: 100000 })
    session.start()
    await vi.advanceTimersByTimeAsync(0)
    session.select('c1')
    await vi.advanceTimersByTimeAsync(0)
    expect(session.getSnapshot().state.nextCursor).toBe('cur')

    session.setReadSource('dsh')
    await vi.advanceTimersByTimeAsync(0)
    const switched = session.getSnapshot().state
    expect(switched.source).toBe('dsh')
    expect(switched.session).toBeUndefined()
    expect(switched.sessionID).toBeUndefined()
    expect(switched.binding).toBeUndefined()
    expect(switched.messages).toEqual([])
    expect(switched.nextCursor).toBeUndefined()
    expect(switched.previousCursor).toBeUndefined()
    expect(session.getSnapshot().candidates.map((item) => item.id)).toEqual(['dsh1'])
    expect(calls.at(-1)).toMatchObject({ source: 'dsh' })
    expect(calls.at(-1)?.session).toBeUndefined()

    // 切回默认读取源同样重建，且不再带 source。
    session.clearReadSource()
    await vi.advanceTimersByTimeAsync(0)
    expect(session.getSnapshot().state.source).toBeUndefined()
    expect(calls.at(-1)?.source).toBeUndefined()
    expect(session.getSnapshot().candidates.map((item) => item.id)).toEqual(['c1'])
    session.stop()
  })

  it('remembers the read source per pane only and forgets it when the login identity changes', async () => {
    await login('pane-session')
    const first = recorder(() => page({ candidates: [] }))
    const paneA = new StructuredChatSession({ hostID: 'host', paneID: 'a', fetchPage: first.fetchPage, pollMs: 1000 })
    paneA.start()
    await vi.advanceTimersByTimeAsync(0)
    paneA.setReadSource('dsh')
    await vi.advanceTimersByTimeAsync(0)
    expect(first.calls.map((call) => call.source)).toEqual([undefined, 'dsh'])
    paneA.stop()

    // 同一个 pane 重新挂载：沿用用户显式选过的读取源。
    const second = recorder(() => page({ candidates: [] }))
    const reopened = new StructuredChatSession({ hostID: 'host', paneID: 'a', fetchPage: second.fetchPage, pollMs: 1000 })
    reopened.start()
    await vi.advanceTimersByTimeAsync(0)
    expect(second.calls[0].source).toBe('dsh')
    reopened.stop()

    // 另一个 pane / 另一台主机都不继承。
    const other = recorder(() => page({ candidates: [] }))
    const paneB = new StructuredChatSession({ hostID: 'host', paneID: 'b', fetchPage: other.fetchPage, pollMs: 1000 })
    paneB.start()
    await vi.advanceTimersByTimeAsync(0)
    expect(other.calls[0].source).toBeUndefined()
    expect(readPaneReadSource('host', 'a')).toBe('dsh')
    expect(readPaneReadSource('host', 'b')).toBeUndefined()
    expect(readPaneReadSource('other-host', 'a')).toBeUndefined()
    paneB.stop()

    // 换一个登录身份后不再复用，也不能被直接写进别的 pane。
    rememberPaneReadSource('host', 'b', 'dsh')
    expect(readPaneReadSource('host', 'b')).toBe('dsh')
    await login('another-session')
    expect(readPaneReadSource('host', 'a')).toBeUndefined()
    expect(readPaneReadSource('host', 'b')).toBeUndefined()
  })

  it('drops a remembered DSH source before the first request when the pane is already recognized', async () => {
    await login('pane-session')
    const first = recorder(() => page({ candidates: [] }))
    const warmup = new StructuredChatSession({ hostID: 'host', paneID: 'a', fetchPage: first.fetchPage, pollMs: 1000 })
    warmup.start()
    await vi.advanceTimersByTimeAsync(0)
    warmup.setReadSource('dsh')
    await vi.advanceTimersByTimeAsync(0)
    warmup.stop()
    expect(readPaneReadSource('host', 'a')).toBe('dsh')

    // 重新挂载时 pane 已经被识别成 claude：先放弃读取源，再开始取数，一次被拒的请求都不发。
    const plain = recorder(() => page({ candidates: [] }))
    const session = new StructuredChatSession({ hostID: 'host', paneID: 'a', fetchPage: plain.fetchPage, pollMs: 1000 })
    session.setReadSource(undefined)
    session.start()
    await vi.advanceTimersByTimeAsync(0)
    expect(plain.calls.map((call) => call.source)).toEqual([undefined])
    expect(session.getSnapshot().state.source).toBeUndefined()
    expect(readPaneReadSource('host', 'a')).toBeUndefined()
    session.stop()
  })

  it('aborts a pending DSH request and resets when the source is dropped', async () => {
    const pending = deferred<StructuredChatResponse>()
    const { fetchPage } = recorder((call) => (call.source === 'dsh'
      ? pending.promise
      : page({ candidates: [candidate('c1')] })))
    const session = new StructuredChatSession({ hostID: 'host', paneID: 'pane', fetchPage, pollMs: 1000 })
    session.start()
    await vi.advanceTimersByTimeAsync(0)
    session.setReadSource('dsh')
    await vi.advanceTimersByTimeAsync(0)
    expect(session.getSnapshot().state.status).toBe('loading')

    session.setReadSource(undefined)
    await vi.advanceTimersByTimeAsync(0)
    expect(session.getSnapshot().state.source).toBeUndefined()
    expect(session.getSnapshot().candidates.map((item) => item.id)).toEqual(['c1'])
    // 迟到的 DSH 响应属于上一个读取源，必须被丢弃。
    pending.resolve(page({ agent: 'dsh', candidates: [candidate('dsh1', 'dsh')] }))
    await vi.advanceTimersByTimeAsync(0)
    expect(session.getSnapshot().state.source).toBeUndefined()
    expect(session.getSnapshot().candidates.map((item) => item.id)).toEqual(['c1'])
    session.stop()
  })

  it('turns a 401 into a readable message', async () => {
    const { fetchPage } = recorder(() => { throw new APIError(401, 'unauthorized', '未登录') })
    const session = new StructuredChatSession({ hostID: 'host', paneID: 'pane', fetchPage, pollMs: 1000 })
    session.start()
    await vi.advanceTimersByTimeAsync(0)
    expect(session.getSnapshot().state.status).toBe('error')
    expect(session.getSnapshot().error).toBe('登录状态已失效，请重新登录后查看')
    session.stop()
  })
})

describe('verified binding', () => {
  it('only sends manual=1 without a session', () => {
    expect(structuredChatPath('host', 'pane', { manual: true })).toBe('/api/hosts/host/panes/pane/transcript?manual=1')
    expect(structuredChatPath('host', 'pane', { manual: true, session: 's' })).toBe('/api/hosts/host/panes/pane/transcript?session=s')
  })

  it('keeps queued and the verified session id from the response, nothing else', () => {
    const normalized = normalizeStructuredChatResponse({
      supported: true,
      binding: 'verified',
      session: 'cand-1',
      messages: [
        { id: 'q', role: 'user', queued: true, blocks: [{ type: 'text', text: '插入' }] },
        { id: 'a', role: 'assistant', queued: true, blocks: [{ type: 'text', text: '答' }] },
      ],
    })
    expect(normalized.session).toBe('cand-1')
    expect(normalized.messages[0].queued).toBe(true)
    expect(normalized.messages[1].queued).toBeUndefined()
    expect(normalizeStructuredChatResponse({ supported: true, binding: 'selected', session: 'x', messages: [] }).session).toBeUndefined()
  })

  it('binds a verified page without an explicit selection and keeps the binding on continuation pages', () => {
    const first = applyStructuredChatPage(state(), page({ binding: 'verified', session: 'cand-1', session_id: 's1', messages: [textRecord('1', 'user', '问')], next_cursor: 'c1' }))
    expect(first).toMatchObject({ status: 'ready', session: 'cand-1', binding: 'verified', sessionID: 's1', nextCursor: 'c1' })
    const next = applyStructuredChatPage(first, page({ binding: 'selected', session_id: 's1', messages: [textRecord('2', 'assistant', '答')] }))
    expect(next.binding).toBe('verified')
    expect(next.messages.map((item) => item.id)).toEqual(['1', '2'])
    // 没有候选 id 的 verified 页无法续读，不认领。
    expect(applyStructuredChatPage(state(), page({ binding: 'verified', messages: [textRecord('1', 'user', '问')] })).messages).toEqual([])
  })

  it('catches up a verified first page with the session it was bound to', async () => {
    vi.useFakeTimers()
    const { calls, fetchPage } = recorder((call) => call.session
      ? page({ binding: 'selected', session_id: 's1', messages: [textRecord('2', 'assistant', '续页')], next_cursor: 'c2' })
      : page({ binding: 'verified', session: 'cand-1', session_id: 's1', messages: [textRecord('1', 'user', '问')], next_cursor: 'c1', has_more: true }))
    const session = new StructuredChatSession({ hostID: 'host', paneID: 'pane', fetchPage, pollMs: 1000 })
    session.start()
    await vi.advanceTimersByTimeAsync(0)
    expect(calls).toHaveLength(2)
    expect(calls[1]).toMatchObject({ session: 'cand-1', cursor: 'c1' })
    expect(session.getSnapshot().state.messages.map((item) => item.id)).toEqual(['1', '2'])
    session.stop()
  })

  it('treats a verified page for another session as a session switch', () => {
    const bound = applyStructuredChatPage(state(), page({ binding: 'verified', session: 'cand-1', session_id: 's1', messages: [textRecord('old', 'user', '旧会话')], next_cursor: 'c1', skipped: 2 }))
    const switched = applyStructuredChatPage(bound, page({ binding: 'verified', session: 'cand-2', session_id: 's2', messages: [textRecord('new', 'user', '新会话')], next_cursor: 'n1' }))
    expect(switched).toMatchObject({ session: 'cand-2', sessionID: 's2', binding: 'verified', nextCursor: 'n1', skipped: 0 })
    expect(switched.messages.map((item) => item.id)).toEqual(['new'])
  })

  it('polls the verified session, asks for candidates only after reselect, and re-checks evidence on refresh', async () => {
    vi.useFakeTimers()
    const { calls, fetchPage } = recorder((call) => call.session
      ? page({ binding: 'selected', session_id: 's1', messages: [], next_cursor: 'c2' })
      : call.manual
        ? page({ candidates: [candidate('cand-1'), candidate('cand-2')] })
        : page({ binding: 'verified', session: 'cand-1', session_id: 's1', candidates: [candidate('cand-1')], messages: [textRecord('1', 'user', '问')], next_cursor: 'c1' }))
    const session = new StructuredChatSession({ hostID: 'host', paneID: 'pane', fetchPage, pollMs: 1000 })
    session.start()
    await vi.advanceTimersByTimeAsync(0)
    expect(session.getSnapshot().state).toMatchObject({ session: 'cand-1', binding: 'verified' })
    await vi.advanceTimersByTimeAsync(1000)
    expect(calls.at(-1)).toMatchObject({ session: 'cand-1', cursor: 'c1' })
    expect(session.getSnapshot().state.binding).toBe('verified')

    session.refresh()
    await vi.advanceTimersByTimeAsync(0)
    expect(calls.at(-1)?.session).toBeUndefined()
    expect(calls.at(-1)?.manual).toBeUndefined()
    expect(session.getSnapshot().state.binding).toBe('verified')

    session.reselect()
    await vi.advanceTimersByTimeAsync(0)
    expect(calls.at(-1)).toMatchObject({ manual: true })
    expect(session.getSnapshot().state.session).toBeUndefined()
    expect(session.getSnapshot().candidates.map((item) => item.id)).toEqual(['cand-1', 'cand-2'])
    session.select('cand-2')
    await vi.advanceTimersByTimeAsync(0)
    expect(calls.at(-1)).toMatchObject({ session: 'cand-2' })
    expect(calls.at(-1)?.manual).toBeUndefined()
    expect(session.getSnapshot().state.binding).toBe('selected')
    session.stop()
  })
})

describe('waiting for a new session to be claimed', () => {
  beforeEach(() => { vi.useFakeTimers() })

  it('keeps polling the default query until Herdr evidence binds the session, then reads forward', async () => {
    // 新建的 Claude 会话收到第一条消息才写记录文件：第一次读取时没有候选，之后服务端按证据认领。
    let written = false
    const { calls, fetchPage } = recorder((call) => {
      if (call.session) return page({ binding: 'selected', session_id: 's1', messages: [textRecord('2', 'assistant', '答')], next_cursor: 'c2' })
      return written
        ? page({ binding: 'verified', session: 'cand-1', session_id: 's1', candidates: [candidate('cand-1')], messages: [textRecord('1', 'user', '第一条')], next_cursor: 'c1' })
        : page({ reason: 'no_session_candidates', candidates: [] })
    })
    const session = new StructuredChatSession({ hostID: 'host', paneID: 'pane', fetchPage, pollMs: 2000 })
    session.start()
    await vi.advanceTimersByTimeAsync(0)
    expect(session.getSnapshot().state).toMatchObject({ status: 'idle', reason: 'no_session_candidates', session: undefined })
    await vi.advanceTimersByTimeAsync(2000)
    expect(calls).toHaveLength(2)
    expect(calls[1].session).toBeUndefined()
    expect(calls[1].manual).toBeUndefined()
    written = true
    await vi.advanceTimersByTimeAsync(2000)
    expect(session.getSnapshot().state).toMatchObject({ status: 'ready', session: 'cand-1', binding: 'verified' })
    expect(session.getSnapshot().state.messages.map((item) => item.id)).toEqual(['1'])
    await vi.advanceTimersByTimeAsync(2000)
    expect(calls.at(-1)).toMatchObject({ session: 'cand-1', cursor: 'c1' })
    expect(session.getSnapshot().state.messages.map((item) => item.id)).toEqual(['1', '2'])
    session.stop()
  })

  it('polls every interval for the first minute, then every 10 seconds', async () => {
    const { calls, fetchPage } = recorder(() => page({ reason: 'no_session_candidates', candidates: [] }))
    const session = new StructuredChatSession({ hostID: 'host', paneID: 'pane', fetchPage, pollMs: 2000 })
    session.start()
    await vi.advanceTimersByTimeAsync(58_000)
    expect(calls).toHaveLength(30)
    await vi.advanceTimersByTimeAsync(2000)
    expect(calls).toHaveLength(31)
    // 等满一分钟后放慢到 10 秒一次。
    await vi.advanceTimersByTimeAsync(9_000)
    expect(calls).toHaveLength(31)
    await vi.advanceTimersByTimeAsync(1_000)
    expect(calls).toHaveLength(32)
    // 用户的刷新重新起算：立即取一次，之后又回到正常间隔。
    session.refresh()
    await vi.advanceTimersByTimeAsync(4000)
    expect(calls).toHaveLength(35)
    session.stop()
  })

  async function pollsWithin(response: () => StructuredChatResponse | Promise<StructuredChatResponse>) {
    const { calls, fetchPage } = recorder(response)
    const session = new StructuredChatSession({ hostID: 'host', paneID: 'pane', fetchPage, pollMs: 2000 })
    session.start()
    await vi.advanceTimersByTimeAsync(30_000)
    session.stop()
    return calls.length
  }

  it('keeps polling only for reasons that can clear up on their own', async () => {
    for (const reason of ['no_session_candidates', 'session_unavailable', 'cwd_unavailable', 'log_root_unavailable'] as const) {
      expect(await pollsWithin(() => page({ reason, candidates: [] })), reason).toBe(16)
    }
    // 列出了候选但还没认领（原因为空）：新会话写下记录后服务端会按证据认领。
    expect(await pollsWithin(() => page({ candidates: [candidate('old')] }))).toBe(16)
  })

  it('reads once and stops for reasons that waiting cannot fix, and after transport errors', async () => {
    // 这些情形每次读取都会在服务端记一条审计，不能靠轮询灌满审计表。
    for (const reason of ['unsupported_transport', 'no_agent', 'unsupported_agent', 'read_denied', 'unrecognized_format', 'read_limit_exceeded', 'internal_error'] as const) {
      expect(await pollsWithin(() => page({ supported: false, reason })), reason).toBe(1)
      expect(await pollsWithin(() => page({ reason, candidates: [] })), `${reason} (supported)`).toBe(1)
    }
    expect(await pollsWithin(() => Promise.reject(new APIError(502, 'bad_gateway', '网关错误')))).toBe(1)
  })

  it('restarts the fast window when a bound session is lost again', async () => {
    let phase: 'waiting' | 'bound' | 'lost' = 'waiting'
    const { calls, fetchPage } = recorder((call) => {
      if (phase === 'bound') return call.session
        ? page({ binding: 'selected', session_id: 's1', messages: [], next_cursor: 'c2' })
        : page({ binding: 'verified', session: 'cand-1', session_id: 's1', messages: [textRecord('1', 'user', '问')], next_cursor: 'c1' })
      if (phase === 'lost' && call.session) return page({ reason: 'session_unavailable', reset: true, candidates: [] })
      return page({ reason: 'no_session_candidates', candidates: [] })
    })
    const session = new StructuredChatSession({ hostID: 'host', paneID: 'pane', fetchPage, pollMs: 2000 })
    session.start()
    // 等过一分钟，已经是 10 秒一次的慢速轮询。
    await vi.advanceTimersByTimeAsync(70_000)
    phase = 'bound'
    await vi.advanceTimersByTimeAsync(10_000)
    expect(session.getSnapshot().state.session).toBe('cand-1')
    phase = 'lost'
    await vi.advanceTimersByTimeAsync(2000)
    expect(session.getSnapshot().state).toMatchObject({ session: undefined, reason: 'session_unavailable' })
    // 失去绑定后重新按正常间隔等认领，而不是沿用之前的慢速。
    const lost = calls.length
    await vi.advanceTimersByTimeAsync(4000)
    expect(calls).toHaveLength(lost + 2)
    session.stop()
  })

  it('keeps refreshing candidates while waiting and stops while inactive', async () => {
    let round = 0
    const { calls, fetchPage } = recorder(() => page({ candidates: round++ ? [candidate('old'), candidate('new')] : [candidate('old')] }))
    const session = new StructuredChatSession({ hostID: 'host', paneID: 'pane', fetchPage, pollMs: 2000 })
    session.start()
    await vi.advanceTimersByTimeAsync(2000)
    expect(session.getSnapshot().candidates.map((item) => item.id)).toEqual(['old', 'new'])
    // 只列候选，绝不自动认领。
    expect(session.getSnapshot().state.session).toBeUndefined()
    session.setActive(false)
    const paused = calls.length
    await vi.advanceTimersByTimeAsync(30_000)
    expect(calls).toHaveLength(paused)
    session.stop()
  })

  it('does not poll or auto-bind after the user chose to reselect manually', async () => {
    const { calls, fetchPage } = recorder((call) => call.manual
      ? page({ candidates: [candidate('cand-1')] })
      : page({ binding: 'verified', session: 'cand-1', session_id: 's1', messages: [textRecord('1', 'user', '问')], next_cursor: 'c1' }))
    const session = new StructuredChatSession({ hostID: 'host', paneID: 'pane', fetchPage, pollMs: 2000 })
    session.start()
    await vi.advanceTimersByTimeAsync(0)
    session.reselect()
    await vi.advanceTimersByTimeAsync(0)
    const listed = calls.length
    await vi.advanceTimersByTimeAsync(30_000)
    expect(calls).toHaveLength(listed)
    expect(session.getSnapshot().state.session).toBeUndefined()
    session.stop()
  })
})

describe('durations', () => {
  it('formats durations', () => {
    expect(formatChatDuration(4_400)).toBe('4 秒')
    expect(formatChatDuration(120_000)).toBe('2 分')
    expect(formatChatDuration(3_725_000)).toBe('1 小时 2 分')
  })
})
