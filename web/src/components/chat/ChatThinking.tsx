// Adapted from Cindy (https://github.com/makecindy/cindy, Apache-2.0) apps/desktop/src/renderer/components/chat/ThinkingCard.tsx@19dad9c225
// Adapted from Cindy (https://github.com/makecindy/cindy, Apache-2.0) apps/desktop/src/renderer/components/chat/ThinkingText.tsx@19dad9c225
//
// 思考过程：默认收起成一行「思考了 3 秒 · 开头几个字」，点开看全文。正文只认成对的 `**加粗**`
// 和 `行内代码`，其余（标题、列表、链接、HTML）一律按原文显示，不走完整 Markdown。

import { Fragment, useState } from 'react'
import { ChevronRight, Sparkles } from 'lucide-react'
import { formatChatDuration } from '../../lib/structuredChat'
import type { ThinkingEntry } from '../../lib/chat/workRuns'

function ThinkingText({ text }: { text: string }) {
  return <>{text.split(/(\*\*[^*\n]+\*\*|`[^`\n]+`)/).map((part, index) => {
    if (/^\*\*[^*\n]+\*\*$/.test(part)) return <strong key={index}>{part.slice(2, -2)}</strong>
    if (/^`[^`\n]+`$/.test(part)) return <code key={index}>{part.slice(1, -1)}</code>
    return <Fragment key={index}>{part}</Fragment>
  })}</>
}

/** 「思考了 3 秒」：不足一秒按一秒算；没有耗时就只写「思考」。 */
function thinkingLabel(durationMs?: number) {
  return durationMs === undefined ? '思考' : `思考了 ${formatChatDuration(Math.max(1000, durationMs))}`
}

export function ChatThinking({ entry }: { entry: ThinkingEntry }) {
  const [expanded, setExpanded] = useState(false)
  const text = entry.text.trim()
  // 收起时只显示一行，没必要把整段思考塞进 DOM。
  const preview = text.slice(0, 200).replace(/\s+/g, ' ')
  const label = thinkingLabel(entry.durationMs)
  if (!text) {
    return <div className="chat-thinking" data-empty="true">
      <span className="chat-row-icon" aria-hidden="true"><Sparkles size={13}/></span>
      <span className="chat-thinking-label">{label}</span>
    </div>
  }
  return <div className="chat-thinking" data-expanded={expanded || undefined}>
    <button type="button" className="chat-thinking-head" aria-expanded={expanded} onClick={() => setExpanded((value) => !value)}>
      <span className="chat-row-icon" aria-hidden="true"><Sparkles size={13}/></span>
      <span className="chat-thinking-label">{label}</span>
      {!expanded && <span className="chat-thinking-preview"><ThinkingText text={preview}/></span>}
      <span className="chat-row-chevron" aria-hidden="true"><ChevronRight size={13}/></span>
    </button>
    {expanded && <p className="chat-thinking-body"><ThinkingText text={text}/></p>}
  </div>
}
