import { applyScreenFrame, plainScreenLine } from '../ansiScreen'
import type { ScreenError, WorkbenchClient } from '../workbench'

// 同一个 WorkbenchClient 对同一 pane 只保留一个 screen.watch：第二次 watchScreen 会顶掉第一次。
// 这里按 pane 做引用计数，所有控制台订阅者共用一份读屏，最后一个退订才停止。
// 画面视图（ScreenView）直接调用 client.watchScreen，不经过这里：调用方要保证同一时刻
// 同一 pane 不同时挂着 ScreenView 和控制台订阅（Web UI 模式里 Claude pane 只显示对话视图）。

/** 控制台只关心屏幕底部：spinner、模式行和最近的正文。 */
export const CONSOLE_SCREEN_LINES = 60

export type ScreenListener = {
  /** 当前屏幕的纯文本行（已去掉 ANSI）。 */
  onLines: (lines: readonly string[]) => void
  onError: (error: ScreenError) => void
}

type SharedWatch = {
  listeners: Set<ScreenListener>
  /** 已去掉 ANSI 的当前画面；还没收到第一帧时为 null。 */
  lines: string[] | null
  stop: () => void
  offEpoch: () => void
}

const watches = new WeakMap<WorkbenchClient, Map<string, SharedWatch>>()

function start(client: WorkbenchClient, paneID: string, entry: SharedWatch) {
  entry.stop = client.watchScreen(paneID, CONSOLE_SCREEN_LINES, {
    onFrame: (frame) => {
      // 增量帧只转换变化的行：保存纯文本，按同样的 drop / set 规则合并。
      const plain = { ...frame, lines: frame.lines?.map(plainScreenLine), set: frame.set?.map(([index, line]): [number, string] => [index, plainScreenLine(line)]) }
      const lines = applyScreenFrame(entry.lines || [], plain)
      entry.lines = lines
      for (const listener of entry.listeners) listener.onLines(lines)
    },
    onError: (error) => {
      for (const listener of entry.listeners) listener.onError(error)
    },
  })
}

/** 订阅一个 pane 的读屏；返回退订函数。已有读屏时立即回放当前画面。 */
export function watchPaneScreen(client: WorkbenchClient, paneID: string, listener: ScreenListener) {
  let panes = watches.get(client)
  if (!panes) {
    panes = new Map()
    watches.set(client, panes)
  }
  let entry = panes.get(paneID)
  if (!entry) {
    const created: SharedWatch = { listeners: new Set(), lines: null, stop: () => {}, offEpoch: () => {} }
    start(client, paneID, created)
    // 每条新连接（server_info）都要重新登记 watch；旧连接上的 watch 已随连接结束。
    created.offEpoch = client.onEpoch(() => {
      created.stop()
      start(client, paneID, created)
    })
    panes.set(paneID, created)
    entry = created
  }
  const shared = entry
  shared.listeners.add(listener)
  if (shared.lines) listener.onLines(shared.lines)
  return () => {
    if (!shared.listeners.delete(listener) || shared.listeners.size > 0) return
    shared.stop()
    shared.offEpoch()
    if (panes.get(paneID) === shared) panes.delete(paneID)
  }
}
