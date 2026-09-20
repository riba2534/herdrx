import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, expect, it, vi } from 'vitest'
import { api } from '../lib/api'
import type { HerdrCapabilities, Host } from '../types'
import { HostCapabilities } from './HostCapabilities'

const host: Host = { id: 'host-a', name: '开发主机', transport: 'ssh', created_at: '', updated_at: '' }
const report: HerdrCapabilities = {
  cli: { version: '0.9.1', protocol: 22 }, daemon: { version: '0.8.2', protocol: 20 },
  generation: 'session-a', checked_at: '2026-09-20T00:00:00Z', status: 'limited', coverage: 'tested',
  features: {
    snapshot: { state: 'available', reason: '' }, observe: { state: 'available', reason: '' },
    input: { state: 'available', reason: '' }, resize: { state: 'available', reason: '' },
    preserve_scroll: { state: 'unavailable', reason: '当前运行版本不支持保尺寸滚轮。' },
    history: { state: 'unknown', reason: '读取超时，请重新检查。' },
  },
}
afterEach(() => vi.restoreAllMocks())

it('separates installed and running versions and shows unknown capability as retryable', async () => {
  const probe = vi.spyOn(api, 'hostCapabilities').mockResolvedValueOnce({ capabilities: report }).mockResolvedValueOnce({ capabilities: { ...report, status: 'available' } })
  render(<HostCapabilities host={host} onClose={() => {}}/>)
  await screen.findByText('0.9.1 · 协议 22')
  expect(screen.getByText('0.8.2 · 协议 20')).toBeVisible()
  expect(screen.getByText('部分功能不可用或尚未确认')).toBeVisible()
  expect(screen.getByText('当前运行版本不支持保尺寸滚轮。')).toBeVisible()
  expect(screen.getByText('读取超时，请重新检查。')).toBeVisible()
  fireEvent.click(screen.getByRole('button', { name: '重新检查' }))
  await screen.findByText('已纳入版本回归')
  expect(probe).toHaveBeenNthCalledWith(2, host.id)
})

it('can retry failed probes and does not let a previous host overwrite the current report', async () => {
  let complete!: (result: { capabilities: HerdrCapabilities }) => void
  const probe = vi.spyOn(api, 'hostCapabilities').mockRejectedValueOnce(new Error('连接失败，请检查主机后重试。')).mockImplementationOnce(() => new Promise((resolve) => { complete = resolve })).mockResolvedValueOnce({ capabilities: { ...report, cli: { version: 'current', protocol: 22 } } })
  const { rerender } = render(<HostCapabilities host={host} onClose={() => {}}/>)
  await screen.findByRole('alert')
  fireEvent.click(screen.getByRole('button', { name: '重新检查' }))
  await waitFor(() => expect(probe).toHaveBeenCalledTimes(2))
  rerender(<HostCapabilities host={{ ...host, id: 'host-b' }} onClose={() => {}}/>)
  await screen.findByText('current · 协议 22')
  await act(async () => complete({ capabilities: report }))
  expect(screen.queryByText('0.9.1 · 协议 22')).not.toBeInTheDocument()
  expect(screen.getByText('current · 协议 22')).toBeVisible()
})
