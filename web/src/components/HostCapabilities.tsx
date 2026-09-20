import { useEffect, useState } from 'react'
import { api } from '../lib/api'
import type { HerdrCapabilities, Host } from '../types'
import { Modal } from './Modal'
import { Button } from './ui'

const featureNames = { snapshot: '会话快照', observe: '终端观察', input: '文本输入', resize: '任务尺寸控制', preserve_scroll: '保尺寸滚轮', history: '历史读取' } as const
const stateNames = { available: '可用', unavailable: '不可用', unknown: '尚未确认' } as const

export function HostCapabilities({ host, onClose }: { host: Host; onClose: () => void }) {
  const [report, setReport] = useState<HerdrCapabilities | null>(null)
  const [error, setError] = useState('')
  const [loading, setLoading] = useState(true)
  const [attempt, setAttempt] = useState(0)
  useEffect(() => {
    let cancelled = false
    setLoading(true)
    setError('')
    setReport(null)
    void api.hostCapabilities(host.id).then(({ capabilities }) => {
      if (!cancelled) setReport(capabilities)
    }).catch((reason) => {
      if (!cancelled) setError(reason instanceof Error ? reason.message : '无法连接主机，请检查连接后重试。')
    }).finally(() => { if (!cancelled) setLoading(false) })
    return () => { cancelled = true }
  }, [host.id, attempt])
  return <Modal title={`Herdr 能力 · ${host.name}`} onClose={onClose}>
    <p className="field-hint">读取此主机已安装的 CLI 与当前运行会话的能力。不会调整任务尺寸或更新 Herdr。</p>
    {loading && <p role="status">正在检查 Herdr 能力…</p>}
    {error && <p className="field-error" role="alert">{error}</p>}
    {report && <div className="host-capabilities">
      <p role="status">{report.status === 'unavailable' ? '连接不可用' : report.status === 'limited' ? '部分功能不可用或尚未确认' : '功能可用'}</p>
      <dl>
        <dt>安装的 Herdr CLI</dt><dd>{report.cli.version || '版本未知'} · 协议 {report.cli.protocol || '未声明'}</dd>
        <dt>运行中的 Herdr</dt><dd>{report.daemon.version || '版本未知'} · 协议 {report.daemon.protocol || '未声明'}</dd>
        <dt>版本回归</dt><dd>{report.coverage === 'tested' ? '已纳入版本回归' : '尚未纳入版本回归'}</dd>
        <dt>检查时间</dt><dd>{report.checked_at ? new Date(report.checked_at).toLocaleString() : '未知'}</dd>
      </dl>
      <ul className="host-capability-list">{Object.entries(featureNames).map(([key, label]) => {
        const feature = report.features[key as keyof typeof featureNames]
        return <li key={key}><strong>{label}</strong><span>{stateNames[feature?.state || 'unknown']}</span>{feature?.reason && <p>{feature.reason}</p>}</li>
      })}</ul>
    </div>}
    <div className="modal-actions"><Button className="button-secondary" disabled={loading} onClick={() => setAttempt((value) => value + 1)}>重新检查</Button><Button className="button-primary" onClick={onClose}>关闭</Button></div>
  </Modal>
}
