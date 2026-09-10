import { useEffect, useState } from 'react'
import { applyUpdate, isStandalone, usePWA } from '../lib/pwa'
import { Button } from './ui'

type InstallPrompt = Event & { prompt: () => Promise<void> }

export function PWAStatus() {
  const pwa = usePWA()
  const [dismissed, setDismissed] = useState(false)
  const [installDismissed, setInstallDismissed] = useState(false)
  const [installEvent, setInstallEvent] = useState<InstallPrompt | null>(null)
  const standalone = isStandalone()
  useEffect(() => {
    if (standalone) return
    const onPrompt = (event: Event) => {
      event.preventDefault()
      setInstallEvent(event as InstallPrompt)
    }
    window.addEventListener('beforeinstallprompt', onPrompt)
    return () => window.removeEventListener('beforeinstallprompt', onPrompt)
  }, [standalone])
  return <>
    {!standalone && installEvent && !installDismissed && <aside className="pwa-update pwa-install" aria-label="添加到主屏幕"><div role="status"><strong>添加到主屏幕</strong><p>安装后可从主屏幕打开 herdrx，远程任务不受影响。</p></div><div className="pwa-update-actions"><Button className="button-secondary" onClick={() => setInstallDismissed(true)}>稍后</Button><Button className="button-primary" onClick={() => { void installEvent.prompt(); setInstallDismissed(true) }}>安装</Button></div></aside>}
    {pwa.updateReady && !dismissed && <aside className="pwa-update" aria-label="应用更新"><div role="status"><strong>herdrx 新版本已就绪</strong><p>刷新后继续连接原会话，远程任务保持运行。请先保存页面中未提交的内容。</p></div><div className="pwa-update-actions"><Button className="button-secondary" onClick={() => setDismissed(true)}>稍后</Button><Button className="button-primary" onClick={applyUpdate}>刷新更新</Button></div></aside>}
  </>
}
