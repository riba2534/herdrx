import { fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { api } from '../lib/api'
import { AdminPage } from './AdminPage'

const page = { offset: 0, limit: 50, has_more: false, next_offset: 2 }
const admin = { id: 'admin', display_name: 'Admin', email: 'admin@example.test', role: 'admin' as const, created_at: '', disabled: false, active_sessions: 1 }
const member = { ...admin, id: 'member', display_name: 'Member', role: 'user' as const }
const auth = { user: admin, sessionID: 'current', registration: 'invite', refresh: vi.fn() }
vi.mock('../auth', () => ({ useAuth: () => auth }))
vi.mock('../lib/api', () => ({ api: { adminSettings: vi.fn(), setRegistration: vi.fn(), adminUsers: vi.fn(), setUserDisabled: vi.fn(), adminSessions: vi.fn(), revokeAllSessions: vi.fn(), adminInvites: vi.fn(), createInvite: vi.fn(), adminAudit: vi.fn() }, authenticationGeneration: () => 1, invalidateAuthentication: vi.fn() }))
beforeEach(() => {
  auth.registration = 'invite'
  vi.mocked(api.adminSettings).mockResolvedValue({ settings: { registration: 'invite', revision: 1, updated_at: '' } })
  vi.mocked(api.adminUsers).mockResolvedValue({ ...page, users: [admin, member] })
  vi.mocked(api.adminSessions).mockResolvedValue({ ...page, sessions: [] })
  vi.mocked(api.adminInvites).mockResolvedValue({ ...page, invites: [] })
  vi.mocked(api.adminAudit).mockResolvedValue({ ...page, events: [] })
})
afterEach(() => { vi.resetAllMocks(); vi.restoreAllMocks() })
it('prevents self-disable and reports a failed member mutation', async () => {
  vi.mocked(api.setUserDisabled).mockRejectedValue(new Error('操作未完成'))
  render(<AdminPage/>)
  await screen.findByText('Member')
  expect(within(screen.getByRole('heading', { name: /^Admin/ }).closest('article')!).getByRole('button', { name: '禁用账号' })).toBeDisabled()
  fireEvent.click(within(screen.getByText('Member').closest('article')!).getByRole('button', { name: '禁用账号' }))
  expect(screen.getByRole('alertdialog')).toHaveTextContent('远程 Herdr 和任务继续运行')
  expect(api.setUserDisabled).not.toHaveBeenCalled()
  fireEvent.click(within(screen.getByRole('alertdialog')).getByRole('button', { name: '禁用账号' }))
  expect(await screen.findByRole('alert')).toHaveTextContent('操作未完成')
  expect(api.setUserDisabled).toHaveBeenCalledWith('member', true)
})
it('hides invite plaintext on navigation and disables creation in closed mode', async () => {
  vi.mocked(api.createInvite).mockResolvedValue({ code: 'one-time-code', invite: { id: 'invite', created_by: 'admin', created_at: '', expires_at: '', status: 'active' } })
  render(<AdminPage/>)
  fireEvent.click(screen.getByRole('button', { name: '邀请' }))
  await waitFor(() => expect(screen.getByRole('button', { name: '创建邀请' })).toBeEnabled())
  fireEvent.click(screen.getByRole('button', { name: '创建邀请' }))
  await screen.findByText('one-time-code')
  fireEvent.click(screen.getByRole('button', { name: '审计记录' }))
  expect(screen.queryByText('one-time-code')).not.toBeInTheDocument()
  fireEvent.click(screen.getByRole('button', { name: '邀请' }))
  vi.mocked(api.adminSettings).mockResolvedValue({ settings: { registration: 'closed', revision: 2, updated_at: '' } })
  await waitFor(() => expect(screen.getByRole('button', { name: '刷新' })).toBeEnabled())
  fireEvent.click(screen.getByRole('button', { name: '刷新' }))
  await waitFor(() => expect(screen.getByRole('button', { name: '开启注册' })).toBeEnabled())
  expect(screen.getByRole('button', { name: '创建邀请' })).toBeDisabled()
})

it('requires a loaded setting, saves its revision, and refreshes public registration state', async () => {
  vi.mocked(api.adminSettings).mockResolvedValue({ settings: { registration: 'closed', revision: 7, updated_at: '' } })
  vi.mocked(api.setRegistration).mockImplementation(async () => {
    const settings = { registration: 'invite' as const, revision: 8, updated_at: '' }
    vi.mocked(api.adminSettings).mockResolvedValue({ settings })
    return { settings }
  })
  render(<AdminPage/>)
  expect(screen.getByRole('button', { name: '开启注册' })).toBeDisabled()
  await waitFor(() => expect(screen.getByRole('button', { name: '开启注册' })).toBeEnabled())
  fireEvent.click(screen.getByRole('button', { name: '开启注册' }))
  await waitFor(() => expect(screen.getByRole('button', { name: '关闭注册' })).toBeEnabled())
  expect(api.setRegistration).toHaveBeenCalledWith('invite', 7)
  expect(auth.refresh).toHaveBeenCalledOnce()
})

it('keeps the confirmed state when a stale settings update fails', async () => {
  vi.mocked(api.setRegistration).mockRejectedValue(new Error('设置已变更，请刷新后重试'))
  render(<AdminPage/>)
  await waitFor(() => expect(screen.getByRole('button', { name: '关闭注册' })).toBeEnabled())
  fireEvent.click(screen.getByRole('button', { name: '关闭注册' }))
  expect(await screen.findByRole('alert')).toHaveTextContent('设置已变更')
  expect(screen.getByRole('button', { name: '关闭注册' })).toBeEnabled()
  expect(auth.refresh).not.toHaveBeenCalled()
})

it('blocks registration controls when settings cannot be read', async () => {
  vi.mocked(api.adminSettings).mockRejectedValue(new Error('无法读取注册设置'))
  render(<AdminPage/>)
  expect(await screen.findByRole('alert')).toHaveTextContent('无法读取')
  expect(screen.getByRole('button', { name: '开启注册' })).toBeDisabled()
  fireEvent.click(screen.getByRole('button', { name: '邀请' }))
  expect(screen.getByRole('button', { name: '创建邀请' })).toBeDisabled()
})

it('applies user search, role, and status together from the first page', async () => {
  render(<AdminPage/>)
  await screen.findByText('Member')
  fireEvent.change(screen.getByLabelText('搜索用户'), { target: { value: ' Member ' } })
  fireEvent.keyDown(screen.getByLabelText('角色'), { key: 'ArrowDown' })
  fireEvent.click(within(document.querySelector('[role="listbox"]') as HTMLElement).getByText('普通用户'))
  fireEvent.keyDown(screen.getByLabelText('账号状态'), { key: 'ArrowDown' })
  fireEvent.click(within(document.querySelector('[role="listbox"]') as HTMLElement).getByText('已禁用'))
  fireEvent.click(screen.getByRole('button', { name: '筛选用户' }))
  await waitFor(() => expect(api.adminUsers).toHaveBeenLastCalledWith(0, { q: 'Member', role: 'user', status: 'disabled' }))
})
it('ignores late session data after switching users', async () => {
  let finish!: (value: Awaited<ReturnType<typeof api.adminSessions>>) => void
  vi.mocked(api.adminSessions).mockImplementationOnce(() => new Promise((resolve) => { finish = resolve }))
  render(<AdminPage/>)
  await screen.findByText('Member')
  fireEvent.click(within(screen.getByRole('heading', { name: /^Admin/ }).closest('article')!).getByRole('button', { name: '查看登录' }))
  fireEvent.click(within(screen.getByText('Member').closest('article')!).getByRole('button', { name: '查看登录' }))
  await screen.findByText('没有有效的 Web 登录。')
  finish({ ...page, sessions: [{ id: 'old', user_id: 'admin', user_agent: 'Old browser', remote_ip: '', created_at: '', expires_at: '' }] })
  await waitFor(() => expect(api.adminSessions).toHaveBeenCalledTimes(2))
  expect(screen.queryByText('Old browser')).not.toBeInTheDocument()
})
