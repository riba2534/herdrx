import { act, fireEvent, render, screen } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { ScreenView } from './ScreenView'
import { terminalThemes } from '../lib/themes'
import type { ScreenFrame } from '../lib/ansiScreen'
import type { ScreenError, WorkbenchClient } from '../lib/workbench'
import type { Pane } from '../types'

type Watch = { paneID: string; lines: number; onFrame: (frame: ScreenFrame) => void; onError: (error: ScreenError) => void; stopped: boolean }

function fakeClient() {
  const watches: Watch[] = []
  const client = {
    watchScreen: vi.fn((paneID: string, lines: number, handlers: { onFrame: (frame: ScreenFrame) => void; onError: (error: ScreenError) => void }) => {
      const watch: Watch = { paneID, lines, ...handlers, stopped: false }
      watches.push(watch)
      return () => { watch.stopped = true }
    }),
  }
  return { client: client as unknown as WorkbenchClient, watches }
}

const pane: Pane = { pane_id: 'p1', workspace_id: 'w1', tab_id: 't1', terminal_id: 'term1', agent: 'claude', agent_status: 'working', revision: 1, focused: true } as Pane
const theme = terminalThemes.Cobalt2

beforeEach(() => { localStorage.clear() })
afterEach(() => { vi.restoreAllMocks() })

describe('ScreenView', () => {
  it('renders full frames and applies deltas from the workbench', () => {
    const { client, watches } = fakeClient()
    render(<ScreenView client={client} pane={pane} theme={theme}/>)
    expect(watches).toHaveLength(1)
    expect(watches[0]).toMatchObject({ paneID: 'p1', lines: 200 })
    act(() => watches[0].onFrame({ pane_id: 'p1', gen: 1, seq: 1, full: true, lines: ['\x1b[0m\x1b[38;5;1merror\x1b[0m: build', 'ok'], total: 2 }))
    const log = screen.getByRole('log', { name: 'Pane 画面' })
    expect(log.textContent).toContain('error: build')
    expect(screen.getByText('error')).toHaveStyle({ color: theme.red })
    act(() => watches[0].onFrame({ pane_id: 'p1', gen: 1, seq: 2, drop: 1, total: 2, set: [[1, '$ ']] }))
    const lines = Array.from(log.querySelectorAll('.screen-line')).map((line) => line.textContent)
    expect(lines).toEqual(['ok', '$ '])
    expect(screen.getByText(/画面 · 实时/)).toBeInTheDocument()
  })

  it('stops watching when hidden behind another pane on a phone and when the host disconnects', () => {
    const { client, watches } = fakeClient()
    const view = render(<ScreenView client={client} pane={pane} theme={theme} compact active/>)
    expect(watches).toHaveLength(1)
    view.rerender(<ScreenView client={client} pane={pane} theme={theme} compact active={false}/>)
    expect(watches[0].stopped).toBe(true)
    view.rerender(<ScreenView client={client} pane={pane} theme={theme} compact active connected={false}/>)
    expect(watches).toHaveLength(1)
    expect(screen.getByText(/主机未连接，画面暂停刷新/)).toBeInTheDocument()
    view.rerender(<ScreenView client={client} pane={pane} theme={theme} compact active connected/>)
    expect(watches).toHaveLength(2)
  })

  it('loads earlier lines in steps once the window is full', () => {
    const { client, watches } = fakeClient()
    render(<ScreenView client={client} pane={pane} theme={theme}/>)
    act(() => watches[0].onFrame({ pane_id: 'p1', gen: 1, seq: 1, full: true, lines: Array.from({ length: 200 }, (_, index) => `line ${index}`), total: 200 }))
    fireEvent.click(screen.getByRole('button', { name: '加载更早内容（最近 500 行）' }))
    expect(watches.at(-1)).toMatchObject({ lines: 500 })
    expect(watches[0].stopped).toBe(true)
  })

  it('reports read failures, toggles wrapping and returns to the terminal', () => {
    const { client, watches } = fakeClient()
    const onSwitchToTerminal = vi.fn()
    render(<ScreenView client={client} pane={pane} theme={theme} onSwitchToTerminal={onSwitchToTerminal}/>)
    act(() => watches[0].onError({ code: 'pane_unavailable', message: '这个 Pane 已不存在，请切换到其他终端' }))
    expect(screen.getByText('读不到画面：这个 Pane 已不存在，请切换到其他终端')).toBeInTheDocument()
    const wrap = screen.getByRole('button', { name: '自动折行' })
    expect(wrap).toHaveAttribute('aria-pressed', 'true')
    fireEvent.click(wrap)
    expect(wrap).toHaveAttribute('aria-pressed', 'false')
    expect(localStorage.getItem('herdrx.screen-wrap')).toBe('false')
    fireEvent.click(screen.getByRole('button', { name: '显示终端' }))
    expect(onSwitchToTerminal).toHaveBeenCalledTimes(1)
  })
})
