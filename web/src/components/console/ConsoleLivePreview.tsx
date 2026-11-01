import { useState } from 'react'
import { ChevronRight, Monitor } from 'lucide-react'

const OPEN_KEY = 'herdrx.console.live-preview.v1'

function readOpen() {
  try { return localStorage.getItem(OPEN_KEY) === 'true' } catch { return false }
}

/**
 * 「实时画面」折叠条：收起时只露出画面最新一行，展开显示最近约 12 行纯文本（等宽）。
 * 展开状态只记在这台设备上。内容来自读屏，只读。
 */
export function ConsoleLivePreview({ preview, error }: { preview: readonly string[]; error?: string }) {
  const [open, setOpen] = useState(readOpen)
  const toggle = () => {
    setOpen((current) => {
      try { localStorage.setItem(OPEN_KEY, String(!current)) } catch { /* storage unavailable */ }
      return !current
    })
  }
  const latest = preview.at(-1)?.trim() || ''
  return <div className="console-live" data-open={open}>
    <button type="button" className="console-live-toggle" aria-expanded={open} onClick={toggle}>
      <ChevronRight className="console-live-chevron" size={14} aria-hidden="true"/>
      <Monitor size={14} aria-hidden="true"/>
      <span className="console-live-title">实时画面</span>
      {!open && latest && <span className="console-live-latest">{latest}</span>}
    </button>
    {open && <pre className="console-live-body" aria-label="终端实时画面" aria-live="off">
      {error ? `读不到实时画面：${error}\n` : ''}{preview.length ? preview.join('\n') : error ? '' : '还没有读到画面'}
    </pre>}
  </div>
}
