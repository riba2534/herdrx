import { fireEvent, render, screen } from '@testing-library/react'
import { describe, expect, it, vi } from 'vitest'
import { ContextMenu } from './ContextMenu'

describe('ContextMenu', () => {
  it('supports keyboard navigation, activation and closing', () => {
    const first = vi.fn()
    const second = vi.fn()
    const close = vi.fn()
    render(<ContextMenu x={20} y={30} label="Pane 菜单" onClose={close} items={[
      { id: 'first', label: '向右分屏', onSelect: first },
      { id: 'second', label: '关闭 Pane', danger: true, onSelect: second },
    ]}/>)

    const menu = screen.getByRole('menu', { name: 'Pane 菜单' })
    const items = screen.getAllByRole('menuitem')
    expect(items[0]).toHaveFocus()
    fireEvent.keyDown(menu, { key: 'ArrowDown' })
    expect(items[1]).toHaveFocus()
    fireEvent.click(items[1])
    expect(second).toHaveBeenCalledOnce()
    expect(close).toHaveBeenCalledOnce()
  })

  it('closes with Escape', () => {
    const close = vi.fn()
    render(<ContextMenu x={0} y={0} label="菜单" onClose={close} items={[{ id: 'rename', label: '重命名', onSelect: vi.fn() }]}/>)
    fireEvent.keyDown(screen.getByRole('menu'), { key: 'Escape' })
    expect(close).toHaveBeenCalledOnce()
  })
})
