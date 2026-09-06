import { useState } from 'react'
import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { describe, expect, it, vi } from 'vitest'
import { Form, Input } from './Form'
import { useConfirm } from './useConfirm'

function Decision({ scope = 'host', effect, decision }: { scope?: string; effect: () => void; decision: (accepted: boolean) => void }) {
  const { confirm, dialog } = useConfirm(scope)
  return <><button onClick={() => { void confirm('关闭运行中的任务？', { title: '关闭任务', confirmLabel: '关闭任务', onConfirm: effect }).then(decision) }}>发起操作</button>{dialog}</>
}
describe('custom decisions', () => {
  it('starts on cancel, cancels without work, and confirms only once', async () => {
    const effect = vi.fn(), decision = vi.fn()
    render(<Decision effect={effect} decision={decision}/>)
    fireEvent.click(screen.getByText('发起操作'))
    const cancel = within(screen.getByRole('alertdialog')).getByRole('button', { name: '取消' })
    expect(cancel).toHaveFocus()
    fireEvent.click(cancel)
    await waitFor(() => expect(decision).toHaveBeenCalledWith(false))
    expect(effect).not.toHaveBeenCalled()
    fireEvent.click(screen.getByText('发起操作'))
    fireEvent.click(within(screen.getByRole('alertdialog')).getByRole('button', { name: '关闭任务' }))
    await waitFor(() => expect(decision).toHaveBeenLastCalledWith(true))
    expect(effect).toHaveBeenCalledOnce()
  })
  it('cancels when navigating to a different host or unmounting', async () => {
    const effect = vi.fn(), decision = vi.fn()
    const view = render(<Decision scope="one" effect={effect} decision={decision}/>)
    fireEvent.click(screen.getByText('发起操作'))
    view.rerender(<Decision scope="two" effect={effect} decision={decision}/>)
    await waitFor(() => expect(decision).toHaveBeenCalledWith(false))
    expect(screen.queryByRole('alertdialog')).not.toBeInTheDocument()
    fireEvent.click(screen.getByText('发起操作'))
    view.unmount()
    await act(async () => {})
    expect(decision.mock.calls).toEqual([[false], [false]])
    expect(effect).not.toHaveBeenCalled()
  })
})
it('shows inline constraints and only submits complete valid values', () => {
  const submit = vi.fn()
  render(<Form onSubmit={submit}><Input aria-label="邮箱" type="email" required/><Input aria-label="密码" required minLength={12}/><button>保存</button></Form>)
  fireEvent.click(screen.getByText('保存'))
  expect(screen.getByLabelText('邮箱')).toHaveFocus()
  expect(screen.getAllByRole('alert')).toHaveLength(2)
  expect(submit).not.toHaveBeenCalled()
  fireEvent.input(screen.getByLabelText('邮箱'), { target: { value: 'person@example.test' } })
  fireEvent.input(screen.getByLabelText('密码'), { target: { value: 'short' } })
  fireEvent.click(screen.getByText('保存'))
  expect(screen.getByRole('alert')).toHaveTextContent('至少输入 12 个字符')
  expect(submit).not.toHaveBeenCalled()
  fireEvent.input(screen.getByLabelText('密码'), { target: { value: 'long-enough-password' } })
  fireEvent.click(screen.getByText('保存'))
  expect(submit).toHaveBeenCalledOnce()
  expect(screen.queryByRole('alert')).not.toBeInTheDocument()
})

it('preserves the first edit to a controlled input after showing an error', () => {
  function Controlled() {
    const [value, setValue] = useState('')
    return <Form><Input aria-label="名称" required value={value} onChange={(event) => setValue(event.target.value)}/><button>保存</button></Form>
  }
  render(<Controlled/>)
  fireEvent.click(screen.getByText('保存'))
  expect(screen.getByRole('alert')).toBeInTheDocument()
  fireEvent.input(screen.getByLabelText('名称'), { target: { value: '第一次输入' } })
  expect(screen.getByLabelText('名称')).toHaveValue('第一次输入')
  expect(screen.queryByRole('alert')).not.toBeInTheDocument()
})
