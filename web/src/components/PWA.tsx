import { useState } from 'react'
import { applyUpdate, usePWA } from '../lib/pwa'
import { Button } from './ui'

export function PWAStatus() {
  const pwa = usePWA()
  const [dismissed, setDismissed] = useState(false)
  if (!pwa.updateReady || dismissed) return null
  return <aside className="pwa-update" aria-label="应用更新"><div role="status"><strong>herdrx 新版本已就绪</strong><p>刷新后继续连接原会话，远程任务保持运行。请先保存页面中未提交的内容。</p></div><div className="pwa-update-actions"><Button className="button-secondary" onClick={() => setDismissed(true)}>稍后</Button><Button className="button-primary" onClick={applyUpdate}>刷新更新</Button></div></aside>
}
