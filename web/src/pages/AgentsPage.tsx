import { useCallback, useEffect, useRef, useState, type MouseEvent as ReactMouseEvent } from 'react'
import { ArrowLeft, Bot, ChevronDown, ChevronRight, LoaderCircle, RefreshCw, Server, TriangleAlert } from 'lucide-react'
import { AppearanceToggle } from '../components/AppearanceToggle'
import { BrandLogo } from '../components/Brand'
import { syncAppBadge } from '../components/AgentOverviewLink'
import { Button, EmptyState, StatusDot } from '../components/ui'
import { api } from '../lib/api'
import { navigate } from '../lib/navigation'
import type { AgentAttention, AgentOverview, AgentOverviewItem } from '../types'
import './AgentsPage.css'

const REFRESH_MS = 5000

const attentionLabels: Record<AgentAttention, string> = {
  needs_you: '需要你',
  unseen_done: '完成未看',
  working: '工作中',
  seen: '已看',
  unknown: '状态未知',
}

export function agentPaneURL(hostID: string, paneID: string) {
  return `/h/${encodeURIComponent(hostID)}?pane=${encodeURIComponent(paneID)}`
}

function lastPathSegment(path?: string) {
  const trimmed = path?.replace(/\/+$/, '') || ''
  return trimmed.slice(trimmed.lastIndexOf('/') + 1) || trimmed || ''
}

function openLink(url: string) {
  return (event: ReactMouseEvent<HTMLAnchorElement>) => {
    if (event.button !== 0 || event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) return
    event.preventDefault()
    navigate(url)
  }
}

function AgentRow({ agent }: { agent: AgentOverviewItem }) {
  const name = agent.name || agent.agent
  const location = [agent.workspace_label, agent.tab_label].filter(Boolean).join(' › ')
  const folder = lastPathSegment(agent.cwd)
  const url = agentPaneURL(agent.host_id, agent.pane_id)
  return <li>
    <a className={`agent-item agent-item-${agent.attention}`} href={url} onClick={openLink(url)}>
      <StatusDot status={agent.agent_status}/>
      <span className="agent-item-main">
        <span className="agent-item-heading"><strong>{name}</strong>{agent.title && agent.title !== name && <span className="agent-item-title">{agent.title}</span>}</span>
        <small>{agent.host_name}{location && ` · ${location}`}{folder && <span className="agent-item-cwd" data-tooltip={agent.cwd}> · {folder}</span>}</small>
      </span>
      <span className={`agent-badge agent-badge-${agent.attention}`}>{attentionLabels[agent.attention]}</span>
    </a>
  </li>
}

export function AgentsPage() {
  const [overview, setOverview] = useState<AgentOverview | null>(null)
  const [error, setError] = useState('')
  const [manualRefresh, setManualRefresh] = useState(false)
  const [showIdle, setShowIdle] = useState(false)
  const inFlight = useRef<AbortController | null>(null)

  const load = useCallback(async () => {
    if (inFlight.current) return
    const controller = new AbortController()
    inFlight.current = controller
    try {
      const result = await api.agents({ signal: controller.signal })
      setOverview(result)
      setError('')
      syncAppBadge(result.attention_count)
    } catch (reason) {
      if (controller.signal.aborted) return
      setError(reason instanceof Error ? reason.message : '无法读取 Agent 状态')
    } finally {
      if (inFlight.current === controller) inFlight.current = null
    }
  }, [])

  // Refresh only while the page is in front; a hidden tab or a closed phone
  // screen stops polling and catches up as soon as it is visible again.
  useEffect(() => {
    const refresh = () => { if (document.visibilityState === 'visible') void load() }
    refresh()
    const timer = window.setInterval(refresh, REFRESH_MS)
    document.addEventListener('visibilitychange', refresh)
    return () => {
      window.clearInterval(timer)
      document.removeEventListener('visibilitychange', refresh)
      inFlight.current?.abort()
      inFlight.current = null
    }
  }, [load])

  const agents = overview?.agents || []
  const attention = agents.filter((agent) => agent.attention === 'needs_you' || agent.attention === 'unseen_done')
  const working = agents.filter((agent) => agent.attention === 'working')
  const idle = agents.filter((agent) => agent.attention === 'seen' || agent.attention === 'unknown')
  const failedHosts = overview?.hosts.filter((host) => host.status === 'error') || []

  let content
  if (!overview) {
    content = error
      ? <EmptyState icon={<Bot size={32}/>} title="无法读取 Agent 状态，请检查网络后重试" detail={error} action={<Button className="button-primary" onClick={() => void load()}>重试</Button>}/>
      : <div role="status" className="skeleton-status"><LoaderCircle className="spin" size={16} aria-hidden="true"/>正在读取各主机的 Agent…</div>
  } else if (overview.hosts.length === 0) {
    content = <EmptyState icon={<Server size={32}/>} title="还没有主机" detail="添加并接入一台运行 Herdr 的远程主机后，这里会汇总它上面的 Agent。" action={<Button className="button-primary" onClick={() => navigate('/')}>去添加主机</Button>}/>
  } else {
    content = <>
      {error && <div className="notice notice-error" role="alert">刷新失败：{error}。显示的是上一次读取的状态。</div>}
      {attention.length > 0 && <section className="agents-section agents-section-attention" aria-labelledby="agents-attention">
        <h2 id="agents-attention">需要你 <span className="agents-count">{attention.length}</span></h2>
        <ul className="agent-list">{attention.map((agent) => <AgentRow key={`${agent.host_id}:${agent.pane_id}`} agent={agent}/>)}</ul>
      </section>}
      {working.length > 0 && <section className="agents-section" aria-labelledby="agents-working">
        <h2 id="agents-working">工作中 <span className="agents-count">{working.length}</span></h2>
        <ul className="agent-list">{working.map((agent) => <AgentRow key={`${agent.host_id}:${agent.pane_id}`} agent={agent}/>)}</ul>
      </section>}
      {idle.length > 0 && <section className="agents-section" aria-label="已看或空闲">
        <button type="button" className="agents-idle-toggle" aria-expanded={showIdle} aria-controls="agents-idle-list" onClick={() => setShowIdle((value) => !value)}>
          {showIdle ? <ChevronDown size={16} aria-hidden="true"/> : <ChevronRight size={16} aria-hidden="true"/>}
          {showIdle ? `已看或空闲 · ${idle.length}` : `还有 ${idle.length} 个空闲`}
        </button>
        {showIdle && <ul id="agents-idle-list" className="agent-list">{idle.map((agent) => <AgentRow key={`${agent.host_id}:${agent.pane_id}`} agent={agent}/>)}</ul>}
      </section>}
      {agents.length === 0 && <EmptyState icon={<Bot size={32}/>} title="还没有 Agent" detail={failedHosts.length === overview.hosts.length ? '暂时无法读取任何主机，请查看下方原因。' : '各主机的 Herdr 里还没有识别到 Agent。在终端里启动 Claude Code、Codex 等 Agent 后会显示在这里。'}/>}
      {failedHosts.length > 0 && <section className="agents-section" aria-labelledby="agents-failed">
        <h2 id="agents-failed">无法读取的主机 <span className="agents-count">{failedHosts.length}</span></h2>
        <ul className="agent-list">{failedHosts.map((host) => <li key={host.host_id} className="agent-host-error">
          <TriangleAlert size={16} aria-hidden="true"/>
          <span className="agent-item-main"><strong>{host.host_name}</strong><small>{host.error || '无法读取 Herdr 状态，请打开主机查看'}</small></span>
          <Button className="button-secondary" onClick={() => navigate(`/h/${encodeURIComponent(host.host_id)}`)}>打开主机</Button>
        </li>)}</ul>
      </section>}
    </>
  }

  return <div className="app-shell">
    <header className="topbar">
      <a className="brand" href="/" onClick={openLink('/')}><BrandLogo/></a>
      <div className="topbar-actions"><AppearanceToggle/><Button className="button-ghost" onClick={() => navigate('/')}><ArrowLeft size={16}/>返回主机</Button></div>
    </header>
    <main className="page page-agents">
      <div className="page-heading">
        <div><h1>Agent 总览</h1><p>所有主机上的 Agent，需要你处理的排在最前。</p></div>
        <Button className="button-secondary" aria-label="刷新 Agent 状态" pending={manualRefresh} onClick={() => { setManualRefresh(true); void load().finally(() => setManualRefresh(false)) }}>{!manualRefresh && <RefreshCw size={16} aria-hidden="true"/>}刷新</Button>
      </div>
      {content}
    </main>
  </div>
}
