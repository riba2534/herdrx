import { useEffect, useRef, useState } from 'react'
import { Pencil } from 'lucide-react'
import { ContextMenu, type ContextMenuItem } from './ContextMenu'
import { Modal } from './Modal'
import { Button } from './ui'
import { Form, Input, Textarea } from './Form'
import { api } from '../lib/api'
import { builtinLaunchers, launcherGuide, launcherShortcut, normalizeLauncher, parseLauncherDocument, type LaunchChoice, type UserLauncher } from '../lib/launchers'

export function AgentLauncher({ x, y, disabled, onClose, onLaunch }: {
  x: number
  y: number
  disabled?: boolean
  onClose: () => void
  onLaunch: (choice: LaunchChoice) => void
}) {
  const [custom, setCustom] = useState<UserLauncher[]>([])
  const [loadError, setLoadError] = useState('')
  const [editing, setEditing] = useState(false)
  const keepOpen = useRef(false)

  useEffect(() => {
    let cancel = false
    api.launchers().then((result) => {
      if (!cancel) setCustom(result.launchers)
    }).catch((reason) => {
      if (!cancel) setLoadError(reason instanceof Error ? reason.message : '自定义启动项没读到，内置项仍可使用')
    })
    return () => { cancel = true }
  }, [])

  useEffect(() => {
    const onKey = (event: KeyboardEvent) => {
      if (editing || event.repeat || event.ctrlKey || event.metaKey || event.altKey || event.defaultPrevented) return
      if (event.key < '1' || event.key > '9') return
      const choice = [...builtinLaunchers, ...custom][Number(event.key) - 1]
      if (!choice || disabled) return
      event.preventDefault()
      event.stopPropagation()
      onLaunch(choice)
    }
    window.addEventListener('keydown', onKey, true)
    return () => window.removeEventListener('keydown', onKey, true)
  }, [custom, disabled, editing, onLaunch])

  const rows = [...builtinLaunchers, ...custom]
  const items: ContextMenuItem[] = rows.map((choice, index) => ({
    id: choice.id,
    label: choice.name,
    icon: launcherShortcut(index) ? <span className="launcher-key">{launcherShortcut(index)}</span> : undefined,
    disabled,
    onSelect: () => onLaunch(choice),
  }))
  if (loadError) items.push({ id: 'load-error', label: loadError, disabled: true, separatorBefore: true, onSelect: () => {} })
  items.push({
    id: 'edit',
    label: '编辑启动项…',
    icon: <Pencil size={15}/>,
    separatorBefore: !loadError,
    onSelect: () => { keepOpen.current = true; setEditing(true) },
  })

  return <>
    {!editing && <ContextMenu className="launcher-menu" x={x} y={y} label="启动程序" items={items} onClose={() => { if (keepOpen.current) { keepOpen.current = false; return } onClose() }}/>}
    {editing && <LauncherEditor custom={custom} onClose={onClose} onSaved={setCustom}/>}
  </>
}

function LauncherEditor({ custom, onClose, onSaved }: { custom: UserLauncher[]; onClose: () => void; onSaved: (rows: UserLauncher[]) => void }) {
  const [rows, setRows] = useState(custom)
  const [name, setName] = useState('')
  const [command, setCommand] = useState('')
  const [imported, setImported] = useState('')
  const [error, setError] = useState('')
  const [notice, setNotice] = useState('')
  const [busy, setBusy] = useState(false)
  const [replaceArmed, setReplaceArmed] = useState(false)

  const persist = async (next: UserLauncher[]) => {
    setBusy(true)
    setError('')
    try {
      const saved = await api.saveLaunchers(next)
      setRows(saved.launchers)
      onSaved(saved.launchers)
      setNotice('')
      setReplaceArmed(false)
      return true
    } catch (reason) {
      setError(reason instanceof Error ? reason.message : '无法保存启动项')
      return false
    } finally {
      setBusy(false)
    }
  }

  return <Modal title="编辑启动项" className="launcher-editor" busy={busy} onClose={onClose}>
    <div className="form-stack">
      <p className="launcher-note">这些启动项跟你的账号走，在每台主机的 + 菜单里都能用。点一项会在当前工作区新建标签页，把命令送进那个终端的 shell 并回车。命令只在远程主机上执行。</p>
      {rows.length === 0 ? <p className="launcher-note">还没有自定义启动项。内置的 Claude Code、Codex 等不用写在这里。</p> : <ul className="launcher-list">{rows.map((row) => <li key={row.id}><span><strong>{row.name}</strong><small>{row.command}</small></span><Button type="button" className="button-secondary" disabled={busy} onClick={() => void persist(rows.filter((item) => item.id !== row.id))}>删除</Button></li>)}</ul>}
      <Form className="launcher-add" onSubmit={(event) => {
        const data = new FormData(event.currentTarget)
        const id = `custom-${Date.now().toString(36)}`
        try {
          const next = normalizeLauncher({ id, name: String(data.get('name') || name), command: String(data.get('command') || command) }, new Set(rows.map((row) => row.id)))
          setName('')
          setCommand('')
          void persist([...rows, next])
        } catch (reason) {
          setError(reason instanceof Error ? reason.message : '无法添加启动项')
        }
      }}>
        <label className="field"><span className="field-label">名称</span><Input name="name" aria-label="启动项名称" value={name} onChange={(event) => setName(event.target.value)} autoComplete="off" required maxLength={32}/></label>
        <label className="field"><span className="field-label">命令</span><Input name="command" aria-label="启动命令" value={command} onChange={(event) => setCommand(event.target.value)} autoComplete="off" required maxLength={200} placeholder="codex review" data-format-hint="请输入一行命令，不要换行。"/></label>
        <Button type="submit" className="button-secondary" pending={busy}>添加</Button>
      </Form>
      <label className="field"><span className="field-label">让 Agent 写好后粘贴到这里</span><Textarea aria-label="启动项 JSON" rows={5} value={imported} onChange={(event) => { setImported(event.target.value); setReplaceArmed(false) }} placeholder='{"launchers":[{"id":"review","name":"审查","command":"codex review"}]}'/></label>
      <div className="modal-actions">
        <Button type="button" className="button-secondary" disabled={busy} onClick={async () => {
          try {
            await navigator.clipboard.writeText(launcherGuide(rows))
            setNotice('已复制。把说明交给当前终端里的 Agent，再把它给出的 JSON 粘贴回来。')
            setError('')
          } catch {
            setError('无法写入剪贴板。请选中说明文本自行复制。')
            setNotice(launcherGuide(rows))
          }
        }}>复制给 Agent 的说明</Button>
        <Button type="button" className="button-primary" pending={busy} onClick={() => {
          let next: UserLauncher[]
          try {
            next = parseLauncherDocument(imported)
          } catch (reason) {
            setError(reason instanceof Error ? reason.message : '无法导入启动项')
            return
          }
          if (rows.length > 0 && !replaceArmed) {
            setReplaceArmed(true)
            setError(`再点一次导入，替换现有 ${rows.length} 项。`)
            return
          }
          void persist(next).then((saved) => { if (saved) setImported('') })
        }}>{replaceArmed ? '确认替换' : '导入 JSON'}</Button>
      </div>
      {notice && <p className="launcher-note" role="status">{notice}</p>}
      {error && <p className="field-error" role="alert">{error}</p>}
    </div>
  </Modal>
}
