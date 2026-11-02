import { useEffect, useId, useMemo, useRef, useState, type FormEvent, type KeyboardEvent } from 'react'
import { RefreshCw } from 'lucide-react'
import { Form } from '../Form'
import { Modal } from '../Modal'
import { Button } from '../ui'
import { APIError, api } from '../../lib/api'
import { createClientID } from '../../lib/workbench'
import type { Launcher, LaunchersResult, Snapshot, StartSessionResult } from '../../types'
import { rovingIndex, Segmented } from './Segmented'
import './workbench-ui.css'

export type SessionLocation = { workspace_id: string; tab_id: string; pane_id: string }

const LAUNCHER_NAME = /^[A-Za-z0-9_][A-Za-z0-9_.-]{0,63}$/
// eslint-disable-next-line no-control-regex
const CONTROL = /[\u0000-\u001f\u007f]/
export const launcherStorageKey = (hostID: string) => `herdrx.launcher.v1.${hostID}`

function storedLauncher(hostID: string) {
  try { return localStorage.getItem(launcherStorageKey(hostID)) || '' } catch { return '' }
}

function rememberLauncher(hostID: string, name: string) {
  try { localStorage.setItem(launcherStorageKey(hostID), name) } catch { /* storage unavailable */ }
}

/** 默认启动器：这台主机上次用过的；没有时优先免授权的那个。 */
function preferredLauncher(hostID: string, launchers: Launcher[]) {
  const last = storedLauncher(hostID)
  return (launchers.find((item) => item.name === last) || launchers.find((item) => item.bypass) || launchers[0])?.name || 'claude'
}

/** 目录建议：当前 pane 的目录在前，其余按快照顺序去重。 */
function cwdSuggestions(snapshot: Snapshot | null, first?: string) {
  const seen = new Set<string>()
  for (const cwd of [first, ...(snapshot?.panes || []).map((pane) => pane.cwd)]) if (cwd?.startsWith('/')) seen.add(cwd)
  return [...seen]
}

const fallbackHints: Record<string, string> = {
  unsupported_agent: '这台主机的受控端版本较旧，列不出 zsh 里的 Claude 启动器。在远程主机执行 herdrx update 升级后重新打开本窗口即可选择；现在可以直接用 claude 启动。',
  shell_unavailable: '远程主机上没有找到 zsh，读不到 alias；可以直接用 claude 启动。',
  timeout: '读取 zsh alias 超时（.zshrc 加载较慢）；可以点「重新读取」，或直接用 claude 启动。',
  read_failed: '读取 zsh alias 失败；可以点「重新读取」，或直接用 claude 启动。',
}

function launcherHint(state: LaunchersState) {
  if (state.phase === 'error') return `读取启动器失败：${state.message}。可以点「重新读取」，或直接用 claude 启动。`
  if (state.phase !== 'ready') return ''
  const { result } = state
  if (!result.supported) return fallbackHints[result.reason || 'read_failed'] || fallbackHints.read_failed
  return '没有在 zsh 里找到调用 claude 的 alias，将直接运行 claude；也可以填写其他启动命令名。'
}

type LaunchersState = { phase: 'loading' } | { phase: 'ready'; result: LaunchersResult } | { phase: 'error'; message: string }
type FieldErrors = Partial<Record<'cwd' | 'launcher' | 'label', string>>
type Failure = { text: string; location?: SessionLocation }

function validate(cwd: string, launcher: string, label: string): FieldErrors {
  const errors: FieldErrors = {}
  if (!cwd.startsWith('/')) errors.cwd = '请填写远程主机上的绝对路径，以 / 开头。'
  else if (CONTROL.test(cwd)) errors.cwd = '目录不能包含换行或控制字符。'
  else if (new TextEncoder().encode(cwd).length > 4096) errors.cwd = '目录太长，最多 4096 字节。'
  if (!LAUNCHER_NAME.test(launcher)) errors.launcher = '启动器只能包含字母、数字、点、下划线和连字符，最多 64 个字符。'
  if ([...label].length > 64) errors.label = '标签最多 64 个字符。'
  else if (CONTROL.test(label)) errors.label = '标签不能包含换行或控制字符。'
  return errors
}

// 服务端明确拒绝、什么都没有执行的错误：改正后用新的请求编号再提交，免得被第一次的结果顶回来。
const REJECTED = new Set(['invalid_request_id', 'invalid_cwd', 'invalid_launcher', 'invalid_label', 'invalid_target', 'launcher_unknown', 'workspace_not_found'])

function failureFor(error: unknown, launcher: string): Failure & { fresh: boolean } {
  if (!(error instanceof APIError)) return { fresh: false, text: '网络中断，不确定会话是否已经创建。请先看一眼侧栏的工作区列表；确认没有新会话后再点「新建会话」，会沿用同一个请求编号，不会重复创建。' }
  const fresh = REJECTED.has(error.code)
  switch (error.code) {
    case 'invalid_cwd': return { fresh, text: '目录必须是远程主机上的绝对路径，不能包含换行或控制字符。' }
    case 'invalid_launcher': return { fresh, text: '启动器名字只能包含字母、数字、点、下划线和连字符。' }
    case 'launcher_unknown': return { fresh, text: `这台主机上没有名为“${launcher}”的启动器。请点「重新读取」后重新选择，或改用 claude。` }
    case 'invalid_label': return { fresh, text: '标签最多 64 个字符，且不能包含换行或控制字符。' }
    case 'invalid_target':
    case 'workspace_not_found': return { fresh, text: '当前工作区已经不存在，请改为「新工作区」后重试。' }
    case 'invalid_request_id': return { fresh, text: '请求编号无效，请关闭本窗口后重新打开再试。' }
    case 'herdr_unavailable': return { fresh, text: '远程主机上的 Herdr 没有响应。请确认 Herdr 正在运行后再点「新建会话」。' }
    case 'start_failed': {
      const payload = error.details as { details?: Partial<SessionLocation> } & Partial<SessionLocation> | undefined
      const raw = payload?.details || payload
      const location = raw?.workspace_id && raw.tab_id && raw.pane_id ? { workspace_id: raw.workspace_id, tab_id: raw.tab_id, pane_id: raw.pane_id } : undefined
      return { fresh, location, text: '已经新建了终端，但没能在里面启动 Claude。可以在 Shell 中查看这个终端，确认启动命令后手动运行；终端不会被关闭。' }
    }
    default: return { fresh, text: error.message || '新建会话失败，请稍后重试。' }
  }
}

/** 目录输入：可自由填写，下方给出快照里出现过的目录。 */
function CwdField({ id, value, suggestions, error, onChange }: { id: string; value: string; suggestions: string[]; error?: string; onChange: (value: string) => void }) {
  const [open, setOpen] = useState(false)
  const [active, setActive] = useState(-1)
  const listID = useId()
  // 输入的不是某条建议时按包含过滤；正好等于某条时列出其余的，供换一个目录。
  const typed = value && !suggestions.includes(value)
  const visible = suggestions.filter((path) => path !== value && (!typed || path.includes(value)))
  const shown = open && visible.length > 0
  const pick = (path: string) => { onChange(path); setOpen(false); setActive(-1) }
  const onKeyDown = (event: KeyboardEvent<HTMLInputElement>) => {
    if (event.key === 'ArrowDown' || event.key === 'ArrowUp') {
      if (!visible.length) return
      event.preventDefault()
      setOpen(true)
      setActive((current) => event.key === 'ArrowDown' ? (current + 1) % visible.length : current <= 0 ? visible.length - 1 : current - 1)
    } else if (event.key === 'Enter' && shown && active >= 0) {
      event.preventDefault()
      pick(visible[active])
    }
  }
  return <div className="console-combo">
    <input
      id={id}
      className={`input console-input${error ? ' input-error' : ''}`}
      role="combobox"
      aria-expanded={shown}
      aria-controls={listID}
      aria-autocomplete="list"
      aria-activedescendant={shown && active >= 0 ? `${listID}-${active}` : undefined}
      aria-invalid={Boolean(error)}
      aria-describedby={error ? `${id}-error` : undefined}
      value={value}
      placeholder="/home/user/project"
      autoComplete="off"
      autoCapitalize="off"
      autoCorrect="off"
      spellCheck={false}
      data-initial-focus
      onChange={(event) => { onChange(event.target.value); setOpen(true); setActive(-1) }}
      onClick={() => setOpen(true)}
      onBlur={() => setOpen(false)}
      onKeyDown={onKeyDown}
    />
    {shown && <ul id={listID} role="listbox" aria-label="最近用过的目录" className="console-combo-list">
      {visible.map((path, index) => <li key={path} id={`${listID}-${index}`} role="option" aria-selected={index === active} className={index === active ? 'console-combo-active' : undefined} onPointerDown={(event) => event.preventDefault()} onClick={() => pick(path)}>{path}</li>)}
    </ul>}
    {error && <span className="field-error" id={`${id}-error`} role="alert">{error}</span>}
  </div>
}

function LauncherTags({ launcher }: { launcher: Launcher }) {
  return <span className="console-launcher-tags">
    {launcher.bypass && <span className="console-tag">免授权</span>}
    {launcher.fullscreen && <span className="console-tag">全屏</span>}
    {launcher.teammate && <span className="console-tag">队友</span>}
  </span>
}

/** 启动器列表：单选，方向键移动并选中。 */
function LauncherList({ launchers, value, onChange }: { launchers: Launcher[]; value: string; onChange: (name: string) => void }) {
  const refs = useRef<Array<HTMLButtonElement | null>>([])
  const selected = Math.max(0, launchers.findIndex((item) => item.name === value))
  useEffect(() => { refs.current[selected]?.scrollIntoView?.({ block: 'nearest' }) }, [selected])
  const onKeyDown = (event: KeyboardEvent<HTMLButtonElement>, index: number) => {
    const next = rovingIndex(event.key, index, launchers.map((_, item) => item))
    if (next === undefined) return
    event.preventDefault()
    refs.current[next]?.focus()
    onChange(launchers[next].name)
  }
  return <div role="radiogroup" aria-label="启动器" className="console-launchers">
    {launchers.map((launcher, index) => <button
      key={launcher.name}
      ref={(element) => { refs.current[index] = element }}
      type="button"
      role="radio"
      aria-checked={index === selected}
      tabIndex={index === selected ? 0 : -1}
      className="console-launcher"
      onClick={() => onChange(launcher.name)}
      onKeyDown={(event) => onKeyDown(event, index)}
    ><code>{launcher.name}</code><LauncherTags launcher={launcher}/></button>)}
  </div>
}

/**
 * 新建 Claude 会话：在远程主机的某个目录新建工作区（或在当前工作区新建标签），用选定的
 * 启动器启动 Claude。启动器只显示名字和三个开关，alias 正文从不离开远程主机。
 *
 * 一次对话框一个 `request_id`，网络中断后重试沿用（服务端 10 分钟内去重，不会重复建会话）；
 * 服务端明确拒绝、什么都没执行时才换新编号。首条消息由工作台在 Claude 就绪后发送。
 */
export function NewSessionDialog({ hostID, snapshot, workspaceID, defaultCwd, onClose, onStarted, onOpenInShell }: {
  hostID: string
  snapshot: Snapshot | null
  /** 当前工作区，「当前工作区里新建标签」用它；为空时只能新建工作区。 */
  workspaceID: string
  defaultCwd?: string
  onClose: () => void
  onStarted: (result: StartSessionResult, firstMessage: string, requestID: string) => void
  /** 启动失败但终端已建好：去 Shell 查看那个终端。 */
  onOpenInShell: (location: SessionLocation) => void
}) {
  const ids = { cwd: useId(), launcher: useId(), label: useId(), message: useId() }
  const [requestID, setRequestID] = useState(createClientID)
  const suggestions = useMemo(() => cwdSuggestions(snapshot, defaultCwd), [snapshot, defaultCwd])
  const [cwd, setCwd] = useState(() => suggestions[0] || '')
  const workspace = snapshot?.workspaces.find((item) => item.workspace_id === workspaceID)
  const [target, setTarget] = useState<'workspace' | 'tab'>('workspace')
  const [launchers, setLaunchers] = useState<LaunchersState>({ phase: 'loading' })
  const [reload, setReload] = useState(0)
  const [launcher, setLauncher] = useState('')
  const [label, setLabel] = useState('')
  const [message, setMessage] = useState('')
  const [busy, setBusy] = useState(false)
  const [errors, setErrors] = useState<FieldErrors>({})
  const [failure, setFailure] = useState<Failure | null>(null)

  useEffect(() => {
    const controller = new AbortController()
    setLaunchers({ phase: 'loading' })
    api.launchers(hostID, { signal: controller.signal }).then((result) => {
      const list = result.supported ? result.launchers || [] : []
      setLaunchers({ phase: 'ready', result: { ...result, launchers: list } })
      setLauncher((current) => list.length ? (list.some((item) => item.name === current) ? current : preferredLauncher(hostID, list)) : current || 'claude')
    }, (error: unknown) => {
      if (controller.signal.aborted) return
      setLaunchers({ phase: 'error', message: error instanceof Error ? error.message : '网络错误' })
      setLauncher((current) => current || 'claude')
    })
    return () => controller.abort()
  }, [hostID, reload])

  const list = launchers.phase === 'ready' ? launchers.result.launchers : []
  const effectiveTarget = workspace ? target : 'workspace'

  const submit = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault()
    if (busy || launchers.phase === 'loading') return
    const name = launcher.trim()
    const next = validate(cwd, name, label.trim())
    setErrors(next)
    const invalid = (['cwd', 'launcher', 'label'] as const).find((field) => next[field])
    if (invalid) {
      document.getElementById(ids[invalid])?.focus()
      return
    }
    setBusy(true)
    setFailure(null)
    try {
      const result = await api.startSession(hostID, {
        request_id: requestID,
        cwd,
        launcher: name,
        target: effectiveTarget === 'tab' ? { kind: 'tab', workspace_id: workspaceID } : { kind: 'workspace' },
        label: label.trim() || undefined,
      })
      rememberLauncher(hostID, result.launcher || name)
      onStarted(result, message, requestID)
    } catch (error) {
      const next = failureFor(error, name)
      if (next.fresh) setRequestID(createClientID())
      setFailure(next)
    } finally {
      setBusy(false)
    }
  }

  const clearError = (field: keyof FieldErrors) => setErrors((current) => ({ ...current, [field]: undefined }))
  const chooseLauncher = (name: string) => { setLauncher(name); clearError('launcher') }
  const hint = launcherHint(launchers)
  return <Modal title="新建 Claude 会话" className="console-dialog" busy={busy} closeLabel="关闭新建会话" onClose={onClose}>
    <Form className="console-form" onSubmit={(event) => void submit(event)}>
      <div className="console-field">
        <label className="console-label" htmlFor={ids.cwd}>目录</label>
        <CwdField id={ids.cwd} value={cwd} suggestions={suggestions} error={errors.cwd} onChange={(value) => { setCwd(value); clearError('cwd') }}/>
        <span className="console-help">远程主机上的绝对路径，Claude 在这里启动。</span>
      </div>
      <div className="console-field">
        <span className="console-label">位置</span>
        <Segmented label="新会话的位置" value={effectiveTarget} onChange={setTarget} className="console-segmented" options={[
          { value: 'workspace', label: '新工作区' },
          { value: 'tab', label: '当前工作区里新建标签', hint: workspace ? `在“${workspace.label}”里新建标签` : '当前没有选中工作区', disabled: !workspace },
        ]}/>
      </div>
      <div className="console-field">
        <span className="console-label-row">
          {list.length ? <span className="console-label">启动器</span> : <label className="console-label" htmlFor={ids.launcher}>启动器</label>}
          {launchers.phase !== 'loading' && !list.length && <button type="button" className="console-link" onClick={() => setReload((value) => value + 1)}><RefreshCw size={12} aria-hidden="true"/>重新读取</button>}
        </span>
        {launchers.phase === 'loading'
          ? <p className="console-help" role="status">正在读取这台主机 zsh 里的 Claude 启动器…</p>
          : list.length
            ? <LauncherList launchers={list} value={launcher} onChange={chooseLauncher}/>
            : <input id={ids.launcher} className={`input console-input${errors.launcher ? ' input-error' : ''}`} value={launcher} autoComplete="off" autoCapitalize="off" autoCorrect="off" spellCheck={false} aria-invalid={Boolean(errors.launcher)} aria-describedby={errors.launcher ? `${ids.launcher}-error` : `${ids.launcher}-hint`} onChange={(event) => chooseLauncher(event.target.value)}/>}
        {errors.launcher && <span className="field-error" id={`${ids.launcher}-error`} role="alert">{errors.launcher}</span>}
        {hint && !list.length && <span className="console-help" id={`${ids.launcher}-hint`}>{hint}</span>}
      </div>
      <div className="console-field">
        <label className="console-label" htmlFor={ids.label}>标签<small>可选</small></label>
        <input id={ids.label} className={`input console-input${errors.label ? ' input-error' : ''}`} value={label} maxLength={128} placeholder={effectiveTarget === 'tab' ? '新标签的名称' : '新工作区的名称'} autoComplete="off" aria-invalid={Boolean(errors.label)} aria-describedby={errors.label ? `${ids.label}-error` : undefined} onChange={(event) => { setLabel(event.target.value); clearError('label') }}/>
        {errors.label && <span className="field-error" id={`${ids.label}-error`} role="alert">{errors.label}</span>}
      </div>
      <div className="console-field">
        <label className="console-label" htmlFor={ids.message}>首条消息<small>可选</small></label>
        <textarea id={ids.message} className="input console-input console-textarea" rows={3} value={message} placeholder="Claude 就绪后自动发送一次" onChange={(event) => setMessage(event.target.value)}/>
        <span className="console-help">先弹出信任目录等对话框时，会等你答完再发；发送结果不确定时不会重发。</span>
      </div>
      {failure && <div className="console-alert" role="alert">
        <span>{failure.text}</span>
        {failure.location && <Button type="button" className="console-button" onClick={() => onOpenInShell(failure.location!)}>在 Shell 中查看</Button>}
      </div>}
      <div className="console-actions">
        <Button type="button" className="console-button" disabled={busy} onClick={onClose}>取消</Button>
        <Button type="submit" className="console-button console-button-primary" pending={busy} disabled={launchers.phase === 'loading'}>{busy ? '正在新建…' : '新建会话'}</Button>
      </div>
    </Form>
  </Modal>
}
