import { DateTimeField } from '../components/DateTimeField'
import { useConfirm } from '../components/useConfirm'
import { Form } from '../components/Form'
import { Select, SelectOption } from '../components/Select'
import { BrandLogo } from '../components/Brand'
import { useEffect, useState, type FormEvent } from 'react'
import { ArrowLeft, Copy, RefreshCw, X } from 'lucide-react'
import { useAuth } from '../auth'
import { Button, Field } from '../components/ui'
import { AppearanceToggle } from '../components/AppearanceToggle'
import { api, authenticationGeneration, invalidateAuthentication } from '../lib/api'
import { navigate } from '../lib/navigation'
import type { AdminUser, AuditEntry, Invite, LoginSession, Pagination, InstanceSettings } from '../types'

type Tab = 'users' | 'invites' | 'audit'
const initialPage: Pagination = { offset: 0, limit: 50, next_offset: 0, has_more: false }
const date = (value?: string) => value ? new Date(value).toLocaleString('zh-CN') : '—'
const statusLabel = { active: '可使用', used: '已使用', expired: '已过期', revoked: '已撤销' }
const actionLabel: Record<string, string> = {
  'user.disabled': '禁用账号', 'user.enabled': '启用账号', 'session.revoked': '注销一条登录',
  'sessions.revoked': '注销全部登录', 'invite.created': '创建邀请', 'invite.revoked': '撤销邀请',
  'login.succeeded': '登录成功', 'login.failed': '登录失败', 'logout.completed': '退出登录', 'invite.used': '受邀注册', 'bootstrap.completed': '初始化管理员',
  'registration.changed': '修改注册设置',
}

function Pager({ page, busy, change }: { page: Pagination; busy: boolean; change: (offset: number) => void }) {
  return <nav className="admin-pager" aria-label="分页">
    <Button className="button-secondary" disabled={busy || !page.offset} onClick={() => change(Math.max(0, page.offset - page.limit))}>上一页</Button>
    <span>第 {Math.floor(page.offset / page.limit) + 1} 页</span>
    <Button className="button-secondary" disabled={busy || !page.has_more} onClick={() => change(page.next_offset)}>下一页</Button>
  </nav>
}

export function AdminPage() {
  const { confirm, dialog: confirmationDialog } = useConfirm()
  const auth = useAuth()
  const [tab, setTab] = useState<Tab>('users')
  const [offset, setOffset] = useState(0)
  const [page, setPage] = useState(initialPage)
  const [users, setUsers] = useState<AdminUser[]>([])
  const [invites, setInvites] = useState<Invite[]>([])
  const [events, setEvents] = useState<AuditEntry[]>([])
  const [selected, setSelected] = useState<AdminUser | null>(null)
  const [sessions, setSessions] = useState<LoginSession[]>([])
  const [sessionOffset, setSessionOffset] = useState(0)
  const [sessionPage, setSessionPage] = useState(initialPage)
  const [loading, setLoading] = useState(true)
  const [sessionsLoading, setSessionsLoading] = useState(false)
  const [error, setError] = useState('')
  const [sessionsError, setSessionsError] = useState('')
  const [pending, setPending] = useState('')
  const [revision, setRevision] = useState(0)
  const [code, setCode] = useState('')
  const [copied, setCopied] = useState(false)
  const [filters, setFilters] = useState<Record<string, string>>({})
  const [filterDraft, setFilterDraft] = useState({ user_id: '', action: '', since: '', until: '' })
  const [settings, setSettings] = useState<InstanceSettings | null>(null)
  const [settingsError, setSettingsError] = useState('')
  const [settingsLoading, setSettingsLoading] = useState(true)
  const [userFilters, setUserFilters] = useState<Record<string, string>>({})
  const [userFilterDraft, setUserFilterDraft] = useState({ q: '', role: '', status: '' })

  useEffect(() => {
    let active = true
    setSettingsLoading(true); setSettingsError('')
    void api.adminSettings().then(({ settings }) => { if (active) setSettings(settings) })
      .catch((reason) => { if (active) setSettingsError(reason instanceof Error ? reason.message : '无法读取注册设置') })
      .finally(() => { if (active) setSettingsLoading(false) })
    return () => { active = false }
  }, [revision])

  useEffect(() => {
    let active = true
    setLoading(true); setError('')
    const load = async () => {
      try {
        if (tab === 'users') { const result = await api.adminUsers(offset, userFilters); if (active) { setUsers(result.users); setPage(result) } }
        if (tab === 'invites') { const result = await api.adminInvites(offset); if (active) { setInvites(result.invites); setPage(result) } }
        if (tab === 'audit') { const result = await api.adminAudit(filters, offset); if (active) { setEvents(result.events); setPage(result) } }
      } catch (reason) { if (active) setError(reason instanceof Error ? reason.message : '读取失败，请重试') }
      finally { if (active) setLoading(false) }
    }
    void load()
    return () => { active = false }
  }, [tab, offset, filters, revision, userFilters])

  useEffect(() => {
    if (!selected) return
    let active = true
    setSessionsLoading(true); setSessionsError(''); setSessions([])
    void api.adminSessions(selected.id, sessionOffset).then((result) => {
      if (active) { setSessions(result.sessions); setSessionPage(result) }
    }).catch((reason) => { if (active) setSessionsError(reason instanceof Error ? reason.message : '无法读取登录会话') })
      .finally(() => { if (active) setSessionsLoading(false) })
    return () => { active = false }
  }, [selected, sessionOffset, revision])

  const mutate = async (key: string, action: () => Promise<unknown>, endsCurrentLogin = false) => {
    if (pending) return
    const epoch = authenticationGeneration()
    setPending(key); setError('')
    try {
      await action()
      if (endsCurrentLogin) invalidateAuthentication(epoch)
      else setRevision((value) => value + 1)
    } catch (reason) { setError(reason instanceof Error ? reason.message : '操作未完成，请重试') }
    finally { setPending('') }
  }
  const changeTab = (value: Tab) => { setTab(value); setOffset(0); setSelected(null); setCode(''); setError('') }
  const disable = async (user: AdminUser) => {
    if (!await confirm(user.disabled ? `重新启用“${user.display_name}”？该用户需要重新登录。` : `禁用“${user.display_name}”？将注销其全部 Web 登录（当前列表显示 ${user.active_sessions} 条）并关闭通知。远程 Herdr 和任务继续运行。`, { title: user.disabled ? '启用账号' : '禁用账号', confirmLabel: user.disabled ? '启用账号' : '禁用账号', danger: !user.disabled })) return
    void mutate(user.id, () => api.setUserDisabled(user.id, !user.disabled))
  }
  const revoke = async (user: AdminUser, session?: LoginSession) => {
    if (!await confirm(`注销“${user.display_name}”的${session ? '这条' : `全部 Web 登录（当前列表显示 ${user.active_sessions} 条）`} ${session ? 'Web 登录' : ''}？对应的工作台连接将断开，远程任务继续运行。`, { title: '注销 Web 登录', confirmLabel: session ? '注销此登录' : '注销全部登录' })) return
    void mutate(session?.id || 'all-sessions', () => session ? api.revokeSession(user.id, session.id) : api.revokeAllSessions(user.id),
      user.id === auth.user?.id && (!session || session.id === auth.sessionID))
  }
  const createInvite = () => void mutate('create-invite', async () => { const result = await api.createInvite(); setCode(result.code); setCopied(false) })
  const toggleRegistration = () => {
    if (!settings || pending || settingsLoading) return
    const registration = settings.registration === 'closed' ? 'invite' : 'closed'
    void mutate('registration', async () => {
      const result = await api.setRegistration(registration, settings.revision)
      setSettings(result.settings)
      if (registration === 'closed') setCode('')
      await auth.refresh()
    })
  }
  const applyFilters = (event: FormEvent) => {
    event.preventDefault()
    const next: Record<string, string> = {}
    for (const [key, value] of Object.entries(filterDraft)) if (value) next[key] = key === 'since' || key === 'until' ? new Date(value).toISOString() : value.trim()
    setFilters(next); setOffset(0)
  }

  return <div className="app-shell">
    <header className="topbar"><a className="brand" href="/" onClick={(event) => { event.preventDefault(); navigate('/') }}><BrandLogo/></a><div className="topbar-actions"><AppearanceToggle/><span className="user-chip"><span data-tooltip={auth.user?.display_name}>{auth.user?.display_name}</span><small>管理员</small></span><Button className="button-ghost" onClick={() => navigate('/')}><ArrowLeft size={16}/>返回主机</Button></div></header>
    <main className="page page-admin">
      <div className="page-heading"><div><h1>访问管理</h1><p>管理注册、用户与登录访问。</p></div><Button className="button-secondary" pending={loading || settingsLoading} disabled={Boolean(pending)} onClick={() => setRevision((value) => value + 1)}><RefreshCw size={16}/>刷新</Button></div>
      <section className="admin-registration" aria-label="注册设置">
        <div><h2>用户注册</h2><p>{settingsLoading ? '正在读取注册状态…' : settings?.registration === 'invite' ? '已开启邀请注册：新用户需要管理员提供的一次性邀请码，注册后为普通用户。' : '已关闭注册：新用户无法创建账号，已有用户可正常登录。'}</p><small>本机 Herdr 仅管理员可用。注册设置会保留到下次修改。</small>{settingsError && <p role="alert" className="notice notice-error">{settingsError}</p>}</div>
        <Button className={settings?.registration === 'invite' ? 'button-secondary' : 'button-primary'} pending={pending === 'registration'} disabled={Boolean(pending) || settingsLoading || Boolean(settingsError) || !settings} onClick={toggleRegistration}>{settings?.registration === 'invite' ? '关闭注册' : '开启注册'}</Button>
      </section>
      <nav className="admin-tabs" aria-label="管理栏目">{([['users', '用户'], ['invites', '邀请'], ['audit', '审计记录']] as const).map(([value, label]) => <Button key={value} className={tab === value ? 'button-primary' : 'button-ghost'} aria-current={tab === value ? 'page' : undefined} onClick={() => changeTab(value)}>{label}</Button>)}</nav>
      {error && <div className="notice notice-error" role="alert">{error}</div>}
      {tab === 'users' && <Form className="admin-filters" aria-label="用户筛选" onSubmit={(event) => { event.preventDefault(); setUserFilters(Object.fromEntries(Object.entries(userFilterDraft).filter(([, value]) => value.trim()).map(([key, value]) => [key, value.trim()]))); setOffset(0); setSelected(null) }}>
        <Field label="搜索用户" placeholder="邮箱或显示名称" maxLength={254} value={userFilterDraft.q} onChange={(event) => setUserFilterDraft({ ...userFilterDraft, q: event.target.value })}/>
        <label className="field"><span className="field-label">角色</span><Select aria-label="角色" className="input" value={userFilterDraft.role} onChange={(event) => setUserFilterDraft({ ...userFilterDraft, role: event.target.value })}><SelectOption value="">全部角色</SelectOption><SelectOption value="admin">管理员</SelectOption><SelectOption value="user">普通用户</SelectOption></Select></label>
        <label className="field"><span className="field-label">账号状态</span><Select aria-label="账号状态" className="input" value={userFilterDraft.status} onChange={(event) => setUserFilterDraft({ ...userFilterDraft, status: event.target.value })}><SelectOption value="">全部状态</SelectOption><SelectOption value="enabled">已启用</SelectOption><SelectOption value="disabled">已禁用</SelectOption></Select></label>
        <Button type="submit" className="button-secondary" disabled={loading}>筛选用户</Button>
      </Form>}
      {tab === 'audit' && <Form className="admin-filters" onSubmit={applyFilters}>
        <Field label="操作人 ID" value={filterDraft.user_id} onChange={(event) => setFilterDraft({ ...filterDraft, user_id: event.target.value })}/>
        <Field label="操作类型" placeholder="例如 user.disabled" value={filterDraft.action} onChange={(event) => setFilterDraft({ ...filterDraft, action: event.target.value })}/>
        <DateTimeField label="开始时间" value={filterDraft.since} onChange={(event) => setFilterDraft({ ...filterDraft, since: event.target.value })}/>
        <DateTimeField label="结束时间" value={filterDraft.until} onChange={(event) => setFilterDraft({ ...filterDraft, until: event.target.value })}/>
        <Button type="submit" className="button-secondary" disabled={loading}>筛选</Button>
      </Form>}
      {tab === 'invites' && <div className="admin-invite-controls"><p>{settings?.registration !== 'invite' ? '当前已关闭注册，先在上方开启注册后再创建邀请。' : '邀请码 7 天有效，成功注册后失效。'}</p><Button className="button-primary" pending={pending === 'create-invite'} disabled={Boolean(pending) || settingsLoading || Boolean(settingsError) || settings?.registration !== 'invite'} onClick={createInvite}>创建邀请</Button></div>}
      {tab === 'invites' && code && <section className="admin-code" aria-label="新邀请码"><div className="modal-header"><h2>请保存这份邀请码</h2><Button className="icon-button" aria-label="隐藏邀请码" onClick={() => setCode('')}><X size={18}/></Button></div><p>原文只显示这一次，离开此栏目后无法再次查看。</p><pre className="key-block">{code}</pre><Button className="button-secondary" onClick={async () => { try { await navigator.clipboard.writeText(code); setCopied(true) } catch { setError('无法自动复制，请手动选中邀请码复制') } }}><Copy size={16}/>{copied ? '已复制' : '复制邀请码'}</Button></section>}
      {loading ? <p role="status">正在读取…</p> : !error && <>
        <div className="admin-list">
          {tab === 'users' && (users.length ? users.map((user) => <article className="admin-row admin-user-row" key={user.id}>
            <div className="admin-row-main"><h2>{user.display_name} {user.id === auth.user?.id && <small>当前账号</small>}</h2><p>{user.email}</p><details className="admin-user-details"><summary>账号详情</summary><small className="admin-id">{user.id}</small><p>创建于 {date(user.created_at)}</p></details></div>
            <div className="admin-user-state"><span className="role-badge">{user.role === 'admin' ? '管理员' : '普通用户'}</span><span className={`account-state ${user.disabled ? 'account-disabled' : ''}`}>{user.disabled ? '已禁用' : '可登录'}</span><p>{user.active_sessions} 条有效登录</p></div>
            <div className="admin-row-actions"><Button className="button-secondary" onClick={() => { setSelected(user); setSessionOffset(0) }}>查看登录</Button><Button className={user.disabled ? 'button-secondary' : 'button-ghost danger-button'} disabled={Boolean(pending) || user.id === auth.user?.id} data-tooltip={user.id === auth.user?.id ? '不能禁用当前管理员' : undefined} pending={pending === user.id} onClick={() => disable(user)}>{user.disabled ? '启用账号' : '禁用账号'}</Button></div>
          </article>) : <p className="admin-empty">没有用户记录。</p>)}
          {tab === 'invites' && (invites.length ? invites.map((invite) => <article className="admin-row" key={invite.id}><div className="admin-row-main"><h2>{statusLabel[invite.status]}</h2><small className="admin-id">{invite.id}</small><p>创建于 {date(invite.created_at)} · 到期 {date(invite.expires_at)}</p><p>创建人：{invite.created_by}</p>{invite.used_by && <p>使用人：{invite.used_by} · {date(invite.used_at)}</p>}{invite.revoked_by && <p>撤销人：{invite.revoked_by} · {date(invite.revoked_at)}</p>}</div><Button className="button-ghost danger-button" disabled={Boolean(pending) || invite.status !== 'active'} pending={pending === invite.id} onClick={async () => { if (await confirm('撤销这个尚未使用的邀请码？', { title: '撤销邀请', confirmLabel: '撤销邀请' })) void mutate(invite.id, () => api.revokeInvite(invite.id)) }}>撤销邀请</Button></article>) : <p className="admin-empty">还没有邀请。</p>)}
          {tab === 'audit' && (events.length ? events.map((event) => <article className="admin-row" key={event.id}><div className="admin-row-main"><h2>{actionLabel[event.action] || event.action}</h2><p>{date(event.created_at)} · 来源 {event.remote_ip || '—'}</p><p>操作人：{event.user_id || '未登录'} · 目标：{event.target_type} {event.target_id || '—'}</p><details><summary>查看记录详情</summary><pre>{JSON.stringify(event.details, null, 2)}</pre></details></div></article>) : <p className="admin-empty">没有符合条件的审计记录。</p>)}
        </div>
        <Pager page={page} busy={Boolean(pending)} change={setOffset}/>
      </>}
      {tab === 'users' && selected && <section className="admin-session-panel" aria-label={`${selected.display_name}的登录会话`}>
        <div className="modal-header"><h2>{selected.display_name}的 Web 登录</h2><Button className="icon-button" aria-label="收起登录会话" onClick={() => setSelected(null)}><X size={18}/></Button></div>
        <p>注销登录会断开对应的工作台连接，远程任务继续运行。</p>
        <Button className="button-secondary danger-button" pending={pending === 'all-sessions'} disabled={Boolean(pending) || sessionsLoading || !sessions.length} onClick={() => revoke(selected)}>注销全部登录</Button>
        {sessionsError && <p className="notice notice-error" role="alert">{sessionsError}</p>}
        {sessionsLoading ? <p role="status">正在读取登录会话…</p> : !sessionsError && <><div className="admin-list">{sessions.length ? sessions.map((session) => <article className="admin-row" key={session.id}><div className="admin-row-main"><h3>{session.id === auth.sessionID ? '当前登录' : 'Web 登录'}</h3><p>{session.user_agent || '未知客户端'}</p><p>来源 {session.remote_ip || '—'}</p><p>登录于 {date(session.created_at)} · 到期 {date(session.expires_at)}</p></div><Button className="button-ghost danger-button" disabled={Boolean(pending)} pending={pending === session.id} onClick={() => revoke(selected, session)}>注销此登录</Button></article>) : <p className="admin-empty">没有有效的 Web 登录。</p>}</div><Pager page={sessionPage} busy={Boolean(pending)} change={setSessionOffset}/></>}
      </section>}
    </main>
    {confirmationDialog}
  </div>
}
