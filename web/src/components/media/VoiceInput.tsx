import { useCallback, useEffect, useRef, useState } from 'react'
import { MicOff, Mic, Square, Volume2, VolumeX } from 'lucide-react'
import { Button } from '../ui'
import { onAuthEvent } from '../../lib/api'
import {
  appendVoiceTranscript,
  type VoiceCapabilities,
  type VoiceInputProps,
  type VoiceInputState,
  type VoiceSessionHandle,
} from '../../lib/chatMediaTypes'
import {
  createVoicePlayback,
  startVoiceCapture,
  voiceSupport,
  type VoiceCapture,
  type VoicePlayback,
} from '../../lib/voiceCapture'
import './VoiceInput.css'

/**
 * 语音输入按钮。
 *
 * 它只做三件事：在用户**显式点击**后拿麦克风、把 PCM 交给 `transport`、
 * 把**用户自己**的最终识别文本追加进当前 pane 草稿。
 *
 * 它没有发送路径：不发 `pane.send_input`、不发 `keys`、不调用 `runComposerSend`。
 * 它也绝不把语音模型的回答写进草稿或终端——那是另一个通道（`onAssistantText`），
 * 由调用方明确标注为「语音助手（非终端 Agent）」。
 *
 * 契约见 docs/design/chat-media-contract.md §4、§5。
 */
export function VoiceInput({
  hostID, paneID, visible, compact = false, transport,
  writeDraft, readDraft, onAssistantText, onStatus,
}: VoiceInputProps) {
  const [state, setState] = useState<VoiceInputState>('idle')
  const [message, setMessage] = useState('')
  const [capabilities, setCapabilities] = useState<VoiceCapabilities | null>(null)
  const [partial, setPartial] = useState('')
  const [wantAudio, setWantAudio] = useState(false)

  // 所有需要在停止时释放的资源都放在 ref 里：state 更新是异步的，
  // 而清理必须同步且幂等。
  const handleRef = useRef<VoiceSessionHandle | null>(null)
  const captureRef = useRef<VoiceCapture | null>(null)
  const playbackRef = useRef<VoicePlayback | null>(null)
  const unsubscribeRef = useRef<Array<() => void>>([])
  const activeRef = useRef(false)
  const wantAudioRef = useRef(false)
  // 每轮会话一个代号：晚到的 promise 回调不能影响新一轮界面。
  const runRef = useRef(0)

  // 回调经 ref 保活：`report` 必须是稳定引用。否则父组件每次传入新的内联回调，
  // 下面几个 effect 都会重跑清理分支，把一条正在进行的语音会话（含麦克风）拆掉。
  const callbacksRef = useRef({ onStatus, onAssistantText, writeDraft, readDraft })
  callbacksRef.current = { onStatus, onAssistantText, writeDraft, readDraft }

  const report = useCallback((next: VoiceInputState, detail = '') => {
    setState(next)
    setMessage(detail)
    callbacksRef.current.onStatus?.(next, detail || undefined)
  }, [])

  /** 幂等停机：先停麦克风，再断连接，绝不后台偷录。 */
  const shutdown = useCallback(() => {
    activeRef.current = false
    runRef.current += 1
    for (const off of unsubscribeRef.current) {
      try { off() } catch { /* already detached */ }
    }
    unsubscribeRef.current = []
    captureRef.current?.stop()
    captureRef.current = null
    playbackRef.current?.stop()
    playbackRef.current = null
    const handle = handleRef.current
    handleRef.current = null
    handle?.close('client')
    setPartial('')
  }, [])

  useEffect(() => {
    if (!visible) {
      shutdown()
      report('idle')
    }
    return () => { shutdown() }
  }, [visible, shutdown, report])

  // 登出 / 会话失效：立即停止。
  useEffect(() => onAuthEvent((event) => {
    if (event.kind !== 'expired' || !activeRef.current) return
    shutdown()
    report('idle')
  }), [shutdown, report])

  // 页面隐藏（含 PWA 被系统回收）。
  useEffect(() => {
    const onHide = () => {
      if (!activeRef.current) return
      shutdown()
      report('idle')
    }
    window.addEventListener('pagehide', onHide)
    return () => window.removeEventListener('pagehide', onHide)
  }, [shutdown, report])

  // 切换 pane：旧会话必须结束，而不是继续对着旧 pane 录音。
  useEffect(() => {
    if (!activeRef.current) return
    shutdown()
    report('idle')
  }, [hostID, paneID, shutdown, report])

  // 能力探测：未配置时直接隐藏入口，绝不触发授权弹窗。
  useEffect(() => {
    if (!visible || !voiceSupport().ok) return
    let cancelled = false
    void transport.capabilities()
      .then((caps) => { if (!cancelled) setCapabilities(caps) })
      .catch(() => { if (!cancelled) setCapabilities(null) })
    return () => { cancelled = true }
  }, [visible, transport])

  const beginSession = useCallback(async () => {
    const support = voiceSupport()
    if (!support.ok) {
      report('error', support.reason)
      return
    }
    if (!paneID) {
      report('error', '请先选择一个终端再使用语音输入')
      return
    }
    const run = runRef.current + 1
    runRef.current = run
    activeRef.current = true
    report('requesting')

    let caps: VoiceCapabilities
    try {
      caps = await transport.capabilities()
    } catch {
      activeRef.current = false
      report('error', '无法获取语音服务状态，请稍后重试')
      return
    }
    if (run !== runRef.current) return
    if (!caps.enabled) {
      activeRef.current = false
      report('error', '本实例未启用语音输入')
      return
    }
    setCapabilities(caps)

    // 先拿麦克风：被拒时不建立上游会话，也不占用服务端并发额度。
    let capture: VoiceCapture
    try {
      capture = await startVoiceCapture({
        sampleRate: caps.inputSampleRate,
        onFrame: (frame) => { handleRef.current?.sendAudio(frame) },
      })
    } catch (error) {
      activeRef.current = false
      report('error', error instanceof Error ? error.message : '无法访问麦克风，请检查浏览器权限后重试')
      return
    }
    if (run !== runRef.current) { capture.stop(); return }
    captureRef.current = capture
    report('connecting')

    const audio = wantAudioRef.current && caps.audioReply === 'allowed'
    let handle: VoiceSessionHandle
    try {
      handle = await transport.open({ output: audio ? 'audio' : 'text' })
    } catch (error) {
      capture.stop()
      captureRef.current = null
      activeRef.current = false
      report('error', error instanceof Error ? error.message : '无法建立语音会话，请稍后重试')
      return
    }
    if (run !== runRef.current) { handle.close('superseded'); capture.stop(); return }
    handleRef.current = handle
    if (audio) playbackRef.current = createVoicePlayback(caps.outputSampleRate)

    unsubscribeRef.current.push(handle.onEvent((event) => {
      if (run !== runRef.current) return
      switch (event.t) {
        case 'ready':
          report('listening')
          break
        case 'partial':
          setPartial(event.text)
          break
        case 'transcript': {
          // 唯一可以写草稿的内容：用户自己的最终识别文本。绝不触发发送。
          if (!event.final) break
          setPartial('')
          const trimmed = event.text.trim()
          if (!trimmed) break
          const { readDraft: read, writeDraft: write } = callbacksRef.current
          const current = read(hostID, paneID)
          const next = appendVoiceTranscript(current, trimmed)
          if (next !== current) write(hostID, paneID, next)
          break
        }
        case 'assistantText':
          // 语音模型的回答**不是**终端 Agent 的话：只交给调用方单独渲染。
          callbacksRef.current.onAssistantText?.(event.text, event.final)
          break
        case 'error':
          report('error', event.message)
          break
        case 'closed':
          shutdown()
          report('idle')
          break
      }
    }))
    unsubscribeRef.current.push(handle.onAudio((pcm) => { playbackRef.current?.push(pcm) }))
  }, [hostID, paneID, transport, report, shutdown])

  const stop = useCallback(() => {
    report('stopping')
    shutdown()
    report('idle')
  }, [shutdown, report])

  const toggle = useCallback(() => {
    if (activeRef.current) stop()
    else void beginSession()
  }, [beginSession, stop])

  const toggleAudio = useCallback(() => {
    setWantAudio((current) => {
      wantAudioRef.current = !current
      return !current
    })
  }, [])

  /** barge-in：打断当前语音回答。 */
  const interrupt = useCallback(() => {
    handleRef.current?.cancelResponse()
    playbackRef.current?.interrupt()
  }, [])

  if (!visible) return null

  const support = voiceSupport()
  if (!support.ok) {
    // 明确的可执行中文说明，而不是静默失败；按钮不存在，也就不会弹授权。
    return <div className={`voice-input voice-input-unavailable${compact ? ' voice-input-compact' : ''}`} role="note">
      <MicOff size={14} aria-hidden="true" />
      <span>{support.reason}</span>
    </div>
  }
  if (!capabilities || !capabilities.enabled) return null

  const active = state === 'requesting' || state === 'connecting' || state === 'listening'
  const audioAllowed = capabilities.audioReply === 'allowed'
  const statusText = voiceStatusText(state, message)

  return <div className={`voice-input${compact ? ' voice-input-compact' : ''}${active ? ' voice-input-active' : ''}`}>
    <Button
      className={`voice-input-toggle${state === 'listening' ? ' voice-input-listening' : ''}`}
      aria-pressed={active}
      aria-label={active ? '停止语音输入' : '开始语音输入'}
      title={active ? '停止语音输入' : '开始语音输入'}
      onClick={toggle}
    >
      <Mic size={16} aria-hidden="true" />
    </Button>
    {active && <Button className="voice-input-stop" aria-label="结束本次语音会话" title="结束本次语音会话" onClick={stop}>
      <Square size={14} aria-hidden="true" />
    </Button>}
    {audioAllowed && <Button
      className="voice-input-audio"
      aria-pressed={wantAudio}
      aria-label={wantAudio ? '关闭语音回答' : '开启语音回答'}
      title={wantAudio ? '关闭语音回答（只回文字）' : '开启语音回答（会朗读，可随时打断）'}
      onClick={toggleAudio}
    >
      {wantAudio ? <Volume2 size={14} aria-hidden="true" /> : <VolumeX size={14} aria-hidden="true" />}
    </Button>}
    {wantAudio && active && <Button className="voice-input-interrupt" aria-label="打断语音助手" title="打断语音助手" onClick={interrupt}>打断</Button>}
    <span className="voice-input-status" role="status" aria-live="polite">{statusText}</span>
    {partial && <span className="voice-input-partial" aria-live="polite">{partial}</span>}
    <span className="voice-input-note">语音助手（非终端 Agent）：只把识别文本写入草稿，不会自动发送</span>
  </div>
}

function voiceStatusText(state: VoiceInputState, message: string): string {
  if (state === 'error') return message || '语音输入出错，请重试'
  switch (state) {
    case 'requesting': return '正在请求麦克风权限…'
    case 'connecting': return '正在连接语音服务…'
    case 'listening': return '正在聆听，识别结果会写入草稿'
    case 'stopping': return '正在结束语音会话…'
    default: return ''
  }
}
