// Adapted from Cindy (https://github.com/makecindy/cindy, Apache-2.0) apps/desktop/src/renderer/components/interaction-portal/InteractionPromptCardShell.tsx@19dad9c225
import type { ReactNode } from 'react'
import { Minus, Plus } from 'lucide-react'

/**
 * 顶替输入框的交互卡片外壳：只管卡片框、收起成 44px 的条和展开。题目、作答和状态由各卡片自己管。
 * 长内容在中间区域内滚动；`pinned` 放在滚动区之外（自由填写、批准动作），长列表也不会把它挤出视野。
 */
export function InteractionCardShell({ label, collapsed, onCollapsedChange, barTitle = label, barMeta, header, children, pinned, footer }: {
  /** 卡片的无障碍名称，也是收起后条上的默认标题。 */
  label: string
  collapsed: boolean
  onCollapsedChange: (collapsed: boolean) => void
  barTitle?: string
  barMeta?: string
  header: ReactNode
  children?: ReactNode
  pinned?: ReactNode
  footer?: ReactNode
}) {
  if (collapsed) {
    return <button type="button" className="console-card-bar" aria-label={`展开卡片：${barTitle}`} onClick={() => onCollapsedChange(false)}>
      <span className="console-card-bar-title">{barTitle}</span>
      {barMeta && <span className="console-card-bar-meta">{barMeta}</span>}
      <span className="console-card-bar-icon" aria-hidden="true"><Plus size={16}/></span>
    </button>
  }
  return <section className="console-card" role="group" aria-label={label}>
    <header className="console-card-head">
      <div className="console-card-head-main">{header}</div>
      <button type="button" className="console-icon-button" aria-label="收起卡片" data-tooltip="收起成一条，只影响这台设备" onClick={() => onCollapsedChange(true)}><Minus size={16} aria-hidden="true"/></button>
    </header>
    {children !== undefined && <div className="console-card-scroll">{children}</div>}
    {pinned && <div className="console-card-pinned">{pinned}</div>}
    {footer}
  </section>
}
