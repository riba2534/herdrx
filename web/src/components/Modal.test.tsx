import { fireEvent, render, screen } from '@testing-library/react'
import { expect, it, vi } from 'vitest'
import { Modal } from './Modal'

it('keeps the close control disabled while busy unless allowed', () => {
  const onClose = vi.fn()
  const view = render(<Modal title="等待" busy onClose={onClose}><p>配对中</p></Modal>)
  expect(screen.getByRole('button', { name: '关闭' })).toBeDisabled()
  fireEvent.keyDown(screen.getByRole('dialog'), { key: 'Escape' })
  expect(onClose).not.toHaveBeenCalled()
  view.rerender(<Modal title="等待" busy allowCloseWhileBusy onClose={onClose}><p>配对中</p></Modal>)
  expect(screen.getByRole('button', { name: '关闭' })).toBeEnabled()
  fireEvent.click(screen.getByRole('button', { name: '关闭' }))
  expect(onClose).toHaveBeenCalledOnce()
})
