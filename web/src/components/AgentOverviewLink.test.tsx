import { act, fireEvent, render, screen } from '@testing-library/react'
import { afterEach, expect, it, vi } from 'vitest'
import { api } from '../lib/api'
import { navigate } from '../lib/navigation'
import { AgentOverviewLink } from './AgentOverviewLink'

vi.mock('../lib/api', () => ({ api: { agents: vi.fn() } }))
vi.mock('../lib/navigation', () => ({ navigate: vi.fn() }))
afterEach(() => { vi.mocked(api.agents).mockReset(); vi.mocked(navigate).mockReset() })

it('shows how many agents need the user and opens the overview', async () => {
  vi.mocked(api.agents).mockResolvedValue({ agents: [], hosts: [], attention_count: 3 })
  render(<AgentOverviewLink/>)
  await act(async () => {})
  const link = screen.getByRole('link', { name: 'Agent 总览，3 个需要你处理' })
  expect(link).toHaveTextContent('3')
  fireEvent.click(link)
  expect(navigate).toHaveBeenCalledWith('/agents')
})

it('stays a plain entry when the overview cannot be read', async () => {
  vi.mocked(api.agents).mockRejectedValue(new Error('offline'))
  render(<AgentOverviewLink compact/>)
  await act(async () => {})
  expect(screen.getByRole('link', { name: 'Agent 总览' })).toHaveAttribute('href', '/agents')
})
