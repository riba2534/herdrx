import { useEffect, useRef, useState, type KeyboardEvent as ReactKeyboardEvent, type ClipboardEvent as ReactClipboardEvent } from 'react'
import { Button } from './ui'
import { clipboardImages } from '../lib/imagePaste'
import { composerInFlight, composerStatusText, idleComposerSend, readComposerDraft, readComposerSend, runComposerSend, subscribeComposer, writeComposerDraft, type ComposerSendState } from '../lib/composerDrafts'

function composing(event: ReactKeyboardEvent<HTMLTextAreaElement>) {
  return event.nativeEvent.isComposing || event.key === 'Process' || event.nativeEvent.keyCode === 229
}

const idleSend: ComposerSendState = { status: 'idle', error: '', revision: 0 }

export function Composer({ hostID, paneID, visible, directInput, sendDisabled, onDirectInput, onLocalInput, submit, onPasteImages }: {
  hostID: string
  paneID: string
  visible: boolean
  directInput: boolean
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
  return <div className="composer" role="region" aria-label="本地输入">
    <textarea
      ref={textareaRef}
      className="input composer-input"
      aria-label="本地输入内容"
      rows={2}
      value={value}
      placeholder={canEdit ? '在本地编辑，发送后整段进入当前终端' : '请先选择一个终端再编辑'}
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
    <div className="composer-meta">
      <div className="composer-modes">
        <button type="button" className={directInput ? '' : 'composer-mode-active'} aria-pressed={!directInput} onClick={() => { onLocalInput(); textareaRef.current?.focus() }}>本地输入</button>
        <button type="button" className={directInput ? 'composer-mode-active' : ''} aria-pressed={directInput} onClick={onDirectInput}>直接输入终端</button>
      </div>
      <span className={`composer-status composer-status-${current.status}`} role="status" aria-live="polite">
        <span className="composer-status-full">{composerStatusText(current)}</span>
        <span className="composer-status-compact">{composerStatusText(current, true)}</span>
      </span>
    </div>
  </div>
}
