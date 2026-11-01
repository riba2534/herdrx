import { useEffect, useState } from 'react'
import { Check, ClipboardCopy } from 'lucide-react'
import { CHAT_COPY_OK, CHAT_COPY_UNAVAILABLE, copyChatText } from '../../lib/structuredChatMarkdown'

/** 复制按钮与反馈：剪贴板不可用时给出可执行的说明，而不是静默失败。 */
export function ChatCopyButton({ label, text, className = '' }: { label: string; text: string; className?: string }) {
  const [status, setStatus] = useState('')
  useEffect(() => {
    if (!status) return
    const timer = window.setTimeout(() => setStatus(''), 4000)
    return () => window.clearTimeout(timer)
  }, [status])
  return <>
    <button type="button" className={`chat-copy ${className}`.trim()} aria-label={label} onClick={() => { void copyChatText(text).then((ok) => setStatus(ok ? CHAT_COPY_OK : CHAT_COPY_UNAVAILABLE)) }}>
      {status === CHAT_COPY_OK ? <Check size={13} aria-hidden="true"/> : <ClipboardCopy size={13} aria-hidden="true"/>}复制
    </button>
    {status && <p className="chat-copy-status" role="status">{status}</p>}
  </>
}
