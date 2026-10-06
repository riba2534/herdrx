import { useState } from 'react'
import { Play } from 'lucide-react'
import { api } from '../lib/api'
import type { HerdrStartResult, Host } from '../types'
import { Button } from './ui'

export type HerdrStartFeedback = { tone: 'ok' | 'warn' | 'error'; text: string }
type Feedback = HerdrStartFeedback

/** What a start result means for the user, and what to do next. */
export function herdrStartFeedback(result: HerdrStartResult, username?: string): Feedback {
  if (result.status === 'running') return { tone: 'ok', text: '远程主机上的 Herdr 已在运行，没有重复启动。若仍无法连接，请检查 SSH 账号和 Herdr 命名会话设置。' }
  if (result.status === 'missing') return { tone: 'error', text: '远程主机上找不到 herdr 命令。请先在远程主机安装 Herdr，安装后再点启动。' }
  if (result.status === 'failed') return { tone: 'error', text: '已尝试启动，但 Herdr 没有在 9 秒内就绪。请在远程主机运行 herdr status server 查看原因。' }
  if (result.method === 'systemd') return { tone: 'ok', text: '已在远程主机启动 Herdr。它运行在远程主机的独立用户服务里，关闭网页或网站重启都不影响它。' }
  if (result.linger === 'no') {
    return { tone: 'warn', text: `已在远程主机启动 Herdr，关闭网页不影响它。远程主机未开启 linger：若系统在用户全部退出登录后清理进程，Herdr 可能被结束；需要长期运行时，可在远程主机执行 sudo loginctl enable-linger ${username || '$USER'}。` }
  }
  return { tone: 'ok', text: '已在远程主机启动 Herdr，关闭网页或断开网站连接都不影响它。' }
}

/**
 * 在 SSH 远程主机上启动 Herdr。只在用户点击时运行；已在运行的 Herdr 不会被替换或重启，
 * 网站也不提供停止 Herdr 的入口。
 */
export function StartHerdrButton({ host, onStarted, className = 'button-primary' }: {
  host: Host
  /** Called after a start or when Herdr was already up. The button may unmount
   *  right after (a reconnect hides it), so the feedback travels with the call. */
  onStarted?: (feedback: HerdrStartFeedback) => void
  className?: string
}) {
  const [pending, setPending] = useState(false)
  const [feedback, setFeedback] = useState<Feedback | null>(null)
  if (host.transport !== 'ssh') return null
  const start = async () => {
    if (pending) return
    setPending(true)
    setFeedback(null)
    try {
      const { result } = await api.startHerdr(host.id)
      const next = herdrStartFeedback(result, host.username)
      setFeedback(next)
      if (result.status === 'started' || result.status === 'running') onStarted?.(next)
    } catch (reason) {
      setFeedback({ tone: 'error', text: reason instanceof Error ? reason.message : '无法在远程主机启动 Herdr，请检查 SSH 连接后重试。' })
    } finally {
      setPending(false)
    }
  }
  return <div className="start-herdr">
    <Button className={className} pending={pending} disabled={pending} data-tooltip="在远程主机后台启动 Herdr；已在运行时不会重复启动" onClick={() => void start()}><Play size={15}/>{pending ? '正在启动 Herdr…' : '在远程主机启动 Herdr'}</Button>
    {feedback && <p className={feedback.tone === 'error' ? 'field-error' : 'field-hint'} role={feedback.tone === 'error' ? 'alert' : 'status'}>{feedback.text}</p>}
  </div>
}
