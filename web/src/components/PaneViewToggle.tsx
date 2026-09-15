import type { PaneViewMode } from '../lib/paneViewMode'
import './PaneViewToggle.css'

export type PaneViewToggleProps = {
  mode: PaneViewMode
  onChange: (mode: PaneViewMode) => void
  disabled?: boolean
  className?: string
}

/**
 * pane 内唯一的视图主开关：视觉为 Terminal [开关] Chat。
 *
 * 它不是两个按钮拼成的分段控件，而是一个 role="switch" 的按钮：
 * 鼠标点击、Space、Enter 都触发同一次切换，aria-checked 为真表示对话视图。
 * 当前模式始终可见：两侧文字高亮 + 滑块位置 + 提示文字。
 */
export function PaneViewToggle({ mode, onChange, disabled = false, className = '' }: PaneViewToggleProps) {
  const chat = mode === 'chat'
  const toggle = () => onChange(chat ? 'terminal' : 'chat')
  return <button
    type="button"
    role="switch"
    aria-checked={chat}
    aria-label="对话视图"
    disabled={disabled}
    data-mode={mode}
    data-tooltip={chat ? '当前：对话 Chat · 点击切回终端' : '当前：终端 Terminal · 点击切到对话'}
    className={`pane-view-toggle pane-view-toggle-${mode}${className ? ` ${className}` : ''}`}
    onClick={toggle}
    onKeyDown={(event) => {
      if (event.key !== 'Enter' && event.key !== ' ' && event.key !== 'Spacebar') return
      // 显式处理并阻止默认动作，避免同一按键又被原生 keyup click 切换一次。
      event.preventDefault()
      if (event.repeat) return
      toggle()
    }}
  >
    <span className="pane-view-toggle-label pane-view-toggle-label-terminal" aria-hidden="true">Terminal</span>
    <span className="pane-view-toggle-track" aria-hidden="true"><span className="pane-view-toggle-thumb"/></span>
    <span className="pane-view-toggle-label pane-view-toggle-label-chat" aria-hidden="true">Chat</span>
  </button>
}
