import { fireEvent, render, screen } from '@testing-library/react'
import { afterEach, expect, it, vi } from 'vitest'
import { APIError, api } from '../lib/api'
import { AccountPage } from './AccountPage'

const user = { id: 'member', email: 'member@example.test', display_name: 'Member', role: 'user' as const, must_change_password: true }
const auth = { user, setAuthenticated: vi.fn(), signOut: vi.fn() }
vi.mock('../auth', () => ({ useAuth: () => auth }))
vi.mock('../lib/navigation', () => ({ navigate: vi.fn() }))
vi.mock('../lib/api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../lib/api')>()
  return { ...actual, api: { changePassword: vi.fn() } }
})
afterEach(() => { vi.resetAllMocks() })

function fill(current: string, next: string, confirmation: string) {
  fireEvent.change(screen.getByLabelText(/^(当前密码|临时密码)$/), { target: { value: current } })
  fireEvent.change(screen.getByLabelText('新密码'), { target: { value: next } })
  fireEvent.change(screen.getByLabelText('确认新密码'), { target: { value: confirmation } })
  fireEvent.submit(screen.getByRole('form', { name: '修改密码' }))
}

it('validates the form before submitting and explains the next step', async () => {
  render(<AccountPage/>)
  fill('old-password-value', 'short', 'short')
  expect(screen.getAllByRole('alert')[0]).toHaveTextContent('请至少输入 12 个字符')
  fill('old-password-value', 'old-password-value', 'old-password-value')
  expect(screen.getByRole('alert')).toHaveTextContent('不能与当前密码相同')
  fill('old-password-value', 'new-password-value', 'new-password-typo')
  expect(screen.getByRole('alert')).toHaveTextContent('两次输入的新密码不一致')
  expect(api.changePassword).not.toHaveBeenCalled()
})

it('shows server errors and reports that other devices were signed out', async () => {
  vi.mocked(api.changePassword).mockRejectedValueOnce(new APIError(400, 'invalid_current_password', '当前密码不正确，请重新输入'))
  render(<AccountPage/>)
  fill('wrong-password-value', 'new-password-value', 'new-password-value')
  expect(await screen.findByRole('alert')).toHaveTextContent('当前密码不正确')
  const changed = { ...user, must_change_password: false }
  vi.mocked(api.changePassword).mockResolvedValueOnce({ ok: true, user: changed, revoked_sessions: 2 })
  fill('old-password-value', 'new-password-value', 'new-password-value')
  expect(await screen.findByRole('status')).toHaveTextContent('其他设备已退出登录')
  expect(api.changePassword).toHaveBeenLastCalledWith({ current_password: 'old-password-value', new_password: 'new-password-value' })
  expect(auth.setAuthenticated).toHaveBeenCalledWith(changed)
})

it('keeps a forced change on this page until the user continues, and allows logout', async () => {
  render(<AccountPage forced/>)
  expect(screen.getByRole('heading', { name: '请先修改密码' })).toBeInTheDocument()
  expect(screen.queryByRole('button', { name: /返回主机/ })).not.toBeInTheDocument()
  fireEvent.click(screen.getByRole('button', { name: /退出登录/ }))
  expect(auth.signOut).toHaveBeenCalled()
  const changed = { ...user, must_change_password: false }
  vi.mocked(api.changePassword).mockResolvedValueOnce({ ok: true, user: changed, revoked_sessions: 0 })
  fill('temporary-password', 'new-password-value', 'new-password-value')
  fireEvent.click(await screen.findByRole('button', { name: '进入工作台' }))
  expect(auth.setAuthenticated).toHaveBeenCalledWith(changed)
})
