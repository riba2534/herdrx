import { useEffect, useId, useLayoutEffect, useRef, useState, type KeyboardEvent as ReactKeyboardEvent, type ClipboardEvent as ReactClipboardEvent } from 'react'
import { ChevronUp, Keyboard, SquareTerminal } from 'lucide-react'
import { Button } from './ui'
import { clipboardImages } from '../lib/imagePaste'
import { composerInFlight, composerStatusText, idleComposerSend, readComposerDraft, readComposerSend, runComposerSend, subscribeComposer, writeComposerDraft, type ComposerSendState } from '../lib/composerDrafts'
import './Composer.css'

function composing(event: ReactKeyboardEvent<HTMLTextAreaElement>) {
  return event.nativeEvent.isComposing || event.key === 'Process' || event.nativeEvent.keyCode === 229
}

const idleSend: ComposerSendState = { status: 'idle', error: '', revision: 0 }

function resizeCompactInput(textarea: HTMLTextAreaElement) {
  const short = Boolean(textarea.closest('.workbench-short'))
  const min = short ? 36 : 44
  const max = short ? 36 : 104
  textarea.style.height = `${min}px`
  const contentHeight = textarea.scrollHeight + 2
  textarea.style.height = `${Math.max(min, Math.min(max, contentHeight))}px`
  textarea.style.overflowY = contentHeight > max ? 'auto' : 'hidden'
}

function InputModeMenu({ directInput, onDirectInput, onLocalInput }: {
  directInput: boolean
  onDirectInput: () => void
  onLocalInput: () => void
}) {
  const [open, setOpen] = useState(false)
  const menuID = useId()
  const containerRef = useRef<HTMLDivElement>(null)
  const triggerRef = useRef<HTMLButtonElement>(null)
  const menuRef = useRef<HTMLDivElement>(null)

  useLayoutEffect(() => {
    if (open) menuRef.current?.querySelector<HTMLButtonElement>('[aria-checked="true"]')?.focus()
  }, [open])

  useEffect(() => {
    if (!open) return
    const closeOutside = (event: PointerEvent) => {
      if (!containerRef.current?.contains(event.target as Node)) setOpen(false)
    }
    document.addEventListener('pointerdown', closeOutside)
    return () => document.removeEventListener('pointerdown', closeOutside)
  }, [open])

  const menuKeyDown = (event: ReactKeyboardEvent<HTMLDivElement>) => {
    if (event.key === 'Escape') {
      event.preventDefault()
      event.stopPropagation()
      setOpen(false)
      triggerRef.current?.focus()
      return
    }
    if (!['ArrowDown', 'ArrowUp', 'Home', 'End'].includes(event.key)) return
    event.preventDefault()
    event.stopPropagation()
    const buttons = [...(menuRef.current?.querySelectorAll<HTMLButtonElement>('button') || [])]
    const current = buttons.indexOf(document.activeElement as HTMLButtonElement)
    const next = event.key === 'Home' ? 0 : event.key === 'End' ? buttons.length - 1 : (current + (event.key === 'ArrowDown' ? 1 : -1) + buttons.length) % buttons.length
    buttons[next]?.focus()
  }

  return <div className="composer-mode-picker" ref={containerRef} onBlur={(event) => {
    if (!event.currentTarget.contains(event.relatedTarget as Node | null)) setOpen(false)
  }}>
    <button
      ref={triggerRef}
      type="button"
      className="composer-mode-trigger"
      aria-label={`输入方式：${directInput ? '直接输入终端' : '本地输入'}`}
      aria-haspopup="menu"
      aria-expanded={open}
      aria-controls={open ? menuID : undefined}
      onClick={() => setOpen(!open)}
      onKeyDown={(event) => {
        if (event.key === 'ArrowDown' || event.key === 'ArrowUp') { event.preventDefault(); setOpen(true) }
      }}
    >
      {directInput ? <SquareTerminal size={18} aria-hidden="true"/> : <Keyboard size={18} aria-hidden="true"/>}
      <ChevronUp size={10} aria-hidden="true"/>
    </button>
    {open && <div id={menuID} ref={menuRef} className="composer-mode-menu" role="menu" aria-label="输入方式" data-ui-overlay="composer-mode" onKeyDown={menuKeyDown}>
      <button type="button" role="menuitemradio" aria-checked={!directInput} onClick={() => { setOpen(false); onLocalInput() }}>
        <Keyboard size={18} aria-hidden="true"/><span>本地输入<small>编辑后整段发送</small></span>
      </button>
      <button type="button" role="menuitemradio" aria-checked={directInput} onClick={() => { setOpen(false); onDirectInput() }}>
        <SquareTerminal size={18} aria-hidden="true"/><span>直接输入终端<small>键入即时进入终端</small></span>
      </button>
    </div>}
  </div>
}

export function Composer({ hostID, paneID, visible, directInput, compact = false, sendDisabled, onDirectInput, onLocalInput, submit, onPasteImages }: {
  hostID: string
  paneID: string
  visible: boolean
  directInput: boolean
  compact?: boolean
  sendDisabled?: boolean
  onDirectInput: () => void
  onLocalInput: () => void
  submit: (paneID: string, text: string) => Promise<void>
  onPasteImages?: (files: File[]) => void
}) {
  const [value, setValue] = useState(() => readComposerDraft(hostID, paneID))
  const [send, setSend] = useState(() => readComposerSend(hostID, paneID))
  const textareaRef = useRef<HTMLTextAreaElement>(null)
  const hostRef = useRef(hostID)
  const paneRef = useRef(paneID)
  hostRef.current = hostID
  paneRef.current = paneID

  useEffect(() => {
    const sync = () => {
      setValue(readComposerDraft(hostRef.current, paneRef.current))
      setSend(readComposerSend(hostRef.current, paneRef.current))
    }
    sync()
    return subscribeComposer(sync)
  }, [hostID, paneID])

  useEffect(() => {
    if (send.status !== 'delivered') return
    const timer = window.setTimeout(() => idleComposerSend(hostID, paneID), 3500)
    return () => window.clearTimeout(timer)
  }, [send.status, send.revision, hostID, paneID])

  useLayoutEffect(() => {
    const textarea = textareaRef.current
    if (!textarea) return
    if (compact) resizeCompactInput(textarea)
    else { textarea.style.removeProperty('height'); textarea.style.removeProperty('overflow-y') }
  }, [compact, value, visible])

  useEffect(() => {
    const textarea = textareaRef.current
    if (!compact || !textarea || typeof ResizeObserver === 'undefined') return
    let lastWidth = textarea.clientWidth
    const observer = new ResizeObserver(() => {
      if (textarea.clientWidth === lastWidth) return
      lastWidth = textarea.clientWidth
      resizeCompactInput(textarea)
    })
    observer.observe(textarea)
    return () => observer.disconnect()
  }, [compact, visible])

  const change = (text: string) => {
    setValue(text)
    writeComposerDraft(hostID, paneID, text)
  }

  const sendNow = () => {
    if (sendDisabled || !paneID || composerInFlight(hostID, paneID) || !value.trim()) return
    void runComposerSend(hostID, paneID, submit)
  }

  const onKeyDown = (event: ReactKeyboardEvent<HTMLTextAreaElement>) => {
    if (composing(event)) return
    if ((event.ctrlKey || event.metaKey) && event.key === 'Enter') {
      event.preventDefault()
      sendNow()
    }
  }

  const onPaste = (event: ReactClipboardEvent<HTMLTextAreaElement>) => {
    const files = clipboardImages(event.clipboardData)
    if (!files.length) return
    event.preventDefault()
    onPasteImages?.(files)
  }

  if (!visible) return null
  const current = send.status ? send : idleSend
  const sendingHere = current.status === 'sending' || composerInFlight(hostID, paneID)
  const empty = !value.trim()
  const canEdit = Boolean(paneID)
  const selectLocalInput = () => { onLocalInput(); textareaRef.current?.focus() }
  return <div className={`composer${compact ? ' composer-compact' : ''}`} role="region" aria-label="本地输入">
    {compact && <InputModeMenu key={`${hostID}:${paneID}`} directInput={directInput} onDirectInput={onDirectInput} onLocalInput={selectLocalInput}/>}
    <textarea
      ref={textareaRef}
      className="input composer-input"
      aria-label="本地输入内容"
      rows={compact ? 1 : 2}
      value={value}
      placeholder={canEdit ? compact ? '本地输入，可换行' : '在本地编辑，发送后整段进入当前终端' : '请先选择一个终端再编辑'}
      autoComplete="off"
      autoCorrect="off"
      autoCapitalize="off"
      spellCheck={false}
      enterKeyHint="enter"
      disabled={!canEdit}
      onChange={(event) => change(event.target.value)}
      onFocus={onLocalInput}
      onKeyDown={onKeyDown}
      onPaste={onPaste}
    />
    <div className="composer-side">
      <Button className="button-primary composer-send" disabled={sendDisabled || empty || sendingHere || !paneID} pending={sendingHere} onClick={sendNow}>发送</Button>
    </div>
    {(!compact || current.status !== 'idle') && <div className="composer-meta">
      {!compact && <div className="composer-modes">
        <button type="button" className={directInput ? '' : 'composer-mode-active'} aria-pressed={!directInput} onClick={selectLocalInput}>本地输入</button>
        <button type="button" className={directInput ? 'composer-mode-active' : ''} aria-pressed={directInput} onClick={onDirectInput}>直接输入终端</button>
      </div>}
      <span className={`composer-status composer-status-${current.status}`} role="status" aria-live="polite">
        {compact ? composerStatusText(current, current.status === 'delivered') : <>
          <span className="composer-status-full">{composerStatusText(current)}</span>
          <span className="composer-status-compact">{composerStatusText(current, true)}</span>
        </>}
      </span>
    </div>}
  </div>
}
