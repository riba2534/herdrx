import { useEffect, useState } from 'react'
import type { WorkbenchClient } from '../workbench'
import { parseClaudeScreen, type ConsoleMode, type ConsoleSpinner } from './claudeScreen'
import { watchPaneScreen } from './screenWatch'

/** 读屏得到的实时状态：spinner、模式和「实时画面」用的正文。 */
export type ConsoleLive = {
  spinner: ConsoleSpinner | null
  /** 最近一次在屏幕上读到的模式；对话框挡住模式行时保持上一次的值。 */
  mode: ConsoleMode | null
  preview: string[]
  /** 读屏失败时的中文说明；收到新画面后清除。 */
  error?: string
}

export const EMPTY_CONSOLE_LIVE: ConsoleLive = { spinner: null, mode: null, preview: [] }

const pageVisible = () => typeof document === 'undefined' || document.visibilityState !== 'hidden'

function sameLines(left: readonly string[], right: readonly string[]) {
  return left.length === right.length && left.every((line, index) => line === right[index])
}

// spinner 字形每帧都在转，只比较显示出来的内容。
function sameSpinner(left: ConsoleSpinner | null, right: ConsoleSpinner | null) {
  return left === right || Boolean(left && right && left.verb === right.verb && left.seconds === right.seconds && left.tokens === right.tokens && left.thinking === right.thinking)
}

function nextLive(previous: ConsoleLive, lines: readonly string[]): ConsoleLive {
  const screen = parseClaudeScreen(lines)
  const mode = screen.mode ?? previous.mode
  // 这三样没变时沿用旧对象，状态栏和卡片不重渲染。
  if (!previous.error && previous.mode === mode && sameSpinner(previous.spinner, screen.spinner) && sameLines(previous.preview, screen.preview)) return previous
  return { spinner: screen.spinner, mode, preview: screen.preview }
}

/**
 * 跟随一个 Claude pane 的读屏。`enabled=false` 或页面隐藏时不订阅；同一 pane 的多个调用
 * 共用一个 screen.watch（见 screenWatch）。只读，不向 pane 发送任何输入。
 */
export function useConsoleLive({ hostID, paneID, client, enabled }: { hostID: string; paneID: string; client: WorkbenchClient; enabled: boolean }): ConsoleLive {
  const key = `${hostID}\n${paneID}`
  const [state, setState] = useState<{ key: string; live: ConsoleLive }>({ key, live: EMPTY_CONSOLE_LIVE })
  const [visible, setVisible] = useState(pageVisible)

  useEffect(() => {
    const update = () => setVisible(pageVisible())
    document.addEventListener('visibilitychange', update)
    return () => document.removeEventListener('visibilitychange', update)
  }, [])

  const watching = enabled && visible
  useEffect(() => {
    if (!watching) return
    const update = (change: (live: ConsoleLive) => ConsoleLive) => setState((current) => {
      const live = change(current.key === key ? current.live : EMPTY_CONSOLE_LIVE)
      return current.key === key && current.live === live ? current : { key, live }
    })
    return watchPaneScreen(client, paneID, {
      onLines: (lines) => update((live) => nextLive(live, lines)),
      onError: (error) => update((live) => (live.error === error.message ? live : { ...live, error: error.message })),
    })
  }, [client, key, paneID, watching])

  if (!enabled || state.key !== key) return EMPTY_CONSOLE_LIVE
  return state.live
}
