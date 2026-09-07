import { Modal } from '../components/Modal'
import { useConfirm } from '../components/useConfirm'
import { Input, Form } from '../components/Form'
import { Select, SelectOption } from '../components/Select'
import { BrandIcon } from '../components/Brand'
import { useCallback, useEffect, useMemo, useRef, useState, type CSSProperties, type ChangeEvent, type FormEvent, type MouseEvent as ReactMouseEvent, type ReactNode } from 'react'
import { Bell, Columns2, FolderOpen, GitBranchPlus, Image as ImageIcon, Maximize2, Menu, Move, PanelLeftClose, PanelLeftOpen, Pencil, Plus, RefreshCw, Rows2, Server, Settings, Trash2, X, ZoomIn } from 'lucide-react'
import { ContextMenu, type ContextMenuItem } from '../components/ContextMenu'
import { Button, StatusDot } from '../components/ui'
import { TerminalPane } from '../components/TerminalPane'
import { DisplaySettings, DisplayToolbar } from '../components/DisplayControls'
import { AppearanceToggle } from '../components/AppearanceToggle'
import { useTerminalDisplay, useWorkbenchViewport } from '../lib/displayPreferences'
import { api } from '../lib/api'
import { navigate } from '../lib/navigation'
import { WorkbenchClient } from '../lib/workbench'
import { terminalThemes } from '../lib/themes'
import type { Agent, Host, Layout, Pane, Snapshot, Tab, Workspace } from '../types'


type ContextTarget =
  | { kind: 'sidebar' }
  | { kind: 'workspace'; workspace: Workspace }
  | { kind: 'tab'; tab: Tab }
  | { kind: 'pane'; pane: Pane; sourcePaneID?: string }

type PromptState = {
  title: string
  label: string
  value: string
  placeholder?: string
  submitLabel: string
  onSubmit: (value: string) => Promise<void>
}

export function WorkbenchPage({ hostID }: { hostID: string }) {
  const { confirm, dialog: confirmationDialog } = useConfirm(hostID)
  const client = useMemo(() => new WorkbenchClient(hostID), [hostID])
  const { mobile, height: viewportHeight } = useWorkbenchViewport()
  const { display, update: updateDisplay, reset: resetDisplay } = useTerminalDisplay(mobile)
  const [actualFontSize, setActualFontSize] = useState(display.fontSize)
  const [host, setHost] = useState<Host | null>(null)
  const [availableHosts, setAvailableHosts] = useState<Host[]>([])
  const [hostListError, setHostListError] = useState('')
  const hostTabsRef = useRef<HTMLElement>(null)
  const [snapshot, setSnapshot] = useState<Snapshot | null>(null)
  const [connection, setConnection] = useState('connecting')
  const [message, setMessage] = useState('')
  const [connectionEpoch, setConnectionEpoch] = useState(0)
  const [workspaceID, setWorkspaceID] = useState('')
  const [tabID, setTabID] = useState('')
  const [paneID, setPaneID] = useState('')
  const [sidebarOpen, setSidebarOpen] = useState(() => localStorage.getItem('herdrx.sidebar-open') !== 'false')
  const [switcherOpen, setSwitcherOpen] = useState(false)
  const [prefix, setPrefix] = useState(false)
  const [terminalInput, setTerminalInput] = useState<((data: string) => void) | null>(null)
  const [settingsOpen, setSettingsOpen] = useState(false)
  const [themeName, setThemeName] = useState(() => localStorage.getItem('herdrx.terminal-theme') || 'Cobalt2')
  const terminalTheme = terminalThemes[themeName] || terminalThemes.Cobalt2
  const [enhancedContrast, setEnhancedContrast] = useState(() => localStorage.getItem('herdrx.enhanced-contrast') !== 'false')
  const [notificationPermission, setNotificationPermission] = useState<NotificationPermission>(() => 'Notification' in window ? Notification.permission : 'denied')
  const [actionError, setActionError] = useState('')
  const [contextMenu, setContextMenu] = useState<{ x: number; y: number; target: ContextTarget } | null>(null)
  const [prompt, setPrompt] = useState<PromptState | null>(null)
  const [promptBusy, setPromptBusy] = useState(false)
  const [workspaceBusy, setWorkspaceBusy] = useState(false)
  const creatingWorkspace = useRef(false)
  const pendingWorkspaceSelection = useRef<string | null>(null)
  const [promptError, setPromptError] = useState('')
  const [collapsedGroups, setCollapsedGroups] = useState<Set<string>>(() => new Set())
  const [rightClickTargets, setRightClickTargets] = useState<Record<string, 'herdr' | 'pane'>>(() => loadRightClickTargets(hostID))
  const [gitWorkspaces, setGitWorkspaces] = useState<Record<string, boolean>>({})
  const previousStatuses = useRef(new Map<string, string>())
  const pendingTabSelection = useRef<{ workspaceID: string; existing: Set<string> } | null>(null)
  const handleControlReady = useCallback((send: ((data: string) => void) | null) => setTerminalInput(send ? () => send : null), [])
  const fileInputRef = useRef<HTMLInputElement>(null)
  const handleImageUpload = async (event: ChangeEvent<HTMLInputElement>) => {
    const file = event.target.files?.[0]
    event.target.value = ''
    if (!file || !host || !paneID) return
    if (file.size > 20 * 1024 * 1024) {
      setActionError('图片大小超过 20MB 限制')
      return
    }
    try {
      await api.pasteImage(host.id, paneID, file, true)
    } catch (err) {
      setActionError(err instanceof Error ? err.message : '图片上传失败')
    }
  }

  useEffect(() => { localStorage.setItem('herdrx.sidebar-open', String(sidebarOpen)) }, [sidebarOpen])




  useEffect(() => {
    hostTabsRef.current?.querySelector<HTMLElement>('[aria-current="page"]')?.scrollIntoView?.({ block: 'nearest', inline: 'nearest' })
  }, [hostID, availableHosts])

  useEffect(() => {
    const tabs = hostTabsRef.current
    if (!tabs) return
    const wheel = (event: WheelEvent) => {
      if (event.ctrlKey || Math.abs(event.deltaX) >= Math.abs(event.deltaY) || tabs.scrollWidth <= tabs.clientWidth) return
      const previous = tabs.scrollLeft
      const unit = event.deltaMode === 1 ? 24 : event.deltaMode === 2 ? tabs.clientWidth : 1
      tabs.scrollLeft += event.deltaY * unit
      if (tabs.scrollLeft !== previous) event.preventDefault()
    }
    tabs.addEventListener('wheel', wheel, { passive: false })
    return () => tabs.removeEventListener('wheel', wheel)
  }, [])

  useEffect(() => {
    let disposed = false
    const refreshHosts = () => {
      void api.hosts().then(({ hosts }) => {
        if (disposed) return
        setAvailableHosts(hosts ?? [])
        setHostListError('')
        const current = hosts?.find((item) => item.id === hostID)
        if (current) setHost(current)
      }).catch(() => { if (!disposed) setHostListError('无法读取主机列表，请重新连接') })
    }
    refreshHosts()
    window.addEventListener('focus', refreshHosts)
    return () => { disposed = true; window.removeEventListener('focus', refreshHosts) }
  }, [hostID])

  useEffect(() => {
    void api.host(hostID).then((result) => setHost(result.host)).catch((reason) => setMessage(reason instanceof Error ? reason.message : '无法读取主机'))
    const offSnapshot = client.onSnapshot((nextSnapshot) => {
      setSnapshot(nextSnapshot)
      for (const agent of nextSnapshot.agents) {
        const previous = previousStatuses.current.get(agent.pane_id)
        if (previous && previous !== agent.agent_status && (agent.agent_status === 'blocked' || agent.agent_status === 'done') && document.visibilityState !== 'visible') {
          void showAgentNotification(agent.name || agent.agent, agent.agent_status, hostID, agent.pane_id)
        }
        previousStatuses.current.set(agent.pane_id, agent.agent_status)
      }
    })
    const offState = client.onState((state, detail) => { setConnection(state); setMessage(detail || '') })
    const offEpoch = client.onEpoch(setConnectionEpoch)
    client.connect()
    return () => { offSnapshot(); offState(); offEpoch(); client.dispose() }
  }, [client, hostID])

  useEffect(() => {
    if (!snapshot) return
    if (pendingWorkspaceSelection.current) {
      const created = snapshot.workspaces.find((item) => item.workspace_id === pendingWorkspaceSelection.current)
      if (!created) return // The RPC result can precede its snapshot.
      pendingWorkspaceSelection.current = null
      setWorkspaceID(created.workspace_id)
      setTabID(created.active_tab_id)
      const pane = snapshot.panes.find((item) => item.tab_id === created.active_tab_id)
      if (pane) setPaneID(pane.pane_id)
      return
    }
    const pendingTab = pendingTabSelection.current
    if (pendingTab) {
      const created = snapshot.tabs.find((item) => item.workspace_id === pendingTab.workspaceID && !pendingTab.existing.has(item.tab_id))
      if (created) {
        pendingTabSelection.current = null
        setWorkspaceID(created.workspace_id)
        setTabID(created.tab_id)
        const createdPane = snapshot.panes.find((item) => item.tab_id === created.tab_id)
        if (createdPane) setPaneID(createdPane.pane_id)
        return
      }
    }
    const workspace = snapshot.workspaces.find((item) => item.workspace_id === workspaceID)
      || snapshot.workspaces.find((item) => item.workspace_id === snapshot.focused_workspace_id)
      || snapshot.workspaces[0]
    if (!workspace) return
    if (workspace.workspace_id !== workspaceID) setWorkspaceID(workspace.workspace_id)
    const tab = snapshot.tabs.find((item) => item.tab_id === tabID && item.workspace_id === workspace.workspace_id)
      || snapshot.tabs.find((item) => item.tab_id === workspace.active_tab_id)
      || snapshot.tabs.find((item) => item.workspace_id === workspace.workspace_id)
    if (!tab) return
    if (tab.tab_id !== tabID) setTabID(tab.tab_id)
    const layout = snapshot.layouts.find((item) => item.tab_id === tab.tab_id)
    const pane = snapshot.panes.find((item) => item.pane_id === paneID && item.tab_id === tab.tab_id)
      || snapshot.panes.find((item) => item.pane_id === layout?.focused_pane_id)
      || snapshot.panes.find((item) => item.tab_id === tab.tab_id)
    if (pane && pane.pane_id !== paneID) setPaneID(pane.pane_id)
  }, [snapshot, workspaceID, tabID, paneID])

  useEffect(() => {
    const keydown = (event: KeyboardEvent) => {
      if ((event.target as HTMLElement).closest('[role="dialog"], [role="alertdialog"], [data-ui-overlay]')) return
      if (event.ctrlKey && !event.altKey && !event.metaKey && event.key.toLowerCase() === 'b') {
        event.preventDefault()
        event.stopPropagation()
        setPrefix((value) => !value)
        return
      }
      if (!prefix) return
      event.preventDefault()
      event.stopPropagation()
      setPrefix(false)
      void runPrefixAction(event.key)
    }
    window.addEventListener('keydown', keydown, true)
    return () => window.removeEventListener('keydown', keydown, true)
  }, [prefix, paneID, tabID, workspaceID, snapshot])

  const runPrefixAction = async (key: string) => {
    try {
      if (!snapshot || !paneID) return
      if (key === 'Escape') return
      if (key === 'v') await client.call('pane.split', { workspace_id: workspaceID, target_pane_id: paneID, direction: 'right', ratio: 0.5, focus: false })
      else if (key === '-') await client.call('pane.split', { workspace_id: workspaceID, target_pane_id: paneID, direction: 'down', ratio: 0.5, focus: false })
      else if (key === 'z') await client.call('pane.zoom', { pane_id: paneID })
      else if (key === 'x' && await confirm('关闭当前 Pane？其中运行的进程也会结束。', { title: '关闭 Pane', confirmLabel: '关闭 Pane' })) await client.call('pane.close', { pane_id: paneID })
      else if (['h', 'j', 'k', 'l'].includes(key)) focusNeighbor(key)
      else if (key === 'c') await createTab()
      else if (key === 'b') setSidebarOpen((value) => !value)
      else if (key === 'w' || key === 'g') setSwitcherOpen(true)
      setActionError('')
    } catch (reason) { setActionError(reason instanceof Error ? reason.message : 'Herdr 操作失败') }
  }

  const createTab = async () => createTabFor(workspaceID)

  const createTabFor = async (targetWorkspaceID: string) => {
    const existing = new Set((snapshot?.tabs || []).filter((tab) => tab.workspace_id === targetWorkspaceID).map((tab) => tab.tab_id))
    pendingTabSelection.current = { workspaceID: targetWorkspaceID, existing }
    try {
      const result = await client.call<{ tab?: Tab; root_pane?: Pane }>('tab.create', { workspace_id: targetWorkspaceID, focus: false })
      if (result.tab) {
        setWorkspaceID(result.tab.workspace_id)
        setTabID(result.tab.tab_id)
        if (result.root_pane) setPaneID(result.root_pane.pane_id)
      }
    } catch (error) {
      pendingTabSelection.current = null
      throw error
    }
  }

  const closeTab = async (tab: Tab) => {
    if (!await confirm(`关闭标签页“${tab.label}”？其中运行的进程也会结束。`, { title: '关闭标签页', confirmLabel: '关闭标签页' })) return
    try {
      await client.call('tab.close', { tab_id: tab.tab_id })
      const next = tabs.find((candidate) => candidate.tab_id !== tab.tab_id)
      if (next) setTabID(next.tab_id)
      setActionError('')
    } catch (reason) { setActionError(reason instanceof Error ? reason.message : '无法关闭标签页') }
  }

  const createWorkspace = async () => {
    if (creatingWorkspace.current) return
    creatingWorkspace.current = true
    setWorkspaceBusy(true)
    try {
      const result = await client.call<{ workspace?: Workspace; tab?: Tab; root_pane?: Pane }>('workspace.create', { focus: false })
      if (result.workspace) {
        pendingWorkspaceSelection.current = result.workspace.workspace_id
        setWorkspaceID(result.workspace.workspace_id)
      }
      setSwitcherOpen(false)
      if (result.tab) setTabID(result.tab.tab_id)
      if (result.root_pane) setPaneID(result.root_pane.pane_id)
      setActionError('')
    } catch (reason) { setActionError(reason instanceof Error ? reason.message : '无法创建工作区') }
    finally { creatingWorkspace.current = false; setWorkspaceBusy(false) }
  }

  const runAction = (action: () => Promise<void>) => {
    setContextMenu(null)
    void action().then(() => setActionError('')).catch((reason) => setActionError(reason instanceof Error ? reason.message : 'Herdr 操作失败'))
  }

  const openContextMenu = (event: ReactMouseEvent, target: ContextTarget) => {
    event.preventDefault()
    event.stopPropagation()
    setContextMenu({ x: event.clientX, y: event.clientY, target })
  }

  const openWorkspaceContextMenu = (event: ReactMouseEvent, workspace: Workspace) => {
    selectWorkspace(workspace)
    openContextMenu(event, { kind: 'workspace', workspace })
    if (gitWorkspaces[workspace.workspace_id] !== undefined || workspace.worktree || workspace.branch) return
    void client.call('worktree.list', { workspace_id: workspace.workspace_id }).then(() => {
      setGitWorkspaces((current) => ({ ...current, [workspace.workspace_id]: true }))
    }).catch(() => {
      setGitWorkspaces((current) => ({ ...current, [workspace.workspace_id]: false }))
    })
  }

  const openAgentContextMenu = (event: ReactMouseEvent, agent: Agent) => {
    const pane = snapshot?.panes.find((item) => item.pane_id === agent.pane_id)
    if (!pane) {
      openContextMenu(event, { kind: 'sidebar' })
      return
    }
    const source = snapshot?.panes.find((item) => item.pane_id === paneID)
    selectAgent(agent)
    openContextMenu(event, {
      kind: 'pane',
      pane: { ...pane, right_click_passthrough: rightClickTargets[pane.pane_id] === 'pane' },
      sourcePaneID: source?.tab_id === pane.tab_id && source.pane_id !== pane.pane_id ? source.pane_id : undefined,
    })
  }

  const openPrompt = (next: PromptState) => {
    setContextMenu(null)
    setPromptError('')
    setPrompt(next)
  }

  const submitPrompt = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault()
    if (!prompt || promptBusy) return
    const value = String(new FormData(event.currentTarget).get('value') || '').trim()
    if (!value) { setPromptError(`${prompt.label}不能为空`); return }
    setPromptBusy(true)
    setPromptError('')
    try {
      await prompt.onSubmit(value)
      setPrompt(null)
      setActionError('')
    } catch (reason) {
      setPromptError(reason instanceof Error ? reason.message : 'Herdr 操作失败')
    } finally {
      setPromptBusy(false)
    }
  }

  const selectCreatedLocation = (result: { workspace?: Workspace; tab?: Tab; root_pane?: Pane }) => {
    if (result.workspace) setWorkspaceID(result.workspace.workspace_id)
    if (result.tab) setTabID(result.tab.tab_id)
    if (result.root_pane) setPaneID(result.root_pane.pane_id)
  }

  const setPaneRightClickTarget = (targetPaneID: string, value: 'herdr' | 'pane') => {
    setRightClickTargets((current) => {
      const next = { ...current, [targetPaneID]: value }
      localStorage.setItem(`herdrx.right-click.${hostID}`, JSON.stringify(next))
      return next
    })
  }

  const contextItems = (target: ContextTarget): ContextMenuItem[] => {
    if (target.kind === 'sidebar') {
      return [
        { id: 'new-workspace', label: '新建工作区', icon: <Plus size={15}/>, disabled: connection !== 'ready', onSelect: () => void createWorkspace() },
        { id: 'switch', label: '切换工作区或终端', icon: <Menu size={15}/>, onSelect: () => setSwitcherOpen(true) },
        { id: 'collapse', label: '收起侧边栏', icon: <PanelLeftClose size={15}/>, separatorBefore: true, onSelect: () => setSidebarOpen(false) },
        { id: 'settings', label: '工作台设置', icon: <Settings size={15}/>, onSelect: () => setSettingsOpen(true) },
      ]
    }
    if (target.kind === 'workspace') {
      const workspace = target.workspace
      const groupKey = workspace.worktree?.repo_key
      const hasChildren = Boolean(groupKey && !workspace.worktree?.is_linked_worktree && workspaces.filter((item) => item.worktree?.repo_key === groupKey).length > 1)
      const isGit = Boolean(workspace.worktree || workspace.branch || gitWorkspaces[workspace.workspace_id])
      const items: ContextMenuItem[] = [
        { id: 'rename', label: '重命名工作区', icon: <Pencil size={15}/>, onSelect: () => openPrompt({ title: '重命名工作区', label: '名称', value: workspace.label, submitLabel: '保存', onSubmit: async (label) => { await client.call('workspace.rename', { workspace_id: workspace.workspace_id, label }) } }) },
        { id: 'close', label: hasChildren ? '关闭工作区组' : '关闭工作区', icon: <X size={15}/>, danger: true, onSelect: async () => { if (await confirm(`关闭${hasChildren ? '工作区组' : '工作区'}“${workspace.label}”？其中运行的进程也会结束。`, { title: hasChildren ? '关闭工作区组' : '关闭工作区', confirmLabel: hasChildren ? '关闭工作区组' : '关闭工作区' })) runAction(async () => { await client.call('workspace.close', { workspace_id: workspace.workspace_id, close_group: true }) }) } },
      ]
      if (!isGit) return items
      if (workspace.worktree?.is_linked_worktree) {
        items.push({ id: 'remove-worktree', label: '删除 Worktree 检出…', icon: <Trash2 size={15}/>, danger: true, separatorBefore: true, onSelect: async () => { if (await confirm(`删除 Worktree 检出“${workspace.worktree?.checkout_path}”？未提交的改动会阻止删除。`, { title: '删除 Worktree 检出', confirmLabel: '删除检出' })) runAction(async () => { await client.call('worktree.remove', { workspace_id: workspace.workspace_id, force: false }) }) } })
        return items
      }
      items.push(
        { id: 'new-worktree', label: '新建 Worktree…', icon: <GitBranchPlus size={15}/>, separatorBefore: true, onSelect: () => openPrompt({ title: '新建 Worktree', label: '分支名', value: '', placeholder: 'feature/my-change', submitLabel: '创建', onSubmit: async (branch) => { selectCreatedLocation(await client.call('worktree.create', { workspace_id: workspace.workspace_id, branch, focus: false })) } }) },
        { id: 'open-worktree', label: '打开 Worktree…', icon: <FolderOpen size={15}/>, onSelect: () => openPrompt({ title: '打开 Worktree', label: '检出路径', value: '', placeholder: '/absolute/path/to/worktree', submitLabel: '打开', onSubmit: async (path) => { selectCreatedLocation(await client.call('worktree.open', { workspace_id: workspace.workspace_id, path, focus: false })) } }) },
      )
      if (hasChildren && groupKey) items.push({ id: 'toggle-group', label: collapsedGroups.has(groupKey) ? '展开 Worktree 组' : '收起 Worktree 组', icon: <PanelLeftClose size={15}/>, separatorBefore: true, onSelect: () => setCollapsedGroups((current) => { const next = new Set(current); if (!next.delete(groupKey)) next.add(groupKey); return next }) })
      return items
    }
    if (target.kind === 'tab') {
      const tab = target.tab
      return [
        { id: 'new-tab', label: '新建标签页', icon: <Plus size={15}/>, onSelect: () => runAction(() => createTabFor(tab.workspace_id)) },
        { id: 'rename', label: '重命名标签页', icon: <Pencil size={15}/>, onSelect: () => openPrompt({ title: '重命名标签页', label: '名称', value: tab.label, submitLabel: '保存', onSubmit: async (label) => { await client.call('tab.rename', { tab_id: tab.tab_id, label }) } }) },
        { id: 'close', label: '关闭标签页', icon: <X size={15}/>, danger: true, separatorBefore: true, onSelect: () => void closeTab(tab) },
      ]
    }
    const pane = target.pane
    const items: ContextMenuItem[] = [
      { id: 'rename', label: '重命名 Pane', icon: <Pencil size={15}/>, onSelect: () => openPrompt({ title: '重命名 Pane', label: '名称', value: pane.label || '', submitLabel: '保存', onSubmit: async (label) => { await client.call('pane.rename', { pane_id: pane.pane_id, label }) } }) },
    ]
    if (pane.label) items.push({ id: 'clear-name', label: '清除 Pane 名称', icon: <X size={15}/>, onSelect: () => runAction(async () => { await client.call('pane.rename', { pane_id: pane.pane_id, label: null }) }) })
    if (target.sourcePaneID) items.push({ id: 'swap', label: '与当前 Pane 互换', icon: <Move size={15}/>, onSelect: () => runAction(async () => { await client.call('pane.swap', { source_pane_id: target.sourcePaneID, target_pane_id: pane.pane_id }); setPaneID(target.sourcePaneID!) }) })
    items.push(
      { id: 'split-right', label: '向右分屏', icon: <Columns2 size={15}/>, separatorBefore: true, onSelect: () => runAction(async () => { await client.call('pane.split', { workspace_id: pane.workspace_id, target_pane_id: pane.pane_id, direction: 'right', ratio: 0.5, focus: false }) }) },
      { id: 'split-down', label: '向下分屏', icon: <Rows2 size={15}/>, onSelect: () => runAction(async () => { await client.call('pane.split', { workspace_id: pane.workspace_id, target_pane_id: pane.pane_id, direction: 'down', ratio: 0.5, focus: false }) }) },
      { id: 'zoom', label: '切换 Pane 缩放', icon: <Maximize2 size={15}/>, onSelect: () => runAction(async () => { await client.call('pane.zoom', { pane_id: pane.pane_id, mode: 'toggle' }) }) },
      { id: 'right-click', label: pane.right_click_passthrough ? '恢复 Herdrx 右键菜单' : '将右键发送给 Pane', icon: <Menu size={15}/>, separatorBefore: true, onSelect: () => runAction(async () => { const value = pane.right_click_passthrough ? 'herdr' : 'pane'; await client.call('pane.input.set', { pane_id: pane.pane_id, right_click: value }); setPaneRightClickTarget(pane.pane_id, value) }) },
      { id: 'close', label: '关闭 Pane', icon: <Trash2 size={15}/>, danger: true, separatorBefore: true, onSelect: async () => { if (await confirm('关闭这个 Pane？其中运行的进程也会结束。', { title: '关闭 Pane', confirmLabel: '关闭 Pane' })) runAction(async () => { await client.call('pane.close', { pane_id: pane.pane_id }) }) } },
    )
    return items
  }

  const focusNeighbor = (key: string) => {
    const layout = currentLayout(snapshot, tabID)
    const current = layout?.panes.find((item) => item.pane_id === paneID)
    if (!layout || !current) return
    const center = (rect: typeof current.rect) => ({ x: rect.x + rect.width / 2, y: rect.y + rect.height / 2 })
    const from = center(current.rect)
    const candidates = layout.panes.filter((item) => item.pane_id !== paneID).map((item) => ({ item, point: center(item.rect) })).filter(({ point }) =>
      key === 'h' ? point.x < from.x : key === 'l' ? point.x > from.x : key === 'k' ? point.y < from.y : point.y > from.y)
    candidates.sort((left, right) => Math.hypot(left.point.x - from.x, left.point.y - from.y) - Math.hypot(right.point.x - from.x, right.point.y - from.y))
    if (candidates[0]) setPaneID(candidates[0].item.pane_id)
  }

  const selectWorkspace = (workspace: Workspace) => {
    setWorkspaceID(workspace.workspace_id)
    setTabID(workspace.active_tab_id)
    setSwitcherOpen(false)
  }
  const selectTab = (tab: Tab) => { setWorkspaceID(tab.workspace_id); setTabID(tab.tab_id); setSwitcherOpen(false) }
  const selectAgent = (agent: Agent) => { setWorkspaceID(agent.workspace_id); setTabID(agent.tab_id); setPaneID(agent.pane_id); setSwitcherOpen(false) }

  const workspaces = snapshot?.workspaces || []
  const visibleWorkspaces = workspaces.filter((workspace) => !workspace.worktree?.is_linked_worktree || !collapsedGroups.has(workspace.worktree.repo_key))
  const tabs = snapshot?.tabs.filter((tab) => tab.workspace_id === workspaceID) || []
  const agents = [...(snapshot?.agents || [])].sort((a, b) => statusPriority(b.agent_status) - statusPriority(a.agent_status))
  const layout = currentLayout(snapshot, tabID)
  const panes = layout?.panes.map((entry) => snapshot?.panes.find((pane) => pane.pane_id === entry.pane_id)).filter(Boolean) as Pane[] | undefined
  const activeWorkspace = workspaces.find((workspace) => workspace.workspace_id === workspaceID)
  const activeTab = tabs.find((tab) => tab.tab_id === tabID)
  const visiblePanes = mobile ? panes?.filter((pane) => pane.pane_id === paneID) : panes
  const hostEntries = availableHosts.some((item) => item.id === hostID) ? availableHosts : [{ id: hostID, name: host?.name || '加载主机…' }, ...availableHosts]

  return <div className={`workbench ${mobile ? 'workbench-compact' : ''} ${mobile && viewportHeight < 430 ? 'workbench-short' : ''} ${!mobile && !sidebarOpen ? 'workbench-sidebar-closed' : ''}`} style={{ '--workbench-height': `${viewportHeight}px`, '--terminal': terminalTheme.background, '--terminal-ink': terminalTheme.foreground, '--terminal-cursor': terminalTheme.cursor } as CSSProperties}>
    <header className="hostbar" aria-label="主机导航">
      <button className="hostbar-home" aria-label="返回主机列表" data-tooltip="管理主机" onClick={() => navigate('/')}><BrandIcon/><span>herdrx</span></button>
      {!mobile && <Button className="tool-button" aria-label={sidebarOpen ? '收起侧边栏' : '展开侧边栏'} data-tooltip="切换侧边栏 · Ctrl+B B" onClick={() => setSidebarOpen((value) => !value)}>{sidebarOpen ? <PanelLeftClose size={14}/> : <PanelLeftOpen size={14}/>}</Button>}
      <nav className="host-tabs" ref={hostTabsRef} aria-label="选择主机">
        {hostEntries.map((item) => <a key={item.id} href={`/h/${encodeURIComponent(item.id)}`} className={`host-tab ${item.id === hostID ? 'host-tab-active' : ''}`} aria-current={item.id === hostID ? 'page' : undefined} data-tooltip={item.name} onClick={(event) => {
          if (event.button !== 0 || event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) return
          event.preventDefault()
          navigate(`/h/${encodeURIComponent(item.id)}`)
        }}>{item.name}</a>)}
      </nav>
      <Button className="tool-button" aria-label="重命名当前主机" data-tooltip="重命名当前主机" disabled={!host} onClick={() => { if (host) openPrompt({ title: '重命名主机', label: '主机名称', value: host.name, submitLabel: '保存名称', onSubmit: async (name) => { const result = await api.renameHost(hostID, name); setHost(result.host); setAvailableHosts((current) => current.map((item) => item.id === hostID ? result.host : item)) } }) }}><Pencil size={13}/></Button>
      <div className={`connection connection-${connection}`} data-tooltip={hostListError || undefined}><span/><span>{hostListError ? '主机列表加载失败' : connection === 'ready' ? host?.transport : connection}</span><Button className="tool-button hostbar-switcher" aria-label="切换工作区或终端" data-tooltip={`切换工作区或终端 · ${activeWorkspace?.label || ""} / ${activeTab?.label || ""} · Ctrl+B W`} onClick={() => setSwitcherOpen(true)}><Menu size={14}/></Button><Button className="tool-button" aria-label="工作台设置" data-tooltip="工作台设置" onClick={() => setSettingsOpen(true)}><Settings size={14}/></Button><Button className="tool-button hostbar-reconnect" aria-label="重新连接" data-tooltip="重新连接" onClick={() => window.location.reload()}><RefreshCw size={13}/></Button></div>
    </header>
    {!mobile && <aside className="workbench-sidebar" onContextMenu={(event) => openContextMenu(event, { kind: 'sidebar' })}>
      <SidebarSection title="工作区" action={<button className="workspace-create" aria-label="新建工作区" disabled={workspaceBusy || connection !== 'ready'} onClick={() => void createWorkspace()}><Plus size={14}/><span>{workspaceBusy ? '创建中…' : '新建'}</span></button>}>
        {visibleWorkspaces.map((workspace) => <button className={`sidebar-row workspace-row ${workspace.workspace_id === workspaceID ? 'sidebar-row-active' : ''}`} key={workspace.workspace_id} aria-current={workspace.workspace_id === workspaceID ? 'true' : undefined} data-tooltip={`${workspace.label} · ${workspace.pane_count} panes · ${workspace.tab_count} tabs`} onClick={() => selectWorkspace(workspace)} onContextMenu={(event) => openWorkspaceContextMenu(event, workspace)}><StatusDot status={workspace.agent_status}/><span className="workspace-number">{workspace.number}</span><strong>{workspace.label}</strong>{workspace.tab_count > 1 && <small className="workspace-count">{workspace.tab_count}</small>}</button>)}
      </SidebarSection>
      <SidebarSection title="Agent 状态">
        {agents.length === 0 ? <p className="sidebar-empty">暂无 Agent</p> : agents.map((agent) => <button className={`sidebar-row agent-row ${agent.pane_id === paneID ? 'sidebar-row-active' : ''}`} key={agent.pane_id} data-tooltip={`${agent.name || agent.agent} · ${workspaceName(workspaces, agent.workspace_id)} · ${agent.agent_status}`} aria-current={agent.pane_id === paneID ? 'true' : undefined} onClick={() => selectAgent(agent)} onContextMenu={(event) => openAgentContextMenu(event, agent)}><StatusDot status={agent.agent_status}/><span className="agent-meta"><strong>{agent.name || agent.agent}</strong><small>{workspaceName(workspaces, agent.workspace_id)}</small></span><span className="agent-state">{({ working: '运行', blocked: '待确认', done: '完成', idle: '空闲', unknown: '未知' })[agent.agent_status]}</span></button>)}
      </SidebarSection>
      <div className="sidebar-footer"><span>herdrx</span><button data-tooltip="工作台设置" onClick={() => setSettingsOpen(true)}><Settings size={13}/>设置</button></div>
    </aside>}

    <main className="workbench-main">
      {mobile && <>
        <div className="mobile-header">
          <button className="mobile-location" aria-label="选择工作区和分屏" onClick={() => setSwitcherOpen(true)}><Menu size={16}/><span><strong>{activeWorkspace?.label || '工作区'}</strong><small>{workspaces.length} 个工作区 · {panes?.length || 0} 个分屏</small></span></button>
          <button className="workspace-create" aria-label="新建工作区" disabled={workspaceBusy || connection !== 'ready'} onClick={() => void createWorkspace()}><Plus size={16}/><span>新建</span></button>
          <button className="mobile-fit" aria-pressed={display.mode === 'responsive'} title="自适应会让当前远端终端按手机宽度换行" onClick={() => updateDisplay({ mode: display.mode === 'responsive' ? 'fixed' : 'responsive', zoom: 100 })}>{display.mode === 'responsive' ? '自适应' : '原始画面'}</button>
        </div>
        <nav className="mobile-tabs" aria-label="标签页">
          <div>{tabs.map((tab) => <button key={tab.tab_id} className={tab.tab_id === tabID ? 'mobile-tab-active' : ''} aria-pressed={tab.tab_id === tabID} onClick={() => selectTab(tab)}><StatusDot status={tab.agent_status}/><span>{tab.label}</span></button>)}</div>
          <button aria-label="新建标签页" onClick={() => runAction(createTab)}><Plus size={16}/></button>
        </nav>
      </>}
      {!mobile && <header className="tabbar">
        <div className="tabs">{tabs.map((tab) => <div className={tab.tab_id === tabID ? 'tab tab-active' : 'tab'} key={tab.tab_id} onContextMenu={(event) => { selectTab(tab); openContextMenu(event, { kind: 'tab', tab }) }}><button className="tab-select" aria-pressed={tab.tab_id === tabID} data-tooltip={tab.label} onClick={() => selectTab(tab)}><StatusDot status={tab.agent_status}/>{tab.label !== String(tab.number) && <small className="tab-number">{tab.number}</small>}<span>{tab.label}{layout?.zoomed && tab.tab_id === tabID ? ' Z' : ''}</span></button><button className="tab-close" aria-label={`关闭标签页 ${tab.label}`} data-tooltip="关闭标签页" onClick={() => void closeTab(tab)}><X size={12}/></button></div>)}<button className="tab-add" aria-label="新建标签页" data-tooltip="新建标签页" onClick={() => runAction(createTab)}><Plus size={14}/></button></div>
        <span className="tabbar-summary" data-tooltip={activeWorkspace?.label}>{activeWorkspace?.label} · {panes?.length || 0} panes</span>
      </header>}

      <section className="terminal-surface" aria-label="终端工作区">
        {message && connection !== 'ready' && <div className="connection-overlay"><Server size={28}/><h2>{connection === 'connecting' ? '正在连接主机' : '主机暂时不可用'}</h2><p>{message || '正在建立安全连接…'}</p><Button className="button-secondary" onClick={() => navigate('/')}>返回主机</Button></div>}
        {visiblePanes?.map((pane) => {
          const interactivePane = { ...pane, right_click_passthrough: rightClickTargets[pane.pane_id] === 'pane' }
          const sourceRect = layout?.panes.find((item) => item.pane_id === pane.pane_id)?.rect
          return <div className="pane-position" key={`${connectionEpoch}:${pane.pane_id}`} style={mobile ? undefined : paneStyle(layout!, pane.pane_id)}>
            <TerminalPane client={client} pane={interactivePane} connectionEpoch={connectionEpoch} active={pane.pane_id === paneID} sourceCols={sourceRect?.width} sourceRows={sourceRect?.height} layoutVersion={sidebarOpen ? 1 : 0} onFocus={() => setPaneID(pane.pane_id)} onContextMenu={(event) => { const sourcePaneID = paneID && paneID !== pane.pane_id ? paneID : undefined; setPaneID(pane.pane_id); openContextMenu(event, { kind: 'pane', pane: interactivePane, sourcePaneID }) }} onControlReady={pane.pane_id === paneID ? handleControlReady : undefined} theme={terminalTheme} enhancedContrast={enhancedContrast} display={display} onFontSizeChange={pane.pane_id === paneID ? setActualFontSize : undefined} headerControls={!mobile && pane.pane_id === paneID ? <DisplayToolbar display={display} actualFontSize={actualFontSize} onChange={updateDisplay} onSettings={() => setSettingsOpen(true)}/> : undefined}/>
          </div>
        })}
        {!snapshot && !message && <div className="terminal-loading"><i/><span>加载 Herdr 会话…</span></div>}
      </section>

      {prefix && <div className="mode-bar"><strong>PREFIX</strong><span>esc cancel</span><span>v split right</span><span>− split down</span><span>hjkl focus</span><span>z zoom</span><span>x close</span><span>w switch</span></div>}
      {actionError && <div className="action-toast" role="alert"><span>{actionError}</span><button aria-label="关闭错误提示" onClick={() => setActionError('')}><X size={14}/></button></div>}
      {mobile && <div className="keybar" onPointerDown={(event) => { if ((event.target as HTMLElement).closest('button')) event.preventDefault() }} role="toolbar" aria-label="终端辅助键">{[
        ['Enter', '\r'], ['Esc', '\u001b'], ['Tab', '\t'], ['Ctrl+C', '\u0003'], ['Ctrl+D', '\u0004'], ['↑', '\u001b[A'], ['↓', '\u001b[B'], ['←', '\u001b[D'], ['→', '\u001b[C'], ['-', '-'], ['/', '/'], ['|', '|'], ['~', '~'],
      ].map(([label, data]) => <button key={label} disabled={!terminalInput} onClick={() => terminalInput?.(data)}>{label}</button>)}<button className={prefix ? 'key-active' : ''} onClick={() => setPrefix((value) => !value)}>⌘B</button><button disabled={!paneID} aria-label="上传图片" data-tooltip="上传图片" onClick={() => fileInputRef.current?.click()}><ImageIcon size={14}/></button></div>}
      <Input ref={fileInputRef} type="file" accept="image/png,image/jpeg,image/webp,image/gif" style={{ display: 'none' }} onChange={(event) => void handleImageUpload(event)} />
    </main>

    {switcherOpen && <div className="switcher-backdrop" onPointerDown={(event) => { if (event.currentTarget === event.target) setSwitcherOpen(false) }}><section className="switcher" role="dialog" aria-modal="true" aria-label="切换 Herdr 位置"><div className="switcher-header"><strong>切换位置</strong><button onClick={() => setSwitcherOpen(false)}>关闭 <X size={15}/></button></div><SwitcherGroup title="Agent">{agents.map((agent) => <button key={agent.pane_id} onClick={() => selectAgent(agent)}><StatusDot status={agent.agent_status}/><span><strong>{agent.name || agent.agent}</strong><small>{workspaceName(workspaces, agent.workspace_id)} · {agent.agent_status}</small></span></button>)}</SwitcherGroup><SwitcherGroup title="工作区"><button disabled={workspaceBusy || connection !== 'ready'} onClick={() => void createWorkspace()}><Plus size={16}/><span><strong>新建工作区</strong></span></button>{workspaces.map((workspace) => <button key={workspace.workspace_id} onClick={() => selectWorkspace(workspace)}><StatusDot status={workspace.agent_status}/><span><strong>{workspace.number} · {workspace.label}</strong><small>{workspace.pane_count} panes</small></span></button>)}</SwitcherGroup><SwitcherGroup title="标签页"><button onClick={() => { setSwitcherOpen(false); runAction(createTab) }}><Plus size={16}/><span><strong>新建标签页</strong></span></button>{tabs.map((tab) => <button key={tab.tab_id} onClick={() => selectTab(tab)}><StatusDot status={tab.agent_status}/><span><strong>{tab.number} · {tab.label}</strong><small>{tab.pane_count} panes</small></span></button>)}</SwitcherGroup><SwitcherGroup title="终端">{panes?.map((pane) => <button key={pane.pane_id} aria-pressed={pane.pane_id === paneID} onClick={() => { setPaneID(pane.pane_id); setSwitcherOpen(false) }}><StatusDot status={pane.agent_status || 'unknown'}/><span><strong>{pane.label || pane.terminal_title_stripped || pane.pane_id}</strong><small>{pane.cwd}</small></span></button>)}</SwitcherGroup><SwitcherGroup title="操作"><button onClick={() => void runPrefixAction('v')}><Columns2 size={16}/><span><strong>左右分屏</strong></span></button><button onClick={() => void runPrefixAction('-')}><Rows2 size={16}/><span><strong>上下分屏</strong></span></button><button onClick={() => void runPrefixAction('z')}><ZoomIn size={16}/><span><strong>聚焦当前终端</strong></span></button><button onClick={() => { setSwitcherOpen(false); setSettingsOpen(true) }}><Settings size={16}/><span><strong>设置</strong></span></button></SwitcherGroup></section></div>}
    {settingsOpen && <Modal title="工作台设置" className="settings-modal" closeLabel="关闭设置" onClose={() => setSettingsOpen(false)}><div className="form-stack"><div className="setting-row"><span><strong>界面外观</strong><small>仅影响工作台导航；网站页面单独设置。</small></span><AppearanceToggle scope="workbench"/></div><DisplaySettings display={display} mobile={mobile} onChange={updateDisplay} onReset={resetDisplay}/><label className="field"><span className="field-label">终端主题</span><Select aria-label="终端主题" className="input" value={themeName} onChange={(event) => { setThemeName(event.target.value); localStorage.setItem('herdrx.terminal-theme', event.target.value) }}>{Object.keys(terminalThemes).map((name) => <SelectOption key={name}>{name}</SelectOption>)}</Select></label><label className="setting-toggle"><Input type="checkbox" checked={enhancedContrast} onChange={(event) => { setEnhancedContrast(event.target.checked); localStorage.setItem('herdrx.enhanced-contrast', String(event.target.checked)) }}/><span><strong>增强终端对比度</strong><small>满足可读性要求，但会轻微修正主题中的低对比色。</small></span></label><div className="setting-row"><span><strong>Agent 通知</strong><small>{notificationPermission === 'granted' ? '页面关闭后仍可接收 blocked / done 推送' : '需要浏览器授权'}</small></span><Button className="button-secondary" disabled={notificationPermission === 'denied'} onClick={async () => setNotificationPermission(await enablePushNotifications())}><Bell size={15}/>{notificationPermission === 'granted' ? '已启用' : '启用'}</Button></div><Button className="button-primary" onClick={() => setSettingsOpen(false)}>完成</Button></div></Modal>}
    {contextMenu && <ContextMenu x={contextMenu.x} y={contextMenu.y} label={`${contextMenu.target.kind} 右键菜单`} items={contextItems(contextMenu.target)} onClose={() => setContextMenu(null)}/>}
    {prompt && <Modal title={prompt.title} className="prompt-modal" busy={promptBusy} onClose={() => setPrompt(null)}><Form className="form-stack" onSubmit={(event) => void submitPrompt(event)}><label className="field"><span className="field-label">{prompt.label}</span><Input aria-label={prompt.label} className={promptError ? 'input input-error' : 'input'} name="value" defaultValue={prompt.value} placeholder={prompt.placeholder} data-initial-focus autoComplete="off" aria-invalid={Boolean(promptError)} aria-describedby={promptError ? 'prompt-error' : undefined}/>{promptError && <span className="field-error" id="prompt-error" role="alert">{promptError}</span>}</label><div className="modal-actions"><Button type="button" className="button-secondary" disabled={promptBusy} onClick={() => setPrompt(null)}>取消</Button><Button type="submit" className="button-primary" pending={promptBusy}>{prompt.submitLabel}</Button></div></Form></Modal>}
    {confirmationDialog}
  </div>
}

function SidebarSection({ title, action, children }: { title: string; action?: ReactNode; children: ReactNode }) {
  return <section className="sidebar-section"><div className="sidebar-section-title"><span>{title}</span>{action}</div><div className="sidebar-list">{children}</div></section>
}

function SwitcherGroup({ title, children }: { title: string; children: ReactNode }) {
  return <section className="switcher-group"><h3>{title}</h3><div>{children}</div></section>
}

function statusPriority(status: string) { return ({ blocked: 5, done: 4, working: 3, idle: 2, unknown: 1 } as Record<string, number>)[status] || 0 }
function workspaceName(workspaces: Workspace[], id: string) { return workspaces.find((workspace) => workspace.workspace_id === id)?.label || id }
function currentLayout(snapshot: Snapshot | null, tabID: string): Layout | undefined { return snapshot?.layouts.find((layout) => layout.tab_id === tabID) }
function paneStyle(layout: Layout, paneID: string) {
  const pane = layout.panes.find((item) => item.pane_id === paneID)
  if (!pane || !layout.area.width || !layout.area.height) return {}
  return {
    left: `${((pane.rect.x - layout.area.x) / layout.area.width) * 100}%`,
    top: `${((pane.rect.y - layout.area.y) / layout.area.height) * 100}%`,
    width: `${(pane.rect.width / layout.area.width) * 100}%`,
    height: `${(pane.rect.height / layout.area.height) * 100}%`,
  }
}

async function showAgentNotification(name: string, status: string, hostID: string, paneID: string) {
  if (!('Notification' in window) || Notification.permission !== 'granted') return
  const title = status === 'blocked' ? `${name} needs attention` : `${name} finished`
  const options: NotificationOptions = { body: status === 'blocked' ? 'Agent 正在等待你的输入。' : 'Agent 已完成后台工作。', tag: `herdrx:${hostID}:${paneID}`, data: { url: `/h/${hostID}` } }
  if ('serviceWorker' in navigator) {
    const registration = await navigator.serviceWorker.ready
    await registration.showNotification(title, options)
  } else {
    new Notification(title, options)
  }
}

async function enablePushNotifications(): Promise<NotificationPermission> {
  if (!('Notification' in window) || !('serviceWorker' in navigator)) return 'denied'
  const permission = await Notification.requestPermission()
  if (permission !== 'granted') return permission
  const registration = await navigator.serviceWorker.ready
  const config = await api.pushConfig()
  let subscription = await registration.pushManager.getSubscription()
  if (!subscription) {
    subscription = await registration.pushManager.subscribe({ userVisibleOnly: true, applicationServerKey: decodeBase64URL(config.public_key) })
  }
  await api.subscribePush(subscription.toJSON())
  return permission
}

function decodeBase64URL(value: string) {
  const normalized = value.replace(/-/g, '+').replace(/_/g, '/')
  const decoded = atob(normalized + '='.repeat((4 - normalized.length % 4) % 4))
  return Uint8Array.from(decoded, (character) => character.charCodeAt(0))
}

function loadRightClickTargets(hostID: string): Record<string, 'herdr' | 'pane'> {
  try {
    const parsed = JSON.parse(localStorage.getItem(`herdrx.right-click.${hostID}`) || '{}') as Record<string, unknown>
    return Object.fromEntries(Object.entries(parsed).filter((entry): entry is [string, 'herdr' | 'pane'] => entry[1] === 'herdr' || entry[1] === 'pane'))
  } catch {
    return {}
  }
}
