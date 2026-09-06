import { fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { api } from '../lib/api'
import { HostsPage } from './HostsPage'

const host = { id: 'hst_test', name: 'Old host', transport: 'ssh' as const, hostname: 'example.test', username: 'me', port: 2222, created_at: '', updated_at: '' }
const auth = { user: { role: 'user', display_name: 'Tester' }, signOut() {} }
vi.mock('../auth', () => ({ useAuth: () => auth }))
vi.mock('../lib/api', () => ({ api: { hosts: vi.fn(), renameHost: vi.fn(), refreshEndpoint: vi.fn(), hostFolders: vi.fn(), sshKeys: vi.fn(), cliRelease: vi.fn() } }))
beforeEach(() => { vi.mocked(api.hostFolders).mockResolvedValue({ folders: [] }); vi.mocked(api.sshKeys).mockResolvedValue({ keys: [] }); vi.mocked(api.cliRelease).mockResolvedValue({ status: 'unpublished' }) })
afterEach(() => { vi.resetAllMocks(); auth.user.role = 'user' })

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
