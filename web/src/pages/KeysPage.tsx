import { Input, Form, Textarea } from '../components/Form'
import { Select, SelectOption } from '../components/Select'
import { BrandLogo } from '../components/Brand'
import { useEffect, useState, type FormEvent } from 'react'
import { ArrowLeft, Copy, KeyRound, Pencil, Plus, Search, Trash2 } from 'lucide-react'
import { AppearanceToggle } from '../components/AppearanceToggle'
import { Modal } from '../components/Modal'
import { PrivateKeyInput } from '../components/PrivateKeyInput'
import { Button, EmptyState } from '../components/ui'
import { api } from '../lib/api'
import { navigate } from '../lib/navigation'
import type { SSHKey } from '../types'

const freshDraft = { name: '', mode: 'import', privateKey: '', passphrase: '', publicKey: '', certificate: '' }

export function KeysPage() {
  const [keys, setKeys] = useState<SSHKey[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')
  const [notice, setNotice] = useState('')
  const [query, setQuery] = useState('')
  const [editing, setEditing] = useState<SSHKey | null>(null)
  const [open, setOpen] = useState(false)
  const [draft, setDraft] = useState(freshDraft)
  const [pending, setPending] = useState(false)
  const [formError, setFormError] = useState('')
  const [deleting, setDeleting] = useState<SSHKey | null>(null)
  const load = async () => {
    try { setKeys((await api.sshKeys()).keys); setError('') }
    catch (reason) { setError(reason instanceof Error ? reason.message : '无法读取密钥') }
    finally { setLoading(false) }
  }
  useEffect(() => { void load() }, [])
  const close = () => { setOpen(false); setEditing(null); setDraft(freshDraft); setFormError('') }
  const edit = (key?: SSHKey) => { setEditing(key || null); setDraft({ ...freshDraft, name: key?.name || '', certificate: key?.certificate || '' }); setFormError(''); setOpen(true) }
  const submit = async (event: FormEvent) => {
    event.preventDefault()
    if (pending) return
    setPending(true); setFormError('')
    try {
      const body: Record<string, unknown> = { name: draft.name.trim() }
      if (editing) {
        body.revision = editing.revision
        if (draft.privateKey.trim()) { body.private_key = draft.privateKey; body.passphrase = draft.passphrase }
        else if (draft.passphrase) body.passphrase = draft.passphrase
        if (draft.certificate !== (editing.certificate || '')) body.certificate = draft.certificate
        await api.updateSSHKey(editing.id, body)
      } else {
        if (draft.mode === 'generate') body.generate = true
        else Object.assign(body, { private_key: draft.privateKey, passphrase: draft.passphrase, public_key: draft.publicKey, certificate: draft.certificate })
        await api.createSSHKey(body)
      }
      setNotice(editing ? '密钥已保存' : '密钥已添加，可在 SSH 主机的认证选项中选择')
      close(); await load()
    } catch (reason) { setFormError(reason instanceof Error ? reason.message : '无法保存密钥') }
    finally { setPending(false) }
  }
  const remove = async () => {
    if (!deleting || pending) return
    setPending(true); setFormError('')
    try { await api.deleteSSHKey(deleting.id); setDeleting(null); await load() }
    catch (reason) { setFormError(reason instanceof Error ? reason.message : '无法删除密钥') }
    finally { setPending(false) }
  }
  const copy = async (value: string) => {
    try { await navigator.clipboard.writeText(value); setNotice('已复制公钥') }
    catch { setError('复制失败，请展开公钥后手动复制') }
  }
  const filtered = keys.filter((key) => `${key.name} ${key.fingerprint} ${key.algorithm}`.toLocaleLowerCase().includes(query.trim().toLocaleLowerCase()))
  return <div className="app-shell">
    <header className="topbar"><a className="brand" href="/" onClick={(event) => { event.preventDefault(); navigate('/') }}><BrandLogo/></a><div className="topbar-actions"><AppearanceToggle/><Button className="button-ghost" onClick={() => navigate('/')}><ArrowLeft size={16}/>返回主机</Button></div></header>
    <main className="page page-keys">
      <div className="page-heading"><div><h1>密钥</h1><p>保存常用 SSH 密钥，让多台主机共用一份认证。</p></div><Button className="button-primary" onClick={() => edit()}><Plus size={17}/>添加密钥</Button></div>
      {error && <div className="notice notice-error" role="alert">{error}<Button className="button-ghost" onClick={() => void load()}>重试</Button></div>}
      {notice && <p className="field-hint" role="status">{notice}</p>}
      <div className="host-list-toolbar"><span>{keys.length} 把密钥</span><label className="host-search"><Search size={16}/><Input aria-label="搜索密钥" placeholder="搜索名称或指纹" value={query} onChange={(event) => setQuery(event.target.value)}/></label></div>
      {loading ? <div className="skeleton-grid"><i/><i/></div> : keys.length === 0 ? <EmptyState icon={<KeyRound size={32}/>} title="还没有密钥" detail="导入已有私钥，或生成一把新的 Ed25519 密钥。" action={<Button className="button-primary" onClick={() => edit()}>添加第一把密钥</Button>}/> : filtered.length === 0 ? <div className="host-search-empty"><h2>没有匹配的密钥</h2><Button className="button-secondary" onClick={() => setQuery('')}>清除搜索</Button></div> : <div className="key-list">{filtered.map((key) => <article className="key-card" key={key.id}>
        <KeyRound className="key-card-icon" size={22}/><div className="key-card-info"><h2>{key.name}</h2><p>{key.algorithm.replace('ssh-', '')} · {key.host_count} 台主机{key.encrypted ? ' · 有私钥口令' : ''}{key.certificate ? ' · 含 SSH 证书' : ''}</p><code className="key-fingerprint" data-tooltip={key.fingerprint}>{key.fingerprint}</code></div>
        <div className="key-card-actions"><Button className="button-secondary" onClick={() => void copy(key.public_key)}><Copy size={15}/>复制公钥</Button><Button className="icon-button" aria-label={`编辑 ${key.name}`} data-tooltip="编辑密钥" onClick={() => edit(key)}><Pencil size={16}/></Button><Button className="icon-button danger-button" aria-label={`删除 ${key.name}`} data-tooltip="删除密钥" onClick={() => { setDeleting(key); setFormError('') }}><Trash2 size={16}/></Button></div>
        <details className="key-details"><summary>查看公钥{key.certificate ? '与证书' : ''}</summary><p className="field-hint">将公钥添加到远程用户的 <code>~/.ssh/authorized_keys</code>。</p><pre className="key-block">{key.public_key}</pre>{key.certificate && <pre className="key-block">{key.certificate}</pre>}</details>
      </article>)}</div>}
    </main>
    {open && <Modal title={editing ? '编辑密钥' : '添加密钥'} busy={pending} onClose={close} className="key-modal"><Form className="form-stack" onSubmit={(event) => void submit(event)}>
      <label className="field"><span className="field-label">密钥名称</span><Input aria-label="密钥名称" className="input" data-initial-focus value={draft.name} onChange={(event) => setDraft({ ...draft, name: event.target.value })} placeholder="例如：常用开发密钥" maxLength={80} required disabled={pending}/></label>
      {!editing && <label className="field"><span className="field-label">创建方式</span><Select aria-label="创建方式" className="input" value={draft.mode} disabled={pending} onChange={(event) => setDraft({ ...freshDraft, name: draft.name, mode: event.target.value })}><SelectOption value="import">导入已有私钥</SelectOption><SelectOption value="generate">生成 Ed25519 密钥</SelectOption></Select></label>}
      {editing || draft.mode === 'import' ? <>
        {editing && <p className="field-hint">私钥留空则保留原值。替换密钥后，使用它的主机需重新连接。</p>}
        <PrivateKeyInput value={draft.privateKey} onChange={(privateKey) => setDraft((current) => ({ ...current, privateKey }))} required={!editing} disabled={pending}/>
        <label className="field"><span className="field-label">私钥口令（可选）</span><Input aria-label="私钥口令（可选）" className="input" type="password" autoComplete="new-password" value={draft.passphrase} onChange={(event) => setDraft({ ...draft, passphrase: event.target.value })} placeholder="仅用于解锁加密私钥" disabled={pending}/></label>
        <details className="key-advanced"><summary>公钥与 SSH 证书（可选）</summary>{!editing && <label className="field"><span className="field-label">公钥</span><Textarea aria-label="公钥" className="input textarea key-input" rows={2} value={draft.publicKey} onChange={(event) => setDraft({ ...draft, publicKey: event.target.value })} placeholder="留空则从私钥自动提取" disabled={pending} spellCheck={false}/></label>}<label className="field"><span className="field-label">SSH 用户证书</span><Textarea aria-label="SSH 用户证书" className="input textarea key-input" rows={3} value={draft.certificate} onChange={(event) => setDraft({ ...draft, certificate: event.target.value })} placeholder="ssh-ed25519-cert-v01@openssh.com …" disabled={pending} spellCheck={false}/></label></details>
      </> : <p className="field-hint">生成后可复制公钥，安装到需要连接的远程主机上。</p>}
      {formError && <p className="field-error" role="alert">{formError}</p>}
      <div className="modal-actions"><Button type="button" className="button-secondary" disabled={pending} onClick={close}>取消</Button><Button type="submit" className="button-primary" pending={pending}>保存密钥</Button></div>
    </Form></Modal>}
    {deleting && <Modal title="删除密钥" busy={pending} onClose={() => setDeleting(null)}><p>删除“{deleting.name}”？{deleting.host_count > 0 ? `当前有 ${deleting.host_count} 台主机使用此密钥，请先修改主机认证。` : '删除后将无法再用它建立 SSH 连接。'}</p>{formError && <p role="alert" className="field-error">{formError}</p>}<div className="modal-actions"><Button className="button-secondary" onClick={() => setDeleting(null)} disabled={pending}>取消</Button><Button className="button-danger" disabled={deleting.host_count > 0} pending={pending} onClick={() => void remove()}>删除密钥</Button></div></Modal>}
  </div>
}
