import { memo, useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState, type UIEvent } from 'react'
import type { ITheme } from '@xterm/xterm'
import { AArrowDown, AArrowUp, ArrowDownToLine, Copy, SquareTerminal, WrapText } from 'lucide-react'
import type { Pane } from '../types'
import type { WorkbenchClient } from '../lib/workbench'
import { applyScreenFrame, parseAnsiLine, plainScreenLine, screenStyleCSS, type ScreenFrame } from '../lib/ansiScreen'
import { TERMINAL_FONT_FAMILY } from '../lib/terminalFit'
import { agentStatusLabel, paneDisplayName } from '../lib/labels'
import { Button, StatusDot } from './ui'
import './ScreenView.css'

// 画面视图：显示 Herdr 已渲染好的 pane 文本。只读、不改远端尺寸；输入仍由本地输入框
// 和辅助键发送到同一个 pane。折行模式按本窗口宽度重排，适合手机阅读 Agent 输出；
// 原样模式保持远端排版，适合全屏程序。
export const SCREEN_LINE_STEPS = [200, 500, 1000]
const FONT_MIN = 10
const FONT_MAX = 22
const WRAP_KEY = 'herdrx.screen-wrap'
const fontKey = (compact: boolean) => `herdrx.screen-font.${compact ? 'mobile' : 'desktop'}`
const pageVisible = () => typeof document === 'undefined' || document.visibilityState !== 'hidden'

function readWrap() {
  try { return localStorage.getItem(WRAP_KEY) !== 'false' } catch { return true }
}

function readFont(compact: boolean) {
  try {
    const value = Number(localStorage.getItem(fontKey(compact)))
    if (Number.isFinite(value) && value >= FONT_MIN && value <= FONT_MAX) return value
  } catch { /* storage unavailable */ }
  return 13
}

function store(key: string, value: string) {
  try { localStorage.setItem(key, value) } catch { /* storage unavailable */ }
}

const ScreenLine = memo(function ScreenLine({ line, theme }: { line: string; theme: ITheme }) {
  const segments = useMemo(() => parseAnsiLine(line), [line])
  return <div className="screen-line">{segments.map((segment, index) => <span key={index} style={screenStyleCSS(segment.style, theme)}>{segment.text}</span>)}</div>
})

type ScreenState = { lines: string[]; start: number; generation: number; truncated: boolean }

export function ScreenView({ client, pane, compact = false, active = true, connected = true, connectionEpoch = 0, theme, onSwitchToTerminal, onFocus }: {
  client: WorkbenchClient
  pane: Pane
  compact?: boolean
  active?: boolean
  connected?: boolean
  connectionEpoch?: number
  theme: ITheme
  onSwitchToTerminal?: () => void
  onFocus?: () => void
}) {
  const [screen, setScreen] = useState<ScreenState>({ lines: [], start: 0, generation: 0, truncated: false })
  const [requested, setRequested] = useState(SCREEN_LINE_STEPS[0])
  const [phase, setPhase] = useState<'connecting' | 'live' | 'error'>('connecting')
  const [error, setError] = useState('')
  const [wrap, setWrap] = useState(readWrap)
  const [fontSize, setFontSize] = useState(() => readFont(compact))
  const [visible, setVisible] = useState(pageVisible)
  const [following, setFollowing] = useState(true)
  const [fresh, setFresh] = useState(false)
  const [copyStatus, setCopyStatus] = useState('')
  const logRef = useRef<HTMLDivElement>(null)
  const followingRef = useRef(true)
  const anchorRef = useRef<number | null>(null)
  const screenRef = useRef(screen)
  screenRef.current = screen

  useEffect(() => {
    const update = () => setVisible(pageVisible())
    document.addEventListener('visibilitychange', update)
    return () => document.removeEventListener('visibilitychange', update)
  }, [])

  // One watch per visible pane. A hidden page, a background pane on a phone
  // or a lost connection stops polling on the workbench as well.
  const watching = connected && visible && (!compact || active)
  useEffect(() => {
    if (!watching) return
    setPhase((current) => (current === 'live' ? current : 'connecting'))
    let lines = screenRef.current.lines
    let start = screenRef.current.start
    let generation = screenRef.current.generation
    return client.watchScreen(pane.pane_id, requested, {
      onFrame: (frame: ScreenFrame) => {
        if (frame.full) {
          generation++
          start = 0
        } else start += frame.drop || 0
        lines = applyScreenFrame(lines, frame)
        setScreen({ lines, start, generation, truncated: Boolean(frame.truncated) })
        setPhase('live')
        setError('')
      },
      onError: (detail) => {
        setPhase('error')
        setError(detail.message)
      },
    })
  }, [client, pane.pane_id, requested, watching, connectionEpoch])

  useLayoutEffect(() => {
    const log = logRef.current
    if (!log) return
    if (anchorRef.current !== null) {
      log.scrollTop = log.scrollHeight - anchorRef.current
      anchorRef.current = null
      return
    }
    if (followingRef.current) log.scrollTop = log.scrollHeight
    else setFresh(true)
  }, [screen])

  useLayoutEffect(() => {
    const log = logRef.current
    if (log && followingRef.current) log.scrollTop = log.scrollHeight
  }, [wrap, fontSize])

  const onScroll = (event: UIEvent<HTMLDivElement>) => {
    const log = event.currentTarget
    const atBottom = log.scrollHeight - log.scrollTop - log.clientHeight < 24
    if (atBottom !== followingRef.current) {
      followingRef.current = atBottom
      setFollowing(atBottom)
    }
    if (atBottom) setFresh(false)
  }

  const jumpToLatest = () => {
    const log = logRef.current
    followingRef.current = true
    setFollowing(true)
    setFresh(false)
    if (log) log.scrollTop = log.scrollHeight
  }

  const nextStep = SCREEN_LINE_STEPS.find((step) => step > requested)
  const windowFull = screen.truncated || screen.lines.length >= requested
  const loadEarlier = () => {
    if (!nextStep) return
    const log = logRef.current
    if (log) anchorRef.current = log.scrollHeight - log.scrollTop
    setRequested(nextStep)
  }

  const changeFont = (delta: number) => {
    setFontSize((current) => {
      const next = Math.min(FONT_MAX, Math.max(FONT_MIN, current + delta))
      store(fontKey(compact), String(next))
      return next
    })
  }
  const toggleWrap = () => {
    setWrap((current) => {
      store(WRAP_KEY, String(!current))
      return !current
    })
  }

  const copyAll = useCallback(async () => {
    const text = screenRef.current.lines.map(plainScreenLine).join('\n')
    try {
      if (!window.isSecureContext || !navigator.clipboard?.writeText) throw new Error('insecure')
      await navigator.clipboard.writeText(text)
      setCopyStatus('已复制画面文本')
    } catch {
      setCopyStatus('当前页面不能写入剪贴板，请长按选择文字后复制')
    }
  }, [])
  useEffect(() => {
    if (!copyStatus) return
    const timer = window.setTimeout(() => setCopyStatus(''), 3000)
    return () => window.clearTimeout(timer)
  }, [copyStatus])

  const status = pane.agent_status || 'unknown'
  const stateText = !connected ? '主机未连接' : phase === 'connecting' ? '正在读取…' : phase === 'error' ? '读取中断' : '实时'
  const empty = screen.lines.length === 0
  return <section className={`screen-view${wrap ? ' screen-view-wrap' : ''}`} role="region" aria-label="画面视图" onPointerDown={onFocus}>
    <header className="screen-head">
      <span className="screen-head-title">
        <StatusDot status={status}/><strong>{paneDisplayName(pane)}</strong><small>{agentStatusLabel(status)} · 画面{stateText ? ` · ${stateText}` : ''}</small>
      </span>
      <span className="screen-head-actions">
        <Button className="tool-button" aria-label="自动折行" aria-pressed={wrap} data-tooltip={wrap ? '按本窗口宽度折行 · 点击改为原样排版' : '保持远端排版 · 点击改为按宽度折行'} onClick={toggleWrap}><WrapText size={14}/></Button>
        <Button className="tool-button" aria-label="缩小文字" data-tooltip="缩小文字" disabled={fontSize <= FONT_MIN} onClick={() => changeFont(-1)}><AArrowDown size={14}/></Button>
        <Button className="tool-button" aria-label="放大文字" data-tooltip="放大文字" disabled={fontSize >= FONT_MAX} onClick={() => changeFont(1)}><AArrowUp size={14}/></Button>
        <Button className="tool-button" aria-label="复制画面文本" data-tooltip="复制画面文本" disabled={empty} onClick={() => void copyAll()}><Copy size={14}/></Button>
        {onSwitchToTerminal && <Button className="tool-button" aria-label="显示终端" data-tooltip="切换回终端视图（终端不中断）" onClick={onSwitchToTerminal}><SquareTerminal size={14}/></Button>}
      </span>
    </header>
    {(!connected || (phase === 'error' && !empty)) && <div className="screen-banner" role="status">{!connected ? '主机未连接，画面暂停刷新；恢复连接后自动继续。' : `画面可能不是最新：${error}`}</div>}
    {copyStatus && <div className="screen-banner" role="status">{copyStatus}</div>}
    <div
      ref={logRef}
      className="screen-log"
      role="log"
      aria-label="Pane 画面"
      aria-live="off"
      tabIndex={0}
      data-following={following}
      onScroll={onScroll}
      style={{ fontFamily: TERMINAL_FONT_FAMILY, fontSize: `${fontSize}px`, color: theme.foreground, backgroundColor: theme.background }}
    >
      {windowFull && !empty && (nextStep
        ? <Button className="screen-earlier" onClick={loadEarlier}>加载更早内容（最近 {nextStep} 行）</Button>
        : <p className="screen-earlier-note">最多显示最近 {requested} 行；更早内容请切回终端查看历史。</p>)}
      {empty
        ? <p className="screen-state">{phase === 'error' ? `读不到画面：${error}` : connected ? '正在读取画面…' : '主机未连接'}</p>
        : screen.lines.map((line, index) => <ScreenLine key={`${screen.generation}:${screen.start + index}`} line={line} theme={theme}/>)}
    </div>
    {fresh && !following && <Button className="screen-latest button-primary" onClick={jumpToLatest}><ArrowDownToLine size={14}/>最新输出</Button>}
  </section>
}
