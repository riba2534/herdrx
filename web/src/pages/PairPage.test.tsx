import { render, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { PairPage } from './PairPage'

vi.mock('../lib/api', () => ({ api: { pairTailcat: vi.fn() } }))

function encodePair(payload: object) {
  return btoa(JSON.stringify(payload)).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '')
}

beforeEach(() => {
  sessionStorage.clear()
  window.history.replaceState({}, '', '/')
})
afterEach(() => {
  sessionStorage.clear()
  window.history.replaceState({}, '', '/')
})

it('replaces #pair= with /pair and dispatches popstate so the app stays on the pairing page', async () => {
  const pops: string[] = []
  const onPop = () => pops.push(window.location.pathname)
  window.addEventListener('popstate', onPop)
  window.history.replaceState({}, '', `/#pair=${encodePair({ host: 'box', os: 'linux', arch: 'amd64', agent_ver: '1' })}`)
  render(<PairPage/>)
  expect(screen.getByRole('heading', { name: '连接 Tailcat 主机' })).toBeInTheDocument()
  await waitFor(() => expect(window.location.pathname).toBe('/pair'))
  await waitFor(() => expect(pops).toContain('/pair'))
  window.removeEventListener('popstate', onPop)
})

it('recommends herdrx pair instead of herdrx-agent pair when the link is invalid', () => {
  render(<PairPage/>)
  expect(screen.getByRole('heading', { name: '配对链接无效' })).toBeInTheDocument()
  expect(screen.getByRole('heading', { name: '配对链接无效' }).closest('section')).toHaveTextContent('~/.local/bin/herdrx pair')
  expect(screen.queryByText(/herdrx-agent pair/)).not.toBeInTheDocument()
})

it('submits the pairing form with the required host name', async () => {
  window.history.replaceState({}, '', `/#pair=${encodePair({ host: 'box', os: 'linux', arch: 'amd64', agent_ver: '1' })}`)
  render(<PairPage/>)
  expect(screen.getByRole('button', { name: '验证并连接' })).toHaveAttribute('type', 'submit')
  expect(screen.getByLabelText('主机名称')).toBeRequired()
})
