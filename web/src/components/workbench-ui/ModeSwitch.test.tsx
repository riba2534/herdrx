import { fireEvent, render, screen } from '@testing-library/react'
import { describe, expect, it, vi } from 'vitest'
import { ModeSwitch } from './ModeSwitch'

describe('ModeSwitch', () => {
  it('is a radio group naming both modes, with only the checked one in the tab order', () => {
    render(<ModeSwitch mode="ui" onChange={() => {}}/>)
    const group = screen.getByRole('radiogroup', { name: '工作台模式' })
    const ui = screen.getByRole('radio', { name: 'Web UI 模式：对话与结构化操作' })
    const shell = screen.getByRole('radio', { name: 'Web Shell 模式：完整终端' })
    expect(group).toContainElement(ui)
    expect(ui).toHaveAttribute('aria-checked', 'true')
    expect(ui).toHaveTextContent('UI')
    expect(shell).toHaveAttribute('aria-checked', 'false')
    expect(shell).toHaveTextContent('Shell')
    expect(ui).toHaveAttribute('tabindex', '0')
    expect(shell).toHaveAttribute('tabindex', '-1')
  })

  it('switches on click and with the arrow keys, but not when the checked mode is clicked again', () => {
    const onChange = vi.fn()
    const { rerender } = render(<ModeSwitch mode="ui" onChange={onChange}/>)
    fireEvent.click(screen.getByRole('radio', { name: /Web UI/ }))
    expect(onChange).not.toHaveBeenCalled()
    fireEvent.click(screen.getByRole('radio', { name: /Web Shell/ }))
    expect(onChange).toHaveBeenLastCalledWith('shell')
    rerender(<ModeSwitch mode="shell" onChange={onChange}/>)
    fireEvent.keyDown(screen.getByRole('radio', { name: /Web Shell/ }), { key: 'ArrowRight' })
    expect(onChange).toHaveBeenLastCalledWith('ui')
    expect(screen.getByRole('radio', { name: /Web UI/ })).toHaveFocus()
    fireEvent.keyDown(screen.getByRole('radio', { name: /Web UI/ }), { key: 'End' })
    expect(onChange).toHaveBeenCalledTimes(2)
  })

  it('jumps to the ends with Home and End', () => {
    const onChange = vi.fn()
    const { rerender } = render(<ModeSwitch mode="ui" onChange={onChange}/>)
    fireEvent.keyDown(screen.getByRole('radio', { name: /Web UI/ }), { key: 'End' })
    expect(onChange).toHaveBeenLastCalledWith('shell')
    expect(screen.getByRole('radio', { name: /Web Shell/ })).toHaveFocus()
    rerender(<ModeSwitch mode="shell" onChange={onChange}/>)
    fireEvent.keyDown(screen.getByRole('radio', { name: /Web Shell/ }), { key: 'Home' })
    expect(onChange).toHaveBeenLastCalledWith('ui')
    fireEvent.keyDown(screen.getByRole('radio', { name: /Web Shell/ }), { key: 'a' })
    expect(onChange).toHaveBeenCalledTimes(2)
  })
})
