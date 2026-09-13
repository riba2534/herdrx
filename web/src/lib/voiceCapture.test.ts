import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import {
  VOICE_FRAME_SECONDS,
  createVoicePlayback,
  floatToPcm16,
  resampleLinear,
  startVoiceCapture,
  voiceSupport,
} from './voiceCapture'

/** 最小 Web Audio 替身，只实现采集与播放真正用到的那几条通路。 */
class FakeAudioWorkletNode {
  static instances: FakeAudioWorkletNode[] = []
  port: { onmessage: ((event: { data: unknown }) => void) | null } = { onmessage: null }
  connect = vi.fn()
  disconnect = vi.fn()
  constructor(_context: unknown, _name: string, _options?: unknown) {
    FakeAudioWorkletNode.instances.push(this)
  }
  /** 测试驱动：把一块浮点样本喂给采集链路。 */
  feed(samples: Float32Array) { this.port.onmessage?.({ data: samples }) }
}

class FakeScriptProcessor {
  static instances: FakeScriptProcessor[] = []
  onaudioprocess: ((event: { inputBuffer: { getChannelData: (index: number) => Float32Array } }) => void) | null = null
  connect = vi.fn()
  disconnect = vi.fn()
  constructor(_size: number, _inputs: number, _outputs: number) { FakeScriptProcessor.instances.push(this) }
  feed(samples: Float32Array) {
    this.onaudioprocess?.({ inputBuffer: { getChannelData: () => samples } })
  }
}

class FakeBufferSource {
  buffer: unknown = null
  connect = vi.fn()
  start = vi.fn()
}

class FakeAudioContext {
  static instances: FakeAudioContext[] = []
  static hasWorklet = true
  /** 模拟浏览器忽略请求的采样率（真实设备常按硬件率运行）。 */
  static forcedSampleRate: number | null = null
  sampleRate: number
  state = 'running'
  currentTime = 0
  destination = {}
  closed = false
  sources: FakeBufferSource[] = []
  audioWorklet = { addModule: vi.fn().mockResolvedValue(undefined) }
  constructor(options?: { sampleRate?: number }) {
    this.sampleRate = FakeAudioContext.forcedSampleRate ?? options?.sampleRate ?? 48000
    if (!FakeAudioContext.hasWorklet) {
      // 模拟没有 AudioWorklet 的浏览器。
      delete (this as unknown as { audioWorklet?: unknown }).audioWorklet
    }
    FakeAudioContext.instances.push(this)
  }
  createMediaStreamSource(_stream: unknown) { return { connect: vi.fn(), disconnect: vi.fn() } }
  createScriptProcessor(size: number, inputs: number, outputs: number) { return new FakeScriptProcessor(size, inputs, outputs) }
  createBuffer(_channels: number, length: number, rate: number) {
    return { duration: length / rate, copyToChannel: vi.fn() }
  }
  createBufferSource() {
    const source = new FakeBufferSource()
    this.sources.push(source)
    return source
  }
  resume() { this.state = 'running'; return Promise.resolve() }
  close() { this.closed = true; return Promise.resolve() }
}

function stubMediaDevices(overrides: Record<string, unknown> = {}) {
  const track = { stop: vi.fn() }
  const stream = { getTracks: () => [track] } as unknown as MediaStream
  const getUserMedia = vi.fn().mockResolvedValue(stream)
  vi.stubGlobal('navigator', { mediaDevices: { getUserMedia }, ...overrides })
  return { getUserMedia, stream, track }
}

beforeEach(() => {
  FakeAudioContext.instances = []
  FakeAudioWorkletNode.instances = []
  FakeScriptProcessor.instances = []
  FakeAudioContext.hasWorklet = true
  FakeAudioContext.forcedSampleRate = null
  vi.stubGlobal('AudioContext', FakeAudioContext)
  vi.stubGlobal('AudioWorkletNode', FakeAudioWorkletNode)
  vi.stubGlobal('URL', { ...URL, createObjectURL: vi.fn(() => 'blob:worklet'), revokeObjectURL: vi.fn() })
  vi.stubGlobal('window', { isSecureContext: true, AudioContext: FakeAudioContext })
})

afterEach(() => {
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
})

describe('floatToPcm16', () => {
  it('writes little-endian signed 16-bit samples', () => {
    const pcm = new DataView(floatToPcm16(new Float32Array([0, 1, -1, 0.5])))
    expect(pcm.getInt16(0, true)).toBe(0)
    expect(pcm.getInt16(2, true)).toBe(0x7fff)
    expect(pcm.getInt16(4, true)).toBe(-0x8000)
    expect(pcm.getInt16(6, true)).toBe(Math.trunc(0.5 * 0x7fff))
  })

  it('clamps out-of-range input instead of wrapping', () => {
    const pcm = new DataView(floatToPcm16(new Float32Array([4, -4])))
    expect(pcm.getInt16(0, true)).toBe(0x7fff)
    expect(pcm.getInt16(2, true)).toBe(-0x8000)
  })
})

describe('resampleLinear', () => {
  it('is a no-op when the rates match', () => {
    const input = new Float32Array([1, 2, 3])
    expect(resampleLinear(input, 24000, 24000)).toBe(input)
  })

  it('halves the sample count when downsampling 48k to 24k', () => {
    const input = new Float32Array(Array.from({ length: 960 }, (_, index) => Math.sin(index / 10)))
    const output = resampleLinear(input, 48000, 24000)
    expect(output.length).toBe(480)
    expect(output[0]).toBeCloseTo(input[0], 5)
  })

  it('keeps a single sample usable', () => {
    expect(resampleLinear(new Float32Array([0.25]), 48000, 24000).length).toBe(1)
  })
})

describe('voiceSupport', () => {
  it('reports an insecure context with an actionable reason', () => {
    vi.stubGlobal('window', { isSecureContext: false })
    vi.stubGlobal('navigator', { mediaDevices: { getUserMedia: vi.fn() } })
    const support = voiceSupport()
    expect(support.ok).toBe(false)
    if (!support.ok) expect(support.reason).toContain('HTTPS')
  })

  it('reports a browser without getUserMedia', () => {
    vi.stubGlobal('navigator', {})
    const support = voiceSupport()
    expect(support.ok).toBe(false)
    if (!support.ok) expect(support.reason).toContain('浏览器')
  })

  it('accepts a secure context with getUserMedia and Web Audio', () => {
    stubMediaDevices()
    expect(voiceSupport()).toEqual({ ok: true })
  })
})

describe('startVoiceCapture', () => {
  it('asks for a mono microphone with the configured sample rate', async () => {
    const { getUserMedia } = stubMediaDevices()
    const frames: ArrayBuffer[] = []
    const capture = await startVoiceCapture({ sampleRate: 24000, onFrame: (pcm) => frames.push(pcm) })

    expect(getUserMedia).toHaveBeenCalledTimes(1)
    expect(getUserMedia.mock.calls[0][0]).toMatchObject({ audio: { channelCount: 1 } })
    expect(FakeAudioContext.instances[0].sampleRate).toBe(24000)
    expect(capture.sampleRate).toBe(24000)
    capture.stop()
  })

  it('emits fixed 40 ms PCM frames through the AudioWorklet', async () => {
    stubMediaDevices()
    const frames: ArrayBuffer[] = []
    const capture = await startVoiceCapture({ sampleRate: 24000, onFrame: (pcm) => frames.push(pcm) })
    const node = FakeAudioWorkletNode.instances[0]
    expect(node).toBeDefined()

    const expected = Math.round(24000 * VOICE_FRAME_SECONDS)
    node.feed(new Float32Array(expected))
    expect(frames).toHaveLength(1)
    expect(frames[0].byteLength).toBe(expected * 2)

    // 半帧不发出，凑满一整帧才发。
    node.feed(new Float32Array(expected / 2))
    expect(frames).toHaveLength(1)
    node.feed(new Float32Array(expected / 2))
    expect(frames).toHaveLength(2)
    capture.stop()
  })

  it('resamples when the AudioContext rate differs from the target', async () => {
    // 上下文被要求 24k 但真的跑在 48k：契约要求走线性重采样。
    FakeAudioContext.forcedSampleRate = 48000
    stubMediaDevices()
    const frames: ArrayBuffer[] = []
    const capture = await startVoiceCapture({ sampleRate: 24000, onFrame: (pcm) => frames.push(pcm) })
    expect(FakeAudioContext.instances[0].sampleRate).toBe(48000)
    const node = FakeAudioWorkletNode.instances[0]
    node.feed(new Float32Array(Math.round(48000 * VOICE_FRAME_SECONDS)))
    expect(frames).toHaveLength(1)
    // 上行始终是目标采样率的 40 ms = 960 样本 = 1920 字节。
    expect(frames[0].byteLength).toBe(960 * 2)
    capture.stop()
  })

  it('falls back to ScriptProcessorNode and reports the degradation', async () => {
    FakeAudioContext.hasWorklet = false
    stubMediaDevices()
    const frames: ArrayBuffer[] = []
    const capture = await startVoiceCapture({ sampleRate: 24000, onFrame: (pcm) => frames.push(pcm) })
    expect(capture.degraded).toBe(true)
    const processor = FakeScriptProcessor.instances[0]
    expect(processor).toBeDefined()
    processor.feed(new Float32Array(960))
    expect(frames).toHaveLength(1)
    capture.stop()
  })

  it('stops the tracks and closes the context, idempotently', async () => {
    const { track } = stubMediaDevices()
    const capture = await startVoiceCapture({ sampleRate: 24000, onFrame: () => {} })
    capture.stop()
    capture.stop()
    expect(track.stop).toHaveBeenCalledTimes(1)
    expect(FakeAudioContext.instances[0].closed).toBe(true)
  })

  it('emits no frame after stop, so nothing is pushed upstream while closing', async () => {
    stubMediaDevices()
    const frames: ArrayBuffer[] = []
    const capture = await startVoiceCapture({ sampleRate: 24000, onFrame: (pcm) => frames.push(pcm) })
    const node = FakeAudioWorkletNode.instances[0]
    capture.stop()
    node.feed(new Float32Array(960))
    expect(frames).toHaveLength(0)
  })

  it('stops the microphone tracks if the audio context cannot be created', async () => {
    const { track } = stubMediaDevices()
    class BrokenAudioContext {
      constructor() { throw new Error('NotSupportedError') }
    }
    vi.stubGlobal('AudioContext', BrokenAudioContext)
    vi.stubGlobal('window', { isSecureContext: true, AudioContext: BrokenAudioContext })
    await expect(startVoiceCapture({ sampleRate: 24000, onFrame: () => {} })).rejects.toThrow()
    // 已经拿到的麦克风必须被释放，否则会一直亮着录音指示灯。
    expect(track.stop).toHaveBeenCalledTimes(1)
  })

  it('translates a denied permission into an actionable message', async () => {
    const denied = new Error('denied')
    denied.name = 'NotAllowedError'
    vi.stubGlobal('navigator', { mediaDevices: { getUserMedia: vi.fn().mockRejectedValue(denied) } })
    await expect(startVoiceCapture({ sampleRate: 24000, onFrame: () => {} }))
      .rejects.toThrow(/未授权麦克风/)
  })

  it('explains a missing device', async () => {
    const missing = new Error('none')
    missing.name = 'NotFoundError'
    vi.stubGlobal('navigator', { mediaDevices: { getUserMedia: vi.fn().mockRejectedValue(missing) } })
    await expect(startVoiceCapture({ sampleRate: 24000, onFrame: () => {} }))
      .rejects.toThrow(/没有找到可用的麦克风/)
  })

  it('refuses to start without a secure context', async () => {
    vi.stubGlobal('window', { isSecureContext: false })
    stubMediaDevices()
    await expect(startVoiceCapture({ sampleRate: 24000, onFrame: () => {} })).rejects.toThrow(/HTTPS/)
  })
})

describe('createVoicePlayback', () => {
  it('schedules downstream frames on its own context', () => {
    const playback = createVoicePlayback(24000)
    playback.push(new Uint8Array([0, 0, 1, 0]).buffer)
    const context = FakeAudioContext.instances[0]
    expect(context.sampleRate).toBe(24000)
    expect(context.sources).toHaveLength(1)
    expect(context.sources[0].start).toHaveBeenCalled()
    playback.stop()
    expect(context.closed).toBe(true)
  })

  it('ignores empty frames and everything after stop', () => {
    const playback = createVoicePlayback(24000)
    playback.push(new ArrayBuffer(0))
    expect(FakeAudioContext.instances).toHaveLength(0)
    playback.push(new Uint8Array([0, 0]).buffer)
    playback.stop()
    playback.push(new Uint8Array([0, 0]).buffer)
    expect(FakeAudioContext.instances[0].sources).toHaveLength(1)
  })

  it('interrupt drops the queue and can restart', () => {
    const playback = createVoicePlayback(24000)
    playback.push(new Uint8Array([0, 0, 1, 0]).buffer)
    const first = FakeAudioContext.instances[0]
    playback.interrupt()
    expect(first.closed).toBe(true)
    playback.push(new Uint8Array([0, 0, 1, 0]).buffer)
    expect(FakeAudioContext.instances).toHaveLength(2)
    playback.stop()
  })
})
