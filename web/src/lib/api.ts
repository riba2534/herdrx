import type { Host, User, AdminUser, LoginSession, Invite, AuditEntry, Pagination, InstanceSettings, SSHKey, HostFolder } from '../types'

let csrfToken = ''
let sessionID = ''
let generation = 0

type AuthEvent = { kind: 'expired'; hadSession: boolean } | { kind: 'authenticated'; user: User; sessionID: string }
const authListeners = new Set<(event: AuthEvent) => void>()
export function onAuthEvent(listener: (event: AuthEvent) => void) {
  authListeners.add(listener)
  return () => { authListeners.delete(listener) }
}
export function authenticationGeneration() { return generation }
export function currentSessionID() { return sessionID }
export function invalidateAuthentication(expected = generation) {
  if (expected !== generation) return
  const hadSession = Boolean(sessionID || csrfToken)
  generation++
  csrfToken = ''
  sessionID = ''
  for (const listener of authListeners) listener({ kind: 'expired', hadSession })
}


export class APIError extends Error {
  constructor(public status: number, public code: string, message: string, public details?: unknown) {
    super(message)
  }
}

async function request<T>(path: string, init: RequestInit = {}): Promise<T> {
  const epoch = generation
  const headers = new Headers(init.headers)
  if (init.body && !(init.body instanceof FormData)) headers.set('Content-Type', 'application/json')
  if (init.method && !['GET', 'HEAD'].includes(init.method)) headers.set('X-CSRF-Token', csrfToken)
  const response = await fetch(path, { ...init, headers, credentials: 'same-origin' })
  const payload = await response.json().catch(() => ({}))
  const publicAuth = ['/api/login', '/api/register', '/api/bootstrap', '/api/bootstrap/status', '/api/logout'].includes(path)
  if (!response.ok) {
    if (response.status === 401 && !publicAuth) invalidateAuthentication(epoch)
    throw new APIError(response.status, payload.code || 'request_failed', payload.error || '请求失败', payload)
  }
  if (payload.csrf_token) {
    if (epoch !== generation) throw new APIError(409, 'auth_changed', '登录状态已变化，请重试')
    const changed = sessionID !== (payload.session_id || '') || publicAuth
    if (changed) generation++
    csrfToken = payload.csrf_token
    sessionID = payload.session_id || ''
    if (changed) for (const listener of authListeners) listener({ kind: 'authenticated', user: payload.user, sessionID })
  }
  return payload as T
}

export type CLIRelease = { status: 'available' | 'unpublished' | 'unavailable'; version?: string; prerelease?: boolean }
export type RelayOffer = { available: boolean; workbench?: string; token?: string; address?: string; expires_at?: string }

export const api = {
  cliRelease: () => request<CLIRelease>('/api/cli-release'),
  relayOffer: () => request<RelayOffer>('/api/tailcat/relay-offer', { method: 'POST' }),
  bootstrapStatus: () => request<{ required: boolean; registration: 'invite' | 'closed' }>('/api/bootstrap/status'),
  bootstrap: (input: { email: string; password: string; display_name: string; token: string }) =>
    request<{ user: User; csrf_token: string; session_id: string }>('/api/bootstrap', { method: 'POST', body: JSON.stringify(input) }),
  login: (input: { email: string; password: string }) =>
    request<{ user: User; csrf_token: string; session_id: string }>('/api/login', { method: 'POST', body: JSON.stringify(input) }),
  register: (input: { email: string; password: string; display_name: string; invite_code: string }) =>
    request<{ user: User; csrf_token: string; session_id: string }>('/api/register', { method: 'POST', body: JSON.stringify(input) }),
  me: () => request<{ user: User; csrf_token: string; session_id: string }>('/api/me'),
  logout: () => request<{ ok: boolean }>('/api/logout', { method: 'POST' }),
  hosts: () => request<{ hosts: Host[] }>('/api/hosts/'),
  host: (id: string) => request<{ host: Host }>(`/api/hosts/${encodeURIComponent(id)}/`),
  createHost: (input: Record<string, unknown>) =>
    request<{ host: Host; public_key?: string }>('/api/hosts/', { method: 'POST', body: JSON.stringify(input) }),
  renameHost: (id: string, name: string) =>
    request<{ host: Host }>(`/api/hosts/${encodeURIComponent(id)}/`, { method: 'PATCH', body: JSON.stringify({ name }) }),
  updateSSHHost: (id: string, input: Record<string, unknown>) => request<{ host: Host; public_key?: string }>(`/api/hosts/${encodeURIComponent(id)}/ssh`, { method: 'PUT', body: JSON.stringify(input) }),
  moveHost: (id: string, folder_id: string) => request<{ host: Host }>(`/api/hosts/${encodeURIComponent(id)}/folder`, { method: 'PATCH', body: JSON.stringify({ folder_id }) }),
  sshKeys: () => request<{ keys: SSHKey[] }>('/api/ssh-keys/'),
  createSSHKey: (input: Record<string, unknown>) => request<{ key: SSHKey }>('/api/ssh-keys/', { method: 'POST', body: JSON.stringify(input) }),
  updateSSHKey: (id: string, input: Record<string, unknown>) => request<{ key: SSHKey }>(`/api/ssh-keys/${encodeURIComponent(id)}`, { method: 'PATCH', body: JSON.stringify(input) }),
  deleteSSHKey: (id: string) => request<{ ok: boolean }>(`/api/ssh-keys/${encodeURIComponent(id)}`, { method: 'DELETE' }),
  hostFolders: () => request<{ folders: HostFolder[] }>('/api/host-folders/'),
  createHostFolder: (name: string, parent_id: string) => request<{ folder: HostFolder }>('/api/host-folders/', { method: 'POST', body: JSON.stringify({ name, parent_id }) }),
  updateHostFolder: (id: string, name: string, parent_id: string) => request<{ folder: HostFolder }>(`/api/host-folders/${encodeURIComponent(id)}`, { method: 'PATCH', body: JSON.stringify({ name, parent_id }) }),
  deleteHostFolder: (id: string) => request<{ ok: boolean }>(`/api/host-folders/${encodeURIComponent(id)}`, { method: 'DELETE' }),
  deleteHost: (id: string) => request<{ ok: boolean }>(`/api/hosts/${encodeURIComponent(id)}/`, { method: 'DELETE' }),
  refreshEndpoint: (id: string, update: string) => request<{ ok: boolean; revision: number }>(`/api/hosts/${encodeURIComponent(id)}/endpoint`, { method: 'POST', body: JSON.stringify({ update }) }),
  trustHostKey: (id: string) => request<{ ok: boolean }>(`/api/hosts/${encodeURIComponent(id)}/trust-host-key`, { method: 'POST' }),
  snapshot: (id: string) => request<{ snapshot: unknown }>(`/api/hosts/${encodeURIComponent(id)}/snapshot`),
  createInvite: () => request<{ code: string; invite: Invite }>('/api/admin/invites', { method: 'POST' }),
  adminSettings: () => request<{ settings: InstanceSettings }>('/api/admin/settings'),
  setRegistration: (registration: InstanceSettings['registration'], revision: number) => request<{ settings: InstanceSettings }>('/api/admin/settings', { method: 'PATCH', body: JSON.stringify({ registration, revision }) }),
  adminUsers: (offset = 0, filters: Record<string, string> = {}) => request<Pagination & { users: AdminUser[] }>('/api/admin/users?' + new URLSearchParams({ ...filters, offset: String(offset) })),
  setUserDisabled: (id: string, disabled: boolean) => request<{ ok: boolean }>(`/api/admin/users/${encodeURIComponent(id)}`, { method: 'PATCH', body: JSON.stringify({ disabled }) }),
  adminSessions: (id: string, offset = 0) => request<Pagination & { sessions: LoginSession[] }>(`/api/admin/users/${encodeURIComponent(id)}/sessions?offset=${offset}`),
  revokeSession: (userID: string, sessionID: string) => request<{ ok: boolean }>(`/api/admin/users/${encodeURIComponent(userID)}/sessions/${encodeURIComponent(sessionID)}`, { method: 'DELETE' }),
  revokeAllSessions: (userID: string) => request<{ ok: boolean }>(`/api/admin/users/${encodeURIComponent(userID)}/sessions`, { method: 'DELETE' }),
  adminInvites: (offset = 0) => request<Pagination & { invites: Invite[] }>(`/api/admin/invites?offset=${offset}`),
  revokeInvite: (id: string) => request<{ ok: boolean }>(`/api/admin/invites/${encodeURIComponent(id)}`, { method: 'DELETE' }),
  adminAudit: (filters: Record<string, string>, offset = 0) => request<Pagination & { events: AuditEntry[] }>('/api/admin/audit?' + new URLSearchParams({ ...filters, offset: String(offset) })),
  createTailcatSetup: () => request<{ setup_id: string; command: string; expires_at: string }>('/api/tailcat/setups', { method: 'POST' }),
  pairTailcat: (payload: Record<string, unknown>) => request<{ host: Host }>('/api/tailcat/pair', { method: 'POST', body: JSON.stringify(payload) }),
  createTailcatEnrollment: (input: { connection_string: string; name?: string; session_name?: string }) =>
    request<{ task_id?: string; id?: string; status: string; agent_id: string; host_id?: string }>('/api/tailcat/enrollments', { method: 'POST', body: JSON.stringify(input) }),
  getTailcatEnrollment: (taskID: string) =>
    request<{ id: string; status: string; host_id?: string; error?: string; agent_id?: string }>('/api/tailcat/enrollments/' + encodeURIComponent(taskID)),
  listTailcatEnrollments: () =>
    request<{ tasks: { id: string; status: string; host_id?: string; error?: string; agent_id?: string }[] }>('/api/tailcat/enrollments'),
  pushConfig: () => request<{ public_key: string }>('/api/push/config'),
  subscribePush: (subscription: PushSubscriptionJSON) => request<{ ok: boolean }>('/api/push/subscriptions', { method: 'POST', body: JSON.stringify(subscription) }),
  pasteImage: (hostID: string, paneID: string, file: File | Blob, inject = true) => {
    const formData = new FormData()
    formData.append('file', file)
    return request<{ ok: boolean; path: string; injected: boolean }>(
      `/api/hosts/${encodeURIComponent(hostID)}/panes/${encodeURIComponent(paneID)}/paste-image?inject=${inject}`,
      { method: 'POST', body: formData }
    )
  },
}

export function csrf() { return csrfToken }
