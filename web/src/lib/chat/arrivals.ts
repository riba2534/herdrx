// 哪些记录是「页面加载之后新追加到尾部」的：助手正文据此播放淡入，「N 条新消息」据此计数。
// 首屏、加载更早的记录、会话重建（轮转 / 截断）得到的都是历史，两者都不算。
// 同一组输入重复观察（StrictMode 双调用、无关的重新渲染）直接返回，不改变结果。

import type { ChatLoadStatus, ChatRecord } from '../structuredChatTypes'

const NONE: ReadonlySet<string> = new Set()

export class ArrivalTracker {
  private session: string | undefined
  private status: ChatLoadStatus | undefined
  private messages: readonly ChatRecord[] | undefined
  private seen = new Set<string>()
  private ready = false
  private arrived = new Map<string, number>()
  private appended: ReadonlySet<string> = NONE

  observe(session: string | undefined, status: ChatLoadStatus, messages: readonly ChatRecord[], now: number) {
    if (session === this.session && status === this.status && messages === this.messages) return
    if (session !== this.session) {
      this.seen = new Set()
      this.ready = false
      this.arrived = new Map()
    }
    this.session = session
    this.status = status
    this.messages = messages
    this.appended = NONE
    if (!session || (status !== 'ready' && status !== 'empty')) return
    if (!this.ready) {
      // 绑定后的第一页：全部是历史。
      this.ready = true
      for (const record of messages) this.seen.add(record.id)
      return
    }
    let lastSeen = -1
    for (let index = messages.length - 1; index >= 0; index--) {
      if (this.seen.has(messages[index].id)) { lastSeen = index; break }
    }
    // 一条已见记录都没有：会话被整体重建过，不当作新消息；但之前一条都没有时是新会话的开头。
    const rebuilt = lastSeen < 0 && this.seen.size > 0
    const appended = new Set<string>()
    messages.forEach((record, index) => {
      if (this.seen.has(record.id)) return
      this.seen.add(record.id)
      if (rebuilt || index < lastSeen) return
      appended.add(record.id)
      if (record.role === 'assistant') this.arrived.set(record.id, now)
    })
    this.appended = appended
  }

  /** 新到达记录的到达时刻（`performance.now()`）；历史记录返回 undefined。 */
  arrivedAt = (recordID: string) => this.arrived.get(recordID)

  /** 最近一次观察到的这份记录里，新追加到尾部的记录 id。 */
  appendedIn(messages: readonly ChatRecord[]) {
    return messages === this.messages ? this.appended : NONE
  }
}
