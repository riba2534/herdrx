import { useSyncExternalStore } from 'react'

/**
 * 工作台的两种模式，是“看同一个 Herdr tab 的两种方式”，底下是同一份 workspace / tab / pane：
 *
 * - `ui`（Web UI，默认）：结构化界面。Claude pane 显示对话、交互卡片和输入框，其他 pane 显示只读画面。
 * - `shell`（Web Shell）：原有的 xterm 终端工作台（分屏、Herdr 快捷键、尺寸控制、滚动）。
 *
 * 选择按设备保存在 localStorage。地址栏的 `?mode=ui|shell` 只覆盖本次打开（便于分享链接和浏览器测试），
 * 不写入存储；用户在界面上切换时才持久化，并把地址栏里的覆盖去掉。切换模式不调用任何 Herdr 接口。
 */
export type WorkbenchMode = 'ui' | 'shell'

export const WORKBENCH_MODE_STORAGE_KEY = 'herdrx.workbench-mode.v1'
export const DEFAULT_WORKBENCH_MODE: WorkbenchMode = 'ui'

const listeners = new Set<() => void>()
let memory: WorkbenchMode | null = null

export function isWorkbenchMode(value: unknown): value is WorkbenchMode {
  return value === 'ui' || value === 'shell'
}

function urlOverride(): WorkbenchMode | null {
  if (typeof window === 'undefined') return null
  try {
    const value = new URLSearchParams(window.location.search).get('mode')
    return isWorkbenchMode(value) ? value : null
  } catch {
    return null
  }
}

function stored(): WorkbenchMode | null {
  try {
    const value = localStorage.getItem(WORKBENCH_MODE_STORAGE_KEY)
    return isWorkbenchMode(value) ? value : null
  } catch {
    return null
  }
}

export function readWorkbenchMode(): WorkbenchMode {
  return urlOverride() ?? memory ?? stored() ?? DEFAULT_WORKBENCH_MODE
}

export function writeWorkbenchMode(mode: WorkbenchMode) {
  memory = mode
  try {
    localStorage.setItem(WORKBENCH_MODE_STORAGE_KEY, mode)
  } catch {
    // 隐私模式下存储不可用时只保留内存态。
  }
  if (typeof window !== 'undefined' && urlOverride()) {
    const url = new URL(window.location.href)
    url.searchParams.delete('mode')
    window.history.replaceState(window.history.state, '', `${url.pathname}${url.search}${url.hash}`)
  }
  for (const listener of listeners) listener()
}

export function subscribeWorkbenchMode(listener: () => void) {
  listeners.add(listener)
  const onStorage = (event: StorageEvent) => {
    if (event.key !== WORKBENCH_MODE_STORAGE_KEY) return
    memory = null
    listener()
  }
  const onPop = () => listener()
  if (typeof window !== 'undefined') {
    window.addEventListener('storage', onStorage)
    window.addEventListener('popstate', onPop)
  }
  return () => {
    listeners.delete(listener)
    if (typeof window !== 'undefined') {
      window.removeEventListener('storage', onStorage)
      window.removeEventListener('popstate', onPop)
    }
  }
}

export function useWorkbenchMode(): [WorkbenchMode, (mode: WorkbenchMode) => void] {
  const mode = useSyncExternalStore(subscribeWorkbenchMode, readWorkbenchMode, () => DEFAULT_WORKBENCH_MODE)
  return [mode, writeWorkbenchMode]
}

/** 仅供测试：清掉内存态，让下一次读取重新看存储和地址栏。 */
export function resetWorkbenchModeForTest() {
  memory = null
}
