import { fireEvent, render, screen } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import { Tooltips } from './Tooltips'

describe('Tooltips', () => {
  it('does not show a tooltip on focusin unless the target is :focus-visible', () => {
    render(<><button type="button" data-tooltip="分屏工具">工具</button><Tooltips/></>)
    const button = screen.getByRole('button', { name: '工具' })
    fireEvent.focusIn(button)
    expect(screen.queryByRole('tooltip')).not.toBeInTheDocument()
  })
})
