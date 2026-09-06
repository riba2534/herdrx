export type User = {
  id: string
  email: string
  display_name: string
  role: 'admin' | 'user'
}

export type InstanceSettings = {
  registration: 'closed' | 'invite'
  revision: number
  updated_at: string
  updated_by?: string
}

export type Host = {
  id: string
  name: string
  transport: 'local' | 'ssh' | 'tailcat'
  hostname?: string
  port?: number
  username?: string
  session_name?: string
  auth_method?: string
  ssh_key_id?: string
  folder_id?: string
  host_key?: string
  pending_host_key?: string
  created_at: string
  updated_at: string
}

export type SSHKey = { id: string; name: string; public_key: string; fingerprint: string; algorithm: string; certificate?: string; encrypted: boolean; revision: number; host_count: number; created_at: string; updated_at: string }
export type HostFolder = { id: string; name: string; parent_id?: string; created_at: string; updated_at: string }

export type AgentStatus = 'blocked' | 'working' | 'done' | 'idle' | 'unknown'

export type Workspace = {
  workspace_id: string
  label: string
  number: number
  active_tab_id: string
  agent_status: AgentStatus
  focused: boolean
  pane_count: number
  tab_count: number
  branch?: string
  worktree?: {
    repo_key: string
    repo_name: string
    repo_root: string
    checkout_path: string
    is_linked_worktree: boolean
  }
}

export type Tab = {
  tab_id: string
  workspace_id: string
  label: string
  number: number
  pane_count: number
  agent_status: AgentStatus
  focused: boolean
}

export type Pane = {
  pane_id: string
  workspace_id: string
  tab_id: string
  terminal_id: string
  label?: string
  terminal_title?: string
  terminal_title_stripped?: string
  agent?: string
  agent_status: AgentStatus
  cwd?: string
  foreground_cwd?: string
  focused: boolean
  revision: number
  right_click_passthrough?: boolean
  scroll?: { max_offset_from_bottom: number; offset_from_bottom: number; viewport_rows: number }
}

export type Agent = {
  name?: string
  agent: string
  agent_status: AgentStatus
  pane_id: string
  workspace_id: string
  tab_id: string
  cwd?: string
  focused: boolean
}

export type Rect = { x: number; y: number; width: number; height: number }
export type Layout = {
  workspace_id: string
  tab_id: string
  focused_pane_id: string
  area: Rect
  panes: Array<{ pane_id: string; focused: boolean; rect: Rect }>
  splits: Array<{ id: string; direction: 'right' | 'down'; ratio: number; rect: Rect }>
  zoomed: boolean
}

export type Snapshot = {
  version: string
  protocol: number
  focused_workspace_id: string
  focused_tab_id: string
  focused_pane_id: string
  workspaces: Workspace[]
  tabs: Tab[]
  panes: Pane[]
  layouts: Layout[]
  agents: Agent[]
}

export type Pagination = { offset: number; limit: number; next_offset: number; has_more: boolean }
export type AdminUser = User & { disabled: boolean; created_at: string; active_sessions: number }
export type LoginSession = { id: string; user_id: string; user_agent: string; remote_ip: string; created_at: string; expires_at: string }
export type Invite = { id: string; created_by: string; created_at: string; expires_at: string; used_by?: string; used_at?: string; revoked_by?: string; revoked_at?: string; status: 'active' | 'used' | 'expired' | 'revoked' }
export type AuditEntry = { id: number; user_id: string; action: string; target_type: string; target_id: string; remote_ip: string; created_at: string; details: Record<string, unknown> }
