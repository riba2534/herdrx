// Adapted from Cindy (https://github.com/makecindy/cindy, Apache-2.0) apps/desktop/src/renderer/features/cc-agent/WorkingStatusText.tsx@19dad9c225
import { useLayoutEffect, useRef, useState } from 'react'
import { useReducedMotion } from '../../lib/console/useReducedMotion'

/** 每段文案至少停留这么久才换下一段，与主题动效时长无关。 */
export const WORKING_STATUS_MIN_INTERVAL_MS = 1000
/** 换字时的淡出时长，与 `--console-motion-fast` 一致。 */
export const WORKING_STATUS_FADE_MS = 150

/**
 * 运行状态文案：新文案到达时，旧文案至少显示满 1 秒，再淡出、换字、淡入；中途又来的文案只保留
 * 最新一段。减弱动效时直接换字。只在运行中挂载，卸载即取消所有待换的文案。
 */
export function WorkingStatusText({ text }: { text: string }) {
  const reducedMotion = useReducedMotion()
  const [displayed, setDisplayed] = useState(text)
  const [fading, setFading] = useState(false)
  const state = useRef({
    displayed: text,
    latest: text,
    changedAt: performance.now(),
    fading: false,
    reducedMotion,
    timer: undefined as ReturnType<typeof setTimeout> | undefined,
  })

  useLayoutEffect(() => {
    const current = state.current
    current.latest = text
    current.reducedMotion = reducedMotion

    const commitLatest = () => {
      current.timer = undefined
      current.displayed = current.latest
      current.changedAt = performance.now()
      current.fading = false
      setDisplayed(current.displayed)
      setFading(false)
    }
    const beginTransition = () => {
      current.timer = undefined
      if (current.latest === current.displayed) return
      if (current.reducedMotion) {
        commitLatest()
        return
      }
      current.fading = true
      setFading(true)
      current.timer = setTimeout(commitLatest, WORKING_STATUS_FADE_MS)
    }

    if (text === current.displayed) {
      clearTimeout(current.timer)
      current.timer = undefined
      current.fading = false
      setFading(false)
    } else if (current.fading) {
      // 保持原来的淡出截止时间；它的回调会取最新的文案。
      if (reducedMotion) {
        clearTimeout(current.timer)
        commitLatest()
      }
    } else if (current.timer === undefined) {
      const remaining = Math.max(0, current.changedAt + WORKING_STATUS_MIN_INTERVAL_MS - performance.now())
      current.timer = setTimeout(beginTransition, remaining)
    }
  }, [text, reducedMotion])

  useLayoutEffect(() => () => clearTimeout(state.current.timer), [])

  return <span className="console-status-text" data-fading={fading && !reducedMotion ? 'true' : undefined}>{displayed}</span>
}
