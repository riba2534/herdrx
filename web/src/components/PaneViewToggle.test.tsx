import { fireEvent, render, screen, within } from '@testing-library/react'
import { useState } from 'react'
import { describe, expect, it, vi } from 'vitest'
import type { PaneViewMode } from '../lib/paneViewMode'
import { PaneViewToggle } from './PaneViewToggle'

function Harness({ initial = 'terminal' as PaneViewMode, onChange }: { initial?: PaneViewMode; onChange?: (mode: PaneViewMode) => void }) {
  const [mode, setMode] = useState<PaneViewMode>(initial)
  return <PaneViewToggle mode={mode} onChange={(next) => { setMode(next); onChange?.(next) }}/>
}

const toggle = () => screen.getByRole('switch')

describe('PaneViewToggle', () => {
  it('is a single switch with aria-checked, not a segmented pair of buttons', () => {
    render(<Harness/>)
    expect(screen.getAllByRole('switch')).toHaveLength(1)
    expect(within(toggle()).queryAllByRole('button')).toHaveLength(0)
    expect(toggle()).toHaveAttribute('aria-label', '对话视图')
    expect(toggle()).toHaveAttribute('aria-checked', 'false')
    expect(toggle()).toHaveAttribute('data-mode', 'terminal')
    // 视觉是 Terminal [开关] Chat：两端文字始终在，当前模式由高亮和滑块位置表示。
    expect(toggle()).toHaveTextContent('Terminal')
    expect(toggle()).toHaveTextContent('Chat')
    expect(toggle().querySelector('.pane-view-toggle-track')).not.toBeNull()
    expect(toggle().querySelector('.pane-view-toggle-thumb')).not.toBeNull()
  })

  it('toggles with a mouse click in both directions', () => {
    const onChange = vi.fn()
    render(<Harness onChange={onChange}/>)
    fireEvent.click(toggle())
    expect(onChange).toHaveBeenLastCalledWith('chat')
    expect(toggle()).toHaveAttribute('aria-checked', 'true')
    expect(toggle()).toHaveAttribute('data-mode', 'chat')
    fireEvent.click(toggle())
    expect(onChange).toHaveBeenLastCalledWith('terminal')
    expect(toggle()).toHaveAttribute('aria-checked', 'false')
    expect(onChange).toHaveBeenCalledTimes(2)
  })

  it('toggles with Space and with Enter, once per key press', () => {
    const onChange = vi.fn()
    render(<Harness onChange={onChange}/>)
    fireEvent.keyDown(toggle(), { key: ' ' })
    expect(onChange).toHaveBeenLastCalledWith('chat')
    expect(onChange).toHaveBeenCalledTimes(1)
    // keydown 已 preventDefault，随后的 keyup 不会再触发一次原生 click。
    fireEvent.keyUp(toggle(), { key: ' ' })
    expect(onChange).toHaveBeenCalledTimes(1)
    fireEvent.keyDown(toggle(), { key: 'Enter' })
    expect(onChange).toHaveBeenLastCalledWith('terminal')
    expect(onChange).toHaveBeenCalledTimes(2)
  })

  it('ignores key repeat and unrelated keys', () => {
    const onChange = vi.fn()
    render(<Harness onChange={onChange}/>)
    fireEvent.keyDown(toggle(), { key: ' ', repeat: true })
    fireEvent.keyDown(toggle(), { key: 'a' })
    fireEvent.keyDown(toggle(), { key: 'Tab' })
    expect(onChange).not.toHaveBeenCalled()
  })

  it('starts from the chat mode when the pane is already in chat', () => {
    render(<Harness initial="chat"/>)
    expect(toggle()).toHaveAttribute('aria-checked', 'true')
    expect(toggle()).toHaveAttribute('data-mode', 'chat')
    fireEvent.click(toggle())
    expect(toggle()).toHaveAttribute('aria-checked', 'false')
  })
})
