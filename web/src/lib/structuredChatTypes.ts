// 结构化对话记录（structured chat）冻结契约。
//
// 本文件只声明类型与常量，不含任何实现，供后端 DTO、前端数据层与 Chat 视图三方并行开发。
// 后端只需能序列化出这里的字段形状，不需要共享代码；字段名与可空性是契约本身。
//
// 权威数据源既不是 Herdr 协议的 RPC，也不是终端屏幕文本，而是 Agent 自己写下的 append-only
// 会话日志（Claude Code 的 `~/.claude/projects/<编码 cwd>/<uuid>.jsonl`、Codex 的
// `~/.codex/sessions/**/rollout-*.jsonl`）。Herdr 的 herdr.sock JSON API 不提供带 role 的
// 消息列表，所以读取能力**不进入 WebSocket 的 RPC 方法白名单**（那条路径零参数校验、原样
// 透传，接入文件读取等于开放路径穿越），而是走单独的、需要 owner 认证的 HTTP 端点：
//
//   GET /api/hosts/{hostID}/panes/{paneID}/transcript
//       ?session=<opaqueCandidateId>&cursor=<opaqueCursor>&before=<opaqueBefore>
//
// 完整语义（候选、显式绑定、游标、分页、半行、rotation、受限读取边界、不支持时的文案）
// 见 `docs/design/structured-chat-contract.md`。改这里的字段等于改契约，必须先改文档。

/**
 * 支持结构化记录的 Agent，闭集合。
 * 其余 Agent（含与 claude/codex 同格式族的 openclaude / grok / omp）在各自的 decoder、
 * 日志根目录与 cwd 匹配规则被真实会话日志验证之前，一律 `supported: false`，不猜、不通配。
 */
export const CHAT_AGENTS = ['claude', 'codex'] as const
export type ChatAgent = (typeof CHAT_AGENTS)[number]

/**
 * 记录角色。
 *
 * 这里**没有** `reasoning`：provider 记录里的思考过程 / 思想链一律在服务端丢弃，
 * 既不输出，也不计入 `skipped`（丢弃是设计行为，不是格式畸形）。
 *
 * `tool` 是派生角色，不是 provider 的原生 role：provider 里 `type: 'user'` 且 blocks
 * 全部是 `tool-result` 的记录会被重判为 `tool`。工具结果因此**永远不会**被当成用户问题
 * 渲染成右侧气泡；反之，只要该 user 记录里还夹着任何非 tool-result 块，它仍然是 `user`。
 */
export const CHAT_ROLES = ['user', 'assistant', 'tool', 'system'] as const
export type ChatRole = (typeof CHAT_ROLES)[number]

/** 普通正文 / Markdown。用户问题、助手回答、系统提示都走这一种块。 */
export type ChatTextBlock = {
  type: 'text'
  text: string
}

/** Agent 发起的一次工具调用。 */
export type ChatToolCallBlock = {
  type: 'tool-call'
  /** provider 的工具调用 id（Claude 的 `tool_use.id` / Codex 的 `call_id`）。
   *   provider 未提供时为空串 `''`，此时只能按记录内的相邻关系与同一条 assistant 记录配对。 */
  call_id: string
  name: string
  /** 工具入参原始对象。每个工具形状不同，客户端只做只读预览，不假设结构。 */
  input: unknown
}

/** 工具执行结果。 */
export type ChatToolResultBlock = {
  type: 'tool-result'
  /** 与对应 `tool-call.call_id` 相同；provider 未提供时为空串 `''`。 */
  call_id: string
  output: string
  is_error: boolean
}

export type ChatBlock = ChatTextBlock | ChatToolCallBlock | ChatToolResultBlock

/**
 * 一条对话记录。
 *
 * 只输出文本、工具调用、工具结果三类块；不输出图片引用、编辑补丁、token 用量、
 * 请求 id、原始 provider 帧等 provider 内部字段。
 */
export type ChatRecord = {
  /**
   * 记录身份：在同一会话内稳定且唯一。合并时必须按 id **原地更新**（upsert），
   * 不是"已存在就丢弃"，也不能再追加一条同 id 的记录。
   *
   * - Claude：source record 的 `uuid`。同一条记录被 provider 重发（内容可能已更新，
   *   例如工具结果补齐）时 uuid 相同，按更新处理。
   * - Codex：没有稳定 uuid，用 `<session_id>:<源文件字节 offset>`。字节 offset 天然区分
   *   重复内容，所以同一个问题被问两次会得到两条不同 id 的记录，**两条都必须保留**。
   *
   * 禁止用 `role + 归一化文本` 的 hash 充当 id 或同源去重键：那会把同源重复 prompt
   * 吞成一条，正好丢掉用户真实问过的第二次。跨源（将来若加入 hook / scrape）才允许
   * 按 turn 合并，且必须显式比较来源优先级；本期只有单一磁盘源，不存在跨源合并。
   */
  id: string
  role: ChatRole
  /** RFC3339 时间戳；provider 未提供时省略（不写空串，不写 `null`）。 */
  at?: string
  blocks: ChatBlock[]
}

/**
 * 一个可绑定的会话候选。
 *
 * 不含完整文件路径、不含 cwd 原文、不含任何用户 prompt 预览——候选列表本身不泄露
 * 会话内容。`id` 是不透明值，客户端只回传，不解析、不构造、不缓存成路径。
 */
export type ChatSessionCandidate = {
  /** 不透明候选 id，由服务端对「pane 作用域 + agent + 文件身份」派生。 */
  id: string
  agent: ChatAgent
  /** provider 侧会话 id（Claude 的 `<uuid>.jsonl` 文件名 / Codex 的 thread id），仅用于展示与区分。 */
  session_id: string
  /** 该会话日志最后写入时间（RFC3339），用于列表排序展示。 */
  updated_at: string
}

/** 服务端是否把该会话绑定到 pane。 */
export const CHAT_BINDINGS = ['selected', 'verified'] as const
export type ChatBinding = (typeof CHAT_BINDINGS)[number]
// 'selected' —— 用户在候选列表里显式选择了这条记录。这是本期唯一允许产生的绑定。
// 'verified' —— 服务端用严格且已验证的 pane/provider 会话证据自动绑定（例如 agent hook
//              上报了属于该 pane 的 session_id + transcript_path）。**本期不得产生**：
//              herdrx 目前没有任何这类证据源，凭空返回 'verified' 就是臆造绑定。

/**
 * 降级原因，闭集合。`supported` 的真假与 `reason` 的取值必须一一对应，不允许自造字符串。
 *
 * - `unsupported_agent`      pane 的 agent 不在 `CHAT_AGENTS` 内。
 * - `no_agent`               pane 上没有 agent（普通 Shell）。
 * - `unsupported_transport`  当前接入方式拿不到受限文件读取能力。
 * - `cwd_unavailable`        snapshot 里该 pane 既没有 `foreground_cwd` 也没有 `cwd`。
 * - `log_root_unavailable`   远端日志根不存在、不可读，或取不到 `$HOME`。
 * - `session_unavailable`    `session` 参数已失效（文件被删除 / 轮转 / 不再是同一个身份）。
 * - `no_session_candidates`  supported 成立，但当前 pane agent + 精确 cwd 下没有候选。
 * - `read_denied`            路径逃逸、符号链接、非普通文件被拒。
 * - `unrecognized_format`    文件存在但不是可识别的会话日志（字段形状全不匹配）。
 * - `internal_error`         服务端内部错误；客户端按可重试处理。
 */
export const CHAT_REASONS = [
  'unsupported_agent',
  'no_agent',
  'unsupported_transport',
  'cwd_unavailable',
  'log_root_unavailable',
  'session_unavailable',
  'no_session_candidates',
  'read_denied',
  'unrecognized_format',
  'internal_error'
] as const
export type ChatReason = (typeof CHAT_REASONS)[number]

/** 客户端请求参数。三个字段都是不透明值，客户端不得推算或伪造其中任何一个。 */
export type StructuredChatQuery = {
  /**
   * 候选 id，来自同一 pane 的 `candidates` 列表。
   * 省略 = 只列候选、不返回任何消息（`messages: []`），服务端**绝不**自动挑一个。
   * 它不是一个路径许可：给出某个候选 id 只授权读取服务端已认定的那一个文件。
   */
  session?: string
  /** 正向增量游标：带上它取上次之后新增的记录。 */
  cursor?: string
  /** 反向游标：取更早的一页。与 `cursor` 互斥；同时出现时服务端以 `before` 为准。 */
  before?: string
}

/**
 * 端点响应。所有降级情形同样返回 HTTP 200（否则前端只能显示网络错误，
 * 无法渲染诚实的空态）。
 *
 * 状态组合是闭合的：
 * 1. `supported: false` → `reason` 必填，`messages: []`，`candidates` 可省略。
 * 2. `supported: true` 且无 `session` → 返回 `candidates`，`messages: []`。
 *    候选为空时 `reason: 'no_session_candidates'`。
 * 3. `supported: true` 且 `session` 失效 → `reason: 'session_unavailable'`，
 *    同时重新返回 `candidates` 让用户重选，`messages: []`，`reset: true`。
 * 4. `supported: true` 且已绑定 → `messages` 为该页记录，`binding: 'selected'`，
 *    附带 `agent` / `session_id` / 游标。文件为空时 `messages: []`，这不是错误。
 */
export type StructuredChatResponse = {
  supported: boolean
  reason?: ChatReason
  /** 仅在 `supported` 且已确定 pane agent 时出现。 */
  agent?: ChatAgent
  /** 无 `session` 查询时返回的候选列表，已按 pane agent 与精确 cwd 限定。 */
  candidates?: ChatSessionCandidate[]
  /** 已绑定会话的 provider 侧会话 id。 */
  session_id?: string
  /**
   * 本次返回的记录。任何未经用户显式绑定的情形下都是 `[]`。
   * 绝不用终端文本、屏幕快照或任何启发式内容填充这个数组。
   */
  messages: ChatRecord[]
  /**
   * 正向游标：下次轮询带上它取新增记录。不透明。
   * 只在服务端消费到**完整行**之后才推进；尾部未闭合的半行不消费、不推进。
   */
  next_cursor?: string
  /** 反向游标：取更早一页。不透明。仅在还有更早内容时出现。 */
  previous_cursor?: string
  /**
   * 游标与当前会话 / 文件身份不再匹配（文件被轮转、截断、替换，或游标未落在行边界）。
   * 客户端必须丢弃已累积的全部记录，用本页内容整体重建。
   */
  reset?: boolean
  /**
   * 相对本次请求方向是否还有未返回的记录：
   * 正向请求 = 现在还有更新的记录；带 `before` 的请求 = 还有更早的记录。
   */
  has_more?: boolean
  /**
   * 本次被跳过的无法识别的源记录数，用于让"上游改字段导致静默丢消息"可见。
   * 被省略的 reasoning 记录**不**计入这里。
   */
  skipped?: number
  /** 绑定来源。本期只允许 `'selected'`。 */
  binding?: ChatBinding
}

/** 前端数据层的请求形状（`hostID` / `paneID` 走路径，其余走进程查询串）。 */
export type StructuredChatRequest = StructuredChatQuery & {
  hostID: string
  paneID: string
  signal?: AbortSignal
}

/** 前端数据层必须实现的取数签名，落在 `web/src/lib/structuredChat.ts`。 */
export type StructuredChatFetch = (request: StructuredChatRequest) => Promise<StructuredChatResponse>

/** 视图状态机。`unavailable` 对应 `supported: false`，`empty` 对应已绑定但文件暂无记录。 */
export const CHAT_LOAD_STATUSES = ['idle', 'loading', 'ready', 'empty', 'unavailable', 'error'] as const
export type ChatLoadStatus = (typeof CHAT_LOAD_STATUSES)[number]

/** 客户端累积状态。`session` 为空时 `messages` 必须恒为空。 */
export type StructuredChatState = {
  status: ChatLoadStatus
  reason?: ChatReason
  agent?: ChatAgent
  /** 用户显式选中的候选 id；未选择时为 undefined。 */
  session?: string
  sessionID?: string
  binding?: ChatBinding
  messages: ChatRecord[]
  nextCursor?: string
  previousCursor?: string
  hasMore: boolean
  skipped: number
}

export const EMPTY_STRUCTURED_CHAT_STATE: StructuredChatState = {
  status: 'idle',
  messages: [],
  hasMore: false,
  skipped: 0
}

/**
 * 分页合并签名，落在 `web/src/lib/structuredChat.ts`。
 * 规则：`reset: true` 先整体清空再应用本页；否则按 `record.id` **原地更新**已存在的记录
 * （不是丢弃，也不是追加第二条），新 id 追加。禁止任何按文本内容去重的逻辑。
 */
export type StructuredChatApply = (
  state: StructuredChatState,
  page: StructuredChatResponse
) => StructuredChatState

/**
 * 轮次分组：把连续的 assistant / tool / system 记录归到前一条 user 之下。
 * 落在 `web/src/lib/structuredChat.ts`，纯函数，不碰 React、不做 IO。
 */
export type ChatTurn = {
  /** 稳定的 React key，取该轮 user 记录的 id；无 user 的轮次取第一条记录的 id。 */
  key: string
  user?: ChatRecord
  responses: ChatRecord[]
}

export type ChatTurnGrouping = (messages: readonly ChatRecord[]) => ChatTurn[]

/**
 * 助手正文的 Markdown 渲染签名。返回值必须是可以直接交给 `dangerouslySetInnerHTML`
 * 的**已净化** HTML：先转义原始 HTML，再过白名单净化。`a` 必须强制
 * `target="_blank" rel="noopener noreferrer"`。若最终不引入 Markdown 依赖，
 * 用纯文本 + `<pre>` 渲染也满足契约，但必须与用户气泡在视觉上明确区分。
 * 若引入依赖，必须重新生成 `THIRD_PARTY_NOTICES.md` 与 `sbom.cdx.json`。
 */
export type ChatMarkdownRenderer = (text: string) => string

export const CHAT_MARKDOWN_ALLOWED_TAGS = [
  'p', 'br', 'strong', 'em', 'del', 'code', 'pre', 'a', 'ul', 'ol', 'li',
  'blockquote', 'h1', 'h2', 'h3', 'h4', 'h5', 'h6', 'table', 'thead', 'tbody', 'tr', 'th', 'td', 'hr'
] as const

// ── 契约上限。服务端可以调低，客户端必须能处理更小的值；任何一侧都不得调高。 ──

/** 一页默认记录数。 */
export const STRUCTURED_CHAT_DEFAULT_PAGE = 200
/** 一页硬上限；超出即切页并置 `has_more: true`。 */
export const STRUCTURED_CHAT_MAX_PAGE = 500
/** 单次响应体上限（字节）。超出的记录留到下一页，绝不截断成半条记录。 */
export const STRUCTURED_CHAT_MAX_RESPONSE_BYTES = 256 * 1024
/** 单个块的上限（字节）。超过后服务端在块**之后**追加一条说明用 text 块，
 *  标记常量见 `STRUCTURED_CHAT_ELISION_PREFIX`；被截断的原始块本身保持结构完整。 */
export const STRUCTURED_CHAT_MAX_BLOCK_BYTES = 64 * 1024
/** 首屏（无游标）只返回日志尾部这一段字节、并按行对齐，避免打开大文件就全量传输。 */
export const STRUCTURED_CHAT_INITIAL_WINDOW_BYTES = 256 * 1024
/** 单次最多返回的候选数。 */
export const STRUCTURED_CHAT_MAX_CANDIDATES = 20
/** 前端轮询间隔；只按游标取增量，不重读全量。 */
export const STRUCTURED_CHAT_POLL_MS = 2000
/** 截断说明块的固定前缀；客户端可据此识别并提示，无需新增响应字段。 */
export const STRUCTURED_CHAT_ELISION_PREFIX = '…（已截断'
