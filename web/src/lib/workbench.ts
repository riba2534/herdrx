import type { Snapshot } from '../types'
import { APIError, api, authenticationGeneration, invalidateAuthentication } from './api'

const KIND = 0x74
const VERSION = 1
const HEADER_SIZE = 16
const OP_FRAME = 1
const OP_ACK = 2
const OP_INPUT = 3
const OP_RESIZE = 4
const OP_RELEASE = 6
const FLAG_FULL = 1

type ConnectionState = 'connecting' | 'ready' | 'degraded' | 'offline'
type TerminalFrame = { streamID: number; seq: bigint; full: boolean; cols: number; rows: number; ansi: Uint8Array }
type TerminalHandler = (frame: TerminalFrame) => void
type TerminalCloseHandler = (reason: string) => void
type StateHandler = (state: ConnectionState, message?: string) => void

type PendingRequest = {
  resolve: (value: unknown) => void
  reject: (error: Error) => void
  timer: number
}

function persistentID(storage: Storage | undefined, key: string) {
  let value: string | null = null
  try {
    value = storage?.getItem(key) ?? null
  } catch {
    // ignore storage access errors
  }
  if (!value) {
    value = createClientID()
    try {
      storage?.setItem(key, value)
    } catch {
      // ignore storage access errors
    }
  }
  return value
}

export function createClientID() {
  if (typeof globalThis.crypto?.randomUUID === 'function') return globalThis.crypto.randomUUID()
  const bytes = new Uint8Array(16)
  if (typeof globalThis.crypto?.getRandomValues === 'function') globalThis.crypto.getRandomValues(bytes)
  else for (let index = 0; index < bytes.length; index++) bytes[index] = Math.floor(Math.random() * 256)
  bytes[6] = (bytes[6] & 0x0f) | 0x40
  bytes[8] = (bytes[8] & 0x3f) | 0x80
  const hex = Array.from(bytes, (value) => value.toString(16).padStart(2, '0')).join('')
  return `${hex.slice(0, 8)}-${hex.slice(8, 12)}-${hex.slice(12, 16)}-${hex.slice(16, 20)}-${hex.slice(20)}`
}

export class WorkbenchClient {
  private socket: WebSocket | null = null
  private disposed = false
  private authGeneration = authenticationGeneration()
  private reconnectAttempt = 0
  private reconnectTimer = 0
  private presenceTimer = 0
  private connectionError = ''
  private retryAfter = 0
  private retryBlocked = false
  private snapshotHandlers = new Set<(snapshot: Snapshot) => void>()
  private stateHandlers = new Set<StateHandler>()
  private epochHandlers = new Set<(epoch: number) => void>()
  private terminalHandlers = new Map<number, TerminalHandler>()
  private terminalCloseHandlers = new Map<number, TerminalCloseHandler>()
  private terminalStreams = new Set<number>()
  private pendingTerminalClosures = new Map<number, string>()
  private pendingFrames = new Map<number, TerminalFrame[]>()
  private pending = new Map<string, PendingRequest>()
  private connectionEpoch = 0
  private deviceID = persistentID(localStorage, 'herdrx.device.v1')
  private browserID = persistentID(sessionStorage, 'herdrx.browser.v1')

  constructor(private hostID: string) {}

  getHostID() {
    return this.hostID
  }

  connect() {
    if (this.socket) return
    this.disposed = false
    this.retryBlocked = false
    window.clearTimeout(this.reconnectTimer)
    this.authGeneration = authenticationGeneration()
    this.openSocket()
  }

  dispose() {
    this.disposed = true
    window.clearTimeout(this.reconnectTimer)
    window.clearInterval(this.presenceTimer)
    this.socket?.close(1000, 'unmount')
    this.socket = null
    for (const request of this.pending.values()) {
      window.clearTimeout(request.timer)
      request.reject(new Error('连接已关闭'))
    }
    this.pending.clear()
    this.terminalHandlers.clear()
    this.terminalCloseHandlers.clear()
    this.terminalStreams.clear()
    this.pendingTerminalClosures.clear()
    this.pendingFrames.clear()
  }

  onSnapshot(handler: (snapshot: Snapshot) => void) {
    this.snapshotHandlers.add(handler)
    return () => this.snapshotHandlers.delete(handler)
  }

  onState(handler: StateHandler) {
    this.stateHandlers.add(handler)
    return () => this.stateHandlers.delete(handler)
  }

  onEpoch(handler: (epoch: number) => void) {
    this.epochHandlers.add(handler)
    return () => this.epochHandlers.delete(handler)
  }

  onTerminal(streamID: number, handler: TerminalHandler, onClosed?: TerminalCloseHandler) {
    const closed = this.pendingTerminalClosures.get(streamID)
    if (closed !== undefined) {
      this.pendingTerminalClosures.delete(streamID)
      this.terminalStreams.delete(streamID)
      onClosed?.(closed)
      return () => {}
    }
    this.terminalHandlers.set(streamID, handler)
    if (onClosed) this.terminalCloseHandlers.set(streamID, onClosed)
    for (const frame of this.pendingFrames.get(streamID) || []) handler(frame)
    this.pendingFrames.delete(streamID)
    return () => {
      if (this.terminalHandlers.get(streamID) === handler) this.terminalHandlers.delete(streamID)
      if (this.terminalCloseHandlers.get(streamID) === onClosed) this.terminalCloseHandlers.delete(streamID)
    }
  }

  async openTerminal(paneID: string, cols: number, rows: number) {
    const response = await this.request<{ stream_id: number }>('terminal.open', { pane_id: paneID, mode: 'observe', cols, rows })
    return response.stream_id
  }

  closeTerminal(streamID: number) {
    this.sendJSON({ t: 'terminal.close', stream_id: streamID })
    this.terminalHandlers.delete(streamID)
    this.terminalCloseHandlers.delete(streamID)
    this.terminalStreams.delete(streamID)
    this.pendingTerminalClosures.delete(streamID)
    this.pendingFrames.delete(streamID)
  }

  call<T = unknown>(method: string, params: unknown) {
    return this.request<T>('call', { method, params })
  }

  sendInput(streamID: number, data: string | Uint8Array) {
    const payload = typeof data === 'string' ? new TextEncoder().encode(data) : data
    this.sendFrame(OP_INPUT, streamID, 0n, payload)
  }

  resize(streamID: number, cols: number, rows: number) {
    const payload = new Uint8Array(4)
    const view = new DataView(payload.buffer)
    view.setUint16(0, cols, true)
    view.setUint16(2, rows, true)
    this.sendFrame(OP_RESIZE, streamID, 0n, payload)
  }

  release(streamID: number) {
    this.sendFrame(OP_RELEASE, streamID, 0n, new Uint8Array())
  }

  acknowledge(streamID: number, seq: bigint) {
    this.sendFrame(OP_ACK, streamID, seq, new Uint8Array())
  }

  private openSocket() {
    if (this.disposed) return
    this.connectionError = ''
    this.retryAfter = 0
    this.emitState('connecting')
    const protocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:'
    const socket = new WebSocket(`${protocol}//${window.location.host}/api/hosts/${encodeURIComponent(this.hostID)}/ws`)
    socket.binaryType = 'arraybuffer'
    this.socket = socket
    socket.onopen = () => {
      if (this.disposed || socket !== this.socket) return
      this.sendJSON({
        t: 'hello', protocol: 1, device_id: this.deviceID, browser_instance_id: this.browserID,
        client_type: 'browser', capabilities: { terminal_ack: 1 },
      })
      window.clearInterval(this.presenceTimer)
      this.presenceTimer = window.setInterval(() => this.sendJSON({ t: 'presence' }), 5000)
    }
    socket.onmessage = (event) => {
      if (this.disposed || socket !== this.socket) return
      if (typeof event.data === 'string') this.handleJSON(event.data)
      else if (event.data instanceof ArrayBuffer) this.handleBinary(event.data)
    }
    socket.onclose = (event) => {
      if (socket !== this.socket) return
      this.socket = null
      window.clearInterval(this.presenceTimer)
      this.emitState('offline', this.connectionError || event.reason || '连接已断开')
      for (const request of this.pending.values()) {
        window.clearTimeout(request.timer)
        request.reject(new Error('连接已断开'))
      }
      this.pending.clear()
      this.terminalHandlers.clear()
      this.terminalCloseHandlers.clear()
      this.terminalStreams.clear()
      this.pendingTerminalClosures.clear()
      this.pendingFrames.clear()
      if (!this.disposed) void this.afterDisconnect(event.code)
    }
  }

  private async afterDisconnect(code: number) {
    if (code === 4401) {
      this.disposed = true
      invalidateAuthentication(this.authGeneration)
      return
    }
    if (code === 4002) { this.emitState('offline', '客户端协议不兼容，请刷新页面更新客户端后重试'); return }
    if (code === 4405 || this.retryBlocked) return
    // Browsers hide HTTP status when an upgrade is rejected. Check the login
    // before retrying, so an expired Cookie cannot cause an endless WS loop.
    try { await api.me() }
    catch (reason) {
      if (reason instanceof APIError && (reason.status === 401 || reason.code === 'auth_changed')) return
    }
    if (this.disposed || this.authGeneration !== authenticationGeneration()) return
    const ladder = [1000, 2000, 5000, 5000, 10_000, 30_000]
    const delay = Math.max(this.retryAfter, ladder[Math.min(this.reconnectAttempt++, ladder.length - 1)])
    this.reconnectTimer = window.setTimeout(() => this.openSocket(), delay * (0.9 + Math.random() * 0.2))
  }

  private handleJSON(encoded: string) {
    let message: Record<string, unknown>
    try { message = JSON.parse(encoded) as Record<string, unknown> } catch { return }
    if (message.t === 'terminal.opened' && typeof message.stream_id === 'number') {
      this.terminalStreams.add(message.stream_id)
    } else if (message.t === 'terminal.closed' && typeof message.stream_id === 'number') {
      const streamID = message.stream_id
      if (!this.terminalStreams.has(streamID)) return
      const reason = String(message.reason || '终端观察连接已关闭')
      const onClosed = this.terminalCloseHandlers.get(streamID)
      this.terminalHandlers.delete(streamID)
      this.terminalCloseHandlers.delete(streamID)
      this.pendingFrames.delete(streamID)
      if (onClosed) {
        this.terminalStreams.delete(streamID)
        onClosed(reason)
      } else {
        // A process can fail between terminal.opened and UI subscription.
        this.pendingTerminalClosures.set(streamID, reason)
      }
    } else if (message.t === 'error' && !message.id) {
      this.connectionError = String(message.message || '主机暂时无法连接')
      this.retryBlocked = message.retryable === false
      this.retryAfter = typeof message.retry_after_ms === 'number' ? Math.min(120_000, Math.max(0, message.retry_after_ms)) : 0
      this.emitState('degraded', this.connectionError)
    } else if (message.t === 'snapshot') {
      this.reconnectAttempt = 0
      this.connectionError = ''
      this.emitState('ready')
      for (const handler of this.snapshotHandlers) handler(message.snapshot as Snapshot)
    } else if (message.t === 'conn') {
      this.emitState(message.state as ConnectionState, message.message as string | undefined)
    } else if (message.t === 'server_info') {
      this.connectionEpoch++
      for (const handler of this.epochHandlers) handler(this.connectionEpoch)
    }
    const id = typeof message.id === 'string' ? message.id : ''
    if (id && this.pending.has(id)) {
      const pending = this.pending.get(id)!
      window.clearTimeout(pending.timer)
      this.pending.delete(id)
      if (message.t === 'error') pending.reject(new Error(String(message.message || '请求失败')))
      else pending.resolve(message)
    }
  }

  private handleBinary(buffer: ArrayBuffer) {
    if (buffer.byteLength < HEADER_SIZE) return
    const view = new DataView(buffer)
    if (view.getUint8(0) !== KIND || view.getUint8(1) !== VERSION || view.getUint8(2) !== OP_FRAME) return
    const streamID = view.getUint32(4, true)
    const payload = new Uint8Array(buffer, HEADER_SIZE)
    if (payload.byteLength < 4) return
    const frame: TerminalFrame = {
      streamID,
      seq: view.getBigUint64(8, true),
      full: (view.getUint8(3) & FLAG_FULL) !== 0,
      cols: new DataView(payload.buffer, payload.byteOffset).getUint16(0, true),
      rows: new DataView(payload.buffer, payload.byteOffset).getUint16(2, true),
      ansi: payload.slice(4),
    }
    const handler = this.terminalHandlers.get(streamID)
    if (handler) handler(frame)
    else {
      const queued = this.pendingFrames.get(streamID) || []
      if (queued.length < 8) queued.push(frame)
      this.pendingFrames.set(streamID, queued)
    }
  }

  private request<T>(type: string, fields: Record<string, unknown>): Promise<T> {
    const id = createClientID()
    return new Promise<T>((resolve, reject) => {
      if (!this.socket || this.socket.readyState !== WebSocket.OPEN) {
        reject(new Error('主机连接尚未就绪'))
        return
      }
      const timer = window.setTimeout(() => {
        this.pending.delete(id)
        reject(new Error('请求超时'))
      }, 15_000)
      this.pending.set(id, {
        resolve: (message) => {
          const payload = message as Record<string, unknown>
          resolve((type === 'call' ? payload.result : payload) as T)
        }, reject, timer,
      })
      this.sendJSON({ t: type, id, ...fields })
    })
  }

  private sendJSON(message: unknown) {
    if (this.socket?.readyState === WebSocket.OPEN) this.socket.send(JSON.stringify(message))
  }

  private sendFrame(opcode: number, streamID: number, seq: bigint, payload: Uint8Array) {
    if (this.socket?.readyState !== WebSocket.OPEN) return
    const frame = new Uint8Array(HEADER_SIZE + payload.byteLength)
    const view = new DataView(frame.buffer)
    view.setUint8(0, KIND)
    view.setUint8(1, VERSION)
    view.setUint8(2, opcode)
    view.setUint32(4, streamID, true)
    view.setBigUint64(8, seq, true)
    frame.set(payload, HEADER_SIZE)
    this.socket.send(frame)
  }

  private emitState(state: ConnectionState, message?: string) {
    for (const handler of this.stateHandlers) handler(state, message)
  }
}
