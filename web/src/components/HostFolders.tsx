import { Form, Input } from './Form'
import { Select, SelectOption } from './Select'
import { useState, type FormEvent } from 'react'
import { Folder, FolderOpen, FolderPlus, Layers, Pencil, Trash2 } from 'lucide-react'
import { api } from '../lib/api'
import type { Host, HostFolder } from '../types'
import { Button } from './ui'
import { Modal } from './Modal'

export function folderOptions(folders: HostFolder[]) {
  const lookup = new Map(folders.map((folder) => [folder.id, folder]))
  return folders.map((folder) => {
    const names = [folder.name], seen = new Set([folder.id])
    let parent = lookup.get(folder.parent_id || '')
    while (parent && !seen.has(parent.id)) { seen.add(parent.id); names.unshift(parent.name); parent = lookup.get(parent.parent_id || '') }
    return { ...folder, path: names.join(' / '), depth: names.length - 1, ancestors: seen }
  }).sort((a, b) => a.path.localeCompare(b.path, 'zh-CN'))
}

export function HostFolders({ folders, hosts, selected, onSelect, onRefresh }: { folders: HostFolder[]; hosts: Host[]; selected: string; onSelect: (id: string) => void; onRefresh: () => Promise<void> }) {
  const [editing, setEditing] = useState<HostFolder | null>(null)
  const [open, setOpen] = useState(false)
  const [name, setName] = useState('')
  const [parent, setParent] = useState('')
  const [pending, setPending] = useState(false)
  const [error, setError] = useState('')
  const [deleting, setDeleting] = useState<HostFolder | null>(null)
  const options = folderOptions(folders)
  const edit = (folder?: HostFolder) => { setEditing(folder || null); setName(folder?.name || ''); setParent(folder ? folder.parent_id || '' : selected === '*' ? '' : selected); setError(''); setOpen(true) }
  const submit = async (event: FormEvent) => {
    event.preventDefault(); if (pending) return
    setPending(true); setError('')
    try {
      if (editing) await api.updateHostFolder(editing.id, name.trim(), parent)
      else await api.createHostFolder(name.trim(), parent)
      await onRefresh(); setOpen(false)
    } catch (reason) { setError(reason instanceof Error ? reason.message : '无法保存文件夹') }
    finally { setPending(false) }
  }
  const remove = async () => {
    if (!deleting || pending) return
    setPending(true); setError('')
    try { await api.deleteHostFolder(deleting.id); if (selected === deleting.id) onSelect(deleting.parent_id || ''); await onRefresh(); setDeleting(null) }
    catch (reason) { setError(reason instanceof Error ? reason.message : '无法删除文件夹') }
    finally { setPending(false) }
  }
  return <aside className="host-folders">
    <div className="folder-heading"><h2>文件夹</h2><Button className="icon-button" data-tooltip="新建文件夹" aria-label="新建文件夹" onClick={() => edit()}><FolderPlus size={17}/></Button></div>
    <nav aria-label="主机文件夹">
      <button className="folder-select" aria-current={selected === '*' ? 'page' : undefined} onClick={() => onSelect('*')}><Layers size={17}/><span>全部主机</span><small>{hosts.length}</small></button>
      <button className="folder-select" aria-current={selected === '' ? 'page' : undefined} onClick={() => onSelect('')}><FolderOpen size={17}/><span>未分组</span><small>{hosts.filter((host) => !host.folder_id).length}</small></button>
      {options.map((folder) => <div className="folder-row" key={folder.id} style={{ paddingLeft: Math.min(folder.depth, 5) * 12 }}>
        <button className="folder-select" aria-current={selected === folder.id ? 'page' : undefined} data-tooltip={folder.path} onClick={() => onSelect(folder.id)}><Folder size={16}/><span>{folder.name}</span><small>{hosts.filter((host) => host.folder_id === folder.id).length}</small></button>
        <Button className="icon-button folder-action" data-tooltip="编辑文件夹" aria-label={`编辑文件夹 ${folder.name}`} onClick={() => edit(folder)}><Pencil size={13}/></Button>
        <Button className="icon-button folder-action" data-tooltip="删除文件夹" aria-label={`删除文件夹 ${folder.name}`} onClick={() => { setDeleting(folder); setError('') }}><Trash2 size={13}/></Button>
      </div>)}
    </nav>
    {open && <Modal title={editing ? '编辑文件夹' : '新建文件夹'} busy={pending} onClose={() => setOpen(false)}><Form className="form-stack" onSubmit={(event) => void submit(event)}>
      <label className="field"><span className="field-label">文件夹名称</span><Input aria-label="文件夹名称" className="input" data-initial-focus value={name} onChange={(event) => setName(event.target.value)} required maxLength={80} disabled={pending}/></label>
      <label className="field"><span className="field-label">上级文件夹</span><Select aria-label="上级文件夹" className="input" value={parent} onChange={(event) => setParent(event.target.value)} disabled={pending}><SelectOption value="">根目录</SelectOption>{options.filter((folder) => !editing || !folder.ancestors.has(editing.id)).map((folder) => <SelectOption key={folder.id} value={folder.id}>{folder.path}</SelectOption>)}</Select></label>
      {error && <p className="field-error" role="alert">{error}</p>}<div className="modal-actions"><Button type="button" className="button-secondary" disabled={pending} onClick={() => setOpen(false)}>取消</Button><Button type="submit" className="button-primary" pending={pending}>保存文件夹</Button></div>
    </Form></Modal>}
    {deleting && <Modal title="删除文件夹" busy={pending} onClose={() => setDeleting(null)}><p>删除“{deleting.name}”？其中的主机和子文件夹会移到上一级，主机连接配置会保留。</p>{error && <p role="alert" className="field-error">{error}</p>}<div className="modal-actions"><Button className="button-secondary" disabled={pending} onClick={() => setDeleting(null)}>取消</Button><Button className="button-danger" pending={pending} onClick={() => void remove()}>删除文件夹</Button></div></Modal>}
  </aside>
}
