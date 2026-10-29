import { act, fireEvent, render, screen, within } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { api } from '../lib/api'
import { navigate } from '../lib/navigation'
import type { AgentOverview, AgentOverviewItem } from '../types'
import { AgentsPage, agentPaneURL } from './AgentsPage'

vi.mock('../lib/api', () => ({ api: { agents: vi.fn() } }))
vi.mock('../lib/navigation', () => ({ navigate: vi.fn() }))

const item = (overrides: Partial<AgentOverviewItem>): AgentOverviewItem => ({
  host_id: 'h1', host_name: '开发机', pane_id: 'w1:p1', workspace_id: 'w1', tab_id: 'w1:t1', workspace_label: '后端', tab_label: '修复',
  agent: 'claude', agent_status: 'idle', attention: 'seen', state_change_seq: 1, cwd: '/srv/herdrx', ...overrides,
})

const overview: AgentOverview = {
  agents: [
    item({ pane_id: 'w1:p1', agent: 'codex', agent_status: 'blocked', attention: 'needs_you', title: '确认写入权限' }),
    item({ pane_id: 'w1:p2', agent: 'claude', agent_status: 'done', attention: 'unseen_done' }),
    item({ pane_id: 'w1:p3', agent: 'gemini', agent_status: 'working', attention: 'working' }),
    item({ pane_id: 'w1:p4', agent: 'opencode', agent_status: 'idle', attention: 'seen' }),
    item({ pane_id: 'w1:p5', agent: 'amp', agent_status: 'done', attention: 'seen' }),
  ],
  hosts: [{ host_id: 'h1', host_name: '开发机', status: 'ok' }, { host_id: 'h2', host_name: '构建机', status: 'error', error: '无法连接主机，请打开主机查看连接状态' }],
  attention_count: 2,
}

let visibility: DocumentVisibilityState = 'visible'
const badge = { setAppBadge: vi.fn(async () => {}), clearAppBadge: vi.fn(async () => {}) }

beforeEach(() => {
  vi.useFakeTimers({ toFake: ['setInterval', 'clearInterval', 'setTimeout', 'clearTimeout'] })
  visibility = 'visible'
  vi.spyOn(document, 'visibilityState', 'get').mockImplementation(() => visibility)
  Object.assign(navigator, badge)
  vi.mocked(api.agents).mockResolvedValue(overview)
})
afterEach(() => {
  vi.useRealTimers()
  vi.restoreAllMocks()
  vi.mocked(api.agents).mockReset()
  vi.mocked(navigate).mockReset()
  badge.setAppBadge.mockClear()
  badge.clearAppBadge.mockClear()
})

async function renderPage() {
  render(<AgentsPage/>)
  await act(async () => {})
}

describe('AgentsPage', () => {
  it('groups agents by attention and collapses the idle ones', async () => {
    await renderPage()
    const attention = screen.getByRole('region', { name: /需要你/ })
    const rows = within(attention).getAllByRole('link')
    expect(rows.map((row) => row.textContent)).toEqual([
      expect.stringContaining('codex'), expect.stringContaining('claude'),
    ])
    expect(within(rows[0]).getByText('确认写入权限')).toBeInTheDocument()
    expect(rows[0]).toHaveTextContent('开发机 · 后端 › 修复 · herdrx')
    expect(within(screen.getByRole('region', { name: /工作中/ })).getByText('gemini')).toBeInTheDocument()

    const toggle = screen.getByRole('button', { name: '还有 2 个空闲' })
    expect(screen.queryByText('opencode')).not.toBeInTheDocument()
    fireEvent.click(toggle)
    expect(screen.getByText('opencode')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: '已看或空闲 · 2' })).toHaveAttribute('aria-expanded', 'true')
  })

  it('opens the pane deep link and lets a failed host be opened', async () => {
    await renderPage()
    const link = screen.getByRole('link', { name: /codex/ })
    expect(link).toHaveAttribute('href', '/h/h1?pane=w1%3Ap1')
    fireEvent.click(link)
    expect(navigate).toHaveBeenCalledWith('/h/h1?pane=w1%3Ap1')

    const failed = screen.getByRole('region', { name: /无法读取的主机/ })
    expect(within(failed).getByText('无法连接主机，请打开主机查看连接状态')).toBeInTheDocument()
    fireEvent.click(within(failed).getByRole('button', { name: '打开主机' }))
    expect(navigate).toHaveBeenCalledWith('/h/h2')
  })

  it('refreshes every 5s only while visible and mirrors the count on the app badge', async () => {
    await renderPage()
    expect(api.agents).toHaveBeenCalledTimes(1)
    expect(badge.setAppBadge).toHaveBeenCalledWith(2)

    visibility = 'hidden'
    await act(async () => { vi.advanceTimersByTime(15_000) })
    expect(api.agents).toHaveBeenCalledTimes(1)

    vi.mocked(api.agents).mockResolvedValue({ ...overview, agents: overview.agents.map((agent) => ({ ...agent, attention: 'seen' })), attention_count: 0 })
    visibility = 'visible'
    await act(async () => { document.dispatchEvent(new Event('visibilitychange')) })
    expect(api.agents).toHaveBeenCalledTimes(2)
    expect(badge.clearAppBadge).toHaveBeenCalled()
    await act(async () => { vi.advanceTimersByTime(5000) })
    expect(api.agents).toHaveBeenCalledTimes(3)
  })

  it('keeps the last state visible when a refresh fails', async () => {
    await renderPage()
    vi.mocked(api.agents).mockRejectedValue(new Error('网络中断'))
    await act(async () => { vi.advanceTimersByTime(5000) })
    expect(screen.getByRole('alert')).toHaveTextContent('刷新失败：网络中断')
    expect(screen.getByText('codex')).toBeInTheDocument()
  })

  it('guides to adding a host when there is none', async () => {
    vi.mocked(api.agents).mockResolvedValue({ agents: [], hosts: [], attention_count: 0 })
    await renderPage()
    fireEvent.click(screen.getByRole('button', { name: '去添加主机' }))
    expect(navigate).toHaveBeenCalledWith('/')
  })

  it('explains that Herdr has not detected agents yet', async () => {
    vi.mocked(api.agents).mockResolvedValue({ agents: [], hosts: [{ host_id: 'h1', host_name: '开发机', status: 'ok' }], attention_count: 0 })
    await renderPage()
    expect(screen.getByText(/各主机的 Herdr 里还没有识别到 Agent/)).toBeInTheDocument()
  })

  it('builds encoded deep links', () => {
    expect(agentPaneURL('h 1', 'w1:p/2')).toBe('/h/h%201?pane=w1%3Ap%2F2')
  })
})
