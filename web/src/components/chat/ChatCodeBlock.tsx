import { useEffect, useState } from 'react'
import { Check, ClipboardCopy } from 'lucide-react'
import { CHAT_COPY_OK, CHAT_COPY_UNAVAILABLE, copyChatText } from '../../lib/structuredChatMarkdown'

/** 代码块：等宽、可横向滚动、整块复制；剪贴板不可用时给出可执行反馈而不是静默失败。 */
export function ChatCodeBlock({ language, text }: { language: string; text: string }) {
  const [status, setStatus] = useState('')

  useEffect(() => {
    if (!status) return
    const timer = window.setTimeout(() => setStatus(''), 4000)
    return () => window.clearTimeout(timer)
  }, [status])

  const copy = async () => {
    setStatus((await copyChatText(text)) ? CHAT_COPY_OK : CHAT_COPY_UNAVAILABLE)
  }

  return <div className="chat-code">
    <div className="chat-code-head">
      <span className="chat-code-lang">{language || '文本'}</span>
      <button type="button" className="chat-copy" aria-label="复制代码" onClick={() => void copy()}>
        {status === CHAT_COPY_OK ? <Check size={12} aria-hidden="true"/> : <ClipboardCopy size={12} aria-hidden="true"/>}复制
      </button>
    </div>
    <pre className="chat-code-pre" tabIndex={0}><code>{text}</code></pre>
    {status && <p className="chat-copy-status" role="status">{status}</p>}
  </div>
}
