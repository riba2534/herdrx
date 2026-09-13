import { useEffect, useState } from 'react'
import { Check, ClipboardCopy } from 'lucide-react'
import type { ChatRecord } from '../../lib/structuredChatTypes'
import { CHAT_COPY_OK, CHAT_COPY_UNAVAILABLE, copyChatText, elisionNotice, recordCopyText, recordText, toolInputText } from '../../lib/structuredChatMarkdown'
import { ChatMarkdown } from './ChatMarkdown'
import { ChatToolCallCard, ChatToolResultCard } from './ChatToolCard'

export type ChatToolIndex = ReadonlyMap<string, { name: string; input: unknown }>

function MessageCopy({ text, label }: { text: string; label: string }) {
  const [status, setStatus] = useState('')
  useEffect(() => {
    if (!status) return
    const timer = window.setTimeout(() => setStatus(''), 4000)
    return () => window.clearTimeout(timer)
  }, [status])
  return <>
    <button type="button" className="chat-copy chat-message-copy" aria-label={label} onClick={() => { void copyChatText(text).then((ok) => setStatus(ok ? CHAT_COPY_OK : CHAT_COPY_UNAVAILABLE)) }}>
      {status === CHAT_COPY_OK ? <Check size={12} aria-hidden="true"/> : <ClipboardCopy size={12} aria-hidden="true"/>}复制
    </button>
    {status && <p className="chat-copy-status" role="status">{status}</p>}
  </>
}

function ElisionNote({ text }: { text: string }) {
  const notice = elisionNotice(text)
  if (!notice) return null
  return <p className="chat-elision" role="note">{notice}</p>
}

/**
 * 一条记录的分列渲染：
 * - `user`    → 靠右气泡，纯文本（用户输入原样展示，不当作 Markdown 解析）；
 * - `assistant` → 靠左文档流 Markdown + 工具调用卡片；
 * - `tool`    → 折叠的工具结果分区（真实工具名由同会话的 tool-call 回填）；
 * - `system`  → 弱化的系统提示。
 */
export function ChatMessage({ record, tools }: { record: ChatRecord; tools?: ChatToolIndex }) {
  const copyText = recordCopyText(record)
  if (record.role === 'user') {
    return <article className="chat-message chat-message-user" data-role="user" data-record-id={record.id}>
      <p className="chat-bubble">{recordText(record)}</p>
      <MessageCopy text={copyText} label="复制这条用户消息"/>
    </article>
  }
  if (record.role === 'assistant') {
    return <article className="chat-message chat-message-assistant" data-role="assistant" data-record-id={record.id}>
      {record.blocks.map((block, index) => {
        if (block.type === 'text') return <div className="chat-assistant-text" key={`text-${index}`}><ChatMarkdown text={block.text}/><ElisionNote text={block.text}/></div>
        if (block.type === 'tool-call') return <ChatToolCallCard key={`call-${block.call_id || index}`} name={block.name} callID={block.call_id} input={block.input}/>
        return <ChatToolResultCard key={`result-${block.call_id || index}`} name={tools?.get(block.call_id)?.name || ''} callID={block.call_id} output={block.output} isError={block.is_error}/>
      })}
      <MessageCopy text={copyText} label="复制这条助手消息"/>
    </article>
  }
  if (record.role === 'tool') {
    return <article className="chat-message chat-message-tool" data-role="tool" data-record-id={record.id}>
      {record.blocks.map((block, index) => {
        if (block.type !== 'tool-result') {
          // 派生角色里的非结果块不该出现；出现时也绝不当作助手正文渲染。
          if (block.type === 'text') return <p className="chat-tool-stray" key={`text-${index}`}>{block.text}</p>
          return <ChatToolCallCard key={`call-${block.call_id || index}`} name={block.name} callID={block.call_id} input={toolInputText(block.input)}/>
        }
        return <ChatToolResultCard key={`result-${block.call_id || index}`} name={tools?.get(block.call_id)?.name || ''} callID={block.call_id} output={block.output} isError={block.is_error}/>
      })}
    </article>
  }
  return <article className="chat-message chat-message-system" data-role="system" data-record-id={record.id}>
    <p>{recordText(record)}</p>
  </article>
}
