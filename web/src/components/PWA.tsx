import { useState } from 'react'
import { Download, RefreshCw } from 'lucide-react'
import { applyUpdate, checkForUpdate, installApp, usePWA } from '../lib/pwa'
import { Modal } from './Modal'
import { Button } from './ui'

export function InstallAppButton() {
  const pwa = usePWA()
  const [open, setOpen] = useState(false)
  return <><Button className="button-ghost" onClick={() => setOpen(true)}><Download size={16}/>{pwa.standalone ? '应用与更新' : '安装应用'}</Button>
    {open && <Modal title="在电脑与手机上使用 herdrx" onClose={() => setOpen(false)}><div className="form-stack">
      <p>安装后可从程序坞或主屏幕打开独立窗口，继续连接原有工作台。关闭应用不会停止远程任务。</p>
      {!window.isSecureContext ? <p role="status">当前 HTTP 地址可以正常使用网站。独立应用安装、离线缓存和推送取决于浏览器对此地址开放的能力。</p> : pwa.standalone ? <p role="status">当前已在独立应用窗口中运行。</p> : <>
        {pwa.canInstall && <Button className="button-primary" onClick={() => void installApp()}>安装 herdrx</Button>}
        <ul className="pwa-install-steps"><li><strong>Mac Safari：</strong>在「文件」菜单选择「添加到程序坞」。</li><li><strong>iPhone / iPad：</strong>在 Safari 分享菜单选择「添加到主屏幕」，如有「作为 Web App 打开」选项，请开启。</li><li><strong>Chrome / Edge / Android：</strong>使用地址栏安装图标，或浏览器菜单中的「安装应用 / 添加到主屏幕」。</li></ul>
        <p className="field-hint">安装后首次打开可能需要重新登录。没有安装入口时，可继续在浏览器中使用。</p>
      </>}
      <p className="field-hint">断网时显示离线提示；终端需要网络连接。恢复网络后重新验证登录并连接原会话，不会自动补发输入。</p>
      {pwa.error && <p role="status">{pwa.error}</p>}
      <Button className="button-secondary" onClick={() => pwa.updateReady ? applyUpdate() : void checkForUpdate()}><RefreshCw size={16}/>{pwa.updateReady ? '刷新并使用新版本' : '检查更新'}</Button>
    </div></Modal>}
  </>
}

export function PWAStatus() {
  const pwa = usePWA()
  const [dismissed, setDismissed] = useState(false)
  if (!pwa.updateReady || dismissed) return null
  return <aside className="pwa-update" aria-label="应用更新"><div role="status"><strong>herdrx 新版本已就绪</strong><p>刷新后继续连接原会话，远程任务保持运行。请先保存页面中未提交的内容。</p></div><div className="pwa-update-actions"><Button className="button-secondary" onClick={() => setDismissed(true)}>稍后</Button><Button className="button-primary" onClick={applyUpdate}>刷新更新</Button></div></aside>
}
