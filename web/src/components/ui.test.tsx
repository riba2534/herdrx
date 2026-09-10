import { render, screen } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import { Button, Field, StatusDot } from './ui'

describe('UI primitives', () => {
  it('exposes status text to assistive technology', () => {
    render(<StatusDot status="blocked" />)
    expect(screen.getByRole('img', { name: '等待确认' })).toBeInTheDocument()
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
})
