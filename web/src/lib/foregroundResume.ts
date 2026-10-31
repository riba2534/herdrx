// 回到前台的探测信号：手机/PWA 切回来时立刻检查连接，而不是干等重连退避或下一次心跳。
// - visibilitychange 变为可见（切标签、恢复窗口、手机切回 App）；
// - pageshow（含从往返缓存 bfcache 恢复）；
// - online（网络恢复）；
// - 定时器间隔异常：设备睡眠时页面定时器停摆，唤醒后第一次 tick 会远晚于预期。
//   只在页面可见时计时，后台页面的定时器本来就会被节流，晚到不说明什么；
//   重新可见时从新的基准开始计时。
// 合并窗口内的多次触发只算一次；浏览器报告离线时不触发，等 online 事件再说。

export type ForegroundResumeReason = 'visible' | 'pageshow' | 'online' | 'wake'

export type ForegroundResumeEnvironment = {
  document: Pick<Document, 'visibilityState' | 'addEventListener' | 'removeEventListener'>
  window: Pick<Window, 'addEventListener' | 'removeEventListener'>
  navigator: Pick<Navigator, 'onLine'>
  now: () => number
  setInterval: (callback: () => void, ms: number) => number
  clearInterval: (handle: number) => void
}

export type ForegroundResumeOptions = {
  /** 与上一次触发间隔小于此值的触发会被合并。 */
  coalesceMs?: number
  /** 睡眠检测定时器的周期。 */
  tickMs?: number
  /** tick 晚于 tickMs + wakeGapMs 视为从睡眠中唤醒。 */
  wakeGapMs?: number
}

export const FOREGROUND_COALESCE_MS = 1000
export const FOREGROUND_TICK_MS = 5000
export const FOREGROUND_WAKE_GAP_MS = 25_000

function browserEnvironment(): ForegroundResumeEnvironment {
  return {
    document, window, navigator,
    now: () => Date.now(),
    setInterval: (callback, ms) => window.setInterval(callback, ms),
    clearInterval: (handle) => window.clearInterval(handle),
  }
}

/** 安装前台恢复监听，返回卸载函数。 */
export function installForegroundResume(
  onResume: (reason: ForegroundResumeReason) => void,
  options: ForegroundResumeOptions = {},
  env: ForegroundResumeEnvironment = browserEnvironment(),
) {
  const coalesceMs = options.coalesceMs ?? FOREGROUND_COALESCE_MS
  const tickMs = options.tickMs ?? FOREGROUND_TICK_MS
  const wakeGapMs = options.wakeGapMs ?? FOREGROUND_WAKE_GAP_MS
  let lastFired = Number.NEGATIVE_INFINITY
  let lastTick = 0
  let timer = 0

  const fire = (reason: ForegroundResumeReason) => {
    if (env.navigator.onLine === false) return
    const now = env.now()
    if (now - lastFired < coalesceMs) return
    lastFired = now
    onResume(reason)
  }
  const stopTimer = () => {
    env.clearInterval(timer)
    timer = 0
  }
  const startTimer = () => {
    stopTimer()
    lastTick = env.now()
    timer = env.setInterval(() => {
      const now = env.now()
      const gap = now - lastTick
      lastTick = now
      if (env.document.visibilityState === 'visible' && gap > tickMs + wakeGapMs) fire('wake')
    }, tickMs)
  }
  const onVisibility = () => {
    if (env.document.visibilityState !== 'visible') { stopTimer(); return }
    startTimer()
    fire('visible')
  }
  const onPageShow = () => fire('pageshow')
  const onOnline = () => fire('online')

  if (env.document.visibilityState === 'visible') startTimer()
  env.document.addEventListener('visibilitychange', onVisibility)
  env.window.addEventListener('pageshow', onPageShow)
  env.window.addEventListener('online', onOnline)
  return () => {
    env.document.removeEventListener('visibilitychange', onVisibility)
    env.window.removeEventListener('pageshow', onPageShow)
    env.window.removeEventListener('online', onOnline)
    stopTimer()
  }
}
