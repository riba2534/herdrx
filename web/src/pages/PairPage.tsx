import { BrandIcon } from '../components/Brand'
import { useMemo, useState } from 'react'
import { Radio, ShieldCheck } from 'lucide-react'
import { Button, Field } from '../components/ui'
import { api } from '../lib/api'
import { navigate } from '../lib/navigation'

function decodePairHash() {
  const encoded = window.location.hash.startsWith('#pair=') ? window.location.hash.slice(6) : ''
  if (!encoded) {
    const saved = sessionStorage.getItem('herdrx.pending-pair.v1')
    if (!saved) return null
    try { return JSON.parse(saved) as Record<string, unknown> } catch { return null }
  }
  try {
    const normalized = encoded.replace(/-/g, '+').replace(/_/g, '/')
    const padding = '='.repeat((4 - normalized.length % 4) % 4)
    const bytes = Uint8Array.from(atob(normalized + padding), (character) => character.charCodeAt(0))
    const payload = JSON.parse(new TextDecoder().decode(bytes)) as Record<string, unknown>
    sessionStorage.setItem('herdrx.pending-pair.v1', JSON.stringify(payload))
    window.history.replaceState({}, '', '/pair')
    return payload
  } catch { return null }
}

export function PairPage() {
  const payload = useMemo(decodePairHash, [])
  const [name, setName] = useState(String(payload?.host || ''))
  const [sessionName, setSessionName] = useState('')
  const [pending, setPending] = useState(false)
  const [error, setError] = useState('')
  if (!payload) return <main className="auth-shell"><section className="auth-card"><h1>配对链接无效</h1><p className="auth-intro">请在被控机重新运行 <code>herdrx-agent pair</code>。</p><Button className="button-primary" onClick={() => navigate('/')}>返回主机</Button></section></main>

  const connect = async () => {
    setPending(true)
    setError('')
    try {
      const result = await api.pairTailcat({ ...payload, name, session_name: sessionName })
      sessionStorage.removeItem('herdrx.pending-pair.v1')
      window.history.replaceState({}, '', `/h/${result.host.id}`)
      window.dispatchEvent(new PopStateEvent('popstate'))
    } catch (reason) { setError(reason instanceof Error ? reason.message : '配对失败') }
    finally { setPending(false) }
  }

  return <main className="auth-shell"><section className="auth-card pair-card">
    <BrandIcon className="pair-brand-icon"/><h1>连接 Tailcat 主机</h1>
    <div className="pair-summary"><Radio size={18}/><span><strong>{String(payload.host || '未命名主机')}</strong><small>{String(payload.os || '')} · {String(payload.arch || '')} · agent {String(payload.agent_ver || '')}</small></span></div>
    <div className="form-stack"><Field label="主机名称" value={name} onChange={(event) => setName(event.target.value)} required/><Field label="Herdr 命名会话（可选）" value={sessionName} onChange={(event) => setSessionName(event.target.value)} placeholder="默认会话"/>{error && <div className="notice notice-error">{error}</div>}<Button className="button-primary button-wide" pending={pending} onClick={() => void connect()}><ShieldCheck size={17}/>验证并连接</Button><Button className="button-ghost" onClick={() => { sessionStorage.removeItem('herdrx.pending-pair.v1'); window.history.replaceState({}, '', '/'); window.dispatchEvent(new PopStateEvent('popstate')) }}>取消</Button></div>
  </section></main>
}
