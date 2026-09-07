import { useConfirm } from './useConfirm'
import { Input, Form } from './Form'
import { useEffect, useLayoutEffect, useRef, useState, type MouseEvent as ReactMouseEvent, type ReactNode, type RefObject } from 'react'
import { SearchAddon } from '@xterm/addon-search'
import { Unicode11Addon } from '@xterm/addon-unicode11'
import { WebLinksAddon } from '@xterm/addon-web-links'
import { Terminal, type ITheme } from '@xterm/xterm'
import { History, ImagePlus, Keyboard, MoreHorizontal, Search, X } from 'lucide-react'
import { api } from '../lib/api'
import { clipboardImages, MAX_IMAGE_SIZE, ownsImagePaste } from '../lib/imagePaste'
import { createFontMeasure, fittedTerminalFont, responsiveTerminalSize } from '../lib/terminalFit'
import { attachTerminalTouch } from '../lib/terminalTouch'
import type { TerminalDisplay } from '../lib/displayPreferences'
import type { WorkbenchClient } from '../lib/workbench'
import type { Pane } from '../types'
import { Button, StatusDot } from './ui'

const defaultDisplay: TerminalDisplay = { fontSize: 14, zoom: 100, mode: 'fixed' }

export function TerminalPane({ compact = false, controlsOpen, onControlsOpenChange, externalControlsTrigger, inputFocusRequest = 0, client, pane, connectionEpoch, active, sourceCols, sourceRows, layoutVersion, onFocus, onContextMenu, onControlReady, theme, enhancedContrast, display = defaultDisplay, onFontSizeChange, headerControls, directInput = true, onDirectInput }: { compact?: boolean; externalControlsTrigger?: RefObject<HTMLButtonElement | null>; inputFocusRequest?: number; controlsOpen?: boolean; onControlsOpenChange?: (open: boolean) => void; client: WorkbenchClient; pane: Pane; connectionEpoch: number; active: boolean; sourceCols?: number; sourceRows?: number; layoutVersion?: number; onFocus: () => void; onContextMenu?: (event: ReactMouseEvent<HTMLElement>) => void; onControlReady?: (send: ((data: string) => void) | null) => void; theme: ITheme; enhancedContrast: boolean; display?: TerminalDisplay; onFontSizeChange?: (size: number) => void; headerControls?: ReactNode; directInput?: boolean; onDirectInput?: () => void }) {
  const { confirm, dialog: confirmationDialog } = useConfirm(client)
  const [localControlsOpen, setLocalControlsOpen] = useState(false)
  const toolbarOpen = controlsOpen ?? localControlsOpen
  const setToolbarOpen = onControlsOpenChange ?? setLocalControlsOpen
  const toolbarRef = useRef<HTMLElement>(null)
  const controlsTriggerRef = useRef<HTMLButtonElement>(null)
  const toolbarOpenerRef = useRef<HTMLElement | null>(null)
  const setToolbarOpenRef = useRef(setToolbarOpen)
  setToolbarOpenRef.current = setToolbarOpen
  useLayoutEffect(() => {
    if (!toolbarOpen) return
    toolbarOpenerRef.current = document.activeElement as HTMLElement | null
    toolbarRef.current?.querySelector<HTMLButtonElement>('button:not(:disabled)')?.focus()
  }, [toolbarOpen])
  const restoreToolbarFocus = () => {
    const opener = externalControlsTrigger?.current || controlsTriggerRef.current || toolbarOpenerRef.current
    requestAnimationFrame(() => { if (opener?.isConnected) opener.focus() })
  }
  useEffect(() => {
    if (!toolbarOpen) return
    const outside = (event: PointerEvent) => {
      const target = event.target as HTMLElement
      if (toolbarRef.current?.contains(target) || target.closest('[data-terminal-controls-trigger], [data-ui-overlay], .terminal-search, .context-menu')) return
      setToolbarOpenRef.current(false)
    }
    const escape = (event: KeyboardEvent) => {
      if (event.key !== 'Escape' || (event.target as HTMLElement).closest('[data-ui-overlay], .terminal-search, .context-menu')) return
      event.preventDefault()
      event.stopPropagation()
      setToolbarOpenRef.current(false)
      restoreToolbarFocus()
    }
    document.addEventListener('pointerdown', outside, true)
    window.addEventListener('keydown', escape, true)
    return () => { document.removeEventListener('pointerdown', outside, true); window.removeEventListener('keydown', escape, true) }
  }, [toolbarOpen])
  const closeToolbar = () => { setToolbarOpen(false); restoreToolbarFocus() }
  const viewportRef = useRef<HTMLDivElement>(null)
  const hostRef = useRef<HTMLDivElement>(null)
  const termRef = useRef<Terminal | null>(null)
  const fontMeasureRef = useRef<ReturnType<typeof createFontMeasure> | null>(null)
  const streamRef = useRef<number | null>(null)
  const responsive = display.mode === 'responsive'
  const sourceColsRef = useRef(sourceCols)
  const sourceRowsRef = useRef(sourceRows)
  sourceColsRef.current = sourceCols
  sourceRowsRef.current = sourceRows
  const observedGridRef = useRef<{ cols: number; rows: number } | null>(null)
  const desiredSizeRef = useRef({ cols: 80, rows: 24 })
  const sentSizeRef = useRef({ cols: 0, rows: 0 })
  const resizeTimerRef = useRef(0)
  const fitRef = useRef<() => void>(() => {})
  const connectionEpochRef = useRef(connectionEpoch)
  connectionEpochRef.current = connectionEpoch
  const [status, setStatus] = useState('正在连接终端…')
  const [streamFailed, setStreamFailed] = useState(false)
  const inputBlockedRef = useRef(false)
  const directInputRef = useRef(directInput)
  const wasDirectInputRef = useRef(directInput)
  const previousFocusRequest = useRef(inputFocusRequest)
  directInputRef.current = directInput
  const applyStdin = () => {
    const terminal = termRef.current
    if (!terminal) return
    terminal.options.disableStdin = inputBlockedRef.current || !directInputRef.current
  }
  const [searchOpen, setSearchOpen] = useState(false)
  const [historyActive, setHistoryActive] = useState(false)
  const [historyError, setHistoryError] = useState('')
  const [streamGeneration, setStreamGeneration] = useState(0)
  const historyRef = useRef({ active: false, loading: false, delta: 0, generation: 0 })
  const returnToLiveRef = useRef<() => void>(() => {})
  const scrollHistoryRef = useRef<(lines: number) => void>(() => {})
  const wheelPositionRef = useRef({ column: 0, row: 0 })
  const nativeScrollRef = useRef({ streamID: null as number | null, epoch: connectionEpoch, inFlight: 0, lines: 0, lastEvent: 0 })
  const scrollWheelRef = useRef<(lines: number) => void>(() => {})
  scrollWheelRef.current = (lines: number) => {
    // Fullscreen applications often have no host scrollback. Only Herdr knows
    // whether to deliver a mouse report, alternate-scroll keys or host scroll.
    if (historyRef.current.active || !pane.scroll || pane.scroll.max_offset_from_bottom > 0) {
      nativeScrollRef.current.lines = 0
      scrollHistoryRef.current(lines)
      return
    }
    if (nativeScrollRef.current.streamID !== streamRef.current || nativeScrollRef.current.epoch !== connectionEpochRef.current) nativeScrollRef.current = { streamID: streamRef.current, epoch: connectionEpochRef.current, inFlight: 0, lines: 0, lastEvent: 0 }
    const queue = nativeScrollRef.current
    if (lines) {
      if (queue.lines * lines < 0) queue.lines = 0
      queue.lines = Math.max(-100, Math.min(100, queue.lines + lines))
      queue.lastEvent = performance.now()
    } else if (performance.now() - queue.lastEvent > 120) {
      queue.lines = 0 // Do not keep scrolling after a slow connection catches up.
    }
    // Pipeline a bounded number of frame-sized gestures instead of waiting a
    // network round trip between each update. WebSocket preserves their order.
    if (queue.inFlight >= 8 || streamRef.current === null || !queue.lines) return
    const amount = queue.lines
    const streamID = streamRef.current
    queue.lines = 0
    queue.inFlight++
    void client.call('terminal.scroll', { stream_id: streamID, lines: amount, ...wheelPositionRef.current }).then(() => {
      queue.inFlight--
      if (!mountedRef.current || nativeScrollRef.current !== queue || streamRef.current !== streamID) return
      setHistoryError('')
      if (queue.lines) scrollWheelRef.current(0)
    }).catch((error) => {
      queue.inFlight--
      queue.lines = 0 // Never replay a gesture whose outcome is uncertain.
      if (mountedRef.current && nativeScrollRef.current === queue && streamRef.current === streamID) setHistoryError(error instanceof Error ? error.message : '终端滚动失败，请重试')
    })
  }
  const [imageFeedback, setImageFeedback] = useState<{ message: string; failed?: boolean; pending?: boolean; files?: File[] } | null>(null)
  const imageInputRef = useRef<HTMLInputElement>(null)
  const imageQueueRef = useRef<Promise<void>>(Promise.resolve())
  const uploadImagesRef = useRef<(files: File[]) => void>(() => {})
  const mountedRef = useRef(false)
  const writeSessionRef = useRef({ pending: 0, waiters: [] as Array<{ kind: 'frame' | 'fit' | 'history' | 'dispose', run: () => void, drop?: () => void }>, pumping: false, closed: false })
  const pumpTerminalIdle = () => {
    const session = writeSessionRef.current
    if (session.pumping) return
    session.pumping = true
    try {
      while (session.pending === 0 && session.waiters.length) session.waiters.shift()!.run()
    } finally {
      session.pumping = false
    }
  }
  const whenTerminalIdle = (run: () => void, options?: { kind?: 'frame' | 'fit' | 'history' | 'dispose', drop?: () => void }) => {
    const session = writeSessionRef.current
    const kind = options?.kind ?? 'frame'
    if (kind === 'fit' || kind === 'history') {
      // Local resizes and history jobs coalesce; terminal frames are never discarded.
      session.waiters = session.waiters.filter((waiter) => waiter.kind !== kind)
    }
    session.waiters.push({ kind, run, drop: options?.drop })
    pumpTerminalIdle()
  }
  const writeTerminal = (terminal: Terminal, data: string | Uint8Array, done?: () => void) => {
    const session = writeSessionRef.current
    if (session.closed || termRef.current !== terminal) {
      done?.()
      return
    }
    session.pending++
    terminal.write(data, () => {
      session.pending = Math.max(0, session.pending - 1)
      done?.()
      pumpTerminalIdle()
    })
  }
  const searchRef = useRef<SearchAddon | null>(null)
  const pendingInputRef = useRef<string[]>([])
  const pendingInputSizeRef = useRef(0)
  const revealCursorRef = useRef<() => void>(() => {})
  revealCursorRef.current = () => {
    const terminal = termRef.current, viewport = viewportRef.current
    const screen = hostRef.current?.querySelector<HTMLElement>('.xterm-screen')
    if (!terminal || !viewport || !screen || historyRef.current.active) return
    const screenRect = screen.getBoundingClientRect(), viewportRect = viewport.getBoundingClientRect()
    const cellWidth = screenRect.width / terminal.cols, cellHeight = screenRect.height / terminal.rows
    const left = screenRect.left - viewportRect.left + viewport.scrollLeft + terminal.buffer.active.cursorX * cellWidth
    const top = screenRect.top - viewportRect.top + viewport.scrollTop + terminal.buffer.active.cursorY * cellHeight
    if (left < viewport.scrollLeft) viewport.scrollLeft = left
    else if (left + cellWidth > viewport.scrollLeft + viewport.clientWidth) viewport.scrollLeft = left + cellWidth - viewport.clientWidth
    if (top < viewport.scrollTop) viewport.scrollTop = top
    else if (top + cellHeight > viewport.scrollTop + viewport.clientHeight) viewport.scrollTop = top + cellHeight - viewport.clientHeight
  }
  const requestInputRef = useRef<(data: string) => void>(() => {})
  requestInputRef.current = (data: string) => {
    if (inputBlockedRef.current) return
    if (historyRef.current.active) returnToLiveRef.current()
    revealCursorRef.current()
    const queue = () => {
      if (pendingInputSizeRef.current + data.length <= 32 * 1024) {
        pendingInputRef.current.push(data)
        pendingInputSizeRef.current += data.length
      }
    }
    if (streamRef.current == null) {
      queue()
      return
    }
    client.sendInput(streamRef.current, data)
  }

  returnToLiveRef.current = () => {
    historyRef.current.generation++
    historyRef.current.active = false
    historyRef.current.loading = false
    historyRef.current.delta = 0
    setHistoryActive(false)
    if (streamRef.current !== null) {
      client.closeTerminal(streamRef.current)
      streamRef.current = null
    }
    // Live deltas were skipped in history. Reopen for a full authoritative frame.
    setStreamGeneration((value) => value + 1)
  }

  scrollHistoryRef.current = (lines: number) => {
    const terminal = termRef.current
    if (!terminal) return
    const history = historyRef.current
    if (history.loading) { history.delta += lines; return }
    if (history.active) {
      terminal.scrollLines(lines)
      if (lines > 0 && terminal.buffer.active.viewportY >= terminal.buffer.active.baseY) returnToLiveRef.current()
      return
    }
    if (lines >= 0) return
    history.active = true
    history.loading = true
    history.delta = lines
    const generation = ++history.generation
    setHistoryActive(true)
    setHistoryError('')
    void client.call<{ read: { text: string; truncated?: boolean } }>('pane.read', { pane_id: pane.pane_id, source: 'recent', format: 'ansi', lines: 10000 }).then(({ read }) => {
      if (!mountedRef.current || history.generation !== generation || termRef.current !== terminal) return
      whenTerminalIdle(() => {
        if (!mountedRef.current || history.generation !== generation || termRef.current !== terminal) return
        terminal.options.scrollback = 10000
        // Herdr's formatted snapshots use LF, while live PTY frames use explicit
        // cursor positioning. Restore CR only for snapshot line separators.
        writeTerminal(terminal, `\x1bc\x1b[?25l${read.text.replace(/\r?\n/g, '\r\n')}`, () => {
          if (history.generation !== generation || termRef.current !== terminal) return
          terminal.scrollToBottom()
          terminal.scrollLines(history.delta)
          history.loading = false
          history.delta = 0
        })
      }, { kind: 'history' })
    }).catch((error) => {
      if (!mountedRef.current || history.generation !== generation) return
      setHistoryError(error instanceof Error ? error.message : '无法读取历史记录，请重试')
      returnToLiveRef.current()
    })
  }

  useEffect(() => {
    mountedRef.current = true
    return () => { mountedRef.current = false; historyRef.current.generation++ }
  }, [])

  useEffect(() => {
    if (!imageFeedback || imageFeedback.failed || imageFeedback.pending) return
    const timer = window.setTimeout(() => setImageFeedback(null), 3500)
    return () => window.clearTimeout(timer)
  }, [imageFeedback])

  uploadImagesRef.current = (files: File[]) => {
    if (!files.length) return
    if (historyRef.current.active) returnToLiveRef.current()
    // Serialize paste gestures so slow uploads cannot reverse attachment order.
    imageQueueRef.current = imageQueueRef.current.then(async () => {
      if (!mountedRef.current) return
      if (files.some((file) => file.size > MAX_IMAGE_SIZE)) {
        setImageFeedback({ message: '图片大小超过 20MB 限制，请选择较小的图片。', failed: true })
        return
      }
      for (let index = 0; index < files.length; index++) {
        try {
          setImageFeedback({ message: `正在粘贴图片 ${index + 1}/${files.length}…`, pending: true })
          await api.pasteImage(client.getHostID(), pane.pane_id, files[index], true)
        } catch (error) {
          if (mountedRef.current) setImageFeedback({ message: error instanceof Error ? error.message : '图片粘贴失败，请重试。', failed: true, files: files.slice(index) })
          return
        }
      }
      if (mountedRef.current) setImageFeedback({ message: `${files.length} 张图片已粘贴` })
    })
  }

  const fitTerminal = () => {
    const terminal = termRef.current
    const host = hostRef.current
    const viewport = viewportRef.current
    if (!mountedRef.current || writeSessionRef.current.closed || !terminal || !host || !viewport || !fontMeasureRef.current) return
    const style = getComputedStyle(host)
    const bounds = {
      width: viewport.clientWidth - parseFloat(style.paddingLeft || '0') - parseFloat(style.paddingRight || '0'),
      height: viewport.clientHeight - parseFloat(style.paddingTop || '0') - parseFloat(style.paddingBottom || '0'),
      dpr: window.devicePixelRatio || 1,
      lineHeight: terminal.options.lineHeight || 1, letterSpacing: terminal.options.letterSpacing || 0,
    }
    const size = responsive ? responsiveTerminalSize(bounds, fontMeasureRef.current.measure, display.fontSize * display.zoom / 100) : null
    const observed = observedGridRef.current
    const cols = size?.cols ?? Math.max(10, observed?.cols || sourceCols || terminal.cols)
    const rows = size?.rows ?? Math.max(3, observed?.rows || sourceRows || terminal.rows)
    const baseSize = display.mode !== 'fit' ? display.fontSize : fittedTerminalFont({ ...bounds, cols, rows }, fontMeasureRef.current.measure, display.fontSize)
    const fontSize = baseSize === null ? null : Math.round(baseSize * display.zoom) / 100
    if (fontSize !== null && terminal.options.fontSize !== fontSize) terminal.options.fontSize = fontSize
    const applyResize = () => {
      if (termRef.current !== terminal || writeSessionRef.current.closed) return
      if (terminal.cols !== cols || terminal.rows !== rows) terminal.resize(cols, rows)
    }
    // A responsive live grid belongs to the incoming frame. Speculatively
    // shrinking it can discard rows before a debounced remote resize arrives,
    // and interleave local reflow with an older frame's parser state.
    if (!responsive || streamRef.current === null) {
      const sizeChanged = terminal.cols !== cols || terminal.rows !== rows
      const hasQueuedFit = writeSessionRef.current.waiters.some((waiter) => waiter.kind === 'fit')
      if (writeSessionRef.current.pending === 0) applyResize()
      else if (sizeChanged || hasQueuedFit) whenTerminalIdle(applyResize, { kind: 'fit' })
    }
    if (size) {
      desiredSizeRef.current = size
      if (streamRef.current !== null && (sentSizeRef.current.cols !== cols || sentSizeRef.current.rows !== rows)) {
        window.clearTimeout(resizeTimerRef.current)
        resizeTimerRef.current = window.setTimeout(() => {
          if (streamRef.current === null) return
          const next = desiredSizeRef.current
          client.resize(streamRef.current, next.cols, next.rows)
          sentSizeRef.current = next
        }, 80)
      }
    }
    if (fontSize !== null) onFontSizeChange?.(fontSize)
  }
  fitRef.current = fitTerminal

  // Fit the final sidebar layout before the next paint, so xterm's scheduled
  // render sees only the final font instead of several visible trial sizes.
  useLayoutEffect(() => { fitTerminal() }, [layoutVersion, sourceCols, sourceRows, display.fontSize, display.zoom, display.mode, active])

  useEffect(() => {
    if (!hostRef.current) return
    const terminal = new Terminal({
      allowProposedApi: true, cursorBlink: true, cursorStyle: 'block', cursorInactiveStyle: 'outline',
      fontFamily: '"JetBrains Mono", "SFMono-Regular", Consolas, monospace', fontSize: 14, lineHeight: 1,
      scrollback: 0, theme, minimumContrastRatio: enhancedContrast ? 4.5 : 1, convertEol: false,
    })
    const unicode = new Unicode11Addon()
    const search = new SearchAddon()
    terminal.loadAddon(unicode)
    terminal.loadAddon(search)
    terminal.loadAddon(new WebLinksAddon((event, uri) => {
      event.preventDefault()
      const url = new URL(uri)
      if (!['http:', 'https:'].includes(url.protocol)) return
      void confirm(`将在新窗口打开以下链接：\n${url.href}`, { title: '打开外部链接', confirmLabel: '打开链接', danger: false, onConfirm: () => { window.open(url.href, '_blank', 'noopener,noreferrer') } })
    }))
    terminal.unicode.activeVersion = '11'
    terminal.open(hostRef.current)
    terminal.attachCustomKeyEventHandler((event) => {
      if ((event.ctrlKey || event.metaKey) && !event.altKey && ['+', '=', '-', '0'].includes(event.key)) {
        return false // Preserve the browser's page zoom and reset shortcuts.
      }
      if (event.key === 'Enter' && event.shiftKey && !event.ctrlKey && !event.altKey && !event.metaKey && !event.isComposing) {
        event.preventDefault()
        event.stopPropagation()
        // A line feed is the terminal newline binding (Ctrl+J). Legacy xterm
        // encodes Shift+Enter as CR, which submits in chat-style terminal UIs.
        if (event.type === 'keydown') requestInputRef.current('\n')
        return false
      }
      if ((event.ctrlKey || event.metaKey) && (event.key === 'v' || event.key === 'V')) {
        return false
      }
      return true
    })
    termRef.current = terminal
    writeSessionRef.current = { pending: 0, waiters: [], pumping: false, closed: false }
    fontMeasureRef.current = createFontMeasure(hostRef.current, terminal.options.fontFamily!)
    searchRef.current = search
    const inputDisposable = terminal.onData((data) => requestInputRef.current(data))
    const host = hostRef.current
    const revealCursor = () => revealCursorRef.current()
    // Scroll gestures use the outer viewport and the public terminal APIs.
    // A delayed native inner-viewport event after RIS can divide by xterm's
    // temporarily zero row height and poison its visible offset with NaN.
    const ignoreInnerScroll = (event: Event) => {
      if (event.target instanceof Element && event.target.classList.contains('xterm-viewport')) event.stopImmediatePropagation()
    }
    host.addEventListener('focusin', revealCursor)
    host.addEventListener('scroll', ignoreInnerScroll, true)
    return () => {
      const session = writeSessionRef.current
      for (const waiter of session.waiters) waiter.drop?.()
      session.waiters = [{ kind: 'dispose', run: () => {
        session.closed = true
        session.waiters.length = 0
        inputDisposable.dispose()
        host.removeEventListener('focusin', revealCursor)
        host.removeEventListener('scroll', ignoreInnerScroll, true)
        fontMeasureRef.current?.dispose()
        fontMeasureRef.current = null
        if (streamRef.current != null) client.closeTerminal(streamRef.current)
        terminal.dispose()
        if (termRef.current === terminal) termRef.current = null
      } }]
      pumpTerminalIdle()
    }
  }, [])

  useEffect(() => {
    if (!onControlReady) return
    onControlReady((data) => requestInputRef.current(data))
    return () => onControlReady(null)
  }, [onControlReady])

  useEffect(() => {
    if (!termRef.current) return
    termRef.current.options.theme = theme
    termRef.current.options.minimumContrastRatio = enhancedContrast ? 4.5 : 1
    termRef.current.refresh(0, termRef.current.rows - 1)
  }, [theme, enhancedContrast])

  useEffect(() => {
    if (!connectionEpoch || !termRef.current) return
    let cancelled = false
    let firstFrame = true
    let resetStream = true
    let unsubscribe = () => {}
    const open = async () => {
      if (streamRef.current != null) client.closeTerminal(streamRef.current)
      streamRef.current = null
      inputBlockedRef.current = false
      setStreamFailed(false)
      setStatus('正在连接终端…')
      observedGridRef.current = null
      fitRef.current()
      const terminal = termRef.current!
      applyStdin()
      const failed = (reason: string) => {
        if (cancelled) return
        inputBlockedRef.current = true
        streamRef.current = null
        terminal.options.disableStdin = true
        pendingInputRef.current = []
        pendingInputSizeRef.current = 0
        setStatus('终端连接已关闭：' + (responsive && reason.includes('already has an attached client') ? '此终端正在其他窗口自适应显示，请关闭那个窗口后重连，或切换为“原始画面”。' : reason))
        setStreamFailed(true)
      }
      try {
        const cols = responsive ? desiredSizeRef.current.cols : Math.max(10, sourceColsRef.current || terminal.cols)
        const rows = responsive ? desiredSizeRef.current.rows : Math.max(3, sourceRowsRef.current || terminal.rows)
        sentSizeRef.current = { cols, rows }
        const streamID = await (responsive ? client.openTerminal(pane.pane_id, cols, rows, true) : client.openTerminal(pane.pane_id, cols, rows))
        if (cancelled) { client.closeTerminal(streamID); return }
        streamRef.current = streamID
        setStatus('可输入')
        unsubscribe = client.onTerminal(streamID, (frame) => {
          if (historyRef.current.active) { client.acknowledge(streamID, frame.seq); return }
          const ack = () => client.acknowledge(streamID, frame.seq)
          const validFrame = frame.cols >= 10 && frame.rows >= 3
          const followObserved = !responsive && validFrame
          const needsResize = validFrame && (terminal.cols !== frame.cols || terminal.rows !== frame.rows)
          const deliver = () => {
            if (cancelled || writeSessionRef.current.closed || termRef.current !== terminal) return
            if (historyRef.current.active) { ack(); return }
            if (followObserved) observedGridRef.current = { cols: frame.cols, rows: frame.rows }
            if (validFrame && (responsive || followObserved) && (terminal.cols !== frame.cols || terminal.rows !== frame.rows)) {
              terminal.resize(frame.cols, frame.rows)
              if (followObserved) fitRef.current()
            }
            let bytes: string | Uint8Array = frame.ansi
            if (resetStream) {
              // Reset once, in the same parser write as the new stream's first
              // image. Synchronous reset() can paint a blank frame before write().
              // Later full frames are repaints and preserve terminal cursor modes.
              resetStream = false
              terminal.options.scrollback = 0
              bytes = new Uint8Array(frame.ansi.length + 2)
              bytes.set([0x1b, 0x63])
              bytes.set(frame.ansi, 2)
            }
            writeTerminal(terminal, bytes, () => {
              ack()
              if (firstFrame) { firstFrame = false; revealCursorRef.current() }
            })
          }
          const session = writeSessionRef.current
          if (session.pending !== 0 && (needsResize || resetStream || session.waiters.length > 0)) {
            whenTerminalIdle(deliver, { kind: 'frame', drop: ack })
            return
          }
          deliver()
        }, failed)
        fitRef.current() // The viewport may have changed while opening the stream.
        if (!inputBlockedRef.current && pendingInputRef.current.length > 0) {
          for (const data of pendingInputRef.current) client.sendInput(streamID, data)
          pendingInputRef.current = []
          pendingInputSizeRef.current = 0
        }
      } catch (reason) {
        failed(reason instanceof Error ? reason.message : '请检查远程 Herdr 后重试')
      }
    }
    void open()
    return () => {
      cancelled = true
      window.clearTimeout(resizeTimerRef.current)
      inputBlockedRef.current = true
      unsubscribe()
      if (streamRef.current != null) {
        client.closeTerminal(streamRef.current)
        streamRef.current = null
      }
    }
  }, [client, pane.pane_id, connectionEpoch, responsive, streamGeneration])

  useEffect(() => {
    applyStdin()
    const becameDirect = directInput && !wasDirectInputRef.current
    wasDirectInputRef.current = directInput
    const focusRequested = inputFocusRequest !== previousFocusRequest.current
    previousFocusRequest.current = inputFocusRequest
    if (directInput && (becameDirect || focusRequested) && active && !inputBlockedRef.current) {
      revealCursorRef.current()
      termRef.current?.focus()
    }
    const host = hostRef.current
    if (!host || directInput) return
    const block = (event: FocusEvent) => {
      const target = event.target
      if (target instanceof HTMLTextAreaElement && target.classList.contains('xterm-helper-textarea')) target.blur()
    }
    host.addEventListener('focusin', block)
    return () => host.removeEventListener('focusin', block)
  }, [directInput, inputFocusRequest, connectionEpoch, streamGeneration, active])

  useEffect(() => {
    const viewport = viewportRef.current
    if (!viewport) return
    const observer = new ResizeObserver(() => fitTerminal())
    observer.observe(viewport)
    return () => { observer.disconnect() }
  }, [client, sourceCols, sourceRows, display.fontSize, display.zoom, display.mode, active])

  useEffect(() => {
    const host = hostRef.current
    const viewport = viewportRef.current
    if (!host || !viewport) return
    let queuedLines = 0
    let pixels = 0
    let timer = 0
    let queueGeneration = ''
    const generation = () => `${connectionEpochRef.current}:${streamRef.current}`
    const flush = () => {
      timer = 0
      if (queueGeneration !== generation()) { queuedLines = 0; pixels = 0; return }
      const lines = Math.min(100, Math.abs(queuedLines))
      if (lines) scrollWheelRef.current(Math.sign(queuedLines) * lines)
      queuedLines -= Math.sign(queuedLines) * lines
      if (queuedLines) timer = window.requestAnimationFrame(flush)
    }
    const queuePixels = (deltaY: number, clientX: number, clientY: number) => {
      if (queueGeneration !== generation()) { queuedLines = 0; pixels = 0; queueGeneration = generation() }
      const screen = host.querySelector<HTMLElement>('.xterm-screen')?.getBoundingClientRect()
      const terminal = termRef.current
      if (screen && terminal && screen.width && screen.height) wheelPositionRef.current = {
        column: Math.max(0, Math.min(terminal.cols - 1, Math.floor((clientX - screen.left) * terminal.cols / screen.width))),
        row: Math.max(0, Math.min(terminal.rows - 1, Math.floor((clientY - screen.top) * terminal.rows / screen.height))),
      }
      const row = host.querySelector<HTMLElement>('.xterm-rows > div')
      const lineHeight = Math.max(1, row?.getBoundingClientRect().height || 16)
      if (pixels * deltaY < 0) pixels = 0
      if (queuedLines * deltaY < 0) queuedLines = 0
      pixels += deltaY
      const lines = Math.trunc(pixels / lineHeight)
      pixels -= lines * lineHeight
      queuedLines = Math.max(-100, Math.min(100, queuedLines + lines))
      if (queuedLines && !timer) timer = window.requestAnimationFrame(flush)
    }
    const canPan = (deltaY: number) => {
      const max = Math.max(0, viewport.scrollHeight - viewport.clientHeight)
      if (max <= 1) return false
      return deltaY < 0 ? viewport.scrollTop > 1 : viewport.scrollTop < max - 1
    }
    const screenClipped = (deltaY: number) => {
      const screen = host.querySelector<HTMLElement>('.xterm-screen')
      if (!screen?.getClientRects().length) return canPan(deltaY)
      const box = screen.getBoundingClientRect()
      const view = viewport.getBoundingClientRect()
      return deltaY < 0 ? box.top < view.top - 1 : box.bottom > view.bottom + 1
    }
    const wheel = (event: WheelEvent) => {
      if (event.ctrlKey || event.metaKey) { event.stopImmediatePropagation(); return }
      if (!event.deltaY || event.shiftKey || Math.abs(event.deltaX) > Math.abs(event.deltaY)) { event.stopImmediatePropagation(); return }
      if (canPan(event.deltaY) && screenClipped(event.deltaY)) {
        event.stopImmediatePropagation()
        return
      }
      event.preventDefault()
      event.stopImmediatePropagation()
      const lineHeight = Math.max(1, host.querySelector<HTMLElement>('.xterm-rows > div')?.getBoundingClientRect().height || 16)
      const unit = event.deltaMode === 1 ? lineHeight : event.deltaMode === 2 ? viewport.clientHeight : 1
      queuePixels(event.deltaY * unit, event.clientX, event.clientY)
    }
    viewport.addEventListener('wheel', wheel, { capture: true, passive: false })
    const cancelQueuedScroll = () => {
      pixels = 0
      queuedLines = 0
      window.cancelAnimationFrame(timer)
      timer = 0
      nativeScrollRef.current.lines = 0
    }
    const detachTouch = attachTerminalTouch(viewport, {
      onScrollPixels: queuePixels, onGestureCancel: cancelQueuedScroll, getGeneration: generation,
      hasSelection: () => termRef.current?.hasSelection?.() ?? false,
    })
    return () => { detachTouch(); viewport.removeEventListener('wheel', wheel, true); window.cancelAnimationFrame(timer) }
  }, [client])

  useEffect(() => {
    const host = hostRef.current
    if (!host) return
    const handlePaste = (event: ClipboardEvent) => {
      if (!ownsImagePaste(host, active, event)) return
      const files = clipboardImages(event.clipboardData)
      if (!files.length) return
      event.preventDefault()
      event.stopImmediatePropagation()
      onFocus()
      uploadImagesRef.current(files)
    }

    const handleDragOver = (event: DragEvent) => {
      if (event.dataTransfer?.types?.includes('Files')) {
        event.preventDefault()
      }
    }

    const handleDrop = (event: DragEvent) => {
      if (!event.dataTransfer?.types?.includes('Files')) return
      event.preventDefault()
      event.stopImmediatePropagation()
      const files = clipboardImages(event.dataTransfer)
      if (!files.length) { setImageFeedback({ message: '请选择 PNG、JPEG、WebP 或 GIF 图片。', failed: true }); return }
      onFocus()
      termRef.current?.focus()
      uploadImagesRef.current(files)
    }

    host.addEventListener('dragover', handleDragOver)
    host.addEventListener('drop', handleDrop, true)
    window.addEventListener('paste', handlePaste, true)

    return () => {
      host.removeEventListener('dragover', handleDragOver)
      host.removeEventListener('drop', handleDrop, true)
      window.removeEventListener('paste', handlePaste, true)
    }
  }, [client, pane.pane_id, active])

  return <section className={`terminal-pane ${active ? 'terminal-pane-active' : ''} ${directInput ? '' : 'terminal-pane-composer'} ${toolbarOpen ? 'terminal-pane-tools-open' : ''}`} data-terminal-status={status} onPointerDown={onFocus} onContextMenu={(event) => {
    const overTerminal = (event.target as HTMLElement).closest('.terminal-host')
    if (overTerminal && pane.right_click_passthrough) {
      event.preventDefault()
      return
    }
    onContextMenu?.(event)
  }}>
    {!compact && <button type="button" ref={controlsTriggerRef} className="tool-button pane-controls-toggle" data-terminal-controls-trigger aria-label="分屏工具" aria-expanded={toolbarOpen} data-tooltip={`${pane.label || pane.agent || '终端'} · 分屏工具`} onClick={() => setToolbarOpen(!toolbarOpen)}><MoreHorizontal size={16}/></button>}
    <header ref={toolbarRef} className="terminal-titlebar" hidden={!toolbarOpen} aria-label="终端工具栏">
      <div className="terminal-title"><StatusDot status={pane.agent_status || 'unknown'} /><span data-tooltip={pane.label || pane.agent || pane.terminal_title_stripped || pane.pane_id}>{pane.label || pane.agent || pane.terminal_title_stripped || pane.pane_id}</span><small data-tooltip={pane.cwd}>{pane.cwd}</small></div>
      {headerControls}
      <div className="terminal-tools">
        {status === '可输入' ? <Button className="tool-button" aria-label="聚焦终端输入" data-tooltip={directInput ? '回到光标并打开键盘' : '改为直接输入终端'} onClick={() => { setToolbarOpen(false); if (directInput) { revealCursorRef.current(); termRef.current?.focus() } else onDirectInput?.() }}><span role="img" aria-label={status}><Keyboard size={13}/></span></Button> : <span className="ownership" aria-live="polite">{streamFailed ? '已断开' : status}</span>}
        <Button className="tool-button" aria-label="上传图片" data-tooltip="上传图片，也可直接粘贴或拖入图片" onClick={() => imageInputRef.current?.click()}><ImagePlus size={14}/></Button>
        {!historyActive && <Button className="tool-button" aria-label="查看终端历史" data-tooltip="向上查看终端内容" onClick={() => scrollWheelRef.current(-10)}><History size={14}/></Button>}
        <Button className="tool-button" onClick={(event) => { event.stopPropagation(); setToolbarOpen(false); setSearchOpen((value) => !value) }} aria-label="搜索终端"><Search size={14}/></Button>
        {onContextMenu && <Button className="tool-button pane-menu-button" aria-label="终端操作" onClick={(event) => { event.stopPropagation(); onContextMenu(event) }}><MoreHorizontal size={16}/></Button>}
        <Button className="tool-button" aria-label="收起终端工具" onClick={closeToolbar}><X size={14}/></Button>
      </div>
    </header>
    {!streamFailed && status !== '可输入' && <div className="terminal-pending" role="status">{status}</div>}
    {streamFailed && <div className="terminal-connection-feedback" role="alert" aria-label="终端连接错误"><span>{status}</span><Button onClick={() => setStreamGeneration((value) => value + 1)}>重连终端</Button></div>}
    {historyError && <div className="image-paste-feedback image-paste-error" role="alert"><span>{historyError}</span><button aria-label="关闭历史错误提示" onClick={() => setHistoryError('')}><X size={14}/></button></div>}
    <Input ref={imageInputRef} type="file" accept="image/png,image/jpeg,image/webp,image/gif" multiple hidden aria-label="选择图片" onChange={(event) => { uploadImagesRef.current(Array.from(event.target.files || [])); event.target.value = ''; termRef.current?.focus() }}/>
    {imageFeedback && <div className={`image-paste-feedback ${imageFeedback.failed ? 'image-paste-error' : ''}`} role={imageFeedback.failed ? 'alert' : 'status'} aria-label="图片粘贴提示">
      <span>{imageFeedback.message}</span>
      {imageFeedback.failed && imageFeedback.files && <button onClick={() => uploadImagesRef.current(imageFeedback.files!)}>重试</button>}
      {!imageFeedback.pending && <button aria-label="关闭图片提示" onClick={() => setImageFeedback(null)}><X size={14}/></button>}
    </div>}
    {searchOpen && <Form className="terminal-search" onSubmit={(event) => { event.preventDefault(); const query = new FormData(event.currentTarget).get('query'); if (typeof query === 'string') searchRef.current?.findNext(query) }}><Input name="query" aria-label="搜索内容" autoFocus placeholder="搜索当前缓冲…"/><button type="submit">查找</button><button type="button" aria-label="关闭搜索" onClick={() => setSearchOpen(false)}><X size={14}/></button></Form>}
    {historyActive && <div className="history-navigation" role="toolbar" aria-label="终端历史导航"><Button className="tool-button" onClick={() => returnToLiveRef.current()}>返回实时</Button><Button className="tool-button" onClick={() => scrollHistoryRef.current(-Math.max(3, termRef.current?.rows || 24))}>上一屏</Button><Button className="tool-button" onClick={() => scrollHistoryRef.current(Math.max(3, termRef.current?.rows || 24))}>下一屏</Button></div>}
    <div className="terminal-viewport" ref={viewportRef} tabIndex={0} role="region" aria-label="终端画面，可滚动查看"><div className="terminal-host" ref={hostRef}/></div>
    {confirmationDialog}
  </section>
}
