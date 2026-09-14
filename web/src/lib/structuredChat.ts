// 结构化对话记录（structured chat）前端数据层。
//
// 数据源是 owner 认证的 HTTP 端点（`docs/design/structured-chat-contract.md`），**不是**
// `pane.read` 终端文本：终端屏幕文本没有 role、没有轮次，也没有字节游标，永远不能当作
// 助手回答。本文件只做三件事：
//
//   1. 构造请求并按既有 `api.ts` 的惯例取数（same-origin cookie、no-store、AbortSignal、
//      401 触发全局登录失效）；
//   2. 实现契约的分页合并规则（`reset` 清空重建、同 id 原地更新、新 id 追加，禁止按文本去重）；
//   3. 提供可独立测试的会话状态机（候选 → 显式选择 → 增量轮询 → 反向分页），
//      包含不重叠轮询、页隐藏 / 断线暂停、迟到响应丢弃、卸载清理。
//
// 这里不碰 React，也不读任何真实用户日志：全部语义都能用假 fetch 断言。

import { APIError, apiErrorMessage, authenticationGeneration, currentSessionID, invalidateAuthentication, onAuthEvent } from './api'
import {
  CHAT_AGENTS,
  CHAT_REASONS,
  CHAT_ROLES,
  CHAT_SOURCES,
  EMPTY_STRUCTURED_CHAT_STATE,
  STRUCTURED_CHAT_POLL_MS,
  type ChatAgent,
  type ChatBlock,
  type ChatBinding,
  type ChatReason,
  type ChatRecord,
  type ChatRole,
  type ChatSessionCandidate,
  type ChatSource,
  type ChatTurn,
  type StructuredChatFetch,
  type StructuredChatQuery,
  type StructuredChatRequest,
  type StructuredChatResponse,
  type StructuredChatState,
} from './structuredChatTypes'

const TRANSCRIPT_PATH_SUFFIX = '/transcript'

/**
 * 请求路径：hostID / paneID 只出现在路径里，不透明值与显式读取源只出现在查询串里。
 *
 * 查询串里**只有** `session` / `cursor` / `before` / `source` 四个键：客户端绝不发送
 * `agent=`、`cwd=` 或任何路径，也不发闭集合之外的 source 值。
 */
export function structuredChatPath(hostID: string, paneID: string, query: StructuredChatQuery = {}) {
  const params = new URLSearchParams()
  if (query.session) params.set('session', query.session)
  if (query.cursor) params.set('cursor', query.cursor)
  if (query.before) params.set('before', query.before)
  if (query.source && (CHAT_SOURCES as readonly string[]).includes(query.source)) params.set('source', query.source)
  const search = params.toString()
  return `/api/hosts/${encodeURIComponent(hostID)}/panes/${encodeURIComponent(paneID)}${TRANSCRIPT_PATH_SUFFIX}${search ? `?${search}` : ''}`
}

function record(value: unknown): Record<string, unknown> | null {
  return value && typeof value === 'object' && !Array.isArray(value) ? (value as Record<string, unknown>) : null
}

function optionalText(value: unknown) {
  return typeof value === 'string' && value ? value : undefined
}

function optionalBoolean(value: unknown) {
  return typeof value === 'boolean' ? value : undefined
}

function optionalNumber(value: unknown) {
  return typeof value === 'number' && Number.isFinite(value) && value >= 0 ? value : undefined
}

function isReason(value: unknown): value is ChatReason {
  return typeof value === 'string' && (CHAT_REASONS as readonly string[]).includes(value)
}

function isAgent(value: unknown): value is ChatAgent {
  return typeof value === 'string' && (CHAT_AGENTS as readonly string[]).includes(value)
}

function isRole(value: unknown): value is ChatRole {
  return typeof value === 'string' && (CHAT_ROLES as readonly string[]).includes(value)
}

function normalizeBlock(value: unknown): ChatBlock | null {
  const raw = record(value)
  if (!raw) return null
  switch (raw.type) {
    case 'text':
      return typeof raw.text === 'string' ? { type: 'text', text: raw.text } : null
    case 'tool-call':
      // `call_id` 允许为空串（provider 未提供时），此时只能按记录内相邻关系配对。
      if (typeof raw.name !== 'string') return null
      return { type: 'tool-call', call_id: typeof raw.call_id === 'string' ? raw.call_id : '', name: raw.name, input: raw.input }
    case 'tool-result':
      if (typeof raw.output !== 'string') return null
      return {
        type: 'tool-result',
        call_id: typeof raw.call_id === 'string' ? raw.call_id : '',
        output: raw.output,
        is_error: raw.is_error === true,
      }
    default:
      // 未知块类型直接丢弃：宁可少显示，也不能猜测内容语义。
      return null
  }
}

/**
 * 把服务端响应收敛成契约形状。字段缺失或类型不符一律丢弃该部分，**不猜测**：
 * 未知 role / 未知块类型不会变成 `user` 或 `assistant`。
 */
export function normalizeStructuredChatResponse(value: unknown): StructuredChatResponse {
  const raw = record(value)
  if (!raw) return { supported: false, reason: 'internal_error', messages: [] }
  const supported = raw.supported === true
  const messages: ChatRecord[] = []
  // `supported:false` 的情形下契约要求 `messages` 恒为空，绝不渲染任何记录。
  if (supported && Array.isArray(raw.messages)) {
    for (const entry of raw.messages) {
      const item = record(entry)
      if (!item || typeof item.id !== 'string' || !item.id || !isRole(item.role)) continue
      if (!Array.isArray(item.blocks)) continue
      const blocks = item.blocks.map(normalizeBlock).filter((block): block is ChatBlock => block !== null)
      const at = optionalText(item.at)
      messages.push(at ? { id: item.id, role: item.role, at, blocks } : { id: item.id, role: item.role, blocks })
    }
  }
  const candidates: ChatSessionCandidate[] = []
  if (Array.isArray(raw.candidates)) {
    for (const entry of raw.candidates) {
      const item = record(entry)
      if (!item || typeof item.id !== 'string' || !item.id) continue
      if (!isAgent(item.agent) || typeof item.session_id !== 'string') continue
      candidates.push({
        id: item.id,
        agent: item.agent,
        session_id: item.session_id,
        updated_at: typeof item.updated_at === 'string' ? item.updated_at : '',
      })
    }
  }
  return {
    supported,
    reason: isReason(raw.reason) ? raw.reason : undefined,
    agent: isAgent(raw.agent) ? raw.agent : undefined,
    candidates: raw.candidates === undefined ? undefined : candidates,
    session_id: optionalText(raw.session_id),
    messages,
    next_cursor: optionalText(raw.next_cursor),
    previous_cursor: optionalText(raw.previous_cursor),
    reset: optionalBoolean(raw.reset),
    has_more: optionalBoolean(raw.has_more),
    skipped: optionalNumber(raw.skipped),
    binding: raw.binding === 'selected' || raw.binding === 'verified' ? (raw.binding as ChatBinding) : undefined,
  }
}

/**
 * 取一页结构化记录。沿用 `api.ts` 的请求惯例：同源 cookie、`no-store`、AbortSignal，
 * 401 触发全局登录失效，其余非 2xx 抛 `APIError`。业务性降级（不支持 / 无候选 / 会话失效）
 * 一律是 HTTP 200，因此这里不会把"这台终端没有会话记录"当成网络错误。
 */
export const fetchStructuredChat: StructuredChatFetch = async (request: StructuredChatRequest) => {
  const epoch = authenticationGeneration()
  let response: Response
  try {
    response = await fetch(structuredChatPath(request.hostID, request.paneID, request), {
      method: 'GET',
      credentials: 'same-origin',
      cache: 'no-store',
      headers: { Accept: 'application/json' },
      signal: request.signal,
    })
  } catch (error) {
    // 取消是正常控制流，交给调用方按代次丢弃。
    if (error instanceof Error && error.name === 'AbortError') throw error
    throw new APIError(0, 'network_error', '无法读取会话记录，请检查网络后重试')
  }
  const payload = await response.json().catch(() => ({}))
  if (!response.ok) {
    if (response.status === 401) invalidateAuthentication(epoch)
    const raw = record(payload)?.code
    const code = typeof raw === 'string' ? raw : 'request_failed'
    // 服务端拒绝客户端声明的读取源（例如 snapshot 已经识别出 claude/codex）时给出可执行的
    // 中文说明，不把英文原因直接摊给用户。
    const message = code === 'invalid_source'
      ? '当前终端已被识别为其它 Agent，不能改用这个读取源；请用「默认源」恢复。'
      : apiErrorMessage(payload, '无法读取会话记录，请稍后重试')
    throw new APIError(response.status, code, message)
  }
  // 请求期间登录会话已经变化（退出或被顶掉）时，结果属于上一身份，直接丢弃。
  if (epoch !== authenticationGeneration()) throw new APIError(409, 'auth_changed', '登录状态已变化，请重试')
  return normalizeStructuredChatResponse(payload)
}

function indexById(records: readonly ChatRecord[]) {
  const index = new Map<string, number>()
  records.forEach((item, position) => index.set(item.id, position))
  return index
}

/**
 * 合并一页记录。**同 id 原地更新**（有序数组里替换那一个位置），新 id 追加到末尾。
 * 绝不按 role + 文本去重：同一条 prompt 被问了两次是两条不同 id 的真实记录。
 */
export function mergeChatRecords(current: readonly ChatRecord[], incoming: readonly ChatRecord[]) {
  if (!incoming.length) return current as ChatRecord[]
  const merged = current.slice()
  const index = indexById(merged)
  for (const item of incoming) {
    const position = index.get(item.id)
    if (position === undefined) {
      index.set(item.id, merged.length)
      merged.push(item)
    } else {
      merged[position] = item
    }
  }
  return merged
}

/** 正向合并时新记录在尾部，反向合并时更早的记录在前部且保持原有相对顺序。 */
type PageDirection = 'forward' | 'earlier'

function mergePage(state: StructuredChatState, page: StructuredChatResponse, direction: PageDirection): StructuredChatState {
  if (!page.supported) {
    return {
      status: 'unavailable',
      reason: page.reason || 'internal_error',
      agent: page.agent,
      // 读取源是用户选择，不随一页响应改变；降级态也要保留它，用户才能看出"是按哪个源读的"。
      source: state.source,
      session: undefined,
      sessionID: undefined,
      binding: undefined,
      messages: [],
      nextCursor: undefined,
      previousCursor: undefined,
      hasMore: false,
      skipped: 0,
    }
  }
  if (page.reason === 'session_unavailable') {
    // 会话失效：清空选择与数据，并把服务端重给的候选交回给用户重选。
    return {
      status: 'idle',
      reason: 'session_unavailable',
      agent: page.agent || state.agent,
      source: state.source,
      session: undefined,
      sessionID: undefined,
      binding: undefined,
      messages: [],
      nextCursor: undefined,
      previousCursor: undefined,
      hasMore: false,
      skipped: 0,
    }
  }
  // 只有用户显式选择过候选（`state.session`）才允许渲染消息：服务端即便误发记录也不认领。
  const bound = state.session !== undefined
  const carried = bound && !page.reset ? state.messages : []
  let messages: ChatRecord[] = carried
  if (!bound) {
    messages = []
  } else if (page.reset || !page.messages.length) {
    messages = page.reset ? mergeChatRecords([], page.messages) : carried
  } else if (direction === 'forward') {
    messages = mergeChatRecords(carried, page.messages)
  } else {
    const lookup = new Map(page.messages.map((item) => [item.id, item]))
    const kept = carried.map((item) => lookup.get(item.id) || item)
    const known = new Set(kept.map((item) => item.id))
    messages = [...page.messages.filter((item) => !known.has(item.id)), ...kept]
  }
  return {
    status: !bound ? 'idle' : messages.length ? 'ready' : 'empty',
    // 服务端给出的降级原因原样保留（包括已绑定会话时的 `read_limit_exceeded`），
    // 否则"读不下"会被渲染成"这个会话还没有记录"，正好是契约禁止的伪装。
    reason: page.reason,
    agent: page.agent || state.agent,
    source: state.source,
    session: state.session,
    sessionID: page.session_id || (bound ? state.sessionID : undefined),
    binding: bound ? page.binding || state.binding || 'selected' : undefined,
    messages,
    nextCursor: page.next_cursor ?? (page.reset ? undefined : state.nextCursor),
    // 反向请求（加载更早）的响应是**权威**的：服务端不再给 previous_cursor 就表示已经读到
    // 文件开头，必须清掉它，否则按钮会一直留着、点了也没有任何变化。正向请求只描述游标之后
    // 的新增记录，不携带「更早」的信息，不能拿它覆盖已有的锚点。
    previousCursor: page.previous_cursor ?? (direction === 'earlier' || page.reset ? undefined : state.previousCursor),
    hasMore: page.has_more === true,
    skipped: (page.reset ? 0 : state.skipped || 0) + (page.skipped || 0),
  }
}

/**
 * 契约 `StructuredChatApply`：把一页响应合并进累积状态。
 *
 * - `reset: true` → 先整体清空再用本页重建（文件轮转 / 截断 / 身份变化）；
 * - 未显式选择候选时 `messages` 恒为空，即使服务端误发了消息也不渲染；
 * - `session_unavailable` → 清空选择与数据，交回候选列表让用户重选；
 * - `has_more` 相对本次请求方向，由调用方决定是"还有更新"还是"还有更早"。
 */
export function applyStructuredChatPage(state: StructuredChatState, page: StructuredChatResponse) {
  return mergePage(state, page, 'forward')
}

/** 反向分页（"加载更早"）：同样的合并规则，但更早的记录插在前面。 */
export function prependStructuredChatPage(state: StructuredChatState, page: StructuredChatResponse) {
  return mergePage(state, page, 'earlier')
}

/** 轮次分组：连续的 assistant / tool / system 记录归到前一条 user 之下。 */
export function groupChatTurns(messages: readonly ChatRecord[]): ChatTurn[] {
  const turns: ChatTurn[] = []
  for (const item of messages) {
    if (item.role === 'user') {
      turns.push({ key: item.id, user: item, responses: [] })
      continue
    }
    const last = turns[turns.length - 1]
    if (last) last.responses.push(item)
    else turns.push({ key: item.id, responses: [item] })
  }
  return turns
}

/** 收集会话里所有 tool-call，供只带结果的 tool 记录回填真实工具名与入参。 */
export function collectToolCalls(messages: readonly ChatRecord[]) {
  const calls = new Map<string, { name: string; input: unknown }>()
  for (const item of messages) {
    for (const block of item.blocks) {
      if (block.type === 'tool-call' && block.call_id) calls.set(block.call_id, { name: block.name, input: block.input })
    }
  }
  return calls
}

/** 时间戳展示：今天只显示时分，其余显示月日时分；无法解析时返回空串。 */
export function formatChatTime(value?: string, now = Date.now()) {
  if (!value) return ''
  const at = Date.parse(value)
  if (Number.isNaN(at)) return ''
  const date = new Date(at)
  const pad = (part: number) => String(part).padStart(2, '0')
  const clock = `${pad(date.getHours())}:${pad(date.getMinutes())}`
  return new Date(now).toDateString() === date.toDateString() ? clock : `${pad(date.getMonth() + 1)}-${pad(date.getDate())} ${clock}`
}

/** 列表里只展示会话 id 片段，避免把整段 provider id 铺开。 */
export function shortSessionID(sessionID: string, length = 8) {
  return sessionID.length <= length ? sessionID : `${sessionID.slice(0, length)}…`
}

// ── 显式读取源的「按 pane」记忆 ─────────────────────────────────────────────────
//
// 用户点过"读取 DSH 会话记录"之后，切回终端再切回对话视图不应该悄悄退回默认读取，
// 否则每次进页面都要重新点一次。这份记忆必须**只按 pane 作用域**存在：
//
//   - key = 登录会话身份 + hostID + paneID：跨 pane、跨主机、跨登录身份都不复用；
//   - 没有任何登录身份时不记忆（转录端点本来就要求 owner 认证）；
//   - 登录失效立即整表清空，换人登录时丢掉不属于新身份的条目。
//
// 它只影响"读哪一类日志"，不改变当前终端运行的程序，也不改变输入目标。

const paneReadSources = new Map<string, ChatSource>()
let paneReadSourceListenerReady = false

function paneReadSourceKey(hostID: string, paneID: string) {
  const session = currentSessionID()
  if (!session || !hostID || !paneID) return ''
  return `${session}\n${hostID}\n${paneID}`
}

function ensurePaneReadSourceListener() {
  if (paneReadSourceListenerReady) return
  paneReadSourceListenerReady = true
  onAuthEvent((event) => {
    if (event.kind === 'expired') {
      paneReadSources.clear()
      return
    }
    const keep = `${event.sessionID}\n`
    for (const key of Array.from(paneReadSources.keys())) {
      if (!key.startsWith(keep)) paneReadSources.delete(key)
    }
  })
}

/** 读取某个 pane 记住的显式读取源；没有（或身份已变）返回 undefined。 */
export function readPaneReadSource(hostID: string, paneID: string): ChatSource | undefined {
  ensurePaneReadSourceListener()
  const key = paneReadSourceKey(hostID, paneID)
  return key ? paneReadSources.get(key) : undefined
}

/**
 * 这个 pane 的 agent 是否已经是一个"已识别、且不是 dsh"的读取源归属。
 *
 * 此时服务端会拒绝 `source=dsh`（400 `invalid_source`：snapshot 每次现取，已识别的
 * claude / codex 不接受客户端覆盖），所以客户端**不能**继续保留显式 DSH 读取源，
 * 否则会一直发注定被拒的请求。空 agent（未识别）与未知 agent 都不算拒绝：
 * 那正是"用户可以显式选 DSH"的情形。
 */
export function paneAgentRejectsReadSource(agent?: string) {
  const value = typeof agent === 'string' ? agent.trim() : ''
  if (!value || value === 'dsh') return false
  return (CHAT_AGENTS as readonly string[]).includes(value)
}

/** 记住 / 清除某个 pane 的显式读取源。只写这一个 key，绝不跨 pane 传播。 */
export function rememberPaneReadSource(hostID: string, paneID: string, source: ChatSource | undefined) {
  ensurePaneReadSourceListener()
  const key = paneReadSourceKey(hostID, paneID)
  if (!key) return
  if (source) paneReadSources.set(key, source)
  else paneReadSources.delete(key)
}

export type StructuredChatSnapshot = {
  /** 契约状态机。未显式选择候选时 `messages` 恒为空。 */
  state: StructuredChatState
  /**
   * 候选列表。契约的 `StructuredChatState` 里没有这个字段（那是累积的**记录**状态），
   * 而候选属于一次响应的元信息，所以放在响应快照这一层，不进 `messages`。
   */
  candidates: readonly ChatSessionCandidate[]
  /** 传输层错误（网络 / 401 / 服务端 5xx）的可读文案；业务降级不走这里。 */
  error: string
}

export type StructuredChatSessionOptions = {
  hostID: string
  paneID: string
  /**
   * 初始显式读取源。省略时取该 pane 上次显式选择过的源（见 `readPaneReadSource`）；
   * 两者都没有就是"按服务端从 snapshot 得到的 agent 读取"。
   */
  source?: ChatSource
  /** 取数实现，测试注入假实现；默认走 owner 认证 HTTP。 */
  fetchPage?: StructuredChatFetch
  /** 轮询间隔，默认契约值。 */
  pollMs?: number
  /** 单次"追上增量"最多连取几页，避免服务端持续 has_more 时打满浏览器。 */
  maxCatchUpPages?: number
}

/**
 * 会话状态机。职责边界：
 *
 * - **不自动认领候选**：没有显式 `select()` 之前永不发带 `session` 的请求；
 * - 一次只跑一个请求（不重叠轮询），`cursor` 递增，不重读全量；
 * - 每个异步结果都绑定「host + pane + 登录身份 + 会话代次」，迟到的旧响应直接丢弃；
 * - `stop()` 与切换候选都会作废在途请求，不串 host / pane / session；
 * - 切换显式读取源等于换数据源：清空选择 / 游标 / 候选后重新列候选，**仍然不自动绑定**。
 */
export class StructuredChatSession {
  private snapshotValue: StructuredChatSnapshot = { state: { ...EMPTY_STRUCTURED_CHAT_STATE }, candidates: [], error: '' }
  private readonly listeners = new Set<() => void>()
  private readonly fetchPage: StructuredChatFetch
  private readonly pollMs: number
  private readonly maxCatchUpPages: number
  /** 显式读取源：会话内单一真值来源，`publish` 每次都把它写回状态。 */
  private source: ChatSource | undefined
  private generation = 0
  private running = false
  private active = true
  private inFlight = false
  private earlierQueued = false
  private timer = 0
  private controller: AbortController | null = null
  private unsubscribeAuth: (() => void) | null = null

  constructor(private readonly options: StructuredChatSessionOptions) {
    this.fetchPage = options.fetchPage || fetchStructuredChat
    this.pollMs = options.pollMs ?? STRUCTURED_CHAT_POLL_MS
    this.maxCatchUpPages = Math.max(1, options.maxCatchUpPages ?? 8)
    // 只认"这个 pane 上显式选过的读取源"；绝不由 host / 标题 / 其它 pane 推断。
    this.source = options.source ?? readPaneReadSource(options.hostID, options.paneID)
  }

  getSnapshot = (): StructuredChatSnapshot => this.snapshotValue

  subscribe = (listener: () => void) => {
    this.listeners.add(listener)
    return () => { this.listeners.delete(listener) }
  }

  private publish(state: StructuredChatState, error = '', candidates = this.snapshotValue.candidates) {
    // 读取源永远以会话字段为准写回状态：合并函数不会因为某一页响应而"忘记"用户选过的源。
    this.snapshotValue = { state: { ...state, source: this.source }, candidates, error }
    for (const listener of this.listeners) listener()
  }

  /** 空白状态：没有任何选择 / 游标 / 记录，只带上当前读取源。 */
  private emptyState(status: StructuredChatState['status']): StructuredChatState {
    return { ...EMPTY_STRUCTURED_CHAT_STATE, status }
  }

  /** 挂载：监听登录失效，并立即请求候选列表（不带 `session`，因此不会返回任何消息）。 */
  start() {
    if (this.running) return
    this.running = true
    this.unsubscribeAuth = onAuthEvent((event) => {
      if (event.kind !== 'expired') return
      // 登录失效：全局认证层会接管跳转；这里只把视图置为可读的错误态，不假装成"没有记录"。
      this.invalidate()
      this.publish({ ...EMPTY_STRUCTURED_CHAT_STATE, status: 'error' }, '登录状态已失效，请重新登录后查看', [])
    })
    this.publish({ ...this.snapshotValue.state, status: 'loading' })
    void this.load({})
  }

  /**
   * 用户显式选择读取源（当前只有 `dsh`），或传 `undefined` 放弃它。
   *
   * 这不是 Agent 身份声明：服务端只在 snapshot 的 agent 为空 / 不认识时采用它，
   * 已识别的 claude / codex 不会被覆盖（会返回 400 `invalid_source`）。
   *
   * 切换读取源 = 换数据源：作废在途请求，清空选择 / 游标 / 候选 / 已累积记录后重新列候选。
   * **不自动绑定任何候选**（即使只有一个），也不改变输入目标与当前终端程序。
   * 还没 `start()` 时只更新会话字段与该 pane 的记忆，不发任何请求（不会先发一次注定被拒的请求）。
   */
  setReadSource(source: ChatSource | undefined) {
    if (this.source === source) return
    this.source = source
    rememberPaneReadSource(this.options.hostID, this.options.paneID, source)
    if (!this.running) return
    this.invalidate()
    this.publish(this.emptyState('loading'))
    void this.load({})
  }

  /** 放弃显式读取源，回到"服务端按 snapshot agent 推导"的默认读取；同样清空全部状态。 */
  clearReadSource() {
    this.setReadSource(undefined)
  }

  /** 卸载：作废所有在途请求与定时器。 */
  stop() {
    this.running = false
    this.generation++
    this.clearTimer()
    this.controller?.abort()
    this.controller = null
    this.inFlight = false
    this.unsubscribeAuth?.()
    this.unsubscribeAuth = null
  }

  /** 连接或页面可见性变化：暂停时不发请求，恢复时立刻补一次增量。 */
  setActive(active: boolean) {
    if (this.active === active) return
    this.active = active
    if (!active) {
      this.clearTimer()
      return
    }
    if (this.running) this.schedule(0)
  }

  /** 用户显式选择候选：先清空旧数据，再开始只读这一个会话。 */
  select(candidateID: string) {
    if (!candidateID || this.snapshotValue.state.session === candidateID) return
    this.invalidate()
    this.publish({ ...EMPTY_STRUCTURED_CHAT_STATE, status: 'loading', session: candidateID, agent: this.snapshotValue.state.agent })
    void this.load({ session: candidateID })
  }

  /** 放弃当前选择，回到候选列表。 */
  reselect() {
    if (this.snapshotValue.state.session === undefined) return
    this.invalidate()
    this.publish({ ...EMPTY_STRUCTURED_CHAT_STATE, status: 'loading' })
    void this.load({})
  }

  /** 用户手动重试 / 刷新：丢掉在途请求，按当前选择重新取尾部窗口。 */
  refresh() {
    if (!this.running) return
    const { session } = this.snapshotValue.state
    this.invalidate()
    void this.load(session ? { session } : {})
  }

  /**
   * 反向分页：取更早的一页。
   *
   * 这是用户显式点击触发的动作，不能被"正在轮询"静默吞掉（一次点击落在在途请求里就什么
   * 都不会发生），所以这里排队等当前请求收尾，而不是直接 return。
   */
  loadEarlier() {
    const { session, previousCursor } = this.snapshotValue.state
    if (!this.running || !session || !previousCursor) return
    if (this.inFlight) {
      this.earlierQueued = true
      return
    }
    this.clearTimer()
    void this.load({ session, before: previousCursor }, 'earlier')
  }

  private invalidate() {
    this.generation++
    this.clearTimer()
    this.controller?.abort()
    this.controller = null
    this.inFlight = false
    // 排队中的反向分页属于上一个选择 / 上一份数据，不能跨过去继续执行。
    this.earlierQueued = false
  }

  private clearTimer() {
    if (!this.timer) return
    window.clearTimeout(this.timer)
    this.timer = 0
  }

  private schedule(delay: number) {
    this.clearTimer()
    if (!this.running || !this.active) return
    this.timer = window.setTimeout(() => { this.timer = 0; void this.tick() }, delay)
  }

  private async tick() {
    if (!this.running || !this.active || this.inFlight) return
    const { session, nextCursor } = this.snapshotValue.state
    if (!session) return
    await this.load(nextCursor ? { session, cursor: nextCursor } : { session })
  }

  private async load(query: StructuredChatQuery, direction: PageDirection = 'forward') {
    if (this.inFlight) { this.schedule(this.pollMs); return }
    const generation = this.generation
    const epoch = authenticationGeneration()
    const login = currentSessionID()
    const { hostID, paneID } = this.options
    const controller = new AbortController()
    this.controller = controller
    this.inFlight = true
    const stale = () => !this.running || generation !== this.generation || epoch !== authenticationGeneration() || login !== currentSessionID()
    let cursor = query.cursor
    try {
      // 服务端触顶后 `has_more` 表示还有记录：立刻续取，而不是等下一个心跳。
      for (let page = 0; page < this.maxCatchUpPages; page++) {
        const response = await this.fetchPage({
          hostID,
          paneID,
          // 显式读取源只在用户选过时出现；请求里永远没有 `agent` / `cwd` / 路径。
          ...(this.source ? { source: this.source } : {}),
          ...(query.session ? { session: query.session } : {}),
          ...(query.before ? { before: query.before } : {}),
          ...(cursor ? { cursor } : {}),
          signal: controller.signal,
        })
        if (stale()) return
        const current = this.snapshotValue.state
        const merged = direction === 'earlier' ? prependStructuredChatPage(current, response) : applyStructuredChatPage(current, response)
        // 候选只在服务端给出时更新；「重选」回到列表时沿用上一次的候选，不必等一次往返。
        const candidates = response.candidates ?? (response.reset ? [] : this.snapshotValue.candidates)
        this.publish(merged, '', candidates)
        if (direction === 'earlier' || !response.has_more || !response.next_cursor) return
        cursor = response.next_cursor
      }
    } catch (error) {
      if (stale()) return
      if (error instanceof Error && error.name === 'AbortError') return
      const message = error instanceof APIError && error.status === 401
        ? '登录状态已失效，请重新登录后查看'
        : error instanceof Error && error.message
          ? error.message
          : '读取会话记录失败，请稍后重试'
      const current = this.snapshotValue.state
      // 有内容时保留内容，只在空态把状态机置为 error，避免一次网络抖动清空正在看的对话。
      this.publish(current.messages.length ? current : { ...current, status: 'error' }, message)
    } finally {
      if (generation === this.generation) {
        this.inFlight = false
        this.controller = null
        // 轮询期间被吞掉的那次「加载更早」在这里补上，不丢用户操作。
        if (this.earlierQueued) {
          this.earlierQueued = false
          this.loadEarlier()
        } else {
          this.schedule(this.pollMs)
        }
      }
    }
  }
}
