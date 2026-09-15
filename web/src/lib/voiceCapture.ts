/**
 * 麦克风采集、重采样与下行播放。**不含任何网络或 React 代码**。
 *
 * 上游是可配置的双工语音模型，只认它自己公布的采样率：这里把麦克风采到的浮点音频
 * 线性重采样成契约要求的 PCM16LE 单声道小端，按固定时长切帧。
 *
 * 契约见 docs/design/chat-media-contract.md §5。
 */

/** 每帧时长：20–40 ms。40 ms 在 24 kHz 下是 960 样本，远低于服务端单帧上限。 */
export const VOICE_FRAME_SECONDS = 0.04

/** 安全上下文与 getUserMedia 的可用性，UI 据此决定是否显示麦克风入口。 */
export type VoiceSupport = { ok: true } | { ok: false; reason: string }

export function voiceSupport(): VoiceSupport {
  if (typeof window !== 'undefined' && window.isSecureContext === false) {
    return { ok: false, reason: '语音输入需要 HTTPS 或 localhost 访问，当前页面不是安全上下文' }
  }
  if (typeof navigator === 'undefined' || !navigator.mediaDevices || typeof navigator.mediaDevices.getUserMedia !== 'function') {
    return {
      ok: false,
      reason:
        typeof window !== 'undefined' && window.isSecureContext === false
          ? '语音输入需要 HTTPS 或 localhost 访问'
          : '当前浏览器不支持麦克风采集，请改用最新版 Chrome、Edge、Safari 或 Firefox',
    }
  }
  if (typeof window !== 'undefined' && typeof window.AudioContext === 'undefined' && typeof (window as unknown as { webkitAudioContext?: unknown }).webkitAudioContext === 'undefined') {
    return { ok: false, reason: '当前浏览器不支持 Web Audio，无法采集语音' }
  }
  return { ok: true }
}

/** 把 -1..1 的浮点样本转成 PCM16LE 单声道小端字节。 */
export function floatToPcm16(samples: Float32Array): ArrayBuffer {
  const buffer = new ArrayBuffer(samples.length * 2)
  const view = new DataView(buffer)
  for (let index = 0; index < samples.length; index++) {
    const clamped = Math.max(-1, Math.min(1, samples[index]))
    view.setInt16(index * 2, clamped < 0 ? clamped * 0x8000 : clamped * 0x7fff, true)
  }
  return buffer
}

/**
 * 线性重采样。输入输出都是单声道浮点。
 * 这是契约里明确要求的降级路径：AudioContext 实际采样率与目标不一致时必须走它。
 */
export function resampleLinear(input: Float32Array, inputRate: number, outputRate: number): Float32Array {
  if (inputRate === outputRate || input.length === 0) return input
  const ratio = inputRate / outputRate
  const length = Math.max(1, Math.round(input.length / ratio))
  const output = new Float32Array(length)
  for (let index = 0; index < length; index++) {
    const position = index * ratio
    const left = Math.floor(position)
    const right = Math.min(left + 1, input.length - 1)
    const weight = position - left
    output[index] = input[left] * (1 - weight) + input[right] * weight
  }
  return output
}

/** AudioWorklet 的内联源码，避免新增静态资源文件。 */
const WORKLET_SOURCE = `
class HerdrxVoiceTap extends AudioWorkletProcessor {
  process(inputs) {
    const channel = inputs[0] && inputs[0][0]
    if (channel && channel.length) {
      this.port.postMessage(new Float32Array(channel))
    }
    return true
  }
}
registerProcessor('herdrx-voice-tap', HerdrxVoiceTap)
`

export type VoiceCapture = {
  /** 上行必须使用的采样率；等于服务端 ready.inputSampleRate。 */
  readonly sampleRate: number
  /** 是否退回了 ScriptProcessorNode（AudioWorklet 不可用时的降级）。 */
  readonly degraded: boolean
  /** 幂等停止：断开节点、停止音轨、关闭 AudioContext。 */
  stop(): void
}

function audioContextConstructor(): typeof AudioContext | null {
  if (typeof window === 'undefined') return null
  const candidate = window.AudioContext || (window as unknown as { webkitAudioContext?: typeof AudioContext }).webkitAudioContext
  return candidate ?? null
}

/**
 * 请求麦克风并开始采集。调用方必须在用户**显式点击**后才调用它，
 * 并且必须在 closed 事件到达前调用 `stop()`，避免上游已关还在推流。
 */
export async function startVoiceCapture(options: {
  sampleRate: number
  onFrame: (pcm: ArrayBuffer) => void
  onError?: (message: string) => void
}): Promise<VoiceCapture> {
  const support = voiceSupport()
  if (!support.ok) throw new Error(support.reason)
  const AudioContextClass = audioContextConstructor()
  if (!AudioContextClass) throw new Error('当前浏览器不支持 Web Audio，无法采集语音')

  let stream: MediaStream
  try {
    stream = await navigator.mediaDevices.getUserMedia({
      audio: { channelCount: 1, echoCancellation: true, noiseSuppression: true, autoGainControl: true },
    })
  } catch (error) {
    throw new Error(describeMicrophoneError(error))
  }

  /** 已拿到的麦克风必须在这里释放：调用方此时还没有拿到 capture 对象。 */
  const releaseMicrophone = () => {
    for (const track of stream.getTracks()) {
      try { track.stop() } catch { /* already ended */ }
    }
  }

  // 采集与播放各用一个 AudioContext：共用会让下行播放把上行采样率带偏。
  // 从这里开始的任何失败都已经持有麦克风，必须释放后再抛。
  let context: AudioContext
  try {
    context = new AudioContextClass({ sampleRate: options.sampleRate })
  } catch {
    releaseMicrophone()
    throw new Error('当前浏览器无法以所需的采样率采集语音，请重试或更换浏览器')
  }
  let stopped = false
  let degraded = false
  const nodes: AudioNode[] = []

  const frameSamples = Math.max(128, Math.round(options.sampleRate * VOICE_FRAME_SECONDS))
  const inputSamples = Math.max(128, Math.round(context.sampleRate * VOICE_FRAME_SECONDS))
  let accumulator = new Float32Array(0)

  const flush = (block: Float32Array) => {
    const merged = new Float32Array(accumulator.length + block.length)
    merged.set(accumulator, 0)
    merged.set(block, accumulator.length)
    accumulator = merged
    while (accumulator.length >= inputSamples) {
      const slice = accumulator.subarray(0, inputSamples)
      accumulator = accumulator.slice(inputSamples)
      const resampled = context.sampleRate === options.sampleRate
        ? slice
        : resampleLinear(slice, context.sampleRate, options.sampleRate)
      const framed = resampled.length === frameSamples ? resampled : resampleLinear(resampled, resampled.length, frameSamples)
      if (!stopped) options.onFrame(floatToPcm16(framed))
    }
  }

  let started = false
  try {
    const source = context.createMediaStreamSource(stream)
    nodes.push(source)

    if (typeof context.audioWorklet !== 'undefined' && typeof AudioWorkletNode !== 'undefined') {
      try {
        const moduleURL = URL.createObjectURL(new Blob([WORKLET_SOURCE], { type: 'application/javascript' }))
        try {
          await context.audioWorklet.addModule(moduleURL)
        } finally {
          URL.revokeObjectURL(moduleURL)
        }
        const node = new AudioWorkletNode(context, 'herdrx-voice-tap', { numberOfOutputs: 0 })
        node.port.onmessage = (event: MessageEvent) => {
          if (stopped) return
          const data = event.data
          if (data instanceof Float32Array) flush(data)
        }
        source.connect(node)
        nodes.push(node)
        started = true
      } catch {
        degraded = true
      }
    } else {
      degraded = true
    }

    if (!started) {
      // 降级：ScriptProcessorNode 已废弃，但在没有 AudioWorklet 的浏览器里是唯一的采集通道。
      const processor = context.createScriptProcessor(4096, 1, 1)
      processor.onaudioprocess = (event) => {
        if (stopped) return
        const channel = event.inputBuffer.getChannelData(0)
        flush(new Float32Array(channel))
      }
      source.connect(processor)
      processor.connect(context.destination)
      nodes.push(processor)
    }
  } catch (error) {
    // 采集图没能建起来：调用方还没有 capture 对象，麦克风只能在这里释放。
    stopped = true
    releaseMicrophone()
    void context.close().catch(() => { /* already closed */ })
    throw error instanceof Error ? error : new Error('无法初始化语音采集，请重试')
  }

  if (context.state === 'suspended') {
    try { await context.resume() } catch { /* the browser may refuse without a user gesture */ }
  }

  return {
    sampleRate: options.sampleRate,
    get degraded() { return degraded },
    stop() {
      if (stopped) return
      stopped = true
      accumulator = new Float32Array(0)
      for (const node of nodes) {
        try { node.disconnect() } catch { /* already detached */ }
      }
      releaseMicrophone()
      void context.close().catch(() => { /* already closed */ })
    },
  }
}

function describeMicrophoneError(error: unknown): string {
  const name = error instanceof Error ? error.name : ''
  switch (name) {
    case 'NotAllowedError':
    case 'SecurityError':
      return '浏览器未授权麦克风。请在地址栏的权限设置里允许本网站使用麦克风后重试'
    case 'NotFoundError':
    case 'OverconstrainedError':
      return '没有找到可用的麦克风设备，请检查设备连接'
    case 'NotReadableError':
      return '麦克风被其它程序占用，请关闭占用后重试'
    case 'AbortError':
      return '麦克风初始化被中断，请重试'
    default:
      return '无法访问麦克风，请检查浏览器权限后重试'
  }
}

export type VoicePlayback = {
  /** 推入一帧下行 PCM16LE；调用方按 ready.outputSampleRate 解释。 */
  push(pcm: ArrayBuffer): void
  /** 打断：立即丢弃排队音频（barge-in）。 */
  interrupt(): void
  /** 幂等释放。 */
  stop(): void
}

/**
 * 下行播放器。独立于采集的 AudioContext；只在服务端允许音频回复时才可能有帧。
 */
export function createVoicePlayback(sampleRate: number): VoicePlayback {
  let context: AudioContext | null = null
  let stopped = false
  let nextTime = 0

  const ensure = (): AudioContext | null => {
    if (stopped) return null
    if (context) return context
    const AudioContextClass = audioContextConstructor()
    if (!AudioContextClass) return null
    context = new AudioContextClass({ sampleRate })
    nextTime = context.currentTime
    return context
  }

  const release = () => {
    nextTime = 0
    if (context) {
      const closing = context
      context = null
      void closing.close().catch(() => { /* already closed */ })
    }
  }

  return {
    push(pcm: ArrayBuffer) {
      if (stopped || pcm.byteLength < 2) return
      const active = ensure()
      if (!active) return
      const samples = new Float32Array(pcm.byteLength / 2)
      const view = new DataView(pcm)
      for (let index = 0; index < samples.length; index++) {
        samples[index] = view.getInt16(index * 2, true) / 0x8000
      }
      const buffer = active.createBuffer(1, samples.length, sampleRate)
      buffer.copyToChannel(samples, 0)
      const source = active.createBufferSource()
      source.buffer = buffer
      source.connect(active.destination)
      // 连续排程：比当前时刻早就立即播，避免积压后突然爆发。
      const startAt = Math.max(nextTime, active.currentTime)
      source.start(startAt)
      nextTime = startAt + buffer.duration
    },
    interrupt() {
      release()
      ensure()
    },
    stop() {
      if (stopped) return
      stopped = true
      release()
    },
  }
}
