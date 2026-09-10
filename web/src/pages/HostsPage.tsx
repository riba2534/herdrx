import { useConfirm } from '../components/useConfirm'
import { Input, Form, Textarea } from '../components/Form'
import { Select, SelectOption } from '../components/Select'
import { BrandLogo } from '../components/Brand'
import { useEffect, useRef, useState, type FormEvent } from 'react'
import { ArrowRight, Copy, FolderInput, KeyRound, Settings2, Laptop, LogOut, MoreHorizontal, Pencil, Plus, Search, Server, ShieldAlert, Trash2, ShieldCheck } from 'lucide-react'
import { ContextMenu, type ContextMenuItem } from '../components/ContextMenu'
import { useAuth } from '../auth'
import { Button, EmptyState } from '../components/ui'
import { HostFolders, folderOptions } from '../components/HostFolders'
import { TailcatSetupGuide } from '../components/TailcatSetupGuide'
import { RelayConnectCommand } from '../components/RelayConnectCommand'
import { Modal } from '../components/Modal'
import { PrivateKeyInput } from '../components/PrivateKeyInput'
import { AppearanceToggle } from '../components/AppearanceToggle'
import { APIError, api } from '../lib/api'
import { navigate } from '../lib/navigation'
import type { Host, HostFolder, SSHKey } from '../types'

type HostDraft = {
  name: string
  transport: 'local' | 'ssh' | 'tailcat'
  hostname: string
  port: number
  username: string
  session_name: string
  proxy_jump: string
  auth_method: 'generated' | 'private_key' | 'private_key_bundle' | 'password' | 'saved_key'
  secret: string
  passphrase: string
  ssh_key_id: string
  folder_id: string
}

const emptyDraft: HostDraft = {
  name: '', transport: 'tailcat', hostname: '', port: 22, username: '', session_name: '', proxy_jump: '', auth_method: 'saved_key', secret: '', passphrase: '', ssh_key_id: '', folder_id: '',
}

function enrollmentErrorMessage(reason: unknown): string {
  if (reason instanceof APIError) {
    switch (reason.code) {
      case 'invalid_connection_string':
      case 'invalid_input':
        return '连接字符串无效，请重新从远程主机复制 herdrx connect --plain 的输出'
      case 'ssrf_violation':
        return '连接地址未通过安全检查，可能包含不允许的中继地址'
      case 'enrollment_conflict':
        return '该主机正在用另一份身份配对，请先在远程主机执行 herdrx unpair'
      case 'task_not_found':
        return '配对任务不存在或无权查看'
      default:
        return reason.message || '导入失败'
    }
  }
  return reason instanceof Error ? reason.message : '连接失败'
}

export function HostsPage() {
  const { confirm, dialog: confirmationDialog } = useConfirm()
  const auth = useAuth()
  const TASK_KEY = `herdrx.enrollmentTask.${auth.user?.id}`
  const [hosts, setHosts] = useState<Host[]>([])
  const [folders, setFolders] = useState<HostFolder[]>([])
  const [keys, setKeys] = useState<SSHKey[]>([])
  const [selectedFolder, setSelectedFolder] = useState('*')
  const [editingHost, setEditingHost] = useState<Host | null>(null)
  const [movingHost, setMovingHost] = useState<Host | null>(null)
  const [moveFolder, setMoveFolder] = useState('')
  const [movePending, setMovePending] = useState(false)
  const [moveError, setMoveError] = useState('')
  const options = folderOptions(folders)
  const [query, setQuery] = useState('')
  const [loading, setLoading] = useState(true)
  const [showAdd, setShowAdd] = useState(false)
  const [draft, setDraft] = useState<HostDraft>(emptyDraft)
  const [pending, setPending] = useState(false)
  const [error, setError] = useState('')
  const [publicKey, setPublicKey] = useState('')
  const [signingOut, setSigningOut] = useState(false)
  const [moreMenu, setMoreMenu] = useState<{ x: number; y: number } | null>(null)
  const [connectionString, setConnectionString] = useState('')
  const [enrollmentProgress, setEnrollmentProgress] = useState<string | null>(null)
  const [formError, setFormError] = useState('')
  const [enrollmentTaskID, setEnrollmentTaskID] = useState('')
  const addTrigger = useRef<HTMLButtonElement | null>(null)
  const connInput = useRef<HTMLTextAreaElement | null>(null)
  const [renamingID, setRenamingID] = useState<string | null>(null)
  const [renameDraft, setRenameDraft] = useState('')
  const [renamePending, setRenamePending] = useState(false)
  const [renameError, setRenameError] = useState('')
  const renameTrigger = useRef<HTMLButtonElement | null>(null)

  const [endpointHost, setEndpointHost] = useState<Host | null>(null)
  const [endpointUpdate, setEndpointUpdate] = useState('')
  const [endpointPending, setEndpointPending] = useState(false)
  const [endpointError, setEndpointError] = useState('')
  const importEndpoint = async (event: FormEvent) => {
    event.preventDefault()
    if (!endpointHost || endpointPending) return
    setEndpointPending(true)
    setEndpointError('')
    try {
      await api.refreshEndpoint(endpointHost.id, endpointUpdate.trim())
      setEndpointHost(null)
      setEndpointUpdate('')
      await load()
    } catch (reason) {
      setEndpointError(reason instanceof Error ? reason.message : '无法导入，请检查更新包后重试')
    } finally { setEndpointPending(false) }
  }

  const cancelRename = () => {
    setRenamingID(null)
    setRenameError('')
  }

  useEffect(() => { if (renamingID === null && !renamePending) renameTrigger.current?.focus() }, [renamingID, renamePending])

  const rename = async (event: FormEvent, host: Host) => {
    event.preventDefault()
    if (renamePending) return
    const name = renameDraft.trim()
    if (!name || Array.from(name).length > 80 || Array.from(name).some((char) => { const code = char.codePointAt(0)!; return code < 32 || (code >= 127 && code <= 159) })) {
      setRenameError('名称须为 1–80 个字符，不能包含换行或控制字符')
      return
    }
    setRenamePending(true)
    setRenameError('')
    try {
      const result = await api.renameHost(host.id, name)
      setHosts((current) => current.map((item) => item.id === host.id ? result.host : item))
      cancelRename()
    } catch (reason) {
      setRenameError(reason instanceof Error ? reason.message : '无法保存名称，请重试')
    } finally {
      setRenamePending(false)
    }
  }

  const load = async () => {
    setLoading(true)
    try {
      const [hostResult, folderResult, keyResult] = await Promise.all([api.hosts(), api.hostFolders(), api.sshKeys()])
      setHosts(hostResult.hosts ?? []); setFolders(folderResult.folders ?? []); setKeys(keyResult.keys ?? []); setError('')
    }
    catch (reason) { setError(reason instanceof Error ? reason.message : '无法读取主机') }
    finally { setLoading(false) }
  }
  useEffect(() => { void load() }, [])

  const closeAdd = () => {
    setShowAdd(false)
    setEditingHost(null)
    setDraft(emptyDraft)
    setConnectionString('')
    setEnrollmentProgress(null)
    setEnrollmentTaskID('')
    setFormError('')
    setPending(false)
    queueMicrotask(() => addTrigger.current?.focus())
  }

  const pollEnrollment = async (taskId: string) => {
    const labels: Record<string, string> = {
      verifying: '正在校验连接串格式与安全边界...',
      connecting: '正在建立临时安全隧道...',
      preparing: '正在与受控端协商绑定预留...',
      committing: '正在验证正式密钥证明并激活绑定...',
      active: '绑定成功，正在进入工作台...',
    }
    for (let i = 0; i < 80; i++) {
      const task = await api.getTailcatEnrollment(taskId)
      setEnrollmentProgress(labels[task.status] || task.status)
      if (task.status === 'active' && task.host_id) {
        sessionStorage.removeItem(TASK_KEY)
        setConnectionString('')
        closeAdd()
        navigate(`/h/${task.host_id}`)
        return
      }
      if (task.status === 'failed') {
        sessionStorage.removeItem(TASK_KEY)
        throw new Error(task.error || '导入失败，请检查连接串是否有效')
      }
      await new Promise((resolve) => setTimeout(resolve, 800))
    }
    throw new Error('仍在配对中，可保持此页面或刷新后续接，无需重新粘贴连接串')
  }

  useEffect(() => {
    const raw = sessionStorage.getItem(TASK_KEY)
    if (!raw) return
    try {
      const saved = JSON.parse(raw) as { id?: string }
      if (!saved.id) return
      setShowAdd(true)
      setEnrollmentTaskID(saved.id)
      setEnrollmentProgress('正在恢复未完成的配对任务...')
      setPending(true)
      void pollEnrollment(saved.id).catch((reason) => {
        setFormError(enrollmentErrorMessage(reason))
        setEnrollmentProgress(null)
      }).finally(() => setPending(false))
    } catch {
      sessionStorage.removeItem(TASK_KEY)
    }
  }, [])

  const submit = async (event: FormEvent) => {
    event.preventDefault()
    if (pending) return
    setPending(true)
    setError('')
    setFormError('')
    try {
      if (draft.transport === 'tailcat') {
        const connStr = connectionString.trim()
        if (!connStr) {
          setFormError('请粘贴在受控机运行 herdrx connect 获取的连接字符串')
          setPending(false)
          connInput.current?.focus()
          return
        }
        setFormError('')
        setEnrollmentProgress('正在提交连接串并校验安全边界...')
        try {
          const res = await api.createTailcatEnrollment({
            connection_string: connStr,
            name: draft.name.trim() || undefined,
            session_name: draft.session_name.trim() || undefined,
          })
          setConnectionString('')
          if (res.status === 'active' && res.host_id) {
            sessionStorage.removeItem(TASK_KEY)
            closeAdd()
            navigate(`/h/${res.host_id}`)
            return
          }
          const taskId = res.task_id || res.id
          if (!taskId) throw new Error('服务器未返回配对任务')
          setEnrollmentTaskID(taskId)
          sessionStorage.setItem(TASK_KEY, JSON.stringify({ id: taskId, agent_id: res.agent_id }))
          await pollEnrollment(taskId)
        } catch (reason) {
          setFormError(enrollmentErrorMessage(reason))
          setEnrollmentProgress(null)
          connInput.current?.focus()
        } finally {
          setPending(false)
        }
        return
      }
      const body = { ...draft, keep_secret: Boolean(editingHost && draft.auth_method === editingHost.auth_method && draft.auth_method !== 'saved_key' && !draft.secret && !draft.passphrase) }
      const result = editingHost ? await api.updateSSHHost(editingHost.id, body) : await api.createHost(body)
      setPublicKey(result.public_key || '')
      closeAdd()
      await load()
    } catch (reason) { setFormError(reason instanceof Error ? reason.message : '无法保存主机') }
    finally { setPending(false) }
  }

  const remove = async (host: Host) => {
    const detail = host.transport === 'tailcat'
      ? '网站会删除连接配置，远程主机上的绑定仍会保留。请在远程主机运行 herdrx unpair 撤销绑定；重新接入时运行 herdrx connect --plain。远程 Herdr 和任务继续运行。'
      : '该主机的连接配置会删除，共享密钥会保留。远程 Herdr 和任务继续运行。'
    if (!await confirm(`删除“${host.name}”？${detail}`, { title: '删除主机', confirmLabel: '删除主机' })) return
    try { await api.deleteHost(host.id); await load() }
    catch (reason) { setError(reason instanceof Error ? reason.message : '无法删除主机') }
  }

  const signOut = async () => {
    if (!await confirm('退出后需要重新登录才能打开工作台。远程主机上的 Herdr 和任务会继续运行。', { title: '退出登录', confirmLabel: '退出登录' })) return
    setSigningOut(true)
    setError('')
    try { await auth.signOut() }
    catch (reason) { setError(reason instanceof Error ? reason.message : '退出未完成，请重试') }
    finally { setSigningOut(false) }
  }

  const visibleHosts = hosts.filter((host) => (selectedFolder === '*' || (host.folder_id || '') === selectedFolder) && `${host.name} ${host.hostname || ''} ${host.transport}`.toLocaleLowerCase().includes(query.trim().toLocaleLowerCase()))
  const openAdd = (trigger: HTMLButtonElement) => { addTrigger.current = trigger; setEditingHost(null); setDraft({ ...emptyDraft, folder_id: selectedFolder === '*' ? '' : selectedFolder, ssh_key_id: keys[0]?.id || '' }); setFormError(''); setShowAdd(true) }
  const editSSH = (host: Host, trigger: HTMLButtonElement) => {
    addTrigger.current = trigger; setEditingHost(host)
    setDraft({ ...emptyDraft, name: host.name, transport: 'ssh', hostname: host.hostname || '', port: host.port || 22, username: host.username || '', session_name: host.session_name || '', proxy_jump: host.proxy_jump || '', auth_method: (host.auth_method || 'password') as HostDraft['auth_method'], ssh_key_id: host.ssh_key_id || '', folder_id: host.folder_id || '' })
    setFormError(''); setShowAdd(true)
  }
  const move = async (event: FormEvent) => {
    event.preventDefault(); if (!movingHost || movePending) return
    setMovePending(true); setMoveError('')
    try { const result = await api.moveHost(movingHost.id, moveFolder); setHosts((current) => current.map((host) => host.id === movingHost.id ? result.host : host)); setMovingHost(null) }
    catch (reason) { setMoveError(reason instanceof Error ? reason.message : '无法移动主机') }
    finally { setMovePending(false) }
  }

  return <div className="app-shell">
    <header className="topbar topbar-hosts">
      <a className="brand" href="/" onClick={(event) => { event.preventDefault(); navigate('/') }}><BrandLogo/></a>
      <div className="topbar-actions">
        <AppearanceToggle/>
        <Button className="button-ghost topbar-nav-action" aria-label="密钥" data-tooltip="密钥管理" onClick={() => navigate('/keys')}><KeyRound size={16}/><span className="nav-action-label">密钥</span></Button>
        <span className="user-chip"><span data-tooltip={auth.user?.display_name}>{auth.user?.display_name}</span><small>{auth.user?.role === 'admin' ? '管理员' : '普通用户'}</small></span>
        {auth.user?.role === 'admin' && <Button className="button-ghost topbar-nav-action" aria-label="管理" data-tooltip="访问管理" onClick={() => navigate('/admin')}><ShieldCheck size={16} /><span className="nav-action-label">管理</span></Button>}
        <Button className="button-ghost topbar-nav-action" aria-label="退出" data-tooltip="退出登录" pending={signingOut} onClick={() => void signOut()}><LogOut size={16} /><span className="nav-action-label">退出</span></Button>
        <Button className="button-ghost topbar-more" aria-label="更多" aria-haspopup="menu" aria-expanded={Boolean(moreMenu)} onClick={(event) => {
          const rect = event.currentTarget.getBoundingClientRect()
          setMoreMenu(moreMenu ? null : { x: Math.min(rect.right, window.innerWidth - 8), y: rect.bottom + 4 })
        }}><MoreHorizontal size={16}/></Button>
      </div>
    </header>
    {moreMenu && <ContextMenu x={moreMenu.x} y={moreMenu.y} label="更多" onClose={() => setMoreMenu(null)} items={([
      { id: 'user', label: `${auth.user?.display_name || '当前用户'} · ${auth.user?.role === 'admin' ? '管理员' : '普通用户'}`, disabled: true, onSelect: () => {} },
      { id: 'keys', label: '密钥', icon: <KeyRound size={15}/>, onSelect: () => navigate('/keys') },
      ...(auth.user?.role === 'admin' ? [{ id: 'admin', label: '管理', icon: <ShieldCheck size={15}/>, onSelect: () => navigate('/admin') }] : []),
      { id: 'logout', label: '退出登录', icon: <LogOut size={15}/>, danger: true, separatorBefore: true, onSelect: () => { void signOut() } },
    ] satisfies ContextMenuItem[])}/>}
    <main className="page page-hosts">
      <div className="page-heading">
        <div><h1>主机</h1><p>选择一台主机，继续你的工作。</p></div>
        <Button className="button-primary" onClick={(event) => openAdd(event.currentTarget)}><Plus size={17} />添加主机</Button>
      </div>
      {error && <div className="notice notice-error" role="alert">{error}</div>}
      <div className="hosts-layout"><HostFolders folders={folders} hosts={hosts} selected={selectedFolder} onSelect={setSelectedFolder} onRefresh={load}/><div className="hosts-content">
      {!loading && hosts.length > 0 && <div className="host-list-toolbar"><span>{selectedFolder === '*' ? '全部主机' : selectedFolder === '' ? '未分组' : options.find((folder) => folder.id === selectedFolder)?.path} · {visibleHosts.length} 台</span><label className="host-search"><Search size={16} aria-hidden="true"/><Input aria-label="搜索主机" placeholder="搜索名称、地址或连接方式" value={query} onChange={(event) => setQuery(event.target.value)}/></label></div>}
      {loading ? <div className="skeleton-grid"><i/><i/><i/></div> : hosts.length === 0 ?
        <EmptyState icon={<Server size={32} />} title="还没有主机" detail="按引导安装 herdrx 并绑定远程主机，也可以使用 SSH 接入。" action={<Button className="button-primary" onClick={(event) => openAdd(event.currentTarget)}><Plus size={17}/>添加第一台主机</Button>} /> :
        visibleHosts.length === 0 ? <div className="host-search-empty"><h2>{query ? '没有匹配的主机' : '此文件夹还没有主机'}</h2><p>{query ? '试试其他名称或连接方式。' : '添加主机，或从其他文件夹移入主机。'}</p>{query && <Button className="button-secondary" onClick={() => setQuery('')}>清除搜索</Button>}</div> :
        <div className="host-grid">{visibleHosts.map((host) => <article className={`host-card ${renamingID === host.id ? 'host-card-renaming' : ''}`} key={host.id}>
          <div className="host-card-top">
            <span className="host-icon">{host.transport === 'local' ? <Laptop size={20}/> : <Server size={20}/>}</span>
            <span className={`transport-badge transport-${host.transport}`}>{host.transport === 'local' ? '本机' : host.transport === 'tailcat' ? 'Tailcat' : 'SSH'}</span>
          </div>
          {renamingID === host.id ? <Form className="host-rename-form form-stack" aria-label="重命名主机" onSubmit={(event) => void rename(event, host)} onKeyDown={(event) => { if (event.key === 'Escape' && !renamePending) { event.preventDefault(); cancelRename() } }}>
            <label className="field"><span className="field-label">主机名称</span><Input aria-label="主机名称" className={renameError ? 'input input-error' : 'input'} value={renameDraft} onChange={(event) => setRenameDraft(event.target.value)} autoFocus onFocus={(event) => event.target.select()} autoComplete="off" disabled={renamePending} aria-invalid={Boolean(renameError)} aria-describedby={renameError ? 'host-rename-error' : 'host-rename-hint'}/></label>
            <small id="host-rename-hint" className="field-hint">修改显示名称，不影响连接地址和凭据。</small>
            {renameError && <span id="host-rename-error" className="field-error" role="alert">{renameError}</span>}
            <div className="host-rename-actions"><Button type="submit" className="button-primary" pending={renamePending}>保存名称</Button><Button type="button" className="button-ghost" disabled={renamePending} onClick={cancelRename}>取消</Button></div>
          </Form> : <h2 data-tooltip={host.name}>{host.name}</h2>}
          <p className="host-address">{host.transport === 'local' ? '工作台主机 · 仅管理员可用' : host.transport === 'tailcat' ? 'Tailcat 加密连接' : `${host.username}@${host.hostname}:${host.port}`}{host.folder_id && <span className="host-folder-label">{options.find((folder) => folder.id === host.folder_id)?.path}</span>}</p>
          {host.pending_host_key && <div className="host-warning"><ShieldAlert size={15}/><span>首次连接需要确认 SSH 指纹</span></div>}
          <div className="host-card-actions">
            {host.pending_host_key && <Button className="button-secondary" onClick={async () => { await api.trustHostKey(host.id); await load() }}>确认指纹</Button>}
            <Button className="button-secondary host-open" onClick={() => navigate(`/h/${host.id}`)}>打开<ArrowRight size={16}/></Button>
            <Button className="button-ghost" aria-label={`重命名 ${host.name}`} data-tooltip="重命名主机" disabled={renamePending} onClick={(event) => { renameTrigger.current = event.currentTarget; setRenamingID(host.id); setRenameDraft(host.name); setRenameError('') }}><Pencil size={15}/>重命名</Button>
            {host.transport === 'tailcat' && <Button className="icon-button" aria-label={`更新连接端点 ${host.name}`} data-tooltip="更新连接端点" onClick={() => { setEndpointHost(host); setEndpointUpdate(''); setEndpointError('') }}><Settings2 size={16}/></Button>}
            {host.transport === 'ssh' && <Button className="icon-button" aria-label={`连接设置 ${host.name}`} data-tooltip="SSH 连接设置" onClick={(event) => editSSH(host, event.currentTarget)}><Settings2 size={16}/></Button>}
            <Button className="icon-button" aria-label={`移动 ${host.name}`} data-tooltip="移动到文件夹" onClick={() => { setMovingHost(host); setMoveFolder(host.folder_id || ''); setMoveError('') }}><FolderInput size={16}/></Button>
            <Button className="icon-button danger-button" aria-label={`删除 ${host.name}`} data-tooltip="删除主机" onClick={() => void remove(host)}><Trash2 size={16}/></Button>
          </div>
        </article>)}</div>}
      </div></div>
    </main>

    {showAdd && <Modal title={editingHost ? 'SSH 连接设置' : '添加主机'} busy={pending} onClose={closeAdd} className={draft.transport === 'tailcat' ? 'tailcat-modal' : ''}>
        <Form onSubmit={submit} className="form-stack">
          <label className="field"><span className="field-label">连接方式</span><Select aria-label="连接方式" className="input" value={draft.transport} disabled={Boolean(editingHost) || pending} onChange={(event) => setDraft({ ...draft, transport: event.target.value as HostDraft['transport'] })}><SelectOption value="tailcat">Tailcat 内网穿透（推荐）</SelectOption><SelectOption value="ssh">SSH</SelectOption>{auth.user?.role === 'admin' && <SelectOption value="local">本机 Herdr</SelectOption>}</Select></label>
          {draft.transport !== 'tailcat' && <label className="field"><span className="field-label">名称</span><Input aria-label="名称" className="input" value={draft.name} onChange={(event) => setDraft({ ...draft, name: event.target.value })} required /></label>}
          {draft.transport === 'local' && <p className="field-hint">本机接入要求网站和 Herdr 以同一用户原生运行。Docker 部署请使用 SSH 或 Tailcat 连接宿主机及其他远程主机。</p>}
          {draft.transport === 'tailcat' && <TailcatSetupGuide pending={pending} resuming={Boolean(enrollmentTaskID || enrollmentProgress)} onClose={closeAdd}>
            <label className="field"><span className="field-label">绑定凭据（herdrx://v1/...）</span><Textarea aria-label="绑定凭据（herdrx://v1/...）" ref={connInput} className={formError ? 'input textarea input-error' : 'input textarea'} value={connectionString} onChange={(event) => setConnectionString(event.target.value)} placeholder="粘贴 herdrx connect --plain 生成的完整内容" rows={3} required disabled={pending} autoComplete="off" spellCheck={false} aria-invalid={Boolean(formError)} aria-describedby={formError ? 'tailcat-enroll-error' : 'tailcat-enroll-hint'} /></label>
            <small id="tailcat-enroll-hint" className="field-hint">凭据 10 分钟内有效，只能绑定一次，请勿分享。受理后立即清除；刷新页面可继续未完成的绑定。</small>
            <label className="field"><span className="field-label">主机名称（可选）</span><Input aria-label="主机名称（可选）" className="input" value={draft.name} disabled={pending} onChange={(event) => setDraft({ ...draft, name: event.target.value })} placeholder="留空使用远程主机默认名称" /></label>
            <label className="field"><span className="field-label">Herdr 命名会话（可选）</span><Input aria-label="Herdr 命名会话（可选）" className="input" value={draft.session_name} disabled={pending} onChange={(event) => setDraft({ ...draft, session_name: event.target.value })} placeholder="默认会话" /></label>
            {enrollmentProgress && <div className="notice notice-info" role="status" aria-live="polite"><span className="spinner-inline" />{enrollmentProgress}</div>}
            {formError && <span id="tailcat-enroll-error" className="field-error" role="alert">{formError}</span>}
          </TailcatSetupGuide>}
          {draft.transport === 'ssh' && <>
            <div className="field-row"><label className="field field-grow"><span className="field-label">主机名或 IP</span><Input aria-label="主机名或 IP" className="input" value={draft.hostname} onChange={(event) => setDraft({ ...draft, hostname: event.target.value })} required /></label><label className="field field-port"><span className="field-label">端口</span><Input aria-label="端口" className="input" type="number" min="1" max="65535" value={draft.port} onChange={(event) => setDraft({ ...draft, port: Number(event.target.value) })} required /></label></div>
            <label className="field"><span className="field-label">SSH 用户</span><Input aria-label="SSH 用户" className="input" autoComplete="username" value={draft.username} onChange={(event) => setDraft({ ...draft, username: event.target.value })} required /></label>
            <label className="field"><span className="field-label">跳板机 ProxyJump（可选）</span><Input aria-label="跳板机 ProxyJump（可选）" className="input" value={draft.proxy_jump} onChange={(event) => setDraft({ ...draft, proxy_jump: event.target.value })} placeholder="user@jump-host:22" disabled={pending} /></label>
            <p className="field-hint">单跳跳板；跳板与目标共用上方认证（密钥/密码）。不支持多跳、ProxyCommand 脚本或 agent 转发。</p>
            <label className="field"><span className="field-label">认证</span><Select aria-label="认证" className="input" value={draft.auth_method} disabled={pending} onChange={(event) => setDraft({ ...draft, auth_method: event.target.value as HostDraft['auth_method'], secret: '', passphrase: '' })}><SelectOption value="saved_key">选择已保存密钥（公钥认证）</SelectOption><SelectOption value="password">密码认证</SelectOption><SelectOption value="private_key">为此主机粘贴或导入私钥</SelectOption>{editingHost?.auth_method === 'private_key_bundle' && <SelectOption value="private_key_bundle">已保存的主机私钥</SelectOption>}<SelectOption value="generated">为此主机生成独立 Ed25519 密钥</SelectOption></Select></label>
            {draft.auth_method === 'saved_key' && <><label className="field"><span className="field-label">认证密钥</span><Select aria-label="认证密钥" className="input" value={draft.ssh_key_id} onChange={(event) => setDraft({ ...draft, ssh_key_id: event.target.value })} required disabled={pending}><SelectOption value="">请选择密钥</SelectOption>{keys.map((key) => <SelectOption key={key.id} value={key.id}>{key.name} · {key.algorithm.replace('ssh-', '')}</SelectOption>)}</Select></label><p className="field-hint">{keys.find((key) => key.id === draft.ssh_key_id)?.fingerprint || '还没有密钥？先在密钥页面导入或生成。'}</p><a className="inline-link" href="/keys" target="_blank" rel="noopener noreferrer">管理密钥（新标签页）</a><Button type="button" className="button-ghost" onClick={async () => { try { setKeys((await api.sshKeys()).keys) } catch (reason) { setFormError(reason instanceof Error ? reason.message : '无法刷新密钥') } }}>刷新密钥列表</Button></>}
            {draft.auth_method === 'password' && <label className="field"><span className="field-label">SSH 密码</span><Input aria-label="SSH 密码" className="input" type="password" autoComplete="new-password" value={draft.secret} onChange={(event) => setDraft({ ...draft, secret: event.target.value })} required={editingHost?.auth_method !== 'password'} placeholder={editingHost?.auth_method === 'password' ? '留空保留已保存的密码' : ''} disabled={pending}/></label>}
            {(draft.auth_method === 'private_key' || draft.auth_method === 'private_key_bundle') && <><PrivateKeyInput value={draft.secret} onChange={(secret) => setDraft((current) => ({ ...current, secret }))} required={editingHost?.auth_method !== draft.auth_method} disabled={pending}/><label className="field"><span className="field-label">私钥口令（可选）</span><Input aria-label="私钥口令（可选）" className="input" type="password" autoComplete="new-password" value={draft.passphrase} onChange={(event) => setDraft({ ...draft, passphrase: event.target.value })} disabled={pending}/></label>{editingHost?.auth_method === draft.auth_method && <small className="field-hint">私钥与口令留空则保留原认证。</small>}</>}

          </>}
          {draft.transport !== 'tailcat' && <label className="field"><span className="field-label">所属文件夹</span><Select aria-label="所属文件夹" className="input" value={draft.folder_id} onChange={(event) => setDraft({ ...draft, folder_id: event.target.value })} disabled={pending}><SelectOption value="">未分组</SelectOption>{options.map((folder) => <SelectOption key={folder.id} value={folder.id}>{folder.path}</SelectOption>)}</Select></label>}
          {editingHost && <p className="field-hint">保存连接配置后，请重新打开该主机。</p>}
          {draft.transport !== 'tailcat' && formError && <p className="field-error" role="alert">{formError}</p>}
          {draft.transport !== 'tailcat' && <><label className="field"><span className="field-label">Herdr 命名会话（可选）</span><Input aria-label="Herdr 命名会话（可选）" className="input" value={draft.session_name} onChange={(event) => setDraft({ ...draft, session_name: event.target.value })} placeholder="默认会话" /></label>
          <div className="modal-actions"><Button type="button" className="button-secondary" disabled={pending} onClick={closeAdd}>取消</Button><Button type="submit" className="button-primary" pending={pending}>保存主机</Button></div></>}
        </Form>
    </Modal>}

    {movingHost && <Modal title={`移动 ${movingHost.name}`} busy={movePending} onClose={() => setMovingHost(null)}><Form className="form-stack" onSubmit={(event) => void move(event)}><label className="field"><span className="field-label">目标文件夹</span><Select aria-label="目标文件夹" className="input" data-initial-focus value={moveFolder} onChange={(event) => setMoveFolder(event.target.value)} disabled={movePending}><SelectOption value="">未分组</SelectOption>{options.map((folder) => <SelectOption key={folder.id} value={folder.id}>{folder.path}</SelectOption>)}</Select></label>{moveError && <p className="field-error" role="alert">{moveError}</p>}<div className="modal-actions"><Button type="button" className="button-secondary" disabled={movePending} onClick={() => setMovingHost(null)}>取消</Button><Button type="submit" className="button-primary" pending={movePending}>移动主机</Button></div></Form></Modal>}
    {publicKey && <Modal title="安装公钥" onClose={() => setPublicKey('')}><p>把下面这一整行加入目标用户的 <code>~/.ssh/authorized_keys</code>，然后打开主机确认指纹。</p><pre className="key-block">{publicKey}</pre><div className="modal-actions"><Button className="button-secondary" onClick={() => void navigator.clipboard.writeText(publicKey)}><Copy size={16}/>复制</Button><Button className="button-primary" onClick={() => setPublicKey('')}>完成</Button></div></Modal>}
    {endpointHost && <Modal title={`更新连接端点 · ${endpointHost.name}`} busy={endpointPending} onClose={() => { setEndpointHost(null); setEndpointUpdate('') }}>
      <p>在这台远程主机上运行下面的命令，将更新包粘贴到这里。原绑定和远程任务会保留。</p>
      <RelayConnectCommand refresh/>
      <Form className="form-stack" onSubmit={(event) => void importEndpoint(event)}>
        <label className="field"><span className="field-label">签名端点更新包</span><Textarea className="input" data-initial-focus aria-label="签名端点更新包" value={endpointUpdate} onChange={(event) => setEndpointUpdate(event.target.value)} maxLength={16384} disabled={endpointPending} autoComplete="off" spellCheck={false} required placeholder="herdrx://endpoint-v1/…"/></label>
        <p className="field-hint">更新包含敏感连接地址，10 分钟内有效，请勿分享。</p>
        {endpointError && <p className="field-error" role="alert">{endpointError}</p>}
        <div className="modal-actions"><Button type="button" className="button-secondary" disabled={endpointPending} onClick={() => { setEndpointHost(null); setEndpointUpdate('') }}>取消</Button><Button type="submit" className="button-primary" pending={endpointPending} disabled={!endpointUpdate.trim()}>导入更新</Button></div>
      </Form>
    </Modal>}
    {confirmationDialog}
  </div>
}
