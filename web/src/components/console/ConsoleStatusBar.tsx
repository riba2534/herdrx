import { useEffect, useState } from 'react'
import { ArrowDown, Brain, Check, LoaderCircle, Sparkles, Square } from 'lucide-react'
import { nextConsoleMode, type ConsoleMode, type ConsoleSpinner } from '../../lib/console/claudeScreen'
import type { ConsoleLive } from '../../lib/console/useConsoleLive'
import { ConsoleLivePreview } from './ConsoleLivePreview'
import { WorkingStatusText } from './WorkingStatusText'
import './console.css'
import './ConsoleStatusBar.css'

/** 运行结束后状态保持完全不透明的时长，随后淡出。 */
export const STATUS_LINGER_MS = 1000
/** 淡出时长，与 `--console-motion-slow` 一致。 */
export const STATUS_FADE_MS = 250

export const CONSOLE_MODE_LABELS: Record<ConsoleMode, string> = {
  bypass: '免授权',
  auto: '自动',
  manual: '手动确认',
  acceptEdits: '接受编辑',
  plan: '计划',
}

export function formatConsoleElapsed(seconds: number) {
  if (seconds < 60) return `${seconds} 秒`
  if (seconds < 3600) return `${Math.floor(seconds / 60)} 分 ${String(seconds % 60).padStart(2, '0')} 秒`
  return `${Math.floor(seconds / 3600)} 小时 ${String(Math.floor((seconds % 3600) / 60)).padStart(2, '0')} 分`
}

export function formatConsoleTokens(tokens: number) {
  const short = (value: number) => (value >= 100 ? String(Math.round(value)) : value.toFixed(1).replace(/\.0$/, ''))
  if (tokens < 1000) return String(tokens)
  if (tokens < 1_000_000) return `${short(tokens / 1000)}k`
  return `${short(tokens / 1_000_000)}M`
}

const failure = (error: unknown, fallback: string) => (error instanceof Error && error.message ? error.message : fallback)

/** 按钮动作：进行中禁用、失败时给出说明；不自动重试。 */
function useAction(run: () => Promise<void>, fallback: string) {
  const [pending, setPending] = useState(false)
  const [error, setError] = useState('')
  useEffect(() => {
    if (!error) return
    const timer = window.setTimeout(() => setError(''), 6000)
    return () => window.clearTimeout(timer)
  }, [error])
  const trigger = async () => {
    if (pending) return
    setPending(true)
    setError('')
    try { await run() } catch (cause) { setError(failure(cause, fallback)) } finally { setPending(false) }
  }
  return { pending, error, trigger }
}

export type ConsoleStatusBarProps = {
  live: ConsoleLive
  /** 调用方判断的「工作中」（如 Herdr 报 working）；读屏看到 spinner 时也算工作中。 */
  working: boolean
  /** 中断当前这一轮（调用方实现为发一次 Esc）。 */
  onInterrupt: () => Promise<void>
  /** 切到下一个模式（调用方实现为按一次 Shift+Tab，再等模式行变化）。 */
  onCycleMode: () => Promise<void>
}

/**
 * 运行状态栏：工作中显示 spinner 动词、计时和 token（文案至少停留 1 秒再淡出淡入），
 * 结束后保持 1 秒再淡出；右侧是模式胶囊（点击循环到下一个模式）和「中断」。
 * 上方是「实时画面」折叠条。数字全部来自读屏，不在本地计时。
 */
export function ConsoleStatusBar({ live, working, onInterrupt, onCycleMode }: ConsoleStatusBarProps) {
  const active = working || Boolean(live.spinner)
  // 读屏偶尔有一帧没有 spinner（重绘中）：工作中保留上一次读到的内容。
  const [held, setHeld] = useState<ConsoleSpinner | null>(live.spinner)
  if (live.spinner && live.spinner !== held) setHeld(live.spinner)
  // tail：运行刚结束的 linger（不透明）与 fading（淡出）阶段。
  const [previousActive, setPreviousActive] = useState(active)
  const [tail, setTail] = useState<'linger' | 'fading' | null>(null)
  if (active !== previousActive) {
    setPreviousActive(active)
    setTail(active ? null : 'linger')
    if (active) setHeld(live.spinner)
  }
  useEffect(() => {
    if (!tail) return
    const timer = window.setTimeout(() => setTail(tail === 'linger' ? 'fading' : null), tail === 'linger' ? STATUS_LINGER_MS : STATUS_FADE_MS)
    return () => window.clearTimeout(timer)
  }, [tail])

  const interrupt = useAction(onInterrupt, '中断没有发出去，请到 Shell 模式按 Esc')
  const cycle = useAction(onCycleMode, '切换模式没有完成，请到 Shell 模式按 Shift+Tab')
  const error = interrupt.error || cycle.error
  const showRun = active || tail !== null
  const spinner = held
  const mode = live.mode
  const next = mode ? nextConsoleMode(mode) : null

  return <div className="console-status" data-state={active ? 'working' : tail || 'idle'}>
    {(live.preview.length > 0 || live.error) && <ConsoleLivePreview preview={live.preview} error={live.error}/>}
    <div className="console-status-row">
      <div className="console-status-run" role="status" aria-live="polite" aria-hidden={!showRun || undefined}>
        {showRun && (active
          ? <>
            <span className="console-status-icon" data-thinking={spinner?.thinking || undefined} aria-hidden="true">{spinner?.thinking ? <Brain size={14}/> : <Sparkles size={14}/>}</span>
            <WorkingStatusText text={spinner ? `${spinner.verb}…` : '正在运行…'}/>
            {spinner?.seconds !== undefined && <span className="console-status-meta">{formatConsoleElapsed(spinner.seconds)}</span>}
            {spinner?.tokens !== undefined && <span className="console-status-meta console-status-tokens"><ArrowDown size={12} aria-hidden="true"/>{formatConsoleTokens(spinner.tokens)} tokens</span>}
            {spinner?.thinking && <span className="console-status-meta console-status-thinking">思考中</span>}
          </>
          : <>
            <span className="console-status-icon console-status-done" aria-hidden="true"><Check size={14}/></span>
            <span className="console-status-text">本轮已结束</span>
            {spinner?.seconds !== undefined && <span className="console-status-meta">{formatConsoleElapsed(spinner.seconds)}</span>}
          </>)}
      </div>
      <div className="console-status-actions">
        {mode && <button
          type="button"
          className="console-mode"
          data-mode={mode}
          disabled={cycle.pending}
          aria-busy={cycle.pending || undefined}
          aria-label={`当前模式：${CONSOLE_MODE_LABELS[mode]}。点击切换到「${CONSOLE_MODE_LABELS[next!]}」`}
          data-tooltip={`点击切换到「${CONSOLE_MODE_LABELS[next!]}」（相当于按一次 Shift+Tab）`}
          onClick={() => void cycle.trigger()}
        >
          {cycle.pending ? <LoaderCircle className="console-spin" size={13} aria-hidden="true"/> : <span className="console-mode-dot" aria-hidden="true"/>}
          {CONSOLE_MODE_LABELS[mode]}
        </button>}
        {active && <button
          type="button"
          className="console-button console-interrupt"
          disabled={interrupt.pending}
          aria-busy={interrupt.pending || undefined}
          data-tooltip="中断这一轮（相当于按一次 Esc），不会关闭终端"
          onClick={() => void interrupt.trigger()}
        >
          {interrupt.pending ? <LoaderCircle className="console-spin" size={13} aria-hidden="true"/> : <Square size={12} aria-hidden="true"/>}中断
        </button>}
      </div>
    </div>
    {error && <p className="console-status-error" role="alert">{error}</p>}
  </div>
}
