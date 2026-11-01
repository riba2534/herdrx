import { useCallback, useEffect, useRef } from 'react'
import type { WorkbenchClient } from '../workbench'
import type { ConsoleMode } from './claudeScreen'
import { errorText } from './errorText'

/** 按 Shift+Tab 之后最多等这么久，看模式行有没有变化。 */
export const MODE_CHANGE_TIMEOUT_MS = 3000

/**
 * 控制台对 Claude 的两个按键：中断（Esc）与切换模式（Shift+Tab）。都走 Herdr 的按键接口，
 * 每次只按一下；送达不确定或没看到变化时只报错，不重发——用户看过终端后自己决定。
 */
export function useConsoleControls({ client, paneID, mode }: { client: WorkbenchClient; paneID: string; mode: ConsoleMode | null }) {
  // 切换模式时等着的回调：模式行一变就调用。
  const waiterRef = useRef<(() => void) | null>(null)
  useEffect(() => { waiterRef.current?.() }, [mode])

  const press = useCallback(async (key: string, label: string) => {
    try {
      await client.call('pane.send_keys', { pane_id: paneID, keys: [key] })
    } catch (error) {
      throw new Error(`没有确认 ${label} 已送达（${errorText(error, '没有收到回应')}），不会自动重发。请查看终端后再决定`)
    }
  }, [client, paneID])

  const interrupt = useCallback(() => press('esc', 'Esc'), [press])

  const cycleMode = useCallback(async () => {
    let changed: () => void = () => {}
    // 先登记再按键：按键回应到达之前画面可能已经变了。
    const seen = new Promise<true>((resolve) => { changed = () => resolve(true) })
    waiterRef.current = changed
    let timer = 0
    try {
      await press('shift+tab', 'Shift+Tab')
      const timeout = new Promise<false>((resolve) => { timer = window.setTimeout(() => resolve(false), MODE_CHANGE_TIMEOUT_MS) })
      if (!await Promise.race([seen, timeout])) throw new Error(`已按 Shift+Tab，但 ${MODE_CHANGE_TIMEOUT_MS / 1000} 秒内没看到模式变化，不会再按。请查看终端后再决定`)
    } finally {
      window.clearTimeout(timer)
      if (waiterRef.current === changed) waiterRef.current = null
    }
  }, [press])

  return { interrupt, cycleMode }
}
