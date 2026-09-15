import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { api, invalidateAuthentication } from '../../lib/api'
import type { VoiceCapabilities, VoiceEvent, VoiceSessionHandle, VoiceTransport } from '../../lib/chatMediaTypes'
import { VoiceInput } from './VoiceInput'

const captureStop = vi.fn()
const playbackStop = vi.fn()
const playbackPush = vi.fn()
const playbackInterrupt = vi.fn()
let captureFrame: ((pcm: ArrayBuffer) => void) | null = null
let supportResult: { ok: true } | { ok: false; reason: string } = { ok: true }
let captureError: Error | null = null

vi.mock('../../lib/voiceCapture', () => ({
  voiceSupport: () => supportResult,
  startVoiceCapture: vi.fn(async (options: { sampleRate: number; onFrame: (pcm: ArrayBuffer) => void }) => {
    if (captureError) throw captureError
    captureFrame = options.onFrame
    return { sampleRate: options.sampleRate, degraded: false, stop: captureStop }
  }),
  createVoicePlayback: vi.fn(() => ({
    push: playbackPush,
    interrupt: playbackInterrupt,
    stop: playbackStop,
  })),
}))

const capabilities: VoiceCapabilities = {
  enabled: true,
  protocol: 'openai-realtime',
  model: 'gpt-realtime',
  inputSampleRate: 24000,
  outputSampleRate: 24000,
  audioReply: 'off',
  maxSessionSeconds: 600,
}

/** 可注入的传输层替身；它只暴露契约里的 VoiceTransport 接口。 */
class FakeTransport implements VoiceTransport {
  events: Array<(event: VoiceEvent) => void> = []
  audio: Array<(pcm: ArrayBuffer) => void> = []
  sent: ArrayBuffer[] = []
  commits = 0
  cancels = 0
  closes: string[] = []
  opened: Array<'text' | 'audio'> = []
  caps: VoiceCapabilities = { ...capabilities }

  async capabilities() { return this.caps }

  async open(input: { output: 'text' | 'audio' }): Promise<VoiceSessionHandle> {
    this.opened.push(input.output)
    return {
      sampleRate: this.caps.inputSampleRate,
      sendAudio: (frame) => { this.sent.push(frame) },
      commit: () => { this.commits++ },
      cancelResponse: () => { this.cancels++ },
      close: (reason) => { this.closes.push(reason) },
      onEvent: (handler) => { this.events.push(handler); return () => { this.events = this.events.filter((item) => item !== handler) } },
      onAudio: (handler) => { this.audio.push(handler); return () => { this.audio = this.audio.filter((item) => item !== handler) } },
    }
  }

  emit(event: VoiceEvent) { for (const handler of Array.from(this.events)) handler(event) }
}

const user = { id: 'user', email: 'user@example.test', role: 'user', display_name: 'User' }

async function login(sessionID = 'voice-session') {
  vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response(JSON.stringify({ user, csrf_token: 'csrf', session_id: sessionID }))))
  await api.login({ email: 'user@example.test', password: 'test' })
}

function renderVoice(overrides: Partial<Parameters<typeof VoiceInput>[0]> = {}, caps: VoiceCapabilities = capabilities) {
  const transport = new FakeTransport()
  transport.caps = { ...caps }
  const writeDraft = vi.fn()
  const readDraft = vi.fn(() => '')
  const onAssistantText = vi.fn()
  const onStatus = vi.fn()
  const view = render(<VoiceInput
    hostID="host-1"
    paneID="pane-1"
    visible
    transport={transport}
    writeDraft={writeDraft}
    readDraft={readDraft}
    onAssistantText={onAssistantText}
    onStatus={onStatus}
    {...overrides}
  />)
  return { transport, writeDraft, readDraft, onAssistantText, onStatus, view }
}

/** 点击麦克风并等待会话真正进入 listening。 */
async function startListening(harness: ReturnType<typeof renderVoice>) {
  fireEvent.click(await waitFor(() => screen.getByRole('button', { name: '开始语音输入' })))
  await waitFor(() => expect(harness.transport.events.length).toBe(1))
  await act(async () => {
    harness.transport.emit({ t: 'ready', protocol: 'openai-realtime', model: 'gpt-realtime', inputSampleRate: 24000, outputSampleRate: 24000, audioReply: false })
  })
}

beforeEach(async () => {
  invalidateAuthentication()
  captureStop.mockClear()
  playbackStop.mockClear()
  playbackPush.mockClear()
  playbackInterrupt.mockClear()
  captureFrame = null
  captureError = null
  supportResult = { ok: true }
  await login()
})

afterEach(() => {
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
})

describe('VoiceInput', () => {
  it('renders nothing when the instance has no voice configured, so no permission prompt can appear', async () => {
    const { transport } = renderVoice()
    transport.caps = { ...capabilities, enabled: false }
    await waitFor(() => expect(screen.queryByRole('button', { name: '开始语音输入' })).toBeNull())
    expect(transport.opened).toHaveLength(0)
  })

  it('explains an insecure context instead of failing silently', () => {
    supportResult = { ok: false, reason: '语音输入需要 HTTPS 或 localhost 访问' }
    renderVoice()
    expect(screen.getByRole('note')).toHaveTextContent('HTTPS')
    expect(screen.queryByRole('button', { name: '开始语音输入' })).toBeNull()
  })

  it('opens a text-mode session on click and only then starts listening', async () => {
    const harness = renderVoice()
    await waitFor(() => expect(screen.getByRole('button', { name: '开始语音输入' })).toBeTruthy())
    await startListening(harness)
    expect(harness.transport.opened).toEqual(['text'])
    expect(harness.onStatus).toHaveBeenCalledWith('listening', undefined)
  })

  it('forwards captured PCM to the session', async () => {
    const harness = renderVoice()
    await startListening(harness)
    act(() => { captureFrame?.(new Uint8Array([1, 2]).buffer) })
    expect(harness.transport.sent).toHaveLength(1)
  })

  it('writes only the final transcript of the user to the draft', async () => {
    const harness = renderVoice()
    harness.readDraft.mockReturnValue('已有内容')
    await startListening(harness)
    act(() => { harness.transport.emit({ t: 'partial', text: '半句' }) })
    expect(harness.writeDraft).not.toHaveBeenCalled()
    expect(screen.getByText('半句')).toBeTruthy()
    act(() => { harness.transport.emit({ t: 'transcript', text: '你好', final: true }) })
    expect(harness.writeDraft).toHaveBeenCalledWith('host-1', 'pane-1', '已有内容 你好')
  })

  it('never writes the assistant answer to the draft or the terminal', async () => {
    const harness = renderVoice()
    await startListening(harness)
    act(() => { harness.transport.emit({ t: 'assistantText', text: '我建议你…', final: true }) })
    expect(harness.onAssistantText).toHaveBeenCalledWith('我建议你…', true)
    expect(harness.writeDraft).not.toHaveBeenCalled()
  })

  it('labels itself as a voice assistant rather than a terminal agent', async () => {
    renderVoice()
    await waitFor(() => expect(screen.getByText(/语音助手（非终端 Agent）/)).toBeTruthy())
  })

  it('ignores an empty transcript', async () => {
    const harness = renderVoice()
    await startListening(harness)
    act(() => { harness.transport.emit({ t: 'transcript', text: '   ', final: true }) })
    expect(harness.writeDraft).not.toHaveBeenCalled()
  })

  it('stops the microphone and the session together', async () => {
    const harness = renderVoice()
    await startListening(harness)
    fireEvent.click(screen.getByRole('button', { name: '停止语音输入' }))
    expect(captureStop).toHaveBeenCalled()
    expect(harness.transport.closes).toContain('client')
  })

  it('keeps the microphone running when an unrelated re-render changes a callback identity', async () => {
    const harness = renderVoice()
    await startListening(harness)
    captureStop.mockClear()
    // 父组件每次渲染都会传入新的内联回调；它们不得影响一条正在进行的语音会话。
    harness.view.rerender(<VoiceInput
      hostID="host-1" paneID="pane-1" visible transport={harness.transport}
      writeDraft={harness.writeDraft} readDraft={harness.readDraft}
      onAssistantText={vi.fn()} onStatus={vi.fn()}
    />)
    expect(captureStop).not.toHaveBeenCalled()
    expect(harness.transport.closes).toHaveLength(0)
    expect(screen.getByRole('button', { name: '停止语音输入' })).toBeTruthy()

    // 会话仍然接得上：识别文本照常写入草稿。
    act(() => { harness.transport.emit({ t: 'transcript', text: '继续', final: true }) })
    expect(harness.writeDraft).toHaveBeenCalledWith('host-1', 'pane-1', '继续')
  })

  it('releases the microphone when the pane changes', async () => {
    const harness = renderVoice()
    await startListening(harness)
    harness.view.rerender(<VoiceInput
      hostID="host-1" paneID="pane-2" visible transport={harness.transport}
      writeDraft={harness.writeDraft} readDraft={harness.readDraft}
    />)
    expect(captureStop).toHaveBeenCalled()
    expect(harness.transport.closes.length).toBeGreaterThan(0)
  })

  it('releases the microphone when it becomes invisible', async () => {
    const harness = renderVoice()
    await startListening(harness)
    harness.view.rerender(<VoiceInput
      hostID="host-1" paneID="pane-1" visible={false} transport={harness.transport}
      writeDraft={harness.writeDraft} readDraft={harness.readDraft}
    />)
    expect(captureStop).toHaveBeenCalled()
    expect(harness.transport.closes.length).toBeGreaterThan(0)
  })

  it('releases the microphone when the login expires', async () => {
    const harness = renderVoice()
    await startListening(harness)
    await act(async () => { invalidateAuthentication() })
    expect(captureStop).toHaveBeenCalled()
    expect(harness.transport.closes.length).toBeGreaterThan(0)
  })

  it('releases the microphone on pagehide', async () => {
    const harness = renderVoice()
    await startListening(harness)
    await act(async () => { window.dispatchEvent(new Event('pagehide')) })
    expect(captureStop).toHaveBeenCalled()
    expect(harness.transport.closes.length).toBeGreaterThan(0)
  })

  it('stops on the server closed frame without leaving anything running', async () => {
    const harness = renderVoice()
    await startListening(harness)
    act(() => { harness.transport.emit({ t: 'closed', reason: 'idle' }) })
    expect(captureStop).toHaveBeenCalled()
    expect(screen.getByRole('button', { name: '开始语音输入' })).toBeTruthy()
  })

  it('shows the server message on error instead of a generic failure', async () => {
    const harness = renderVoice()
    await startListening(harness)
    act(() => { harness.transport.emit({ t: 'error', code: 'voice_upstream_error', message: '语音上游暂时不可用，请稍后重试' }) })
    expect(screen.getByRole('status')).toHaveTextContent('语音上游暂时不可用，请稍后重试')
  })

  it('surfaces a denied microphone permission without retrying', async () => {
    captureError = new Error('浏览器未授权麦克风。请在地址栏的权限设置里允许本网站使用麦克风后重试')
    const harness = renderVoice()
    fireEvent.click(await waitFor(() => screen.getByRole('button', { name: '开始语音输入' })))
    await waitFor(() => expect(screen.getByRole('status')).toHaveTextContent('未授权麦克风'))
    expect(harness.transport.opened).toHaveLength(0)
  })

  it('hides the audio reply switch when the operator left it off', async () => {
    renderVoice()
    await waitFor(() => expect(screen.getByRole('button', { name: '开始语音输入' })).toBeTruthy())
    expect(screen.queryByRole('button', { name: '开启语音回答' })).toBeNull()
  })

  it('plays downstream audio and supports barge-in when the operator allows it', async () => {
    const allowed = renderVoice({}, { ...capabilities, audioReply: 'allowed' })
    fireEvent.click(await waitFor(() => screen.getByRole('button', { name: '开启语音回答' })))
    await startListening(allowed)
    expect(allowed.transport.opened).toEqual(['audio'])
    await act(async () => {
      allowed.transport.emit({ t: 'ready', protocol: 'openai-realtime', model: 'gpt-realtime', inputSampleRate: 24000, outputSampleRate: 24000, audioReply: true })
    })

    for (const handler of allowed.transport.audio) handler(new Uint8Array([1, 2]).buffer)
    expect(playbackPush).toHaveBeenCalled()

    fireEvent.click(screen.getByRole('button', { name: '打断语音助手' }))
    expect(playbackInterrupt).toHaveBeenCalled()
    expect(allowed.transport.cancels).toBe(1)
  })

  it('keeps the session in text mode when audio is not toggled on', async () => {
    const allowed = renderVoice({}, { ...capabilities, audioReply: 'allowed' })
    await startListening(allowed)
    expect(allowed.transport.opened).toEqual(['text'])
  })

  it('does not expose any send path: the draft is written, never submitted', async () => {
    const submit = vi.fn()
    const harness = renderVoice()
    await startListening(harness)
    act(() => { harness.transport.emit({ t: 'transcript', text: '执行 rm -rf /', final: true }) })
    // 组件只调用 writeDraft；没有任何提交/发送入口被触碰。
    expect(harness.writeDraft).toHaveBeenCalledTimes(1)
    expect(submit).not.toHaveBeenCalled()
    expect(screen.queryByRole('button', { name: /发送|提交/ })).toBeNull()
  })
})
