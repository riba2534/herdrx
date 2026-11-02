import { act, fireEvent, render, screen } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { ConsoleLive } from '../../lib/console/useConsoleLive'
import { CONSOLE_MODE_LABELS, ConsoleStatusBar, formatConsoleElapsed, formatConsoleTokens, STATUS_FADE_MS, STATUS_LINGER_MS } from './ConsoleStatusBar'
import { WORKING_STATUS_FADE_MS } from './WorkingStatusText'

const spinner = (verb: string, seconds = 15) => ({ verb, seconds, tokens: 1200, thinking: true, raw: `✻ ${verb}… (${seconds}s · ↓ 1.2k tokens · thinking)` })
const live = (patch: Partial<ConsoleLive> = {}): ConsoleLive => ({ spinner: spinner('Wibbling'), mode: 'bypass', preview: ['● Update(calc.py)', '  ⎿  Added 1 line'], ...patch })
const noop = () => Promise.resolve()

beforeEach(() => { localStorage.clear() })
afterEach(() => {
  vi.useRealTimers()
  vi.restoreAllMocks()
})

describe('ConsoleStatusBar', () => {
  it('shows the spinner verb, the elapsed time, tokens, the mode and the interrupt button while working', () => {
    render(<ConsoleStatusBar live={live()} working onInterrupt={noop} onCycleMode={noop}/>)
    expect(screen.getByText('Wibbling…')).toBeInTheDocument()
    expect(screen.getByText('15 秒')).toBeInTheDocument()
    expect(screen.getByText('1.2k tokens')).toBeInTheDocument()
    expect(screen.getByText('思考中')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: '当前模式：免授权。点击切换到下一个模式' })).toHaveAttribute('data-tooltip', '切换到下一个模式（相当于按一次 Shift+Tab）')
    expect(screen.getByRole('button', { name: '中断' })).toBeEnabled()
  })

  it('shows a visible spinner as running and keeps the last one through a frame without it', () => {
    const view = render(<ConsoleStatusBar live={live()} working={false} onInterrupt={noop} onCycleMode={noop}/>)
    expect(screen.getByRole('button', { name: '中断' })).toBeInTheDocument()
    view.rerender(<ConsoleStatusBar live={live({ spinner: null })} working onInterrupt={noop} onCycleMode={noop}/>)
    expect(screen.getByText('Wibbling…')).toBeInTheDocument()
    expect(screen.getByText('15 秒')).toBeInTheDocument()
  })

  it('locks the mode pill and interrupt while a dialog is up, and interrupts on Herdr working or a visible spinner', () => {
    const view = render(<ConsoleStatusBar live={live()} working locked="终端正弹着对话框" onInterrupt={noop} onCycleMode={noop}/>)
    const pill = screen.getByRole('button', { name: /当前模式/ })
    expect(pill).toBeDisabled()
    expect(pill).toHaveAttribute('data-tooltip', '终端正弹着对话框')
    expect(screen.getByRole('button', { name: '中断' })).toBeDisabled()
    // Herdr 状态滞后、只读到 spinner：照样可以中断。
    view.rerender(<ConsoleStatusBar live={live()} working={false} onInterrupt={noop} onCycleMode={noop}/>)
    expect(screen.getByRole('button', { name: '中断' })).toBeEnabled()
    expect(screen.getByRole('button', { name: /当前模式/ })).toBeEnabled()
    view.rerender(<ConsoleStatusBar live={live({ spinner: null })} working onInterrupt={noop} onCycleMode={noop}/>)
    expect(screen.getByRole('button', { name: '中断' })).toBeEnabled()
  })

  it('interrupts once per click and explains a failure without retrying', async () => {
    let finish: () => void = () => {}
    const onInterrupt = vi.fn(() => new Promise<void>((resolve) => { finish = resolve }))
    render(<ConsoleStatusBar live={live()} working onInterrupt={onInterrupt} onCycleMode={noop}/>)
    const button = screen.getByRole('button', { name: '中断' })
    fireEvent.click(button)
    fireEvent.click(button)
    expect(onInterrupt).toHaveBeenCalledTimes(1)
    expect(button).toBeDisabled()
    await act(async () => finish())
    expect(button).toBeEnabled()
    onInterrupt.mockImplementationOnce(() => Promise.reject(new Error('主机连接尚未就绪')))
    await act(async () => { fireEvent.click(button) })
    expect(screen.getByRole('alert')).toHaveTextContent('主机连接尚未就绪')
    expect(onInterrupt).toHaveBeenCalledTimes(2)
  })

  it('cycles the mode through the caller and labels every mode', async () => {
    const onCycleMode = vi.fn(() => Promise.reject(new Error('')))
    const view = render(<ConsoleStatusBar live={live({ mode: 'plan' })} working={false} onInterrupt={noop} onCycleMode={onCycleMode}/>)
    const pill = screen.getByRole('button', { name: /当前模式：计划/ })
    expect(pill).toHaveAttribute('data-mode', 'plan')
    await act(async () => { fireEvent.click(pill) })
    expect(onCycleMode).toHaveBeenCalledTimes(1)
    expect(screen.getByRole('alert')).toHaveTextContent('切换模式没有完成，请到 Shell 模式按 Shift+Tab')
    for (const [mode, label] of Object.entries(CONSOLE_MODE_LABELS)) {
      view.rerender(<ConsoleStatusBar live={live({ mode: mode as ConsoleLive['mode'] })} working={false} onInterrupt={noop} onCycleMode={noop}/>)
      expect(screen.getByRole('button', { name: new RegExp(`当前模式：${label}`) })).toBeInTheDocument()
    }
    view.rerender(<ConsoleStatusBar live={live({ mode: null })} working={false} onInterrupt={noop} onCycleMode={noop}/>)
    expect(screen.queryByRole('button', { name: /当前模式/ })).toBeNull()
  })

  it('holds the finished state for a second, fades it out and then hides it', () => {
    vi.useFakeTimers()
    const view = render(<ConsoleStatusBar live={live()} working onInterrupt={noop} onCycleMode={noop}/>)
    const root = view.container.querySelector('.console-status')!
    view.rerender(<ConsoleStatusBar live={live({ spinner: null })} working={false} onInterrupt={noop} onCycleMode={noop}/>)
    expect(root).toHaveAttribute('data-state', 'linger')
    expect(screen.getByText('本轮已结束')).toBeInTheDocument()
    expect(screen.getByText('15 秒')).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: '中断' })).toBeNull()
    act(() => { vi.advanceTimersByTime(STATUS_LINGER_MS - 1) })
    expect(root).toHaveAttribute('data-state', 'linger')
    act(() => { vi.advanceTimersByTime(1) })
    expect(root).toHaveAttribute('data-state', 'fading')
    act(() => { vi.advanceTimersByTime(STATUS_FADE_MS) })
    expect(root).toHaveAttribute('data-state', 'idle')
    expect(screen.queryByText('本轮已结束')).toBeNull()
    // 新一轮开始时不带上一轮的动词。
    view.rerender(<ConsoleStatusBar live={live({ spinner: null })} working onInterrupt={noop} onCycleMode={noop}/>)
    expect(screen.getByText('正在运行…')).toBeInTheDocument()
  })

  it('keeps each verb for at least a second and fades between them', () => {
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout', 'setInterval', 'clearInterval', 'Date', 'performance'] })
    const view = render(<ConsoleStatusBar live={live()} working onInterrupt={noop} onCycleMode={noop}/>)
    view.rerender(<ConsoleStatusBar live={live({ spinner: spinner('Herding', 16) })} working onInterrupt={noop} onCycleMode={noop}/>)
    // 计时立刻更新，动词等满 1 秒。
    expect(screen.getByText('16 秒')).toBeInTheDocument()
    act(() => { vi.advanceTimersByTime(999) })
    expect(screen.getByText('Wibbling…')).not.toHaveAttribute('data-fading')
    act(() => { vi.advanceTimersByTime(1) })
    expect(screen.getByText('Wibbling…')).toHaveAttribute('data-fading', 'true')
    act(() => { vi.advanceTimersByTime(WORKING_STATUS_FADE_MS) })
    expect(screen.getByText('Herding…')).not.toHaveAttribute('data-fading')
  })

  it('shows the live screen only while working', () => {
    vi.useFakeTimers()
    const view = render(<ConsoleStatusBar live={live({ spinner: null })} working={false} onInterrupt={noop} onCycleMode={noop}/>)
    expect(screen.queryByRole('button', { name: /实时画面/ })).toBeNull()
    expect(view.container.querySelector('.console-status')).toHaveAttribute('data-state', 'idle')
    view.rerender(<ConsoleStatusBar live={live()} working onInterrupt={noop} onCycleMode={noop}/>)
    expect(screen.getByRole('button', { name: /实时画面/ })).toBeInTheDocument()
    // 一结束就收起，不等淡出。
    view.rerender(<ConsoleStatusBar live={live({ spinner: null })} working={false} onInterrupt={noop} onCycleMode={noop}/>)
    expect(screen.queryByRole('button', { name: /实时画面/ })).toBeNull()
    act(() => { vi.advanceTimersByTime(STATUS_LINGER_MS) })
    act(() => { vi.advanceTimersByTime(STATUS_FADE_MS) })
    expect(view.container.querySelector('.console-status')).toHaveAttribute('data-state', 'idle')
  })

  it('folds the live screen into one line and expands it on this device only', () => {
    const view = render(<ConsoleStatusBar live={live()} working onInterrupt={noop} onCycleMode={noop}/>)
    const toggle = screen.getByRole('button', { name: /实时画面/ })
    expect(toggle).toHaveAttribute('aria-expanded', 'false')
    expect(toggle).toHaveTextContent('⎿ Added 1 line')
    fireEvent.click(toggle)
    expect(toggle).toHaveAttribute('aria-expanded', 'true')
    expect(screen.getByLabelText('终端实时画面').textContent).toBe('● Update(calc.py)\n  ⎿  Added 1 line')
    expect(localStorage.getItem('herdrx.console.live-preview.v1')).toBe('true')
    view.rerender(<ConsoleStatusBar live={live({ preview: [], error: '这个 Pane 已不存在' })} working onInterrupt={noop} onCycleMode={noop}/>)
    expect(screen.getByLabelText('终端实时画面')).toHaveTextContent('读不到实时画面：这个 Pane 已不存在')
    view.rerender(<ConsoleStatusBar live={live({ preview: [] })} working onInterrupt={noop} onCycleMode={noop}/>)
    expect(screen.queryByRole('button', { name: /实时画面/ })).toBeNull()
  })
})

describe('status formatting', () => {
  it('formats elapsed time and token counts compactly', () => {
    expect(formatConsoleElapsed(15)).toBe('15 秒')
    expect(formatConsoleElapsed(65)).toBe('1 分 05 秒')
    expect(formatConsoleElapsed(3723)).toBe('1 小时 02 分')
    expect(formatConsoleTokens(300)).toBe('300')
    expect(formatConsoleTokens(1200)).toBe('1.2k')
    expect(formatConsoleTokens(1000)).toBe('1k')
    expect(formatConsoleTokens(123_456)).toBe('123k')
    expect(formatConsoleTokens(2_500_000)).toBe('2.5M')
  })
})
