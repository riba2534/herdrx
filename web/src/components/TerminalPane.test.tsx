import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { api } from '../lib/api'
import { WorkbenchClient } from '../lib/workbench'
import type { Pane } from '../types'
import { TerminalPane } from './TerminalPane'

const terminalHarness = vi.hoisted(() => ({ linkHandler: null as null | ((event: MouseEvent, uri: string) => void), keyHandler: null as null | ((event: KeyboardEvent) => boolean), scrolls: [] as number[], fontWrites: [] as number[], instances: 0, resets: 0, writes: [] as Array<string | Uint8Array>, deferWrites: false, writeCallbacks: [] as Array<() => void>, last: null as { options: { disableStdin: boolean } } | null }))

vi.mock('@xterm/xterm', () => {
  return {
    Terminal: class MockTerminal {
      options = { fontSize: 14, theme: {}, minimumContrastRatio: 1, disableStdin: false }
      constructor() {
        terminalHarness.instances++
        terminalHarness.last = this
        let size = 14
        Object.defineProperty(this.options, 'fontSize', { get: () => size, set: (value: number) => { size = value; terminalHarness.fontWrites.push(value) } })
      }
      unicode = { activeVersion: '11' }
      cols = 80
      rows = 24
      buffer = { active: { viewportY: 0, baseY: 200 } }
      loadAddon() {}
      open(el: HTMLElement) {
        const div = document.createElement('div')
        div.className = 'xterm'
        el.appendChild(div)
      }
      fit() {}
      resize(cols: number, rows: number) { this.cols = cols; this.rows = rows }
      reset() { terminalHarness.resets++ }
      write(data: string | Uint8Array, callback?: () => void) {
        terminalHarness.writes.push(data)
        if (!callback) return
        if (terminalHarness.deferWrites) terminalHarness.writeCallbacks.push(callback)
        else callback()
      }
      scrollToBottom() { this.buffer.active.viewportY = this.buffer.active.baseY }
      scrollLines(lines: number) { terminalHarness.scrolls.push(lines); this.buffer.active.viewportY = Math.max(0, Math.min(this.buffer.active.baseY, this.buffer.active.viewportY + lines)) }
      refresh() {}
      dispose() {}
      focus() {}
      attachCustomKeyEventHandler(handler: (event: KeyboardEvent) => boolean) { terminalHarness.keyHandler = handler }
      onData() {
        return { dispose: () => {} }
      }
    },
  }
})

vi.mock('../lib/terminalFit', async (importOriginal) => ({
  ...await importOriginal<typeof import('../lib/terminalFit')>(),
  createFontMeasure: () => ({ measure: (size: number) => ({ width: size * 0.6, height: size }), dispose() {} }),
}))

vi.mock('@xterm/addon-fit', () => ({
  FitAddon: class {
    fit() {}
    proposeDimensions() {
      return { cols: 80, rows: 24 }
    }
  },
}))
vi.mock('@xterm/addon-search', () => ({ SearchAddon: class {} }))
vi.mock('@xterm/addon-unicode11', () => ({ Unicode11Addon: class {} }))
vi.mock('@xterm/addon-web-links', () => ({ WebLinksAddon: class { constructor(handler: (event: MouseEvent, uri: string) => void) { terminalHarness.linkHandler = handler } } }))

const mockPane: Pane = {
  pane_id: 'p1',
  workspace_id: 'w1',
  tab_id: 't1',
  terminal_id: 'term1',
  agent_status: 'idle',
  cwd: '/home/user',
  focused: true,
  revision: 1,
  right_click_passthrough: false,
}

describe('TerminalPane paste interception', () => {
  afterEach(() => {
    vi.restoreAllMocks()
    terminalHarness.resets = 0
    terminalHarness.writes = []
    terminalHarness.deferWrites = false
    terminalHarness.writeCallbacks = []
  })
  it('opens links only from the custom confirmation click without restarting the terminal', async () => {
    const client = new WorkbenchClient('hst_test')
    vi.spyOn(client, 'openTerminal').mockResolvedValue(7)
    const open = vi.spyOn(window, 'open').mockReturnValue(null)
    const focus = vi.fn()
    const { unmount } = render(<TerminalPane client={client} pane={mockPane} connectionEpoch={1} active onFocus={focus} theme={{}} enhancedContrast={false}/>)
    const instances = terminalHarness.instances
    act(() => terminalHarness.linkHandler!(new MouseEvent('click'), 'https://example.test/docs'))
    expect(open).not.toHaveBeenCalled()
    fireEvent.click(within(screen.getByRole('alertdialog')).getByRole('button', { name: '取消' }))
    expect(open).not.toHaveBeenCalled()
    act(() => terminalHarness.linkHandler!(new MouseEvent('click'), 'https://example.test/docs'))
    const accept = within(screen.getByRole('alertdialog')).getByRole('button', { name: '打开链接' })
    fireEvent.pointerDown(accept)
    expect(focus).not.toHaveBeenCalled()
    fireEvent.click(accept)
    expect(open).toHaveBeenCalledExactlyOnceWith('https://example.test/docs', '_blank', 'noopener,noreferrer')
    expect(terminalHarness.instances).toBe(instances)
    act(() => terminalHarness.linkHandler!(new MouseEvent('click'), 'https://example.test/unmounted'))
    unmount()
    await act(async () => {})
    expect(open).toHaveBeenCalledTimes(1)
  })
  it('resets only the first frame in each stream and preserves queued full and delta frames before parsing finishes', async () => {
    terminalHarness.deferWrites = true
    const client = new WorkbenchClient('hst_test')
    vi.spyOn(client, 'openTerminal').mockResolvedValueOnce(7).mockResolvedValue(8)
    const frames = vi.spyOn(client, 'onTerminal').mockReturnValue(() => true)
    const acknowledge = vi.spyOn(client, 'acknowledge').mockImplementation(() => {})
    const props = { client, pane: mockPane, active: true, onFocus: () => {}, theme: {}, enhancedContrast: false }
    const { rerender } = render(<TerminalPane {...props} connectionEpoch={1}/>)
    await waitFor(() => expect(frames).toHaveBeenCalledTimes(1))
    const full = new TextEncoder().encode('\x1b[H首帧'), repaint = new TextEncoder().encode('\x1b[H重绘'), delta = new TextEncoder().encode('\x1b[2;1H增量')
    act(() => {
      frames.mock.calls[0][1]({ streamID: 7, seq: 1n, full: true, cols: 80, rows: 24, ansi: full })
      frames.mock.calls[0][1]({ streamID: 7, seq: 2n, full: true, cols: 80, rows: 24, ansi: repaint })
      frames.mock.calls[0][1]({ streamID: 7, seq: 3n, full: false, cols: 80, rows: 24, ansi: delta })
    })
    // xterm has accepted all three writes, but none of their parse callbacks
    // has fired. Consuming the reset flag in the callback would reset all three.
    expect(terminalHarness.writes).toHaveLength(3)
    expect(terminalHarness.writes[0]).toEqual(new Uint8Array([0x1b, 0x63, ...full]))
    expect(terminalHarness.writes[1]).toBe(repaint)
    expect(terminalHarness.writes[2]).toBe(delta)
    expect(terminalHarness.resets).toBe(0)
    expect(acknowledge).not.toHaveBeenCalled()
    act(() => { for (const callback of terminalHarness.writeCallbacks.splice(0)) callback() })
    expect(acknowledge.mock.calls).toEqual([[7, 1n], [7, 2n], [7, 3n]])

    rerender(<TerminalPane {...props} connectionEpoch={2}/>)
    await waitFor(() => expect(frames).toHaveBeenCalledTimes(2))
    act(() => frames.mock.calls[1][1]({ streamID: 8, seq: 1n, full: true, cols: 80, rows: 24, ansi: full }))
    expect(terminalHarness.writes[3]).toEqual(new Uint8Array([0x1b, 0x63, ...full]))
    expect(terminalHarness.resets).toBe(0)
  })
  it('shows a closed terminal, retries only when requested and never replays input from the closed stream', async () => {
    const client = new WorkbenchClient('hst_test')
    const open = vi.spyOn(client, 'openTerminal').mockResolvedValueOnce(7).mockResolvedValue(8)
    const frames = vi.spyOn(client, 'onTerminal').mockReturnValue(() => {})
    const input = vi.spyOn(client, 'sendInput').mockImplementation(() => {})
    const call = vi.spyOn(client, 'call')
    let send: ((data: string) => void) | null = null
    render(<TerminalPane client={client} pane={mockPane} connectionEpoch={1} active onFocus={() => {}} onControlReady={(handler) => { send = handler }} theme={{}} enhancedContrast={false}/>)
    await waitFor(() => expect(frames).toHaveBeenCalledTimes(1))
    act(() => send!('sent once'))
    act(() => frames.mock.calls[0][2]!('herdr: command not found'))
    expect(screen.getByText('终端连接已关闭：herdr: command not found')).toBeVisible()
    expect(screen.queryByRole('button', { name: '聚焦终端输入' })).not.toBeInTheDocument()
    act(() => send!('discard while closed'))
    expect(open).toHaveBeenCalledTimes(1)
    fireEvent.click(screen.getByRole('button', { name: '重连终端' }))
    await waitFor(() => expect(frames).toHaveBeenCalledTimes(2))
    expect(input.mock.calls).toEqual([[7, 'sent once']])
    expect(call).not.toHaveBeenCalled()
    expect(screen.getByRole('button', { name: '聚焦终端输入' })).toBeVisible()
    act(() => send!('new explicit input'))
    expect(input).toHaveBeenLastCalledWith(8, 'new explicit input')
  })
  it('keeps xterm stdin disabled in composer mode so local typing is not sent as keystrokes', async () => {
    const client = new WorkbenchClient('hst_test')
    vi.spyOn(client, 'openTerminal').mockResolvedValue(7)
    const frames = vi.spyOn(client, 'onTerminal').mockReturnValue(() => {})
    const input = vi.spyOn(client, 'sendInput')
    render(<TerminalPane client={client} pane={mockPane} connectionEpoch={1} active onFocus={() => {}} theme={{}} enhancedContrast={false} directInput={false}/>)
    await waitFor(() => expect(frames).toHaveBeenCalled())
    expect(terminalHarness.last?.options.disableStdin).toBe(true)
    expect(input).not.toHaveBeenCalled()
  })
  it('replaces history and returning live content in-band while skipping live updates during history', async () => {
    terminalHarness.deferWrites = true
    const client = new WorkbenchClient('hst_test')
    vi.spyOn(client, 'openTerminal').mockResolvedValueOnce(7).mockResolvedValue(8)
    const frames = vi.spyOn(client, 'onTerminal').mockReturnValue(() => true)
    const acknowledge = vi.spyOn(client, 'acknowledge').mockImplementation(() => {})
    const read = vi.spyOn(client, 'call').mockResolvedValue({ read: { text: 'first\nsecond\r\nthird' } })
    render(<TerminalPane client={client} pane={mockPane} connectionEpoch={1} active onFocus={() => {}} theme={{}} enhancedContrast={false}/>)
    await waitFor(() => expect(frames).toHaveBeenCalledTimes(1))
    const live = new TextEncoder().encode('\x1b[Hlive')
    act(() => frames.mock.calls[0][1]({ streamID: 7, seq: 1n, full: true, cols: 80, rows: 24, ansi: live }))
    act(() => terminalHarness.writeCallbacks.shift()!())

    fireEvent.click(screen.getByRole('button', { name: '查看终端历史' }))
    await waitFor(() => expect(terminalHarness.writes).toHaveLength(2))
    expect(read).toHaveBeenCalledExactlyOnceWith('pane.read', { pane_id: 'p1', source: 'recent', format: 'ansi', lines: 10000 })
    expect(terminalHarness.writes[1]).toBe('\x1bc\x1b[?25lfirst\r\nsecond\r\nthird')
    act(() => frames.mock.calls[0][1]({ streamID: 7, seq: 2n, full: true, cols: 80, rows: 24, ansi: live }))
    expect(terminalHarness.writes).toHaveLength(2)
    expect(acknowledge).toHaveBeenLastCalledWith(7, 2n)
    act(() => terminalHarness.writeCallbacks.shift()!())

    fireEvent.click(screen.getByRole('button', { name: '返回实时' }))
    await waitFor(() => expect(frames).toHaveBeenCalledTimes(2))
    act(() => {
      frames.mock.calls[1][1]({ streamID: 8, seq: 1n, full: true, cols: 80, rows: 24, ansi: live })
      frames.mock.calls[1][1]({ streamID: 8, seq: 2n, full: false, cols: 80, rows: 24, ansi: live })
    })
    expect(terminalHarness.writes[2]).toEqual(new Uint8Array([0x1b, 0x63, ...live]))
    expect(terminalHarness.writes[3]).toBe(live)
    expect(terminalHarness.resets).toBe(0)
    act(() => { for (const callback of terminalHarness.writeCallbacks.splice(0)) callback() })
    expect(acknowledge.mock.calls).toEqual([[7, 1n], [7, 2n], [8, 1n], [8, 2n]])
  })
  it('routes fullscreen application wheel through Herdr without freezing a snapshot or guessing input bytes', async () => {
    const client = new WorkbenchClient('hst_test')
    vi.spyOn(client, 'openTerminal').mockResolvedValue(7)
    const frames = vi.spyOn(client, 'onTerminal').mockReturnValue(() => true)
    const call = vi.spyOn(client, 'call').mockResolvedValue({ ok: true })
    const input = vi.spyOn(client, 'sendInput')
    render(<TerminalPane client={client} pane={{ ...mockPane, scroll: { max_offset_from_bottom: 0, offset_from_bottom: 0, viewport_rows: 40 } }} connectionEpoch={1} active onFocus={() => {}} theme={{}} enhancedContrast={false}/>)
    await waitFor(() => expect(frames).toHaveBeenCalled())
    fireEvent.wheel(document.querySelector('.terminal-viewport')!, { deltaY: -48, cancelable: true })
    await waitFor(() => expect(call).toHaveBeenCalledWith('terminal.scroll', { stream_id: 7, lines: -3, column: 0, row: 0 }))
    expect(call).toHaveBeenCalledTimes(1)
    expect(input).not.toHaveBeenCalled()
    expect(screen.queryByRole('button', { name: '返回实时' })).not.toBeInTheDocument()
  })
  it('pipelines continuous wheel frames with bounded requests and drops stale queued motion', async () => {
    let flush: FrameRequestCallback = () => {}
    vi.spyOn(window, 'requestAnimationFrame').mockImplementation((callback) => { flush = callback; return 1 })
    let now = 0
    vi.spyOn(performance, 'now').mockImplementation(() => now)
    const replies: Array<() => void> = []
    const client = new WorkbenchClient('hst_test')
    vi.spyOn(client, 'openTerminal').mockResolvedValue(7)
    const frames = vi.spyOn(client, 'onTerminal').mockReturnValue(() => true)
    const call = vi.spyOn(client, 'call').mockImplementation(() => new Promise((resolve) => replies.push(() => resolve({ ok: true }))))
    render(<TerminalPane client={client} pane={{ ...mockPane, scroll: { max_offset_from_bottom: 0, offset_from_bottom: 0, viewport_rows: 40 } }} connectionEpoch={1} active onFocus={() => {}} theme={{}} enhancedContrast={false}/>)
    await waitFor(() => expect(frames).toHaveBeenCalled())
    const wheel = (delta: number) => {
      now += 16
      fireEvent.wheel(document.querySelector('.terminal-viewport')!, { deltaY: delta, cancelable: true })
      act(() => flush(now))
    }
    for (let i = 0; i < 10; i++) wheel(-48)
    expect(call).toHaveBeenCalledTimes(8) // Eight gestures proceeded without a single reply.
    await act(async () => replies[0]())
    expect(call).toHaveBeenCalledTimes(9)
    expect(call.mock.calls[8][1]).toMatchObject({ lines: -6 })
    wheel(-48)
    wheel(48) // Reverse direction; discard the older unsent upward movement.
    await act(async () => replies[1]())
    expect(call.mock.calls[9][1]).toMatchObject({ lines: 3 })
    wheel(-48)
    now += 121
    await act(async () => replies[2]())
    expect(call).toHaveBeenCalledTimes(10) // No delayed tail after the user stopped.
  })
  it('does not transfer queued motion or late replies to a reconnected terminal', async () => {
    let flush: FrameRequestCallback = () => {}
    vi.spyOn(window, 'requestAnimationFrame').mockImplementation((callback) => { flush = callback; return 1 })
    const replies: Array<() => void> = []
    const client = new WorkbenchClient('hst_test')
    vi.spyOn(client, 'openTerminal').mockResolvedValueOnce(7).mockResolvedValue(8)
    const frames = vi.spyOn(client, 'onTerminal').mockReturnValue(() => true)
    const call = vi.spyOn(client, 'call').mockImplementation(() => new Promise((resolve) => replies.push(() => resolve({ ok: true }))))
    const props = { client, pane: { ...mockPane, scroll: { max_offset_from_bottom: 0, offset_from_bottom: 0, viewport_rows: 40 } }, active: true, onFocus: () => {}, theme: {}, enhancedContrast: false }
    const { rerender } = render(<TerminalPane {...props} connectionEpoch={1}/>)
    await waitFor(() => expect(frames).toHaveBeenCalledTimes(1))
    const wheel = () => {
      fireEvent.wheel(document.querySelector('.terminal-viewport')!, { deltaY: -48, cancelable: true })
      act(() => flush(performance.now()))
    }
    for (let i = 0; i < 9; i++) wheel()
    expect(call).toHaveBeenCalledTimes(8)
    rerender(<TerminalPane {...props} connectionEpoch={2}/>)
    await waitFor(() => expect(frames).toHaveBeenCalledTimes(2))
    wheel()
    expect(call.mock.calls[8][1]).toMatchObject({ stream_id: 8, lines: -3 })
    await act(async () => { for (const reply of replies.slice(0, 8)) reply() })
    expect(call).toHaveBeenCalledTimes(9)
  })
  it('starts a fresh wheel queue when a reconnected WebSocket reuses the same stream ID', async () => {
    let flush: FrameRequestCallback = () => {}
    vi.spyOn(window, 'requestAnimationFrame').mockImplementation((callback) => { flush = callback; return 1 })
    vi.spyOn(performance, 'now').mockReturnValue(0)
    const replies: Array<{ resolve: () => void; reject: () => void }> = []
    const client = new WorkbenchClient('hst_test')
    vi.spyOn(client, 'openTerminal').mockResolvedValue(7)
    const frames = vi.spyOn(client, 'onTerminal').mockReturnValue(() => true)
    const call = vi.spyOn(client, 'call').mockImplementation(() => new Promise((resolve, reject) => replies.push({ resolve: () => resolve({ ok: true }), reject: () => reject(new Error('old connection closed')) })))
    const props = { client, pane: { ...mockPane, scroll: { max_offset_from_bottom: 0, offset_from_bottom: 0, viewport_rows: 40 } }, active: true, onFocus: () => {}, theme: {}, enhancedContrast: false }
    const { rerender } = render(<TerminalPane {...props} connectionEpoch={1}/>)
    await waitFor(() => expect(frames).toHaveBeenCalledTimes(1))
    const wheel = () => {
      fireEvent.wheel(document.querySelector('.terminal-viewport')!, { deltaY: -48, cancelable: true })
      act(() => flush(0))
    }
    for (let i = 0; i < 9; i++) wheel()
    expect(call).toHaveBeenCalledTimes(8)

    rerender(<TerminalPane {...props} connectionEpoch={2}/>)
    await waitFor(() => expect(frames).toHaveBeenCalledTimes(2))
    wheel()
    expect(call).toHaveBeenCalledTimes(9) // Old in-flight requests cannot stall the new connection.
    expect(call.mock.calls[8][1]).toMatchObject({ stream_id: 7, lines: -3 })
    for (let i = 0; i < 8; i++) wheel()
    expect(call).toHaveBeenCalledTimes(16) // Eight new requests plus one queued gesture.

    await act(async () => {
      for (const [index, reply] of replies.slice(0, 8).entries()) {
        if (index % 2) reply.reject()
        else reply.resolve()
      }
    })
    expect(call).toHaveBeenCalledTimes(16)
    expect(screen.queryByRole('alert')).not.toBeInTheDocument()
    await act(async () => replies[8].resolve())
    expect(call).toHaveBeenCalledTimes(17)
    expect(call.mock.calls[16][1]).toMatchObject({ stream_id: 7, lines: -3 })
  })
  it('changes font and zoom without sending remote resize/input or reopening the observation stream', async () => {
    vi.spyOn(HTMLElement.prototype, 'clientWidth', 'get').mockReturnValue(390)
    vi.spyOn(HTMLElement.prototype, 'clientHeight', 'get').mockReturnValue(600)
    const client = new WorkbenchClient('hst_test')
    const open = vi.spyOn(client, 'openTerminal').mockResolvedValue(7)
    const close = vi.spyOn(client, 'closeTerminal')
    const resize = vi.spyOn(client, 'resize')
    const input = vi.spyOn(client, 'sendInput')
    const frames = vi.spyOn(client, 'onTerminal').mockReturnValue(() => true)
    const props = { client, pane: mockPane, connectionEpoch: 1, active: true, sourceCols: 150, sourceRows: 40, onFocus: () => {}, theme: {}, enhancedContrast: false }
    terminalHarness.instances = 0
    const { rerender } = render(<TerminalPane {...props}/>)
    await waitFor(() => expect(frames).toHaveBeenCalledTimes(1))
    terminalHarness.fontWrites = []
    rerender(<TerminalPane {...props} display={{ fontSize: 20, zoom: 150, mode: 'fixed' }}/>)
    expect(terminalHarness.fontWrites).toEqual([30])
    rerender(<TerminalPane {...props} display={{ fontSize: 20, zoom: 100, mode: 'fit' }}/>)
    expect(terminalHarness.fontWrites.at(-1)).toBeLessThan(5)
    expect(open).toHaveBeenCalledExactlyOnceWith(mockPane.pane_id, 150, 40)
    expect(close).not.toHaveBeenCalled()
    expect(resize).not.toHaveBeenCalled()
    expect(input).not.toHaveBeenCalled()
    expect(terminalHarness.instances).toBe(1)
  })
  it('applies only the final font on sidebar changes without reopening the terminal', async () => {
    let width = 900
    vi.spyOn(HTMLElement.prototype, 'clientWidth', 'get').mockImplementation(() => width)
    vi.spyOn(HTMLElement.prototype, 'clientHeight', 'get').mockReturnValue(600)
    const client = new WorkbenchClient('hst_test')
    const open = vi.spyOn(client, 'openTerminal').mockResolvedValue(7)
    const frames = vi.spyOn(client, 'onTerminal').mockReturnValue(() => true)
    const props = { client, pane: mockPane, connectionEpoch: 1, active: true, sourceCols: 150, sourceRows: 40, onFocus: () => {}, theme: {}, enhancedContrast: false }
    terminalHarness.instances = 0
    const { rerender } = render(<TerminalPane {...props} layoutVersion={0}/>)
    await waitFor(() => expect(frames).toHaveBeenCalledTimes(1))
    terminalHarness.fontWrites = []
    width = 1080
    rerender(<TerminalPane {...props} layoutVersion={1}/>)
    expect(terminalHarness.fontWrites).toHaveLength(1)
    expect(terminalHarness.fontWrites[0]).toBeCloseTo(12, 1)
    expect(open).toHaveBeenCalledTimes(1)
    expect(terminalHarness.instances).toBe(1)
  })
  it('reflows a 295-column pane at readable font size and resizes without reopening on rotation or font changes', async () => {
    let width = 479, height = 600
    vi.spyOn(HTMLElement.prototype, 'clientWidth', 'get').mockImplementation(() => width)
    vi.spyOn(HTMLElement.prototype, 'clientHeight', 'get').mockImplementation(() => height)
    const client = new WorkbenchClient('hst_test')
    const open = vi.spyOn(client, 'openTerminal').mockResolvedValue(7)
    const resize = vi.spyOn(client, 'resize').mockImplementation(() => {})
    const frames = vi.spyOn(client, 'onTerminal').mockReturnValue(() => {})
    const font = vi.fn()
    const props = { client, pane: mockPane, connectionEpoch: 1, active: true, sourceCols: 295, sourceRows: 40, onFocus: () => {}, onFontSizeChange: font, theme: {}, enhancedContrast: false }
    const { rerender, unmount } = render(<TerminalPane {...props} display={{ mode: 'responsive', fontSize: 14, zoom: 100 }}/>)
    await waitFor(() => expect(frames).toHaveBeenCalledTimes(1))
    expect(open).toHaveBeenCalledExactlyOnceWith('p1', 56, 42, true)
    expect(font).toHaveBeenLastCalledWith(14)
    width = 320; height = 300
    rerender(<TerminalPane {...props} layoutVersion={1} display={{ mode: 'responsive', fontSize: 14, zoom: 100 }}/>)
    await waitFor(() => expect(resize).toHaveBeenLastCalledWith(7, 37, 21))
    expect(font).toHaveBeenLastCalledWith(14)
    rerender(<TerminalPane {...props} sourceCols={37} sourceRows={21} display={{ mode: 'responsive', fontSize: 18, zoom: 100 }}/>)
    await waitFor(() => expect(resize).toHaveBeenLastCalledWith(7, 29, 16))
    expect(open).toHaveBeenCalledTimes(1)
    expect(font).toHaveBeenLastCalledWith(18)
    unmount()
  })

  it('sends one newline for Shift+Enter without submitting or duplicating keyup', async () => {
    const client = new WorkbenchClient('hst_test')
    vi.spyOn(client, 'openTerminal').mockResolvedValue(7)
    const input = vi.spyOn(client, 'sendInput').mockImplementation(() => {})
    const frames = vi.spyOn(client, 'onTerminal').mockReturnValue(() => true)
    render(<TerminalPane client={client} pane={mockPane} connectionEpoch={1} active onFocus={() => {}} theme={{}} enhancedContrast={false}/>)
    await waitFor(() => expect(frames).toHaveBeenCalled())
    const event = new KeyboardEvent('keydown', { key: 'Enter', shiftKey: true, cancelable: true })
    expect(terminalHarness.keyHandler!(event)).toBe(false)
    expect(event.defaultPrevented).toBe(true)
    terminalHarness.keyHandler!(new KeyboardEvent('keyup', { key: 'Enter', shiftKey: true }))
    expect(input).toHaveBeenCalledExactlyOnceWith(7, '\n')
    expect(terminalHarness.keyHandler!(new KeyboardEvent('keydown', { key: 'Enter' }))).toBe(true)
    expect(terminalHarness.keyHandler!(new KeyboardEvent('keydown', { key: 'Enter', shiftKey: true, isComposing: true }))).toBe(true)
    for (const key of ['+', '=', '-', '0']) {
      const zoom = new KeyboardEvent('keydown', { key, ctrlKey: true, cancelable: true })
      expect(terminalHarness.keyHandler!(zoom)).toBe(false)
      expect(zoom.defaultPrevented).toBe(false)
    }
  })

  it('routes mouse wheel to Herdr history while keeping browser zoom untouched', async () => {
    terminalHarness.scrolls = []
    const client = new WorkbenchClient('hst_test')
    const open = vi.spyOn(client, 'openTerminal').mockResolvedValue(7)
    const frames = vi.spyOn(client, 'onTerminal').mockReturnValue(() => true)
    const read = vi.spyOn(client, 'call').mockResolvedValue({ read: { text: 'history\r\ncurrent' } })
    render(<TerminalPane client={client} pane={mockPane} connectionEpoch={1} active onFocus={() => {}} theme={{}} enhancedContrast={false}/>)
    await waitFor(() => expect(frames).toHaveBeenCalled())
    const host = document.querySelector('.terminal-host')!
    expect(fireEvent.wheel(host, { deltaY: -48, cancelable: true })).toBe(false)
    await waitFor(() => expect(terminalHarness.scrolls).toEqual([-3]))
    expect(read).toHaveBeenCalledWith('pane.read', { pane_id: 'p1', source: 'recent', format: 'ansi', lines: 10000 })
    expect(fireEvent.wheel(host, { deltaY: 2, deltaMode: 1, cancelable: true })).toBe(false)
    await waitFor(() => expect(terminalHarness.scrolls).toEqual([-3, 2]))
    expect(fireEvent.wheel(host, { deltaY: 48, ctrlKey: true, cancelable: true })).toBe(true)
    expect(read).toHaveBeenCalledTimes(1)
    fireEvent.click(screen.getByRole('button', { name: '返回实时' }))
    await waitFor(() => expect(open).toHaveBeenCalledTimes(2))
  })
  it('ignores plain text paste and does not call pasteImage', () => {
    const pasteSpy = vi.spyOn(api, 'pasteImage').mockResolvedValue({ ok: true, path: '/test.png', injected: true })
    const client = new WorkbenchClient('hst_test')
    render(
      <TerminalPane
        client={client}
        pane={mockPane}
        connectionEpoch={1}
        active={true}
        onFocus={() => {}}
        theme={{}}
        enhancedContrast={false}
      />
    )

    const host = document.querySelector('.terminal-host')!
    const clipboardData = {
      items: [
        {
          kind: 'string',
          type: 'text/plain',
          getAsFile: () => null,
        },
      ],
    }
    const pasteEvent = new Event('paste', { bubbles: true, cancelable: true })
    Object.defineProperty(pasteEvent, 'clipboardData', { value: clipboardData })

    fireEvent(host, pasteEvent)
    expect(pasteSpy).not.toHaveBeenCalled()
  })

  it('intercepts image paste and calls api.pasteImage', async () => {
    const pasteSpy = vi.spyOn(api, 'pasteImage').mockResolvedValue({ ok: true, path: '/staged/img_123.png', injected: true })
    const client = new WorkbenchClient('hst_test')
    render(
      <TerminalPane
        client={client}
        pane={mockPane}
        connectionEpoch={1}
        active={true}
        onFocus={() => {}}
        theme={{}}
        enhancedContrast={false}
      />
    )

    const host = document.querySelector('.terminal-host')!
    const mockFile = new File(['fake-png-data'], 'screenshot.png', { type: 'image/png' })
    const clipboardData = {
      items: [
        {
          kind: 'file',
          type: 'image/png',
          getAsFile: () => mockFile,
        },
      ],
    }
    const pasteEvent = new Event('paste', { bubbles: true, cancelable: true })
    Object.defineProperty(pasteEvent, 'clipboardData', { value: clipboardData })

    fireEvent(host, pasteEvent)
    expect(pasteEvent.defaultPrevented).toBe(true)
    await waitFor(() => expect(pasteSpy).toHaveBeenCalledWith('hst_test', 'p1', mockFile, true))
    expect(pasteSpy).toHaveBeenCalledTimes(1)
  })

  it('rejects oversize images above 20MB', async () => {
    const pasteSpy = vi.spyOn(api, 'pasteImage')
    const client = new WorkbenchClient('hst_test')
    render(
      <TerminalPane
        client={client}
        pane={mockPane}
        connectionEpoch={1}
        active={true}
        onFocus={() => {}}
        theme={{}}
        enhancedContrast={false}
      />
    )

    const host = document.querySelector('.terminal-host')!
    const bigFile = new File(['x'], 'big.png', { type: 'image/png' })
    Object.defineProperty(bigFile, 'size', { value: 21 * 1024 * 1024 })

    const clipboardData = {
      items: [
        {
          kind: 'file',
          type: 'image/png',
          getAsFile: () => bigFile,
        },
      ],
    }
    const pasteEvent = new Event('paste', { bubbles: true, cancelable: true })
    Object.defineProperty(pasteEvent, 'clipboardData', { value: clipboardData })

    fireEvent(host, pasteEvent)
    expect(pasteEvent.defaultPrevented).toBe(true)
    expect(pasteSpy).not.toHaveBeenCalled()
    expect(await screen.findByRole('alert', { name: '图片粘贴提示' })).toHaveTextContent('图片大小超过 20MB 限制')
  })

  it('routes paste to the targeted pane even when a different pane is active', async () => {
    const pasteSpy = vi.spyOn(api, 'pasteImage').mockResolvedValue({ ok: true, path: '/staged/image.png', injected: true })
    const client = new WorkbenchClient('hst_test')
    render(<>
      <TerminalPane client={client} pane={mockPane} connectionEpoch={1} active onFocus={() => {}} theme={{}} enhancedContrast={false}/>
      <TerminalPane client={client} pane={{ ...mockPane, pane_id: 'w1:p2' }} connectionEpoch={1} active={false} onFocus={() => {}} theme={{}} enhancedContrast={false}/>
    </>)
    const file = new File(['image'], 'screen.png', { type: 'image/png' })
    fireEvent.paste(document.querySelectorAll('.terminal-host')[1], { clipboardData: { files: [file] } })
    await waitFor(() => expect(pasteSpy).toHaveBeenCalledTimes(1))
    expect(pasteSpy).toHaveBeenCalledWith('hst_test', 'w1:p2', file, true)
  })

  it('leaves a persistent failure with retry, and supports file selection', async () => {
    const pasteSpy = vi.spyOn(api, 'pasteImage').mockRejectedValueOnce(new Error('连接中断')).mockResolvedValue({ ok: true, path: '/staged/image.png', injected: true })
    const client = new WorkbenchClient('hst_test')
    render(<TerminalPane client={client} pane={mockPane} connectionEpoch={1} active onFocus={() => {}} theme={{}} enhancedContrast={false}/>)
    const file = new File(['image'], 'screen.png', { type: 'image/png' })
    fireEvent.change(screen.getByLabelText('选择图片'), { target: { files: [file] } })
    expect(await screen.findByRole('alert', { name: '图片粘贴提示' })).toHaveTextContent('连接中断')
    fireEvent.click(screen.getByRole('button', { name: '重试' }))
    await waitFor(() => expect(screen.getByRole('status')).toHaveTextContent('1 张图片已粘贴'))
    expect(pasteSpy).toHaveBeenCalledTimes(2)
  })

  it('pastes multiple images once each and in order', async () => {
    let finishFirst!: (result: { ok: boolean; path: string; injected: boolean }) => void
    const pasteSpy = vi.spyOn(api, 'pasteImage')
      .mockImplementationOnce(() => new Promise((resolve) => { finishFirst = resolve }))
      .mockResolvedValue({ ok: true, path: '/staged/second.png', injected: true })
    const client = new WorkbenchClient('hst_test')
    render(<TerminalPane client={client} pane={mockPane} connectionEpoch={1} active onFocus={() => {}} theme={{}} enhancedContrast={false}/>)
    const files = ['first', 'second'].map((name) => new File(['image'], `${name}.png`, { type: 'image/png' }))
    fireEvent.paste(document.querySelector('.terminal-host')!, { clipboardData: { files, items: files.map((file) => ({ kind: 'file', getAsFile: () => file })) } })
    await waitFor(() => expect(pasteSpy).toHaveBeenCalledTimes(1))
    finishFirst({ ok: true, path: '/staged/first.png', injected: true })
    await waitFor(() => expect(screen.getByRole('status')).toHaveTextContent('2 张图片已粘贴'))
    expect(pasteSpy).toHaveBeenNthCalledWith(1, 'hst_test', 'p1', files[0], true)
    expect(pasteSpy).toHaveBeenNthCalledWith(2, 'hst_test', 'p1', files[1], true)
  })

  it('does not paste into a background terminal when editing a modal', () => {
    const pasteSpy = vi.spyOn(api, 'pasteImage')
    const client = new WorkbenchClient('hst_test')
    render(<>
      <TerminalPane client={client} pane={mockPane} connectionEpoch={1} active onFocus={() => {}} theme={{}} enhancedContrast={false}/>
      <section role="dialog" aria-modal="true"><div contentEditable suppressContentEditableWarning data-testid="editor"/></section>
    </>)
    const event = new Event('paste', { bubbles: true, cancelable: true })
    Object.defineProperty(event, 'clipboardData', { value: { files: [new File(['image'], 'screen.png', { type: 'image/png' })] } })
    fireEvent(screen.getByTestId('editor'), event)
    expect(event.defaultPrevented).toBe(false)
    expect(pasteSpy).not.toHaveBeenCalled()
  })
})
