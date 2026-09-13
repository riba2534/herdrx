/**
 * Chat 语音输入与图片附件的冻结契约（类型唯一正文）。
 *
 * 语义、路由、ENV 与接线点见 docs/design/chat-media-contract.md。
 * 本文件只定义类型与纯函数，不含任何网络、DOM、React 或音频代码。
 *
 * 冻结范围：VoiceInput / ChatMediaComposer 的 props、语音 WS 帧、附件状态机、
 * 提交文本合成规则。实现方（components/media/**、lib/voice*、lib/chatMedia*）
 * 必须逐字遵守；改动本文件等于改契约。
 */

import type { ReactNode } from 'react'
import type { ComposerSendStatus } from './composerDrafts'

/* ────────────────────────────── 图片附件 ────────────────────────────── */

/** 附件在 composer 内的生命周期。占位在 `staging` 就已存在，不等上传完成。 */
export type ChatMediaAttachmentStatus = 'staging' | 'staged' | 'failed'

export type ChatMediaAttachment = {
  /** 本地稳定 id，仅用于占位渲染、去重与移除；绝不进入提交文本。 */
  id: string
  kind: 'image'
  /** 原始文件名，仅用于 UI 显示与失败文案；绝不进入提交文本。 */
  name: string
  mime: string
  size: number
  status: ChatMediaAttachmentStatus
  /** stage-only 上传成功后的远端路径；`staged` 时必有，且是提交文本里唯一的引用。 */
  remotePath?: string
  /** 面向用户的中文短句失败原因；`failed` 时必有。 */
  error?: string
  /** 本地预览 object URL；由调用方负责 revoke。绝不提交、绝不回传服务端。 */
  previewURL?: string
}

/** 服务端 upload 契约上限，与 internal/httpapi/paste.go 保持一致。 */
export const CHAT_MEDIA_MAX_IMAGE_BYTES = 20 * 1024 * 1024
/** 与 `web/src/lib/imagePaste.ts` 的 MAX_IMAGE_SIZE 保持一致。 */
export const CHAT_MEDIA_MAX_ATTACHMENTS = 10

const IMAGE_MIME = /^image\/(png|jpeg|jpg|webp|gif)$/i
const IMAGE_NAME = /\.(png|jpe?g|webp|gif)$/i

/**
 * 附件准入判定。拒绝时返回面向用户的中文原因，UI 据此把占位标成 `failed`。
 * 判定不依赖网络，也不读取文件内容。
 */
export function canStageChatMedia(file: File): { ok: true } | { ok: false; reason: string } {
  if (!IMAGE_MIME.test(file.type) && !IMAGE_NAME.test(file.name)) {
    return { ok: false, reason: '仅支持 PNG、JPEG、WebP、GIF 图片' }
  }
  if (file.size <= 0) return { ok: false, reason: '图片内容为空' }
  if (file.size > CHAT_MEDIA_MAX_IMAGE_BYTES) return { ok: false, reason: '图片超过 20MB 上限' }
  return { ok: true }
}

/* ───────────────────────── 提交文本合成（冻结） ───────────────────────── */

/**
 * 文本与图片引用之间的分隔符。整个提交仍是一次 `pane.send_input`：
 * Herdr 按目标程序当前的 bracketed-paste 模式编码这一整段，再单独发一次 Enter。
 * 见 docs/design/adr/0002-image-paste-delivery.md。
 */
export const CHAT_MEDIA_REFERENCE_SEPARATOR = '\n'

/**
 * 把「草稿文本 + 已 staged 的远端路径」合成为**一次**提交文本。
 *
 * 冻结规则：
 * - 图片引用按附件顺序逐行附在文本之后，行内不加前缀、不加引号、不加空格。
 * - 文本为空时只提交引用（**仅图片也能发**）。
 * - 末尾不留分隔符，也不追加空格或 Enter。
 * - **幂等**：先剥掉草稿末尾那些恰好等于某个待提交路径的行，再合成。
 *   这样「发送失败后保留附件与草稿、用户手动重试」不会把引用追加两次。
 */
export function composeChatMediaSubmission(text: string, remotePaths: string[]): string {
  const refs = remotePaths.filter((path) => path !== '')
  const lines = text.replace(/\r\n/g, '\n').split('\n')
  const refSet = new Set(refs)
  while (lines.length > 0 && refSet.has(lines[lines.length - 1].trim())) lines.pop()
  const base = lines.join('\n').replace(/\s+$/, '')
  return [base, ...refs].filter((part) => part !== '').join(CHAT_MEDIA_REFERENCE_SEPARATOR)
}

/** 仅有引用、没有正文时也算可发送（「仅图片也能发」）。 */
export function chatMediaSubmissionSendable(text: string, attachments: ChatMediaAttachment[]): boolean {
  if (text.trim() !== '') return true
  return attachments.some((item) => item.status === 'staged' && Boolean(item.remotePath))
}

/* ────────────────────────────── 语音输入 ────────────────────────────── */

/** VoiceInput 的可见状态。`error` 必须带可执行的中文 message。 */
export type VoiceInputState = 'idle' | 'requesting' | 'connecting' | 'listening' | 'stopping' | 'error'

/**
 * 服务端能力。**由服务端下发，客户端不得自行指定**模型、网关地址或凭据；
 * 未配置（`enabled: false`）时 UI 必须隐藏麦克风入口，绝不触发授权弹窗。
 */
export type VoiceCapabilities = {
  enabled: boolean
  /** 固定字面量：只有 OpenAI Realtime 兼容会话传输被实现。 */
  protocol: 'openai-realtime'
  /** 服务端选定的语音模型名（如 `gpt-realtime`）。 */
  model: string
  /** 上行 PCM 采样率（Hz）。客户端必须按此重采样。 */
  inputSampleRate: number
  /** 下行 PCM 采样率（Hz）。 */
  outputSampleRate: number
  /** 服务端是否允许请求音频回复；`off` 时 `open({ output: 'audio' })` 必须被拒绝。 */
  audioReply: 'allowed' | 'off'
  /** 单次语音会话时长上限（秒）。 */
  maxSessionSeconds: number
}

/** 打开会话的结果：一次性 ws 票据 + 该会话实际生效的能力。 */
export type VoiceSessionTicket = {
  ticket: string
  expiresAt: string
  capabilities: VoiceCapabilities
}

/**
 * 浏览器侧收到的语音事件。音频走二进制帧（见 VoiceSessionHandle.onAudio），
 * 不进这个联合；文本与控制走这里。
 *
 * 关键区分：`transcript` 是**用户自己说的话**，是唯一可以写入草稿的内容；
 * `assistantText` 是**语音模型的回答**，它绝不写草稿、绝不进终端。
 */
export type VoiceEvent =
  | { t: 'ready'; protocol: 'openai-realtime'; model: string; inputSampleRate: number; outputSampleRate: number; audioReply: boolean }
  | { t: 'partial'; text: string }
  | { t: 'transcript'; text: string; final: boolean }
  | { t: 'assistantText'; text: string; final: boolean }
  | { t: 'error'; code: string; message: string }
  | { t: 'closed'; reason: string }

/** 一次已建立的语音会话。实现方：`web/src/lib/voiceClient.ts`。 */
export type VoiceSessionHandle = {
  /** 上行必须使用的采样率；等于 `ready.inputSampleRate`。 */
  sampleRate: number
  /** 上行原始 PCM16LE 单声道小端字节。实现方负责 base64 与 `input_audio_buffer.append`。 */
  sendAudio(frame: ArrayBuffer): void
  /** 手动提交一轮（`input_audio_buffer.commit`）。服务端未启用自动轮次检测时使用。 */
  commit(): void
  /** 打断当前回复（`response.cancel`），用于 barge-in。 */
  cancelResponse(): void
  /** 幂等关闭。stop / unmount / logout / pagehide 都必须调用。 */
  close(reason: string): void
  /** 订阅事件；返回退订函数。同一事件不得投递两次。 */
  onEvent(handler: (event: VoiceEvent) => void): () => void
  /** 订阅下行音频；返回退订函数。仅在 `open({ output: 'audio' })` 且服务端 `audioReply === 'allowed'` 时可能有帧。 */
  onAudio(handler: (pcm: ArrayBuffer) => void): () => void
}

/** 可注入的语音传输层，便于测试与替换；生产实现是 voiceClient.ts。 */
export type VoiceTransport = {
  capabilities(): Promise<VoiceCapabilities>
  open(input: { output: 'text' | 'audio' }): Promise<VoiceSessionHandle>
}

/**
 * VoiceInput 的独立 props（冻结）。
 *
 * 组件只做三件事：拿麦克风、把 PCM 交给 `transport`、把**用户自己**的最终识别文本
 * 追加进当前 pane 草稿。它不发送、不切换 pane、不写终端、不渲染语音模型的回答。
 */
export type VoiceInputProps = {
  hostID: string
  paneID: string
  /** 不可见时必须释放麦克风与连接，不得在后台继续采集。 */
  visible: boolean
  compact?: boolean
  transport: VoiceTransport
  /** 把最终识别文本写入草稿；生产实现固定为 `writeComposerDraft`。绝不触发发送。 */
  writeDraft: (hostID: string, paneID: string, text: string) => void
  /** 读取当前草稿，用于拼接已有内容。 */
  readDraft: (hostID: string, paneID: string) => string
  /** 语音模型回答的文本帧；调用方决定展示位置，**不得**写入草稿或终端。 */
  onAssistantText?: (text: string, final: boolean) => void
  onStatus?: (state: VoiceInputState, message?: string) => void
}

/**
 * 识别文本追加规则（冻结）。仅对 `transcript` 且 `final === true` 调用；
 * `partial` 只用于组件内联显示，绝不写草稿。
 *
 * 规则：草稿非空且末尾不是空白时，插入一个半角空格；否则直接拼接。
 * 不插入换行——换行留到用户自己在编辑框里排。
 */
export function appendVoiceTranscript(draft: string, transcript: string): string {
  const text = transcript.trim()
  if (text === '') return draft
  if (draft === '') return text
  return /\s$/.test(draft) ? draft + text : `${draft} ${text}`
}

/* ─────────────────────── Composer 最小接线（冻结） ─────────────────────── */

/**
 * 媒体层交给 Composer 的最小侵入接口。Composer 在 `mediaHost` 缺席时的行为
 * 必须与今天逐字节一致，既有 Composer 测试不受影响。
 */
export type ComposerMediaHost = {
  /** 有已 staged 的附件时为 true；为 true 时允许空草稿发送（仅图片也能发）。 */
  canSend: () => boolean
  /** 发送前把草稿合成为一次提交文本；返回 null 表示放弃本次发送。 */
  compose: (draft: string) => string | null
  /** 一次发送事务结束后回调，携带最终状态，用于清空或保留附件。 */
  onSettled?: (status: ComposerSendStatus) => void
  /** 附件占位区渲染槽，渲染在 textarea 上方。 */
  tray?: ReactNode
  /** 拖放/粘贴/选择的图片文件交给媒体层；**绝不**在这里直接发往终端。 */
  onFiles?: (files: File[]) => void
}

/** ChatMediaComposer 的独立 props（冻结）。 */
export type ChatMediaComposerProps = {
  hostID: string
  paneID: string
  visible: boolean
  compact?: boolean
  variant?: 'dock' | 'chat'
  sendDisabled?: boolean
  placeholder?: string
  onDirectInput: () => void
  onLocalInput: () => void
  /** 单次输入；必须沿用 Composer 的提交事务。 */
  submit: (paneID: string, text: string) => Promise<void>
  /**
   * 图片 stage-only 上传，返回远端路径。
   * 生产实现固定为 `api.pasteImage(hostID, paneID, file, false)`——
   * **`inject=false`，绝不把路径打进终端**。
   */
  stageImage: (hostID: string, paneID: string, file: File) => Promise<{ path: string }>
  /** 发送事务；默认 `runComposerSend`。 */
  send: (hostID: string, paneID: string, submit: (paneID: string, text: string) => Promise<void>) => Promise<void>
  /** 读取发送状态，默认 `readComposerSend`。 */
  readSend: (hostID: string, paneID: string) => { status: ComposerSendStatus }
  /** 可选语音槽位；由调用方决定是否渲染 VoiceInput。 */
  voice?: ReactNode
}
