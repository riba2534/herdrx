import { useEffect, useRef, useState, type ReactNode } from 'react'
import { ArrowLeft, ArrowRight, Check, ExternalLink } from 'lucide-react'
import { api, type CLIRelease } from '../lib/api'
import { Button } from './ui'
import { RelayConnectCommand } from './RelayConnectCommand'
import { Command } from './SetupCommand'

const repository = 'https://github.com/riba2534/herdrx'
const steps = ['安装 CLI', '后台运行', '绑定主机']


export function TailcatSetupGuide({ children, pending, resuming, onClose, onDefer }: { children: ReactNode; pending: boolean; resuming: boolean; onClose: () => void; onDefer?: () => void }) {
  const [step, setStep] = useState(resuming ? 2 : 0)
  const [release, setRelease] = useState<CLIRelease | null>(null)
  const [checking, setChecking] = useState(true)
  const [retry, setRetry] = useState(0)
  const heading = useRef<HTMLHeadingElement>(null)
  useEffect(() => {
    let active = true
    void api.cliRelease().then((value) => { if (active) setRelease(value) }).catch(() => {
      if (active) setRelease({ status: 'unavailable' })
    }).finally(() => { if (active) setChecking(false) })
    return () => { active = false }
  }, [retry])
  const go = (next: number) => { setStep(next); queueMicrotask(() => {
    heading.current?.focus({ preventScroll: true })
    heading.current?.closest('form')?.scrollTo?.({ top: 0 })
  }) }
  const version = release?.status === 'available' && /^v\d+\.\d+\.\d+(?:-[\w.-]+)?$/.test(release.version || '') ? release.version : null
  const download = `${repository}/releases/download/${version}`

  return <div className="tailcat-guide">
    <p className="setup-intro">通过 SSH 登录远程主机，使用运行 Herdr 的同一用户执行以下命令。</p>
    <ol className="setup-stepper" aria-label="Tailcat 接入步骤">{steps.map((label, index) => <li key={label} aria-current={step === index ? 'step' : undefined}><span>{step > index ? <Check size={14}/> : index + 1}</span>{label}</li>)}</ol>
    <h3 ref={heading} tabIndex={-1}>{steps[step]}</h3>
    {step === 0 && <div className="form-stack">
      {checking ? <p className="field-hint" role="status">正在检查可下载的 CLI 版本…</p> : version ? <>
        <Command title="下载安装命令" value={`curl -fsSL ${download}/install-herdrx.sh | sh -s -- --version ${version}`}/>
        <p className="field-hint">复制到远程终端运行，自动识别架构并校验安装包。支持 Linux x86_64 / ARM64。</p>
        <div className="setup-download"><span className="setup-version">{version}{release?.prerelease ? ' · 预发布' : ''}</span><a href={`${repository}/releases/tag/${version}`} target="_blank" rel="noopener noreferrer">版本说明<ExternalLink size={13}/></a></div>
        <details className="setup-details"><summary>安装说明与手动下载</summary><p className="field-hint">需要 curl、tar 和 SHA-256 校验工具。默认安装到 <code>~/.local/bin/herdrx</code>，后续命令无需配置 PATH。</p><p className="field-hint">手动安装时，按 CPU 选择压缩包，并下载 SHA256SUMS。</p><div className="setup-download"><a href={`${download}/herdrx-linux-amd64.tar.gz`}>Linux x86_64</a><a href={`${download}/herdrx-linux-arm64.tar.gz`}>Linux ARM64</a><a href={`${download}/SHA256SUMS`}>SHA256SUMS</a></div><p className="field-hint">Release 附带 README-CLI.md，包含手动校验及安装的完整命令。</p></details>
      </> : <div className="notice notice-info" role="status"><p>{release?.status === 'unpublished' ? 'CLI 安装包尚未发布。请等待管理员发布首个 GitHub Release；已有 CLI 的主机可继续下一步。' : '暂时无法获取版本。请打开 GitHub Releases 查看可用版本，并按附件 README-CLI.md 安装；已有 CLI 的主机可继续下一步。'}</p><a className="inline-link" href={`${repository}/releases`} target="_blank" rel="noopener noreferrer">GitHub Releases<ExternalLink size={13}/></a><Button type="button" className="button-ghost" onClick={() => { heading.current?.focus(); setChecking(true); setRetry((value) => value + 1) }}>重新检查</Button></div>}
    </div>}
    {step === 1 && <div className="form-stack">
      <p className="field-hint">Herdr 需要已安装并独立运行。herdrx 会检查兼容性，然后安装并启动用户级 systemd 服务。</p>
      <a className="inline-link" href="https://herdr.dev/docs/install/" target="_blank" rel="noopener noreferrer">Herdr 官方安装说明 <ExternalLink size={13}/></a>
      <Command title="启动后台服务" value="~/.local/bin/herdrx setup && ~/.local/bin/herdrx status"/>
      <p className="field-hint">看到 herdrx daemon“运行中”和 Herdr 状态“ok”后继续。setup 可重复执行，会保留已有身份与绑定。</p>
      <Command title="检查开机与登出保活" value={'loginctl show-user "$(id -un)" --property=Linger'}/>
      <p className="field-hint">结果需为 <code>Linger=yes</code>。若为 no，请执行下面命令；需要管理员权限时再加 sudo，然后重新检查。</p>
      <Command title="启用用户后台保活" value={'loginctl enable-linger "$(id -un)"'}/>
      <details className="setup-details"><summary>启动失败或没有 systemd</summary><p className="field-hint">运行 <code>~/.local/bin/herdrx doctor</code> 检查 Herdr，运行 <code>~/.local/bin/herdrx logs -n 100</code> 查看服务日志。找不到 Herdr 时可用 <code>~/.local/bin/herdrx setup --herdr-bin /path/to/herdr</code> 指定路径。</p><p className="field-hint">没有 systemd 用户会话时，可执行 <code>~/.local/bin/herdrx setup --skip-service</code> 后运行 <code>~/.local/bin/herdrx serve</code>，并交给你自己的进程管理器保活；直接关闭该终端会中断 Tailcat 访问。也可以返回选择 SSH 接入。</p></details>
    </div>}
    {step === 2 && <div className="form-stack">
      <p className="field-hint">后台服务就绪后，生成一次性绑定凭据并粘贴到下方。绑定完成会自动进入工作台。</p>
      <RelayConnectCommand release={release}/>
      {children}
      <details className="setup-details"><summary>凭据过期或连接失败</summary><p className="field-hint">重新运行 <code>~/.local/bin/herdrx connect --plain</code> 获取有效凭据；要作废尚未使用的旧凭据，请执行 <code>~/.local/bin/herdrx connect --renew --plain</code>。通过 <code>~/.local/bin/herdrx status</code>、<code>~/.local/bin/herdrx doctor</code>、<code>~/.local/bin/herdrx logs -n 100</code> 检查服务和网络。</p></details>
    </div>}
    <div className="setup-footer"><div className="setup-lifecycle">关闭浏览器或退出工作台后，远程主机上的 Herdr 与任务继续运行。</div>
    <div className="modal-actions setup-actions">
      <Button type="button" className="button-secondary" disabled={pending && !onDefer} onClick={() => pending && onDefer ? onDefer() : step === 0 ? onClose() : go(step - 1)}>{pending && onDefer ? '稍后再看' : step === 0 ? '取消' : <><ArrowLeft size={15}/>上一步</>}</Button>
      {step < 2 ? <Button key="next" type="button" className="button-primary" onClick={(event) => { event.preventDefault(); go(step + 1) }}>{step === 0 ? '已安装，下一步' : '服务已就绪，下一步'}<ArrowRight size={15}/></Button> : <Button key="bind" type="submit" className="button-primary" pending={pending}>绑定并打开主机</Button>}
    </div></div>
  </div>
}
