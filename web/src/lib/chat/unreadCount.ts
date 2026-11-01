// Adapted from Cindy (https://github.com/makecindy/cindy, Apache-2.0) apps/desktop/src/renderer/components/chat/unreadCount.ts@19dad9c225
//
// 「↓ N 条新消息」的计数。返回本次新增的未读键，调用方按键去重累加，未读数 = 键的个数：
//   - 按用户看到的「一条消息」计数：同一轮（两条提问之间）的助手输出共用一个键，执行中的
//     中间正文、工具调用最终都并进同一段回复，逐条计数会让一次回复报出几十条新消息；
//     需要作答的交互（AskUserQuestion / ExitPlanMode）各自单独计数。
//   - 只数追加到尾部的记录（由 ArrivalTracker 判定）：「加载更早的记录」与会话重建都不算。
//   - 视口在底部时不累计；用户提问本身不计数（它就是一轮的开头）。

import type { ChatRecord } from '../structuredChatTypes'
import { INTERACTION_TOOLS } from './toolDescriptor'

const UNKNOWN_TURN_KEY = 'turn:'

function isTurnBoundary(record: ChatRecord) {
  return record.role === 'user' && !record.queued
}

function isInteraction(record: ChatRecord) {
  return record.blocks.some((block) => block.type === 'tool-call' && INTERACTION_TOOLS.has(block.name))
}

export function collectUnreadKeys({ messages, appended, nearBottom }: {
  messages: readonly ChatRecord[]
  /** 这次新追加到尾部的记录 id。 */
  appended: ReadonlySet<string>
  /** 视口贴底（自动跟随接管）时不累计。 */
  nearBottom: boolean
}): string[] {
  if (nearBottom || !appended.size) return []
  const first = messages.findIndex((record) => appended.has(record.id))
  if (first < 0) return []
  let turnKey = UNKNOWN_TURN_KEY
  for (let index = first - 1; index >= 0; index--) {
    if (isTurnBoundary(messages[index])) { turnKey = `turn:${messages[index].id}`; break }
  }
  const keys = new Set<string>()
  for (let index = first; index < messages.length; index++) {
    const record = messages[index]
    if (isTurnBoundary(record)) turnKey = `turn:${record.id}`
    if (!appended.has(record.id) || record.role !== 'assistant') continue
    keys.add(isInteraction(record) ? record.id : turnKey)
  }
  return [...keys]
}
