export const connectionLabels = {
  connecting: '连接中',
  ready: '已连接',
  degraded: '连接受限',
  offline: '已断开',
} as const

export const transportLabels = {
  ssh: 'SSH',
  tailcat: 'Tailcat',
  local: '本机',
} as const

export const agentStatusLabels = {
  idle: '空闲',
  working: '运行中',
  blocked: '等待确认',
  done: '已完成',
  unknown: '未知',
} as const

const contextMenuLabels = {
  sidebar: '侧栏右键菜单',
  workspace: '工作区右键菜单',
  tab: '标签页右键菜单',
  pane: '终端右键菜单',
} as const

export function connectionLabel(state: string) {
  return connectionLabels[state as keyof typeof connectionLabels] || state
}

export function transportLabel(value?: string) {
  if (!value) return ''
  return transportLabels[value as keyof typeof transportLabels] || value
}

export function agentStatusLabel(status: string) {
  return agentStatusLabels[status as keyof typeof agentStatusLabels] || status
}

export function paneDisplayName(pane: { label?: string; agent?: string; terminal_title_stripped?: string; pane_id: string }) {
  return pane.label || pane.agent || pane.terminal_title_stripped || pane.pane_id
}

export function terminalCountLabel(count: number) {
  return `${count} 个终端`
}

export function tabCountLabel(count: number) {
  return `${count} 个标签页`
}

export function workspaceCountLabel(paneCount: number, tabCount: number) {
  return `${terminalCountLabel(paneCount)} · ${tabCountLabel(tabCount)}`
}

export function agentNotificationTitle(name: string, status: string) {
  return status === 'blocked' ? `${name} 等待你的确认` : `${name} 已完成`
}

export function contextMenuLabel(kind: string) {
  return contextMenuLabels[kind as keyof typeof contextMenuLabels] || '终端右键菜单'
}

export function hostConnectionText(connection: string, transport?: string, hostListError?: string) {
  if (hostListError) return '主机列表加载失败'
  if (connection === 'ready') return transportLabel(transport) || connectionLabel(connection)
  return connectionLabel(connection)
}
