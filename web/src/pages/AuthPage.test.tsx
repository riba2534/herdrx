import { fireEvent, render, screen } from '@testing-library/react'
import { afterEach, expect, it, vi } from 'vitest'
import { AuthPage } from './AuthPage'

const auth = { registration: 'closed', notice: '', setAuthenticated: vi.fn(), refresh: vi.fn() }
vi.mock('../auth', () => ({ useAuth: () => auth }))
afterEach(() => { auth.registration = 'closed'; vi.resetAllMocks() })

it('shows login without registration or bootstrap fields by default', () => {
  render(<AuthPage bootstrap={false}/>)
  expect(screen.getByRole('button', { name: '登录' })).toBeInTheDocument()
  expect(screen.queryByRole('button', { name: '有邀请码？创建账号' })).not.toBeInTheDocument()
  expect(screen.queryByLabelText('初始化令牌')).not.toBeInTheDocument()
  expect(screen.getByText('当前注册已关闭，如需账号请联系管理员。')).toBeInTheDocument()
  expect(screen.getByLabelText('密码')).not.toHaveAttribute('minlength')
  expect(screen.getByRole('button', { name: '显示密码' })).toHaveAttribute('aria-pressed', 'false')
})

it('leaves registration when the administrator closes it during a pending form', () => {
  auth.registration = 'invite'
  const { rerender } = render(<AuthPage bootstrap={false}/>)
  fireEvent.click(screen.getByRole('button', { name: '有邀请码？创建账号' }))
  expect(screen.getByLabelText('邀请码')).toBeInTheDocument()
  expect(screen.getByLabelText('密码')).toHaveAttribute('minlength', '12')
  auth.registration = 'closed'
  rerender(<AuthPage bootstrap={false}/>)
  expect(screen.queryByLabelText('邀请码')).not.toBeInTheDocument()
  expect(screen.getByRole('button', { name: '登录' })).toBeInTheDocument()
})

it('allows the first administrator setup while registration is closed', () => {
  render(<AuthPage bootstrap/>)
  expect(screen.getByLabelText('初始化令牌')).toBeRequired()
  expect(screen.getByRole('button', { name: '创建管理员' })).toBeInTheDocument()
  expect(screen.queryByRole('button', { name: '有邀请码？创建账号' })).not.toBeInTheDocument()
})
