import { render, screen } from '@testing-library/react'
import { afterEach, expect, it, vi } from 'vitest'
import { api } from '../lib/api'
import { RelayConnectCommand } from './RelayConnectCommand'

vi.mock('../lib/api', () => ({ api: { cliRelease: vi.fn(), relayOffer: vi.fn() } }))
afterEach(() => vi.resetAllMocks())

it('keeps the original command for a CLI that predates workbench relay support', async () => {
  render(<RelayConnectCommand release={{ status: 'available', version: 'v0.1.0-rc.1' }}/>)
  expect(await screen.findByLabelText('生成绑定凭据')).toHaveTextContent(/^~\/.local\/bin\/herdrx connect --plain$/)
  expect(api.relayOffer).not.toHaveBeenCalled()
})

it('offers a scoped relay command for endpoint migration after release discovery', async () => {
  vi.mocked(api.cliRelease).mockResolvedValue({ status: 'available', version: 'v0.1.0-rc.2' })
  vi.mocked(api.relayOffer).mockResolvedValue({ available: true, workbench: 'https://workbench.example.com', address: '203.0.113.20', token: 'a'.repeat(43), expires_at: new Date(Date.now() + 1_200_000).toISOString() })
  render(<RelayConnectCommand refresh/>)
  expect(await screen.findByLabelText('生成端点更新包')).toHaveTextContent(`~/.local/bin/herdrx connect --refresh-endpoint --plain --workbench 'https://workbench.example.com' --relay-token '${'a'.repeat(43)}' --relay-address '203.0.113.20'`)
})

it.each(['unavailable', 'failed', 'expired'])('keeps connection available when relay discovery is %s', async (state) => {
  if (state === 'failed') vi.mocked(api.relayOffer).mockRejectedValue(new Error('offline'))
  else vi.mocked(api.relayOffer).mockResolvedValue(state === 'unavailable' ? { available: false } : { available: true, workbench: 'https://workbench.example.com', token: 'a'.repeat(43), expires_at: new Date(0).toISOString() })
  render(<RelayConnectCommand release={{ status: 'available', version: 'v0.1.0-rc.2' }}/>)
  expect(await screen.findByLabelText('生成绑定凭据')).toHaveTextContent(/^~\/.local\/bin\/herdrx connect --plain$/)
})
