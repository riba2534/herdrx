import { useEffect, useState } from 'react'
import { Check, ClipboardCopy, TriangleAlert, Wrench } from 'lucide-react'
import { CHAT_COPY_OK, CHAT_COPY_UNAVAILABLE, copyChatText, toolInputText, toolOutputText } from '../../lib/structuredChatMarkdown'

// 工具调用与结果一律是可折叠的独立分区，显示**真实的工具名与 call id**，
// 折叠态只给摘要，展开后是不可执行的只读预览。工具结果永远不进入用户气泡。

function useCopyStatus() {
  const [status, setStatus] = useState('')
  useEffect(() => {
    if (!status) return
    const timer = window.setTimeout(() => setStatus(''), 4000)
    return () => window.clearTimeout(timer)
  }, [status])
  return {
    status,
    copy: async (text: string) => { setStatus((await copyChatText(text)) ? CHAT_COPY_OK : CHAT_COPY_UNAVAILABLE) },
  }
}

function CopyButton({ label, text }: { label: string; text: string }) {
  const { status, copy } = useCopyStatus()
  return <>
    <button type="button" className="chat-copy" aria-label={label} onClick={() => void copy(text)}>
      {status === CHAT_COPY_OK ? <Check size={12} aria-hidden="true"/> : <ClipboardCopy size={12} aria-hidden="true"/>}复制
    </button>
    {status && <p className="chat-copy-status" role="status">{status}</p>}
  </>
}

/** Agent 发起的一次工具调用。 */
export function ChatToolCallCard({ name, callID, input }: { name: string; callID: string; input: unknown }) {
  const body = toolInputText(input)
  return <details className="chat-tool chat-tool-call">
    <summary>
      <Wrench size={12} aria-hidden="true"/><span className="chat-tool-name">{name}</span>
      <span className="chat-tool-kind">工具调用</span>
      {callID && <code className="chat-tool-id">{callID}</code>}
    </summary>
    <div className="chat-tool-body">
      <pre className="chat-tool-pre" tabIndex={0}>{body}</pre>
      <CopyButton label={`复制工具入参（${name}）`} text={body}/>
    </div>
  </details>
}

/** 工具执行结果。`name` 由同一会话里对应的 tool-call 回填，取不到时只显示 call id。 */
export function ChatToolResultCard({ name, callID, output, isError }: { name: string; callID: string; output: string; isError: boolean }) {
  const body = toolOutputText(output)
  return <details className={`chat-tool chat-tool-result${isError ? ' chat-tool-error' : ''}`}>
    <summary>
      {isError ? <TriangleAlert size={12} aria-hidden="true"/> : <Wrench size={12} aria-hidden="true"/>}
      <span className="chat-tool-name">{name || '工具结果'}</span>
      <span className="chat-tool-kind">{isError ? '工具出错' : '工具结果'}</span>
      {callID && <code className="chat-tool-id">{callID}</code>}
    </summary>
    <div className="chat-tool-body">
      <pre className="chat-tool-pre" tabIndex={0}>{body}</pre>
      <CopyButton label={`复制工具结果（${name || callID || '未知工具'}）`} text={body}/>
    </div>
  </details>
}
