// Adapted from Cindy (https://github.com/makecindy/cindy, Apache-2.0) apps/desktop/src/renderer/components/chat/JumpToBottomChip.tsx@19dad9c225
// Adapted from Cindy (https://github.com/makecindy/cindy, Apache-2.0) apps/desktop/src/renderer/components/chat/NewMessageIndicator.tsx@19dad9c225
//
// 记录区底部居中的胶囊：离开底部时出现。有新消息时显示「↓ N 条新消息」，没有时显示「跳到底部」，
// 两者同一位置、同款外形、互斥出现。

import { ArrowDown } from 'lucide-react'

export function ChatChips({ visible, unread, onJump }: { visible: boolean; unread: number; onJump: () => void }) {
  if (!visible) return null
  return <div className="chat-chips" aria-live="polite" aria-atomic="true">
    <button type="button" className="chat-chip" data-kind={unread > 0 ? 'unread' : 'jump'} aria-label={unread > 0 ? `回到底部，查看 ${unread} 条新消息` : undefined} onClick={onJump}>
      <ArrowDown size={14} aria-hidden="true"/>{unread > 0 ? `${unread} 条新消息` : '跳到底部'}
    </button>
  </div>
}
