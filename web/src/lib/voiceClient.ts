/**
 * 语音传输层的生产实现。
 *
 * 浏览器原生 WebSocket 不能设置 `Authorization` 头，所以浏览器**不可能**直连上游语音网关：
 * 只能经 herdrx 同源中继。本文件正是那条中继的浏览器端——它只认服务端下发的票据，
 * 永远不知道上游地址、模型或凭据（那些都是服务端财产，见 internal/voicegateway）。
 *
 * 契约见 docs/design/chat-media-contract.md §3；类型唯一正文是 ./chatMediaTypes。
 */

import { APIError, apiErrorMessage, authenticationGeneration, csrf, invalidateAuthentication, onAuthEvent } from './api'
import type { VoiceCapabilities, VoiceEvent, VoiceSessionHandle, VoiceTransport } from './chatMediaTypes'

/** 未配置 / 未启用时的能力。UI 据此隐藏麦克风入口，绝不触发授权弹窗。 */
export const DISABLED_VOICE_CAPABILITIES: VoiceCapabilities = {
  enabled: false,
  protocol: 'openai-realtime',
  model: '',
  inputSampleRate: 24000,
  outputSampleRate: 24000,
  audioReply: 'off',
  maxSessionSeconds: 0,
}

/** 单条控制帧上限，与服务端 HERDRX_VOICE_MAX_CONTROL_BYTES 对齐。 */
export const VOICE_MAX_CONTROL_BYTES = 4096

type RequestOptions = { method?: string; body?: unknown; signal?: AbortSignal }

/**
 * 与 `api.ts` 的 request 同构的最小请求器：同一套 Cookie/CSRF/401 失效语义，
 * 但不改动 api.ts（本工作流不允许改它）。
 */
async function requestJSON<T>(path: string, options: RequestOptions = {}): Promise<T> {
  const epoch = authenticationGeneration()
  const headers = new Headers()
  if (options.body !== undefined) headers.set('Content-Type', 'application/json')
  if (options.method && options.method !== 'GET' && options.method !== 'HEAD') headers.set('X-CSRF-Token', csrf())
  const response = await fetch(path, {
    method: options.method ?? 'GET',
    headers,
    body: options.body === undefined ? undefined : JSON.stringify(options.body),
    credentials: 'same-origin',
    signal: options.signal,
  })
  const payload = await response.json().catch(() => ({}))
  if (epoch !== authenticationGeneration()) throw authenticationChanged()
  if (!response.ok) {
    if (response.status === 401) invalidateAuthentication(epoch)
    throw new APIError(response.status, payload.code || 'request_failed', apiErrorMessage(payload), payload)
  }
  return payload as T
}

/**
 * 读取服务端能力。未配置（404）时返回 disabled，**不抛错**：
 * 页面加载阶段不该因为没开语音而显示错误。
 */
export async function fetchVoiceCapabilities(signal?: AbortSignal): Promise<VoiceCapabilities> {
  try {
    const payload = await requestJSON<VoiceCapabilities>('/api/voice/capabilities', { signal })
    if (!payload || payload.enabled !== true) return DISABLED_VOICE_CAPABILITIES
    return {
      enabled: true,
      protocol: 'openai-realtime',
      model: typeof payload.model === 'string' ? payload.model : '',
      inputSampleRate: Number(payload.inputSampleRate) || DISABLED_VOICE_CAPABILITIES.inputSampleRate,
      outputSampleRate: Number(payload.outputSampleRate) || DISABLED_VOICE_CAPABILITIES.outputSampleRate,
      audioReply: payload.audioReply === 'allowed' ? 'allowed' : 'off',
      maxSessionSeconds: Number(payload.maxSessionSeconds) || 0,
    }
  } catch (error) {
    if (error instanceof APIError && (error.status === 401 || error.code === 'auth_changed')) throw error
    return DISABLED_VOICE_CAPABILITIES
  }
}

function authenticationChanged() {
  return new APIError(409, 'auth_changed', '登录状态已变化，请重新开始语音输入')
}

function relayURL(ticket: string): string {
  const scheme = typeof location !== 'undefined' && location.protocol === 'https:' ? 'wss:' : 'ws:'
  const host = typeof location !== 'undefined' ? location.host : 'localhost'
  return `${scheme}//${host}/api/voice/ws?ticket=${encodeURIComponent(ticket)}`
}

/** 把服务端文本帧解析成 VoiceEvent；无法识别的一律忽略。 */
function parseVoiceEvent(payload: string): VoiceEvent | null {
  let raw: Record<string, unknown>
  try {
    raw = JSON.parse(payload) as Record<string, unknown>
  } catch {
    return null
  }
  const text = typeof raw.text === 'string' ? raw.text : ''
  switch (raw.t) {
    case 'ready':
      return {
        t: 'ready',
        protocol: 'openai-realtime',
        model: typeof raw.model === 'string' ? raw.model : '',
        inputSampleRate: Number(raw.inputSampleRate) || DISABLED_VOICE_CAPABILITIES.inputSampleRate,
        outputSampleRate: Number(raw.outputSampleRate) || DISABLED_VOICE_CAPABILITIES.outputSampleRate,
        audioReply: raw.audioReply === true,
      }
    case 'partial':
      return { t: 'partial', text }
    case 'transcript':
      return { t: 'transcript', text, final: raw.final === true }
    case 'assistantText':
      return { t: 'assistantText', text, final: raw.final === true }
    case 'error':
      return {
        t: 'error',
        code: typeof raw.code === 'string' ? raw.code : 'voice_error',
        message: typeof raw.message === 'string' && raw.message ? raw.message : '语音服务出错，请重试',
      }
    case 'closed':
      return { t: 'closed', reason: typeof raw.reason === 'string' ? raw.reason : 'server' }
    default:
      return null
  }
}

/** 挂起事件缓冲上限，防止上游刷帧把内存撑爆。 */
const MAX_PENDING_EVENTS = 256

class RelaySession implements VoiceSessionHandle {
  readonly sampleRate: number
  readonly capabilities: VoiceCapabilities

  private socket: WebSocket
  private output: 'text' | 'audio'
  private eventHandlers = new Set<(event: VoiceEvent) => void>()
  private audioHandlers = new Set<(pcm: ArrayBuffer) => void>()
  private pendingEvents: VoiceEvent[] = []
  private pendingAudio: ArrayBuffer[] = []
  private closed = false
  private closeReason = ''
  private closedPromise: Promise<void>
  private resolveClosed!: () => void
  private readonly authEpoch = authenticationGeneration()
  private releaseAuth: (() => void) | null = null

  constructor(socket: WebSocket, capabilities: VoiceCapabilities, output: 'text' | 'audio') {
    this.socket = socket
    this.capabilities = capabilities
    // 音频回复只在服务端显式允许时才是真的；否则一律降级为文字回复。
    this.output = output === 'audio' && capabilities.audioReply === 'allowed' ? 'audio' : 'text'
    this.sampleRate = capabilities.inputSampleRate
    this.closedPromise = new Promise((resolve) => { this.resolveClosed = resolve })
    this.releaseAuth = onAuthEvent(() => {
      if (this.authEpoch !== authenticationGeneration()) this.close('auth')
    })
    socket.binaryType = 'arraybuffer'
    socket.addEventListener('message', (event) => this.receive(event))
    socket.addEventListener('close', () => this.settle('transport'))
    socket.addEventListener('error', () => {
      this.emit({ t: 'error', code: 'voice_transport_error', message: '语音连接中断，请重新开始' })
      this.settle('transport')
    })
    socket.addEventListener('open', () => {
      // `start` 必须是浏览器发出的第一帧；服务端据此下发 session.update。
      if (socket.readyState === WebSocket.OPEN) {
        this.send({ t: 'start', output: this.output })
      }
    })
  }

  private receive(event: MessageEvent) {
    if (this.closed || this.authEpoch !== authenticationGeneration()) return
    if (typeof event.data === 'string') {
      const parsed = parseVoiceEvent(event.data)
      if (parsed) this.emit(parsed)
      return
    }
    if (event.data instanceof ArrayBuffer && event.data.byteLength > 0) {
      if (this.audioHandlers.size === 0) {
        if (this.pendingAudio.length < MAX_PENDING_EVENTS) this.pendingAudio.push(event.data)
        return
      }
      for (const handler of this.audioHandlers) handler(event.data)
    }
  }

  private emit(event: VoiceEvent) {
    if (event.t === 'closed') { this.settle(event.reason); return }
    if (this.closed) return
    this.dispatch(event)
  }

  private dispatch(event: VoiceEvent) {
    if (this.eventHandlers.size === 0) {
      if (this.pendingEvents.length < MAX_PENDING_EVENTS) this.pendingEvents.push(event)
      return
    }
    for (const handler of this.eventHandlers) handler(event)
  }

  private settle(reason: string) {
    if (this.closed) return
    this.closed = true
    this.closeReason = reason
    this.releaseAuth?.()
    this.releaseAuth = null
    this.resolveClosed()
    this.pendingAudio = []
    this.dispatch({ t: 'closed', reason })
  }

  private send(frame: Record<string, unknown>) {
    if (this.closed || this.socket.readyState !== WebSocket.OPEN) return
    const encoded = JSON.stringify(frame)
    if (encoded.length > VOICE_MAX_CONTROL_BYTES) return
    this.socket.send(encoded)
  }

  sendAudio(frame: ArrayBuffer) {
    if (this.closed || this.socket.readyState !== WebSocket.OPEN) return
    this.socket.send(frame)
  }

  commit() { this.send({ t: 'commit' }) }

  cancelResponse() { this.send({ t: 'cancel' }) }

  /** 幂等关闭：stop / unmount / 登出 / pagehide 都会调用它。 */
  close(reason: string) {
    this.pendingEvents = []
    this.pendingAudio = []
    if (this.closed) {
      this.socket.close()
      return
    }
    this.send({ t: 'stop' })
    this.settle(reason)
    try { this.socket.close(1000, 'client stop') } catch { /* already closing */ }
  }

  onEvent(handler: (event: VoiceEvent) => void) {
    if (this.authEpoch !== authenticationGeneration()) {
      this.pendingEvents = []
      handler({ t: 'closed', reason: 'auth' })
      return () => {}
    }
    this.eventHandlers.add(handler)
    if (this.pendingEvents.length > 0) {
      const flush = this.pendingEvents
      this.pendingEvents = []
      for (const event of flush) {
        if (this.authEpoch !== authenticationGeneration()) break
        handler(event)
      }
    }
    return () => { this.eventHandlers.delete(handler) }
  }

  onAudio(handler: (pcm: ArrayBuffer) => void) {
    if (this.closed || this.authEpoch !== authenticationGeneration()) return () => {}
    this.audioHandlers.add(handler)
    if (this.pendingAudio.length > 0) {
      const flush = this.pendingAudio
      this.pendingAudio = []
      for (const frame of flush) {
        if (this.closed || this.authEpoch !== authenticationGeneration()) break
        handler(frame)
      }
    }
    return () => { this.audioHandlers.delete(handler) }
  }

  /** 供调用方等待“连接真的结束了”，避免在 closed 事件到达前还在推流。 */
  whenClosed(): Promise<void> { return this.closedPromise }

  get reason() { return this.closeReason }
}

type VoiceSessionResponse = {
  ticket: string
  expiresAt: string
  capabilities: VoiceCapabilities
}

/** 生产传输层：一次会话 = 一张一次性票据 + 一条同源 WebSocket。 */
export class HttpVoiceTransport implements VoiceTransport {
  private cached: VoiceCapabilities | null = null

  async capabilities(): Promise<VoiceCapabilities> {
    if (this.cached) return this.cached
    this.cached = await fetchVoiceCapabilities()
    return this.cached
  }

  /** 让能力缓存失效，例如用户在设置页开启语音后重新探测。 */
  invalidate() { this.cached = null }

  async open(input: { output: 'text' | 'audio' }): Promise<VoiceSessionHandle> {
    const epoch = authenticationGeneration()
    const session = await requestJSON<VoiceSessionResponse>('/api/voice/sessions', {
      method: 'POST',
      body: { output: input.output },
    })
    if (epoch !== authenticationGeneration()) throw authenticationChanged()
    if (!session?.ticket) throw new APIError(502, 'voice_ticket_missing', '语音服务未返回会话票据，请稍后重试')
    this.cached = session.capabilities ?? this.cached

    const socket = new WebSocket(relayURL(session.ticket))
    const handle = new RelaySession(socket, this.cached ?? DISABLED_VOICE_CAPABILITIES, input.output)

    await new Promise<void>((resolve, reject) => {
      const onOpen = () => {
        if (epoch !== authenticationGeneration()) { onAuthChanged(); return }
        cleanup()
        resolve()
      }
      const onAuthChanged = () => {
        if (epoch === authenticationGeneration()) return
        cleanup()
        handle.close('auth')
        reject(authenticationChanged())
      }
      const releaseAuth = onAuthEvent(onAuthChanged)
      const onError = () => { cleanup(); reject(new APIError(502, 'voice_socket_failed', '无法连接语音服务，请稍后重试')) }
      const onClose = () => { cleanup(); reject(new APIError(502, 'voice_socket_closed', '语音连接在建立前被关闭，请重试')) }
      const cleanup = () => {
        releaseAuth()
        socket.removeEventListener('open', onOpen)
        socket.removeEventListener('error', onError)
        socket.removeEventListener('close', onClose)
      }
      socket.addEventListener('open', onOpen)
      socket.addEventListener('error', onError)
      socket.addEventListener('close', onClose)
    })
    return handle
  }
}

export const voiceTransport = new HttpVoiceTransport()
