# Chat 语音输入与图片附件契约（chat media contract）

日期：2026-09-13。状态：**已冻结，待实现**。本文是实现前的契约，不代表已实现或已验收。

本契约定义 Chat 视图新增的**语音输入**与**图片附件**两条数据路径。类型与常量的唯一正文是
[`web/src/lib/chatMediaTypes.ts`](../../web/src/lib/chatMediaTypes.ts)；本文解释语义、路由、
ENV、生命周期与接线点，不重复字段定义。

本文只冻结接口与边界。**本契约不实现任何业务代码**；`components/media/**`、`lib/voice*`、
`lib/chatMedia*`、`internal/voicegateway/**`、`internal/httpapi/voice*.go` 由后续实现工作流
按 §9 的分工创建，主代理最后接线。

---

## 0. 一句话结论

- **语音**：上游网关只实测支持 **OpenAI Realtime API 的 standalone WebSocket 会话传输**。
  浏览器原生 `WebSocket` 不能带 `Authorization` 头，所以语音**必须**经 herdrx 自己的同源中继；
  服务端凭据不回浏览器。默认把**用户自己**的最终识别文本追加进草稿，**不自动发送**；
  音频回复是显式可选项；语音模型的回答**绝不**写入草稿或终端，也就不冒充终端 Agent。
- **图片**：拖拽/粘贴/选择后先落**占位**，随即以 **stage-only**（`inject=false`）上传，
  **不**因拖入而向终端打字；发送时把「文本 + 引用」合成**一次** `pane.send_input`，
  **仅图片也能发**。

---

## 1. 上游语音能力：实测证据

本节记录**真实探测**结果。凭据取自本机既有进程配置，不打印、不落盘、不写入源码。

### 1.1 网关与身份

- 语音网关是一个已配置的 **AsterGate 兼容网关**，对所有路径统一加 CORS 头并带
  `x-astergate-trace-id`；前置是 Caddy。地址与密钥**只存在于部署 ENV**，源码与本文都不写。
- 鉴权：`Authorization: Bearer <key>` 与 `X-API-Key: <key>` 都可用；`?api_key=` 一律 401。
- **浏览器不能设置这两个头**，所以浏览器直连上游在协议上就不可能——这正是需要同源中继的原因。

### 1.2 路由与协议（实测）

| 探测 | 结果 | 结论 |
|---|---|---|
| `GET /v1/models`（Bearer） | 200，319 个模型 | 目录可用，但**不含任何 realtime 名字** |
| 319 个模型里含 `realtime`/`live`/`audio` 的 | 只有 `gpt-live-1-codex` | 该名字是 **WebRTC/quicksilver 传输**的，会话传输会拒绝它 |
| `GET /v1/realtime`（无凭据） | 401 `invalid or missing API key` | 已注册且鉴权生效 |
| `GET /v1/realtime`（纯 HTTP，无 Upgrade） | 426 `a WebSocket upgrade is required` | 确认是 WebSocket 端点 |
| `OPTIONS /v1/realtime` | 204 | 该路径预检被应答（与我们无关，浏览器不直连） |
| `POST /v1/audio/speech`、`POST /v1/audio/transcriptions` | 404 | **没有独立 STT/TTS 端点**；识别只能走 realtime 会话 |

**真实联调（一次，`gpt-realtime`，1.4s 完成）**：

```
wss://<ag-gateway>/v1/realtime?model=gpt-realtime
  Authorization: Bearer <server key>            → 101
← session.created   { model: "gpt-realtime" }
→ session.update    { session: { type: "realtime", output_modalities: ["text"] } }
← session.updated
→ conversation.item.create { type:"message", role:"user", content:[{type:"input_text", text:"Reply with exactly: ok"}] }
→ response.create
← response.output_text.delta … response.output_text.done { text: "ok" }
```

即：**会话传输可用，`output_modalities: ["text"]` 可用，无 audio 帧**。这直接支持
"文字回复"（不朗读）的一种模式，且验证了中继链路本身。

### 1.3 协议与模型（冻结）

- **协议 = OpenAI Realtime API**，网关**逐字节转发**，不重塑这一路（其自身文档明说不改写）。
  因此事件名以 OpenAI Realtime 官方 wire 为准，**不猜事件**。
- **可用模型**：`gpt-realtime`、`gpt-realtime-mini`（以及带日期的快照）。默认用 `gpt-realtime`。
- **不要**发 `OpenAI-Beta: realtime=v1`（上游返回 "The Realtime Beta API is no longer supported"）。
- `session.update` **必须**带 `session.type: "realtime"`，否则报 `missing_required_parameter`。
- **不支持 Gemini Live**，也没有第二种双工协议。上游不支持 WebRTC 传输（Face 级设备认证），
  只有 standalone WebSocket 会话传输可用。**"支持兼容双工语音模型"= 支持 OpenAI Realtime
  兼容的模型名**，通过服务端 ENV 切换，不是多协议适配。

### 1.4 未实测（必须在实现阶段复核，不得当作已通）

- **上行音频识别**（`conversation.item.input_audio_transcription.*`）。这是"默认转写到草稿"
  的必需环节，是 OpenAI Realtime 的标准能力且被逐字节转发，但**本次没有花额度实测**。
  实现阶段每个模型最多再测一次；若上游拒绝，按 §7 降级，**不得**声称已联调。
- 音频回复的实际音质/延迟/上限。本次只验证了 `output_modalities: ["text"]`。
- 并发与配额：网关对实时会话的并发与时长上限由上游账号决定，herdrx 侧只做自己的上限。

---

## 2. 语音：ENV（冻结）

沿用 `HERDRX_*` 命名。**全部默认关闭/为空**；未配置时能力接口返回 `enabled: false`，
UI 隐藏麦克风入口，**不触发**授权弹窗。

| ENV | 默认 | 含义 |
|---|---|---|
| `HERDRX_VOICE_ENABLED` | `false` | 总开关。false 时所有 `/api/voice/*` 返回 404 |
| `HERDRX_VOICE_BASE_URL` | 空 | 上游网关基址（`https://`）。**密钥在同一台工作台主机上，不回浏览器** |
| `HERDRX_VOICE_API_KEY` | 空 | 服务端凭据。只在服务端进程内存与上游连接中出现 |
| `HERDRX_VOICE_MODEL` | `gpt-realtime` | 兼容双工语音模型名。**客户端不可覆盖** |
| `HERDRX_VOICE_REALTIME_PATH` | `/v1/realtime` | 上游路径。**固定，不接受客户端输入** |
| `HERDRX_VOICE_INPUT_SAMPLE_RATE` | `24000` | 上行 PCM 采样率（Hz） |
| `HERDRX_VOICE_OUTPUT_SAMPLE_RATE` | `24000` | 下行 PCM 采样率（Hz） |
| `HERDRX_VOICE_AUDIO_REPLY` | `off` | `allowed` 才允许客户端请求音频回复 |
| `HERDRX_VOICE_MAX_SESSIONS_PER_USER` | `1` | 每用户并发语音会话上限 |
| `HERDRX_VOICE_MAX_SESSIONS` | `4` | 实例并发语音会话上限 |
| `HERDRX_VOICE_MAX_SESSION_SECONDS` | `600` | 单次会话硬时长上限 |
| `HERDRX_VOICE_IDLE_SECONDS` | `30` | 双向都无帧则关闭 |
| `HERDRX_VOICE_MAX_UPLINK_AUDIO_SECONDS` | `120` | 单次会话累计上行音频时长上限 |
| `HERDRX_VOICE_MAX_UPLINK_BYTES` | `8388608` | 单次会话累计上行字节上限（音频+控制） |
| `HERDRX_VOICE_MAX_CONTROL_BYTES` | `4096` | 单条上行 JSON 控制帧上限 |
| `HERDRX_VOICE_MAX_AUDIO_FRAME_BYTES` | `65536` | 单条上行二进制音频帧上限 |
| `HERDRX_VOICE_CREATE_PER_MINUTE` | `10` | 每用户每分钟建会话次数上限 |

`BASE_URL`、`API_KEY`、`MODEL` 任一为空（或 `ENABLED=false`）时能力为 `enabled: false`。
`BASE_URL` 必须能被 `config.NormalizeOrigin` 风格校验接受（精确 scheme+host，无凭据、无路径、
无通配符）；**不接受**用户输入。

---

## 3. 语音：新 HTTP / WS 路由（冻结）

全部挂在 `internal/httpapi/server.go` 的 `authenticate` 组内（新增 `router.Route("/voice", a.voiceRoutes)`），
沿用现有 cookie 会话 + CSRF + Origin 三道边界。**不复用** `/api/hosts/{hostID}/ws`：
那条连接承载终端观察流，语义与生命周期都不同。

### 3.1 `GET /api/voice/capabilities`

- 中间件：`authenticate`（GET 不需要 CSRF）。
- 响应 200：`VoiceCapabilities`（见类型文件）。
- 用途：页面加载时判断是否显示麦克风入口。`enabled: false` 时 UI 直接隐藏，**不请求麦克风**。
- 不泄露上游地址、密钥或账号信息。

### 3.2 `POST /api/voice/sessions`

- 中间件：`authenticate` + `requireCSRF` + `requireJSON`。
- 请求体：`{ "output": "text" | "audio" }`。**只允许这两个字面量**；出现任何其他字段一律 400。
- **不接受** `model`、`url`、`base_url`、`api_key`、`target`、`instructions` 等字段——多余字段直接拒绝，
  而不是忽略。
- `output: "audio"` 在 `audioReply !== 'allowed'` 时返回 403 `voice_audio_disabled`。
- 响应 200：`{ ticket, expires_at, capabilities }`。
  - `ticket`：一次性不透明令牌（32 字节 CSPRNG，base64url），**仅绑定** `{user_id, session_id, 签发代数}`。
    单次使用，TTL 30 秒，未使用即过期；每用户同时最多 3 个未消费票据。
  - 不在响应里出现上游地址、密钥或上游 session id。
- 频率限制：复用 `internal/httpapi/guards.go` 的 `requestLimiter`（key = user id）。
- 并发限制：超过 `MAX_SESSIONS_PER_USER` / `MAX_SESSIONS` 返回 429 `voice_busy`，
  并给出可执行中文文案；**不排队**。

### 3.3 `GET /api/voice/ws?ticket=…`

- 中间件：`authenticate` + `requireOrigin`（与 `/api/hosts/{hostID}/ws` 一致）。
- 握手前消费票据：一次性、未过期、属于当前 user+session 才允许升级；否则 403 并**不建立上游连接**。
- 握手成功后：服务端用**自己的**密钥向
  `wss://<HERDRX_VOICE_BASE_URL><HERDRX_VOICE_REALTIME_PATH>?model=<HERDRX_VOICE_MODEL>`
  发起上游连接，并**逐字节**中继帧。
- 上游连接失败：向浏览器发 `{"t":"error","code":"voice_upstream_unavailable","message":"…"}` 后
  以 1011 关闭；**不**把上游原始错误体（可能含账号信息）透传给浏览器。
- 读上限：控制帧 4 KiB、二进制音频帧 64 KiB；超限关闭。
- 生命周期见 §5。

### 3.4 浏览器 ↔ herdrx 帧协议（冻结）

**控制走文本 JSON，音频走二进制帧**。浏览器侧不出现 base64。

浏览器 → herdrx：

| 帧 | 载荷 | 映射到上游 |
|---|---|---|
| 文本 `{"t":"start","output":"text"\|"audio"}` | 必须为首帧 | `session.update`（`session.type:"realtime"`、`output_modalities`、`audio.input.transcription`、`audio.output.voice`、`turn_detection`、`instructions`） |
| 二进制 | 原始 PCM16LE、单声道、`inputSampleRate` | base64 后 `input_audio_buffer.append` |
| 文本 `{"t":"commit"}` | — | `input_audio_buffer.commit` |
| 文本 `{"t":"cancel"}` | — | `response.cancel`（barge-in） |
| 文本 `{"t":"stop"}` | — | 关闭上游，随后关闭本连接 |

herdrx → 浏览器：

| 帧 | 来源 |
|---|---|
| 文本 `{"t":"ready",…}` | `session.created` + `session.updated` 合并（`model`、采样率、是否允许音频回复） |
| 文本 `{"t":"partial","text":…}` | `conversation.item.input_audio_transcription.delta` |
| 文本 `{"t":"transcript","text":…,"final":bool}` | `…input_audio_transcription.completed`；**这是唯一可写草稿的内容** |
| 文本 `{"t":"assistantText","text":…,"final":bool}` | `response.output_audio_transcript.delta` / `response.output_text.delta` |
| 二进制 | `response.output_audio.delta` 解码后的 PCM16LE |
| 文本 `{"t":"error","code":…,"message":…}` | 归一化后的错误，**不透传**上游原文 |
| 文本 `{"t":"closed","reason":…}` | 会话结束原因 |

上游的其他帧一律丢弃并计入计数，不转发。

---

## 4. 语音：语义约束（"不冒充终端 Agent"）

1. **写草稿的只有用户自己的话。** 只有 `transcript`（`final: true`）经
   `appendVoiceTranscript` 追加进当前 pane 草稿，且只调用 `writeComposerDraft`。
2. **绝不自动发送。** VoiceInput 不存在任何发送路径：不发 `pane.send_input`、不发 `keys`、
   不调用 `runComposerSend`。发送永远由用户点发送按钮触发。
3. **语音模型的回答不是终端 Agent 的话。** `assistantText` 只交给 `onAssistantText` 由调用方
   单独渲染（建议标签"语音助手（非终端 Agent）"），**绝不**写入草稿、聊天记录或终端。
4. **语音模型不碰终端。** 中继不暴露任何 `pane.*`、`agent.*` 或 `terminal.*` 方法；上游即使
   发来工具/委派帧也不映射到终端操作。`delegation` 一律不启用。
5. **`instructions` 由服务端固定**，明确说明这是"语音转写/对话助手"，不得声称代表 pane 内的
   终端 Agent，也不得声称已执行任何终端操作。
6. **音频回复是显式可选项**，默认 `output: "text"`；`audioReply: "off"` 时服务端拒绝音频请求。

---

## 5. 语音：生命周期与清理（具体）

**麦克风授权**

- 只在用户**显式点击**麦克风按钮后调用 `navigator.mediaDevices.getUserMedia({ audio: {...} })`。
- **前置条件**：`internal/httpapi/server.go` 现在发 `Permissions-Policy: camera=(), microphone=(), geolocation=()`。
  `microphone=()` **会直接禁止**本源的麦克风。必须改为 **`microphone=(self)`**。这是 §8 的必改接线点。
- **安全上下文**：`getUserMedia` 只在 HTTPS 或 localhost 可用。项目默认只提供 HTTP；在非 localhost
  的 HTTP 页面上 `navigator.mediaDevices` 为 `undefined`。此时必须显示可执行的中文说明
  （"语音输入需要 HTTPS 或 localhost 访问"），并隐藏按钮——**不**静默失败。
- 授权被拒：显示"浏览器未授权麦克风…"，停在 `error` 状态，不重试弹窗。

**PCM 采样率**

- 上行：`new AudioContext({ sampleRate: ready.inputSampleRate })`；若实际 `audioCtx.sampleRate`
  与之不等，走线性重采样，**必须**按 `ready.inputSampleRate` 输出 PCM16LE 单声道小端。
- 采集用 `AudioWorklet`（不可用时回退 `ScriptProcessorNode`，并在状态里注明降级）。
- 每帧固定 20–40 ms（24000 Hz 下 480–960 样本），不超过 `MAX_AUDIO_FRAME_BYTES`。
- 下行：按 `ready.outputSampleRate` 解释二进制帧；播放用独立 `AudioContext`，**不要**与采集共用。

**stop / unmount / logout / 后台 清理**（必须全部覆盖，且幂等）

| 触发 | 客户端 | 服务端 |
|---|---|---|
| 用户按停止 | `handle.close('user')`，停止音轨、`track.stop()`、断开 AudioWorklet、关闭两个 AudioContext | 收到 `stop` → 关上游 → 关本连接 |
| 组件 unmount / `visible=false` | `useEffect` cleanup 里同上 | 连接关闭即释放上游 |
| 登出 / 会话失效 | 订阅 `onAuthEvent`；`kind === 'expired'` 时立即关闭 | `authenticate` 的 access lease 被 `access.change` 取消，`lease.ctx` 到点 → 关上游、关连接（沿用 `internal/httpapi/workbench.go` 的同一套语义） |
| 页面 `pagehide` | 关闭 | 连接关闭即释放 |
| 断线（浏览器） | 不重连（语音会话不重放）；回到 `idle` | `ctx.Done()` → 关上游 |

- 麦克风必须**在 `closed` 事件到达前**停止采集，避免上游已关还在推流。
- 服务端并发计数必须在**连接关闭时**释放（`defer`），并在实例 `Close()` 时关闭所有语音上游。

---

## 6. 图片：现状核实（不是猜测）

| 事实 | 位置 |
|---|---|
| 上传路由 `POST /api/hosts/{hostID}/panes/{paneID}/paste-image?inject=<bool>`，`authenticate` + `requireCSRF` | `internal/httpapi/hosts.go:53` |
| `inject := request.URL.Query().Get("inject") != "false"`；即 **`inject=false` → 只落盘，不发送** | `internal/httpapi/paste.go` |
| `inject=false` 的返回体是 `200 {"ok":true,"path":"<远端路径>","injected":false}` | `internal/httpapi/paste.go` |
| 注入用 `pane.send_input`、`keys: []`（空 keys，**不带 Enter、不带空格**），让 Herdr 按目标程序 bracketed-paste 模式编码 | `internal/httpapi/paste.go`、`docs/design/adr/0002-image-paste-delivery.md` |
| 服务端上限：整体 multipart 25MB、单图 20MB，允许 png/jpeg/webp/gif（按魔数嗅探） | `internal/httpapi/paste.go` |
| 前端 `api.pasteImage(hostID, paneID, file, inject = true)` | `web/src/lib/api.ts:118` |
| 前端已有限制 `MAX_IMAGE_SIZE = 20MB`、`clipboardImages`、`ownsImagePaste` | `web/src/lib/imagePaste.ts` |
| **单次输入接口**：WS `client.call('pane.send_input', composerSubmitParams(paneID, text))`，即 `{pane_id, text, keys:['Enter']}` | `web/src/lib/composerDrafts.ts`、`web/src/lib/workbench.ts:184` |
| 草稿事务：`writeComposerDraft` 按 session/host/pane 隔离并持久化；`runComposerSend` 整段提交**一次**，仅在 revision 未变时清空草稿；登录失效时 `clearComposerDrafts` | `web/src/lib/composerDrafts.ts` |

**结论**：`inject=false` 的 stage-only 路径**已经存在且返回远端路径**，不需要新的服务端上传接口；
图片能力的缺口全在前端（占位、引用合成、仅图片可发）。

---

## 7. 图片：语义约束（冻结）

1. **先占位**。拖拽 / 粘贴 / 选择产生文件后**立刻**建立 `staging` 占位并显示进度；
   不等上传完成。
2. **stage-only**。上传固定 `api.pasteImage(hostID, paneID, file, false)`。**任何**拖入、粘贴或
   选择都**不会**向终端打字——只有 `status: 'staged'` 的远端路径会在用户点发送时进入提交文本。
3. **一次提交**。发送时 `composeChatMediaSubmission(draft, stagedPaths)` 合成一个字符串，
   经草稿事务发出**一次** `pane.send_input`（`keys: ['Enter']`）。
4. **仅图片也能发**。正文为空但有 staged 附件时，发送按钮必须可用，提交文本就是引用行本身。
5. **幂等**。合成前剥掉草稿末尾等于待提交路径的行，保证"失败后保留附件与草稿、手动重试"
   不会重复追加。
6. **失败保留**。`failed`/`unknown` 时**保留**附件与草稿；沿用既有文案，**不自动重放**。
   `delivered` 时清空附件。
7. **前端预检**。类型/大小不合规时不发请求，占位直接置 `failed` 并给出中文原因。
8. **不劫持输入**。沿用 `ownsImagePaste`：对话框、搜索框、富文本输入不被劫持；一次粘贴事件
   只处理一次（优先 `items`，回退 `files`，不同时读两份）。

---

## 8. 必要最小接线点（留给主代理）

以下文件**本契约不改**，列出实现时必须做的最小改动，除此之外不要扩大范围。

### 8.1 `internal/httpapi/server.go`

- `Handler()` 的 `authenticate` 组内新增 `router.Route("/voice", a.voiceRoutes)`（在 `a.voiceRoutes`
  由 `internal/httpapi/voice*.go` 提供之前，先不接线）。
- **必改**：`securityHeaders` 的 `Permissions-Policy` 由
  `camera=(), microphone=(), geolocation=()` 改为 **`camera=(), microphone=(self), geolocation=()`**。
  不改这一行，浏览器会直接拒绝麦克风。
- CSP **不需要**改：`connect-src 'self' ws: wss:` 已覆盖同源 WebSocket。

### 8.2 `internal/config/config.go`（可选）

`voicegateway.LoadConfigFromEnv()` 可自给自足；若要保持配置单一正文，可把 §2 的 ENV 收进
`config.Config` 的一个 `Voice` 字段并在 `Validate()` 里校验（`BASE_URL` 精确 origin、数值范围），
再由 `cmd/herdrx-server` 注入。**二选一，不要两处都解析。**

### 8.3 `web/src/components/Composer.tsx`（最小侵入，四处）

新增**可选** prop `mediaHost?: ComposerMediaHost`。缺席时行为必须与今天**完全一致**。

1. `sendNow()`：空草稿判定由 `!value.trim()` 改为 `!value.trim() && !mediaHost?.canSend()`；
   真正发送前 `const composed = mediaHost ? mediaHost.compose(readComposerDraft(hostID, paneID)) : current`，
   为 `null` 则放弃；否则 `writeComposerDraft(hostID, paneID, composed)` 后照旧 `runComposerSend`。
2. 发送按钮 `disabled`：`empty` 改为 `empty && !mediaHost?.canSend()`。
3. `onPaste`：有 `mediaHost` 时把图片文件交给 `mediaHost.onFiles`，否则沿用 `onPasteImages`。
4. 渲染 `mediaHost.tray` 于 textarea 上方；`runComposerSend` 结束后调用
   `mediaHost?.onSettled?.(readComposerSend(hostID, paneID).status)`（需要把 `void runComposerSend(...)`
   改成 `.then(...)` 链，一行）。

### 8.4 `web/src/components/ChatView.tsx`

把 `<Composer … variant="chat" />` 换成
`<ChatMediaComposer … variant="chat" stageImage={…} voice={<VoiceInput …/>} />`，
其余 props 原样透传。ChatView 不感知附件状态。

### 8.5 `web/src/pages/WorkbenchPage.tsx`

- `pasteImagesTo` / `pasteImages` 现有实现是 `api.pasteImage(..., true)`（**会往终端打字**）。
  附件路径**不得**复用它；`ChatMediaComposer` 用自己注入的 `stageImage`（`inject=false`）。
  若保留旧的整页拖放入口，两条路径必须**分开命名**，避免误用。
- `submit` 与 `onPasteImages` 的既有传参保持不变。

---

## 9. 并行文件分工（互不重叠）

| 工作流 | 允许创建/修改的文件 | 职责 |
|---|---|---|
| **A（服务端语音网关）** | `internal/voicegateway/**`（新建） | 上游 Realtime WS 拨号、逐字节中继、帧归一化（§3.4）、票据、限额、`ctx` 生命周期、`LoadConfigFromEnv` |
| **B（服务端路由）** | `internal/httpapi/voice*.go`（新建） | 三条路由、`requireCSRF`/`requireOrigin` 接线、`requestLimiter` 复用、`voicegateway` 实例的构造与 `Close()` |
| **C（前端语音）** | `web/src/lib/voiceClient.ts`、`web/src/lib/voiceCapture.ts`（新建） | `VoiceTransport` 实现、AudioWorklet 采集、重采样、播放、清理 |
| **D（前端媒体）** | `web/src/lib/chatMedia*.ts`、`web/src/components/media/**`（新建） | 附件状态机、`ChatMediaComposer`、`VoiceInput`、占位与 tray |
| **E（接线）** | §8 列出的既有文件 | 主代理最后做，**不与其他工作流并行** |

- **本契约文件**（`docs/design/chat-media-contract.md`、`web/src/lib/chatMediaTypes.ts`）是 A–D 的共同输入，
  **只读**；要改接口先改这里并重新冻结。
- **禁改**（其他工作流正在改，本契约也不碰）：`web/src/components/ChatView.tsx`、`Composer.tsx`、
  `TerminalPane.tsx`、`web/src/styles.css`、`lib/structuredChat*`、`internal/herdr/transcript*`、
  `internal/httpapi/transcript.go`、`internal/httpapi/hosts.go`、`web/package.json` 及锁文件。
- 新增前端样式只写 `web/src/components/media/*.css`，不追加到 `styles.css`。
- **不新增依赖**：不装全局工具，不改 `package.json`。

---

## 10. 必须复核的风险

1. **上行识别未实测**（§1.4）。若上游拒绝 `audio.input.transcription`，"默认转写到草稿"不成立；
   降级顺序：(a) 关掉语音入口并如实记录；(b) 用 `output_modalities:["text"]` + `response.output_text.delta`
   让模型只回显用户原话——**这会让语音模型充当"转写器"**，必须把它标注为语音助手输出、
   **不**写草稿，因此**不构成**合格的转写降级，只能作为明确的降级说明。**不得**声称 AG 联调已通。
2. **麦克风与安全上下文**：HTTP + 非 localhost 下语音不可用。README/docs 需如实说明，不能推荐
   未实现的能力。
3. **音频回复**：`HERDRX_VOICE_AUDIO_REPLY` 默认 `off`；音频路径未实测，开启前必须实测一次并记录。
4. **上游并发/配额**：herdrx 侧上限不能替代上游账号上限；429 要作为可执行提示返回用户。
5. **凭据边界**：`HERDRX_VOICE_API_KEY` 只在服务端；任何日志、审计、错误体、浏览器响应、
   Git 候选文件都不得出现它，也不得出现私人网关域名。
