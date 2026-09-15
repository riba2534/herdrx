import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { APIError, invalidateAuthentication } from './api'
import { DISABLED_VOICE_CAPABILITIES, HttpVoiceTransport, fetchVoiceCapabilities } from './voiceClient'
import type { VoiceEvent } from './chatMediaTypes'

/** 最小 WebSocket 替身：记录发出的帧，并允许测试注入服务端帧。 */
class FakeWebSocket {
  static CONNECTING = 0
  static OPEN = 1
  static CLOSING = 2
  static CLOSED = 3
  static instances: FakeWebSocket[] = []

  readonly url: string
  readyState = FakeWebSocket.CONNECTING
  binaryType = 'blob'
  sent: Array<string | ArrayBuffer> = []
  closedWith: Array<{ code?: number; reason?: string }> = []
  private listeners = new Map<string, Set<(event: unknown) => void>>()

  constructor(url: string) {
    this.url = url
    FakeWebSocket.instances.push(this)
  }

  addEventListener(type: string, handler: (event: unknown) => void) {
    if (!this.listeners.has(type)) this.listeners.set(type, new Set())
    this.listeners.get(type)?.add(handler)
  }

  removeEventListener(type: string, handler: (event: unknown) => void) {
    this.listeners.get(type)?.delete(handler)
  }

  send(data: string | ArrayBuffer) { this.sent.push(data) }

  close(code?: number, reason?: string) {
    this.closedWith.push({ code, reason })
    this.readyState = FakeWebSocket.CLOSED
  }

  /** 测试驱动：触发一次事件。 */
  emit(type: string, event: unknown = {}) {
    for (const handler of Array.from(this.listeners.get(type) ?? [])) handler(event)
  }

  open() {
    this.readyState = FakeWebSocket.OPEN
    this.emit('open')
  }

  message(payload: string | ArrayBuffer) { this.emit('message', { data: payload }) }
}

const capabilities = {
  enabled: true,
  protocol: 'openai-realtime' as const,
  model: 'gpt-realtime',
  inputSampleRate: 24000,
  outputSampleRate: 24000,
  audioReply: 'off' as const,
  maxSessionSeconds: 600,
}

function jsonResponse(payload: unknown, status = 200) {
  return new Response(JSON.stringify(payload), { status, headers: { 'Content-Type': 'application/json' } })
}

/** open() awaits the ticket request before it constructs the socket. */
async function nextSocket(): Promise<FakeWebSocket> {
  for (let attempt = 0; attempt < 100; attempt++) {
    const socket = FakeWebSocket.instances[FakeWebSocket.instances.length - 1]
    if (socket) return socket
    await new Promise((resolve) => setTimeout(resolve, 0))
  }
  throw new Error('the relay socket was never created')
}

beforeEach(() => {
  invalidateAuthentication()
  FakeWebSocket.instances = []
  vi.stubGlobal('WebSocket', FakeWebSocket)
  vi.stubGlobal('location', { protocol: 'https:', host: 'workbench.example.test' })
})

afterEach(() => {
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
})

describe('fetchVoiceCapabilities', () => {
  it('returns the server capabilities when voice is configured', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(jsonResponse(capabilities)))
    await expect(fetchVoiceCapabilities()).resolves.toMatchObject({
      enabled: true, model: 'gpt-realtime', inputSampleRate: 24000, audioReply: 'off',
    })
  })

  it('treats a 404 as "this instance does not have voice" instead of an error', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(jsonResponse({ error: '本实例未启用语音输入', code: 'voice_disabled' }, 404)))
    await expect(fetchVoiceCapabilities()).resolves.toEqual(DISABLED_VOICE_CAPABILITIES)
  })

  it('surfaces a 401 so the caller can end the login', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(jsonResponse({ error: '登录已失效', code: 'unauthenticated' }, 401)))
    await expect(fetchVoiceCapabilities()).rejects.toBeInstanceOf(APIError)
  })

  it('never sends a model, url or credential of its own', async () => {
    const fetchMock = vi.fn().mockResolvedValue(jsonResponse(capabilities))
    vi.stubGlobal('fetch', fetchMock)
    await fetchVoiceCapabilities()
    const [path, init] = fetchMock.mock.calls[0] as [string, RequestInit]
    expect(path).toBe('/api/voice/capabilities')
    expect(init.body).toBeUndefined()
    expect(init.credentials).toBe('same-origin')
  })
})

describe('HttpVoiceTransport.open', () => {
  function transportWith(session: unknown, status = 200) {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(jsonResponse(session, status)))
    return new HttpVoiceTransport()
  }

  it('rejects a delayed ticket response after authentication changes', async () => {
    let respond!: (response: Response) => void
    vi.stubGlobal('fetch', vi.fn(() => new Promise<Response>((resolve) => { respond = resolve })))
    const opening = new HttpVoiceTransport().open({ output: 'text' })
    const result = opening.catch((error: unknown) => error)
    invalidateAuthentication()
    respond(jsonResponse({ ticket: 'old-ticket', expiresAt: 'later', capabilities }))
    await expect(result).resolves.toMatchObject({ code: 'auth_changed' })
    expect(FakeWebSocket.instances).toHaveLength(0)
  })

  it('closes a pending websocket handshake when authentication changes', async () => {
    const transport = transportWith({ ticket: 't', expiresAt: 'later', capabilities })
    const result = transport.open({ output: 'text' }).catch((error: unknown) => error)
    const socket = await nextSocket()
    invalidateAuthentication()
    // A late browser open must not revive the previous login's handle.
    socket.open()
    await expect(result).resolves.toMatchObject({ code: 'auth_changed' })
    expect(socket.closedWith.length).toBeGreaterThan(0)
    expect(socket.sent).toHaveLength(0)
  })

  it('posts only the output mode and carries the CSRF token', async () => {
    const fetchMock = vi.fn().mockResolvedValue(jsonResponse({ ticket: 'ticket-1', expiresAt: 'later', capabilities }))
    vi.stubGlobal('fetch', fetchMock)
    const transport = new HttpVoiceTransport()
    const opening = transport.open({ output: 'text' })
    ;(await nextSocket()).open()
    await opening

    const [path, init] = fetchMock.mock.calls[0] as [string, RequestInit]
    expect(path).toBe('/api/voice/sessions')
    expect(init.method).toBe('POST')
    expect(JSON.parse(String(init.body))).toEqual({ output: 'text' })
  })

  it('opens the same-origin relay with the one-time ticket and starts in text mode', async () => {
    const transport = transportWith({ ticket: 'ticket 1/2', expiresAt: 'later', capabilities })
    const opening = transport.open({ output: 'text' })
    const socket = await nextSocket()
    expect(socket.url).toBe('wss://workbench.example.test/api/voice/ws?ticket=ticket%201%2F2')
    socket.open()
    await opening

    expect(socket.sent).toEqual([JSON.stringify({ t: 'start', output: 'text' })])
    // The browser must never learn or transmit the upstream address or key.
    expect(socket.url).not.toContain('voice.example.com')
  })

  it('keeps audio output off unless the operator allowed it', async () => {
    const transport = transportWith({ ticket: 't', expiresAt: 'later', capabilities })
    const opening = transport.open({ output: 'audio' })
    const socket = await nextSocket()
    socket.open()
    await opening
    expect(socket.sent).toEqual([JSON.stringify({ t: 'start', output: 'text' })])
  })

  it('requests audio output when the server allows it', async () => {
    const transport = transportWith({
      ticket: 't', expiresAt: 'later', capabilities: { ...capabilities, audioReply: 'allowed' },
    })
    const opening = transport.open({ output: 'audio' })
    const socket = await nextSocket()
    socket.open()
    await opening
    expect(socket.sent).toEqual([JSON.stringify({ t: 'start', output: 'audio' })])
  })

  it('fails with an actionable message when the session cannot be created', async () => {
    const transport = transportWith({ error: '语音会话数已达上限，请先结束一个语音会话再试', code: 'voice_busy' }, 429)
    await expect(transport.open({ output: 'text' })).rejects.toMatchObject({ code: 'voice_busy' })
    expect(FakeWebSocket.instances).toHaveLength(0)
  })
})

describe('RelaySession', () => {
  async function opened(audioReply: 'allowed' | 'off' = 'off', output: 'text' | 'audio' = 'text') {
    const caps = { ...capabilities, audioReply }
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(jsonResponse({ ticket: 't', expiresAt: 'later', capabilities: caps })))
    const transport = new HttpVoiceTransport()
    const opening = transport.open({ output })
    const socket = await nextSocket()
    socket.open()
    const handle = await opening
    return { handle, socket }
  }

  it('drops buffered transcripts and audio after an authentication change', async () => {
    const { handle, socket } = await opened('allowed', 'audio')
    socket.message(JSON.stringify({ t: 'transcript', text: 'old login', final: true }))
    socket.message(new Uint8Array([1, 2]).buffer)
    invalidateAuthentication()
    const events: VoiceEvent[] = []
    const audio: ArrayBuffer[] = []
    handle.onEvent((event) => events.push(event))
    handle.onAudio((frame) => audio.push(frame))
    socket.message(new Uint8Array([3, 4]).buffer)
    expect(events).toEqual([{ t: 'closed', reason: 'auth' }])
    expect(audio).toHaveLength(0)
    expect(socket.closedWith.length).toBeGreaterThan(0)
  })

  it('reports an abrupt socket close exactly once so capture can stop', async () => {
    const { handle, socket } = await opened()
    const events: VoiceEvent[] = []
    handle.onEvent((event) => events.push(event))
    socket.emit('close')
    socket.emit('close')
    socket.message(JSON.stringify({ t: 'transcript', text: 'late', final: true }))
    expect(events).toEqual([{ t: 'closed', reason: 'transport' }])
  })

  it('delivers every frame the server can send', async () => {
    const { handle, socket } = await opened()
    const events: VoiceEvent[] = []
    handle.onEvent((event) => events.push(event))

    socket.message(JSON.stringify({ t: 'ready', model: 'gpt-realtime', inputSampleRate: 24000, outputSampleRate: 24000, audioReply: false }))
    socket.message(JSON.stringify({ t: 'partial', text: 'hel' }))
    socket.message(JSON.stringify({ t: 'transcript', text: 'hello', final: true }))
    socket.message(JSON.stringify({ t: 'assistantText', text: 'hi', final: false }))
    socket.message(JSON.stringify({ t: 'error', code: 'voice_upstream_error', message: '语音上游暂时不可用，请稍后重试' }))
    socket.message(JSON.stringify({ t: 'closed', reason: 'user' }))

    expect(events.map((event) => event.t)).toEqual(['ready', 'partial', 'transcript', 'assistantText', 'error', 'closed'])
    expect(events[2]).toMatchObject({ text: 'hello', final: true })
  })

  it('buffers frames that arrive before anyone subscribes', async () => {
    const { handle, socket } = await opened()
    socket.message(JSON.stringify({ t: 'ready', model: 'gpt-realtime', inputSampleRate: 24000, outputSampleRate: 24000, audioReply: false }))
    const events: VoiceEvent[] = []
    handle.onEvent((event) => events.push(event))
    expect(events).toHaveLength(1)
    expect(events[0].t).toBe('ready')
  })

  it('never delivers the same event twice and unsubscribes cleanly', async () => {
    const { handle, socket } = await opened()
    const first: VoiceEvent[] = []
    const off = handle.onEvent((event) => first.push(event))
    socket.message(JSON.stringify({ t: 'partial', text: 'a' }))
    off()
    socket.message(JSON.stringify({ t: 'partial', text: 'b' }))
    expect(first).toHaveLength(1)
  })

  it('routes downstream audio as raw binary frames', async () => {
    const { handle, socket } = await opened('allowed', 'audio')
    const frames: ArrayBuffer[] = []
    handle.onAudio((pcm) => frames.push(pcm))
    const pcm = new Uint8Array([1, 2, 3, 4]).buffer
    socket.message(pcm)
    expect(frames).toHaveLength(1)
    expect(new Uint8Array(frames[0])).toEqual(new Uint8Array([1, 2, 3, 4]))
  })

  it('sends commit and cancel as control frames', async () => {
    const { handle, socket } = await opened()
    handle.commit()
    handle.cancelResponse()
    expect(socket.sent.slice(1)).toEqual([
      JSON.stringify({ t: 'commit' }),
      JSON.stringify({ t: 'cancel' }),
    ])
  })

  it('stops idempotently and never replays the session', async () => {
    const { handle, socket } = await opened()
    handle.close('user')
    handle.close('user')
    expect(socket.sent.filter((frame) => typeof frame === 'string' && frame.includes('"stop"'))).toHaveLength(1)
    expect(socket.closedWith.length).toBeGreaterThan(0)
  })

  it('ignores malformed server frames instead of throwing', async () => {
    const { handle, socket } = await opened()
    const events: VoiceEvent[] = []
    handle.onEvent((event) => events.push(event))
    socket.message('not json at all')
    socket.message(JSON.stringify({ t: 'unknown-upstream-frame', secret: 'x' }))
    expect(events).toHaveLength(0)
  })

  it('reports a transport error instead of silently dying', async () => {
    const { handle, socket } = await opened()
    const events: VoiceEvent[] = []
    handle.onEvent((event) => events.push(event))
    socket.emit('error', {})
    expect(events[0]).toMatchObject({ t: 'error', code: 'voice_transport_error' })
  })
})
