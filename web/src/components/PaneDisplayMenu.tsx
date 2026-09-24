import { useEffect, useRef } from 'react'
import { MessageSquare } from 'lucide-react'
import type { TerminalDisplay } from '../lib/displayPreferences'
import type { TerminalControlState } from '../lib/workbench'
import { Button } from './ui'

type Grid = { cols: number; rows: number }
export type PaneDisplayTone = 'plain' | 'warn' | 'owned' | 'other'

// Fewer columns than this and agent tables, diffs and status lines wrap.
export const NARROW_TASK_COLUMNS = 50
const MIN_FONT = 10
const MAX_FONT = 28

const gridText = (grid: Grid | null) => grid ? `${grid.cols}×${grid.rows}` : '—'
const pixels = (value: number) => `${value.toFixed(1).replace(/\.0$/, '')} px`
const sameGrid = (a: Grid | null, b: Grid | null) => Boolean(a && b && a.cols === b.cols && a.rows === b.rows)
const modes: Array<{ mode: TerminalDisplay['mode']; label: string }> = [
  { mode: 'auto', label: '自动' },
  { mode: 'fit', label: '完整显示' },
  { mode: 'fixed', label: '固定字号' },
]

export type PaneDisplaySizing = {
  supported: boolean
  state: TerminalControlState['state']
  reason?: string
  grid: Grid | null
  /** The grid this window would request at its current font. */
  capacity: Grid | null
  /** The remote grid before this window took control. */
  previous: Grid | null
  undersized: boolean
  disabled: boolean
  onResizeOnce: () => void
  onTakeControl: () => void
  onRelease: () => void
  onRestoreAndRelease: () => void
}

/** A pane-local chip that states the remote grid and opens the reading and sizing tools. */
export function PaneDisplayMenu({ compact, label, tone, open, onOpenChange, display, actualFontSize, fitFontSize, onDisplayChange, sizing, onChatView }: {
  compact: boolean
  label: string
  tone: PaneDisplayTone
  open: boolean
  onOpenChange: (open: boolean) => void
  display: TerminalDisplay
  actualFontSize: number | null
  fitFontSize: number | null
  onDisplayChange?: (patch: Partial<TerminalDisplay>) => void
  sizing: PaneDisplaySizing
  onChatView?: () => void
}) {
  const rootRef = useRef<HTMLDivElement>(null)
  const chipRef = useRef<HTMLButtonElement>(null)
  useEffect(() => {
    if (!open) return
    const outside = (event: PointerEvent) => {
      if (!rootRef.current?.contains(event.target as Node)) onOpenChange(false)
    }
    const escape = (event: KeyboardEvent) => {
      if (event.key !== 'Escape') return
      event.preventDefault()
      event.stopPropagation()
      onOpenChange(false)
      chipRef.current?.focus()
    }
    document.addEventListener('pointerdown', outside, true)
    window.addEventListener('keydown', escape, true)
    return () => { document.removeEventListener('pointerdown', outside, true); window.removeEventListener('keydown', escape, true) }
  }, [open, onOpenChange])
  const act = (run: () => void) => () => { onOpenChange(false); run() }
  const currentFont = actualFontSize ?? display.fontSize * display.zoom / 100
  const step = (delta: number) => {
    const fontSize = Math.min(MAX_FONT, Math.max(MIN_FONT, Math.round(currentFont) + delta))
    onDisplayChange?.({ mode: 'fixed', fontSize, zoom: 100 })
  }
  const narrow = sizing.capacity !== null && sizing.capacity.cols < NARROW_TASK_COLUMNS
  const canRestore = sizing.previous !== null && !sameGrid(sizing.previous, sizing.grid)
  return <div className="pane-display" ref={rootRef} onPointerDown={(event) => event.stopPropagation()}>
    <button type="button" ref={chipRef} className={`pane-display-chip pane-display-chip-${tone}`} aria-haspopup="dialog" aria-expanded={open} aria-label={`显示与尺寸：${label}`} data-tooltip="本地显示与任务尺寸" onClick={() => onOpenChange(!open)}><span>{label}</span></button>
    {open && <div className="pane-display-menu" role="dialog" aria-label="显示与尺寸">
      {onChatView && <Button className="pane-display-chat" onClick={act(onChatView)}><MessageSquare size={15}/><span><strong>切换到对话视图</strong><small>按手机宽度重排 Agent 的问答，远端尺寸不变</small></span></Button>}
      <section className="pane-display-section" aria-label="本地显示">
        <header><strong>本地显示</strong><small>只影响此{compact ? '设备' : '窗口'}，不改变远端</small></header>
        <div className="pane-display-modes" role="group" aria-label="显示方式">
          {modes.map((item) => <Button key={item.mode} className="tool-button" aria-pressed={display.mode === item.mode} title={item.mode === 'fit' && fitFontSize !== null ? `完整显示当前 ${gridText(sizing.grid)}，字号约 ${pixels(fitFontSize)}` : undefined} onClick={() => onDisplayChange?.({ mode: item.mode, zoom: 100 })}>{item.label}</Button>)}
        </div>
        <div className="pane-display-font" role="group" aria-label="终端字号">
          <Button className="tool-button" aria-label="缩小字号" disabled={Math.round(currentFont) <= MIN_FONT} onClick={() => step(-1)}>A−</Button>
          <output aria-live="polite">{pixels(currentFont)}</output>
          <Button className="tool-button" aria-label="放大字号" disabled={Math.round(currentFont) >= MAX_FONT} onClick={() => step(1)}>A+</Button>
        </div>
        <small className="field-hint">{display.mode === 'auto' ? '自动：能读清时完整显示或铺满宽度，否则保持字号并滑动查看。' : display.mode === 'fit' ? `完整显示当前 ${gridText(sizing.grid)}${fitFontSize === null ? '' : `，字号约 ${pixels(fitFontSize)}`}。` : '固定字号：超出部分滑动查看。'}{compact ? ' 在终端上双指捏合也可调整字号。' : ''}</small>
      </section>
      {sizing.supported && <section className="pane-display-section" aria-label="任务尺寸">
        <header><strong>任务尺寸</strong><small>远端当前 {gridText(sizing.grid)}</small></header>
        {sizing.state === 'owned' ? <>
          {canRestore && <Button className="button-primary" onClick={act(sizing.onRestoreAndRelease)}>恢复为 {gridText(sizing.previous)} 并释放</Button>}
          <Button className={canRestore ? 'button-secondary' : 'button-primary'} onClick={act(sizing.onRelease)}>释放控制，保持 {gridText(sizing.grid)}</Button>
          <small className="field-hint">此窗口正在控制尺寸，旋转屏幕或调整字号会重新排版；弹出键盘不会改变远端行数。</small>
        </> : sizing.state === 'other' ? <>
          <Button className="button-primary" disabled={sizing.disabled} onClick={act(sizing.onTakeControl)}>转到此窗口控制{sizing.capacity ? `（${gridText(sizing.capacity)}）` : ''}</Button>
          <small className="field-hint">本站其他窗口正在控制此终端的尺寸。</small>
        </> : sizing.state === 'pending' ? <small className="field-hint" role="status">正在申请尺寸控制…</small>
          : sizing.state === 'blocked' ? <small className="field-error" role="alert">{sizing.reason || '尺寸控制暂不可用'}</small>
            : <>
              {sizing.undersized && <Button className="button-primary" disabled={sizing.disabled} onClick={act(sizing.onResizeOnce)}>按此窗口尺寸恢复{sizing.capacity ? `（${gridText(sizing.capacity)}）` : ''}</Button>}
              <Button className={sizing.undersized ? 'button-secondary' : 'button-primary'} disabled={sizing.disabled} onClick={act(sizing.onTakeControl)}>使用此窗口尺寸{sizing.capacity ? `（${gridText(sizing.capacity)}）` : ''}</Button>
              <small className="field-hint">{sizing.undersized ? '远端比此窗口小，可能是其他设备释放控制后留下的尺寸。恢复只调整一次，随后继续观察。' : `远端会从 ${gridText(sizing.grid)} 改为 ${gridText(sizing.capacity)} 并重新排版，同一终端的其他窗口也会看到变化。`}</small>
              {narrow && !sizing.undersized && <small className="field-error">只有 {sizing.capacity!.cols} 列，Claude Code、Codex 的表格和 diff 可能折行；可以横屏或减小字号后再使用。</small>}
            </>}
        {sizing.reason && sizing.state !== 'blocked' && <small className="field-hint">{sizing.reason}</small>}
      </section>}
    </div>}
  </div>
}
