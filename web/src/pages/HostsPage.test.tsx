import { fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { api } from '../lib/api'
import { HostsPage } from './HostsPage'

const host = { id: 'hst_test', name: 'Old host', transport: 'ssh' as const, hostname: 'example.test', username: 'me', port: 2222, created_at: '', updated_at: '' }
const auth = { user: { id: 'tester', role: 'user' as string, display_name: 'Tester' }, signOut() {} }
vi.mock('../auth', () => ({ useAuth: () => auth }))
vi.mock('../lib/api', () => ({
  APIError: class APIError extends Error { constructor(public status: number, public code: string, message: string) { super(message) } },
  api: {
    hosts: vi.fn(), renameHost: vi.fn(), refreshEndpoint: vi.fn(), hostFolders: vi.fn(), sshKeys: vi.fn(), cliRelease: vi.fn(),
    createHost: vi.fn(), updateSSHHost: vi.fn(), createTailcatEnrollment: vi.fn(), getTailcatEnrollment: vi.fn(),
    deleteHost: vi.fn(), relayOffer: vi.fn(),
  },
}))
beforeEach(() => {
  sessionStorage.clear()
  vi.mocked(api.hostFolders).mockResolvedValue({ folders: [] })
  vi.mocked(api.sshKeys).mockResolvedValue({ keys: [] })
  vi.mocked(api.cliRelease).mockResolvedValue({ status: 'unpublished' })
  vi.mocked(api.relayOffer).mockResolvedValue({ available: false })
})
afterEach(() => { vi.resetAllMocks(); auth.user.role = 'user'; sessionStorage.clear() })

describe('host renaming', () => {
  it.each(['user', 'admin'])('offers local Herdr only to an administrator (%s)', async (role) => {
    auth.user.role = role
    vi.mocked(api.hosts).mockResolvedValue({ hosts: [host] })
    render(<HostsPage/>)
    await screen.findByRole('heading', { name: 'Old host' })
    fireEvent.click(screen.getByRole('button', { name: '添加主机' }))
    await screen.findByText(/CLI 安装包尚未发布/)
    fireEvent.keyDown(screen.getByRole('combobox', { name: '连接方式' }), { key: 'ArrowDown' })
    const list = within(document.querySelector('[role="listbox"]') as HTMLElement)
    expect(list.getByText('SSH', { exact: true })).toBeInTheDocument()
    expect(list.getByText(/Tailcat/)).toBeInTheDocument()
    expect(list.queryByText('本机 Herdr') !== null).toBe(role === 'admin')
  })

  it('prefills the name, saves it, and updates the card without changing its address', async () => {
    vi.mocked(api.hosts).mockResolvedValue({ hosts: [host] })
    vi.mocked(api.renameHost).mockResolvedValue({ host: { ...host, name: '工作站' } })
    render(<HostsPage/>)
    fireEvent.click(await screen.findByRole('button', { name: '重命名 Old host' }))
    const input = screen.getByRole('textbox', { name: '主机名称' })
    expect(input).toHaveValue('Old host')
    fireEvent.change(input, { target: { value: '  工作站  ' } })
    fireEvent.click(screen.getByRole('button', { name: '保存名称' }))
    await screen.findByRole('heading', { name: '工作站' })
    expect(api.renameHost).toHaveBeenCalledWith('hst_test', '工作站')
    expect(screen.getByText('me@example.test:2222')).toBeInTheDocument()
    await waitFor(() => expect(screen.getByRole('button', { name: '重命名 工作站' })).toHaveFocus())
  })

  it('validates empty names, preserves failed edits, and cancels without another write', async () => {
    vi.mocked(api.hosts).mockResolvedValue({ hosts: [host] })
    vi.mocked(api.renameHost).mockRejectedValue(new Error('保存失败，请重试'))
    render(<HostsPage/>)
    fireEvent.click(await screen.findByRole('button', { name: '重命名 Old host' }))
    const input = screen.getByRole('textbox', { name: '主机名称' })
    fireEvent.change(input, { target: { value: '  ' } })
    fireEvent.click(screen.getByRole('button', { name: '保存名称' }))
    expect(api.renameHost).not.toHaveBeenCalled()
    expect(screen.getByRole('alert')).toHaveTextContent('1–80')
    fireEvent.change(input, { target: { value: 'New host' } })
    fireEvent.click(screen.getByRole('button', { name: '保存名称' }))
    await waitFor(() => expect(screen.getByRole('alert')).toHaveTextContent('保存失败'))
    expect(input).toHaveValue('New host')
    fireEvent.keyDown(input, { key: 'Escape' })
    expect(screen.getByRole('heading', { name: 'Old host' })).toBeInTheDocument()
    expect(api.renameHost).toHaveBeenCalledTimes(1)
  })
})


it('imports a Tailcat endpoint update, retains a failed draft, and clears it after success', async () => {
  const tailcat = { ...host, transport: 'tailcat' as const }
  vi.mocked(api.hosts).mockResolvedValue({ hosts: [tailcat] })
  vi.mocked(api.refreshEndpoint).mockRejectedValueOnce(new Error('更新包已过期')).mockResolvedValueOnce({ ok: true, revision: 2 })
  render(<HostsPage/>)
  fireEvent.click(await screen.findByRole('button', { name: '更新连接端点 Old host' }))
  const input = screen.getByRole('textbox', { name: '签名端点更新包' })
  fireEvent.change(input, { target: { value: 'herdrx://endpoint-v1/fixture' } })
  fireEvent.click(screen.getByRole('button', { name: '导入更新' }))
  await screen.findByText('更新包已过期')
  expect(input).toHaveValue('herdrx://endpoint-v1/fixture')
  fireEvent.click(screen.getByRole('button', { name: '导入更新' }))
  await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument())
  expect(api.refreshEndpoint).toHaveBeenLastCalledWith(host.id, 'herdrx://endpoint-v1/fixture')
  expect(screen.getAllByRole('heading', { name: 'Old host' })).toHaveLength(1)
  fireEvent.click(screen.getByRole('button', { name: '更新连接端点 Old host' }))
  expect(screen.getByRole('textbox', { name: '签名端点更新包' })).toHaveValue('')
})

it('keeps an empty SSH port instead of coercing it to 0', async () => {
  vi.mocked(api.hosts).mockResolvedValue({ hosts: [host] })
  render(<HostsPage/>)
  fireEvent.click(await screen.findByRole('button', { name: '添加主机' }))
  await screen.findByText(/CLI 安装包尚未发布/)
  fireEvent.keyDown(screen.getByRole('combobox', { name: '连接方式' }), { key: 'ArrowDown' })
  fireEvent.click(within(document.querySelector('[role="listbox"]') as HTMLElement).getByText('SSH', { exact: true }))
  const port = screen.getByLabelText('端口')
  expect(port).toHaveValue(22)
  fireEvent.change(port, { target: { value: '' } })
  expect(port).toHaveValue(null)
})

describe('System OpenSSH connection settings', () => {
  it.each([0, 2222])('preserves port %s when saving an existing system host', async (port) => {
    auth.user.role = 'admin'
    const systemHost = { ...host, port, username: '', auth_method: 'system_ssh' }
    vi.mocked(api.hosts).mockResolvedValue({ hosts: [systemHost] })
    vi.mocked(api.updateSSHHost).mockResolvedValue({ host: systemHost })
    render(<HostsPage/>)
    fireEvent.click(await screen.findByRole('button', { name: '连接设置 Old host' }))
    expect(screen.getByLabelText('端口')).toHaveValue(port)
    expect(screen.getByLabelText('SSH 用户')).not.toBeRequired()
    expect(screen.queryByLabelText('跳板机 ProxyJump（可选）')).not.toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: '保存主机' }))
    await waitFor(() => expect(api.updateSSHHost).toHaveBeenCalledWith(host.id, expect.objectContaining({
      auth_method: 'system_ssh', port, username: '', keep_secret: false,
    })))
    expect(await screen.findByRole('status')).toHaveTextContent('已保存 Old host，打开主机连接')
  })

  it('clears website credentials and ProxyJump when switching to the system identity', async () => {
    auth.user.role = 'admin'
    const keyHost = { ...host, auth_method: 'saved_key', ssh_key_id: 'key-1', proxy_jump: 'jump@example.test:2200' }
    vi.mocked(api.hosts).mockResolvedValue({ hosts: [keyHost] })
    vi.mocked(api.updateSSHHost).mockResolvedValue({ host: { ...host, auth_method: 'system_ssh', port: 0 } })
    render(<HostsPage/>)
    fireEvent.click(await screen.findByRole('button', { name: '连接设置 Old host' }))
    fireEvent.keyDown(screen.getByRole('combobox', { name: '认证', exact: true }), { key: 'ArrowDown' })
    fireEvent.click(within(document.querySelector('[role="listbox"]') as HTMLElement).getByText('System OpenSSH（Kerberos / SSH 配置）'))
    expect(screen.getByLabelText('端口')).toHaveValue(0)
    expect(screen.queryByLabelText('跳板机 ProxyJump（可选）')).not.toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: '保存主机' }))
    await waitFor(() => expect(api.updateSSHHost).toHaveBeenCalledWith(host.id, expect.objectContaining({
      auth_method: 'system_ssh', port: 0, proxy_jump: '', secret: '', passphrase: '', ssh_key_id: '', keep_secret: false,
    })))
  })
})

it('matches host search against username and folder path', async () => {
  vi.mocked(api.hostFolders).mockResolvedValue({ folders: [{ id: 'f-work', name: '公司', created_at: '', updated_at: '' }, { id: 'f-sub', name: '测试环境', parent_id: 'f-work', created_at: '', updated_at: '' }] })
  vi.mocked(api.hosts).mockResolvedValue({
    hosts: [
      { ...host, id: 'h-user', name: '节点-01', username: 'ubuntu', folder_id: 'f-sub' },
      { ...host, id: 'h-other', name: '其他', username: 'deploy', folder_id: 'f-work' },
    ],
  })
  render(<HostsPage/>)
  await screen.findByRole('heading', { name: '节点-01' })
  fireEvent.change(screen.getByLabelText('搜索主机'), { target: { value: 'ubuntu' } })
  expect(screen.getByRole('heading', { name: '节点-01' })).toBeInTheDocument()
  expect(screen.queryByRole('heading', { name: '其他' })).not.toBeInTheDocument()
  fireEvent.change(screen.getByLabelText('搜索主机'), { target: { value: '测试环境' } })
  expect(screen.getByRole('heading', { name: '节点-01' })).toBeInTheDocument()
  expect(screen.queryByRole('heading', { name: '其他' })).not.toBeInTheDocument()
})

it('shows a retry empty state when the host list cannot be loaded', async () => {
  vi.mocked(api.hosts).mockRejectedValue(new Error('boom'))
  render(<HostsPage/>)
  expect(await screen.findByRole('heading', { name: '无法读取主机列表，请检查网络后重试' })).toBeInTheDocument()
  expect(screen.queryByRole('heading', { name: '还没有主机' })).not.toBeInTheDocument()
  vi.mocked(api.hosts).mockResolvedValue({ hosts: [host] })
  fireEvent.click(screen.getByRole('button', { name: '重试' }))
  await screen.findByRole('heading', { name: 'Old host' })
})

it('keeps Tailcat credentials, shows the error next to them, and allows closing while waiting', async () => {
  vi.mocked(api.hosts).mockResolvedValue({ hosts: [] })
  vi.mocked(api.createTailcatEnrollment).mockResolvedValue({ task_id: 'task-1', status: 'connecting', agent_id: 'agent-1' })
  let fail!: (reason?: unknown) => void
  vi.mocked(api.getTailcatEnrollment).mockImplementation(() => new Promise((_, reject) => { fail = reject }))
  render(<HostsPage/>)
  fireEvent.click(await screen.findByRole('button', { name: '添加第一台主机' }))
  fireEvent.click(await screen.findByRole('button', { name: '已安装，下一步' }))
  fireEvent.click(await screen.findByRole('button', { name: '服务已就绪，下一步' }))
  const creds = await screen.findByRole('textbox', { name: /绑定凭据/ })
  fireEvent.change(creds, { target: { value: 'herdrx://v1/keep-me' } })
  fireEvent.click(screen.getByRole('button', { name: '绑定并打开主机' }))
  expect(await screen.findByRole('button', { name: '关闭' })).toBeEnabled()
  expect(screen.getByRole('button', { name: '稍后再看' })).toBeEnabled()
  await waitFor(() => expect(api.createTailcatEnrollment).toHaveBeenCalled())
  expect(creds).toHaveValue('herdrx://v1/keep-me')
  fireEvent.click(screen.getByRole('button', { name: '稍后再看' }))
  await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument())
  fireEvent.click(screen.getByRole('button', { name: '添加主机' }))
  expect(await screen.findByRole('textbox', { name: /绑定凭据/ })).toHaveValue('herdrx://v1/keep-me')
  fail(new Error('受控端拒绝了绑定：凭据已过期'))
  expect(await screen.findByRole('alert')).toHaveTextContent('凭据已过期')
  expect(screen.getByRole('textbox', { name: /绑定凭据/ })).toHaveValue('herdrx://v1/keep-me')
})

it('shows known status instead of restarting a 64s wait after reload', async () => {
  sessionStorage.setItem('herdrx.enrollmentTask.tester', JSON.stringify({ id: 'task-1', startedAt: Date.now() - 70_000 }))
  vi.mocked(api.hosts).mockResolvedValue({ hosts: [] })
  vi.mocked(api.getTailcatEnrollment).mockResolvedValue({ id: 'task-1', status: 'connecting' })
  render(<HostsPage/>)
  expect(await screen.findByText(/配对仍在进行/)).toBeInTheDocument()
  expect(vi.mocked(api.getTailcatEnrollment).mock.calls.length).toBeLessThan(5)
  expect(screen.getByRole('button', { name: '关闭' })).toBeEnabled()
})

it('shows a success status after saving an SSH host', async () => {
  vi.mocked(api.hosts).mockResolvedValue({ hosts: [] })
  vi.mocked(api.sshKeys).mockResolvedValue({ keys: [{ id: 'k-1', name: 'dev', public_key: 'ssh-ed25519 AAAA', fingerprint: 'SHA256:abc', algorithm: 'ssh-ed25519', encrypted: false, revision: 1, host_count: 0, created_at: '', updated_at: '' }] })
  vi.mocked(api.createHost).mockResolvedValue({ host: { id: 'h-new', name: '办公机', transport: 'ssh', hostname: 'h', username: 'u', port: 22, created_at: '', updated_at: '' } })
  render(<HostsPage/>)
  fireEvent.click(await screen.findByRole('button', { name: '添加第一台主机' }))
  fireEvent.keyDown(screen.getByRole('combobox', { name: '连接方式' }), { key: 'ArrowDown' })
  fireEvent.click(within(document.querySelector('[role="listbox"]') as HTMLElement).getByText('SSH', { exact: true }))
  fireEvent.change(screen.getByLabelText('名称'), { target: { value: '办公机' } })
  fireEvent.change(screen.getByLabelText('主机名或 IP'), { target: { value: 'h' } })
  fireEvent.change(screen.getByLabelText('SSH 用户'), { target: { value: 'u' } })
  fireEvent.keyDown(screen.getByRole('combobox', { name: '认证密钥' }), { key: 'ArrowDown' })
  fireEvent.click(within(document.querySelector('[role="listbox"]') as HTMLElement).getByText(/dev/))
  fireEvent.click(screen.getByRole('button', { name: '保存主机' }))
  expect(await screen.findByRole('status')).toHaveTextContent('已添加 办公机，打开主机确认指纹')
})

describe('site topbar', () => {
  it('opens a more menu with the account name and confirms sign out', async () => {
    const signOut = vi.fn()
    auth.signOut = signOut
    vi.mocked(api.hosts).mockResolvedValue({ hosts: [host] })
    render(<HostsPage/>)
    await screen.findByRole('heading', { name: 'Old host' })
    fireEvent.click(screen.getByRole('button', { name: '更多' }))
    expect(screen.getByRole('menu', { name: '更多' })).toBeInTheDocument()
    expect(screen.getByRole('menuitem', { name: /Tester/ })).toBeDisabled()
    expect(screen.getByRole('menuitem', { name: '密钥' })).toBeInTheDocument()
    fireEvent.click(screen.getByRole('menuitem', { name: '退出登录' }))
    const dialog = await screen.findByRole('alertdialog', { name: '退出登录' })
    expect(signOut).not.toHaveBeenCalled()
    fireEvent.click(within(dialog).getByRole('button', { name: '退出登录' }))
    await waitFor(() => expect(signOut).toHaveBeenCalled())
  })
})
