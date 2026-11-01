import { useCallback, useEffect, useRef } from 'react'
import type { WorkbenchClient } from '../workbench'
import type { ConsoleMode } from './claudeScreen'

/** 按 Shift+Tab 之后最多等这么久，看模式行有没有变化。 */
export const MODE_CHANGE_TIMEOUT_MS = 3000

const reason = (error: unknown) => (error instanceof Error && error.message ? error.message : '没有收到回应')

/**
 * 控制台对 Claude 的两个按键：中断（Esc）与切换模式（Shift+Tab）。都走 Herdr 的按键接口，
 * 每次只按一下；送达不确定或没看到变化时只报错，不重发——用户看过终端后自己决定。
 */
export function useConsoleControls({ client, paneID, mode }: { client: WorkbenchClient; paneID: string; mode: ConsoleMode | null }) {
  const modeRef = useRef(mode)
  modeRef.current = mode
  const waiterRef = useRef<{ from: ConsoleMode | null; done: () => void } | null>(null)

  useEffect(() => {
    const waiter = waiterRef.current
    if (waiter && mode !== waiter.from) waiter.done()
  }, [mode])

  const press = useCallback(async (key: string, label: string) => {
    try {
      await client.call('pane.send_keys', { pane_id: paneID, keys: [key] })
    } catch (error) {
      throw new Error(`没有确认 ${label} 已送达（${reason(error)}），不会自动重发。请查看终端后再决定`)
    }
  }, [client, paneID])

  const interrupt = useCallback(() => press('esc', 'Esc'), [press])

  const cycleMode = useCallback(async () => {
    const from = modeRef.current
    await press('shift+tab', 'Shift+Tab')
    const changed = await new Promise<boolean>((resolve) => {
      const timer = window.setTimeout(() => finish(false), MODE_CHANGE_TIMEOUT_MS)
      function finish(value: boolean) {
        window.clearTimeout(timer)
        if (waiterRef.current === waiter) waiterRef.current = null
        resolve(value)
      }
      const waiter = { from, done: () => finish(true) }
      waiterRef.current = waiter
      // 按键回应到达之前画面可能已经变了。
      if (modeRef.current !== from) finish(true)
    })
    if (!changed) throw new Error('已按 Shift+Tab，但 3 秒内没看到模式变化，不会再按。请查看终端后再决定')
  }, [press])

  return { interrupt, cycleMode }
}
