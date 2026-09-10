import { fireEvent, render, screen } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import { Button, Field, StatusDot } from './ui'

describe('UI primitives', () => {
  it('exposes status text to assistive technology', () => {
    render(<StatusDot status="blocked" />)
    expect(screen.getByRole('img', { name: 'blocked' })).toBeInTheDocument()
  })

  it('disables a pending action', () => {
    render(<Button pending>Save</Button>)
    expect(screen.getByRole('button', { name: 'Save' })).toBeDisabled()
  })

  it('associates field errors with visible content', () => {
    render(<Field name="email" label="邮箱" error="邮箱无效" />)
    expect(screen.getByLabelText('邮箱')).toHaveAttribute('aria-invalid', 'true')
    expect(screen.getByRole('alert')).toHaveTextContent('邮箱无效')
  })

  it('toggles password visibility without leaving the field', () => {
    render(<Field name="password" label="密码" type="password" />)
    const input = screen.getByLabelText('密码')
    const toggle = screen.getByRole('button', { name: '显示密码' })
    expect(input).toHaveAttribute('type', 'password')
    expect(toggle).toHaveAttribute('aria-pressed', 'false')
    fireEvent.click(toggle)
    expect(input).toHaveAttribute('type', 'text')
    expect(screen.getByRole('button', { name: '隐藏密码' })).toHaveAttribute('aria-pressed', 'true')
  })
})
