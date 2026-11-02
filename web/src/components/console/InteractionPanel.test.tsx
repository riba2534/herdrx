import { act, fireEvent, render, screen } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi, type MockInstance } from 'vitest'
import { APIError, api } from '../../lib/api'
import { EMPTY_CONSOLE_LIVE, type ConsoleLive } from '../../lib/console/useConsoleLive'
import { INTERACTION_POLL_MS, RECEIPT_TIMEOUT_MS } from '../../lib/console/useDialogSession'
import type { ChatRecord } from '../../lib/structuredChatTypes'
import type { Pane, PaneDialog } from '../../types'
import { InteractionPanel } from './InteractionPanel'

const questions = [
  { header: '测试框架', multiSelect: false, question: '测试框架用 pytest 还是 unittest？', options: [{ label: 'pytest', description: '使用 pytest 作为测试框架' }, { label: 'unittest', description: '使用 unittest 作为测试框架' }] },
  { header: '类型注解', multiSelect: false, question: '要不要加类型注解？', options: [{ label: '要', description: '给函数加上类型注解' }, { label: '不要', description: '不加类型注解' }] },
]
const ask = (callID = 'ask1', input: unknown = { questions }): ChatRecord[] => [{ id: `r-${callID}`, role: 'assistant', blocks: [{ type: 'tool-call', call_id: callID, name: 'AskUserQuestion', input }] }]
const exitPlan = (plan: string): ChatRecord[] => [{ id: 'r-plan', role: 'assistant', blocks: [{ type: 'tool-call', call_id: 'plan1', name: 'ExitPlanMode', input: { plan } }] }]
const answered = (records: ChatRecord[], callID: string): ChatRecord[] => [...records, { id: `res-${callID}`, role: 'tool', blocks: [{ type: 'tool-result', call_id: callID, output: 'ok', is_error: false }] }]
const pane = (agent_status: Pane['agent_status'] = 'idle') => ({ pane_id: 'p1', workspace_id: 'w1', tab_id: 't1', terminal_id: 'term1', agent: 'claude', agent_status, focused: true, revision: 1 }) as Pane

const options = (labels: string[], selected = 0) => labels.map((label, index) => ({ index: index + 1, label, selected: index === selected }))
const q1: PaneDialog = { fingerprint: 'fp-q1', kind: 'question', title: '测试框架用 pytest 还是 unittest？', lines: ['←  ☐ 测试框架  ☐ 类型注解  ✔ Submit  →'], options: options(['pytest', 'unittest', 'Type something.', 'Chat about this']) }
const q1Free: PaneDialog = { ...q1, fingerprint: 'fp-q1-free', options: options(['pytest', 'unittest', 'Type something.', 'Chat about this'], 2) }
const q2: PaneDialog = { fingerprint: 'fp-q2', kind: 'question', title: '要不要加类型注解？', lines: [], options: options(['要', '不要', 'Type something.', 'Chat about this']) }
const review: PaneDialog = { fingerprint: 'fp-review', kind: 'review', title: 'Ready to submit your answers?', lines: ['Review your answers', '● 测试框架用 pytest 还是 unittest？', '→ pytest', '● 要不要加类型注解？', '→ 要'], options: options(['Submit answers', 'Cancel']) }
const planDialog: PaneDialog = { fingerprint: 'fp-plan', kind: 'plan', title: 'Would you like to proceed?', lines: [], options: options(['Yes, and switch to BYPASS PERMISSIONS (no further prompts) for this session', 'Yes, manually approve edits', 'Tell Claude what to change']) }
const trust: PaneDialog = { fingerprint: 'fp-trust', kind: 'trust', title: 'Accessing workspace:', lines: ['/tmp/project', 'Quick safety check'], options: options(['No, exit', 'Yes, I trust this folder']) }
const permission: PaneDialog = { fingerprint: 'fp-perm', title: 'Do you want to proceed?', lines: ['Bash command', 'rm -rf build'], options: options(['Yes', 'No']) }

let shown: PaneDialog | null = null
let read: MockInstance<typeof api.paneDialog>
let answer: MockInstance<typeof api.answerPaneDialog>
let prompt: MockInstance<typeof api.panePrompt>

function setVisibility(state: DocumentVisibilityState) {
  Object.defineProperty(document, 'visibilityState', { configurable: true, get: () => state })
  document.dispatchEvent(new Event('visibilitychange'))
}

const settle = (ms = 0) => act(async () => { await vi.advanceTimersByTimeAsync(ms) })

function mount(props: { records?: ChatRecord[]; status?: Pane['agent_status']; live?: ConsoleLive; onOpenShell?: () => void } = {}) {
  const onActiveChange = vi.fn()
  const onDialogChange = vi.fn()
  const element = (next: typeof props) => <InteractionPanel hostID="host" pane={pane(next.status)} records={next.records || []} live={next.live || EMPTY_CONSOLE_LIVE} onActiveChange={onActiveChange} onDialogChange={onDialogChange} onOpenShell={next.onOpenShell}/>
  const view = render(element(props))
  return { ...view, onActiveChange, onDialogChange, update: (next: typeof props) => view.rerender(element(next)) }
}

beforeEach(() => {
  vi.useFakeTimers()
  setVisibility('visible')
  shown = null
  read = vi.spyOn(api, 'paneDialog').mockImplementation(async () => ({ dialog: shown }))
  answer = vi.spyOn(api, 'answerPaneDialog').mockResolvedValue({ ok: true })
  prompt = vi.spyOn(api, 'panePrompt').mockResolvedValue({ delivered: 'sent' })
})

afterEach(() => {
  vi.useRealTimers()
  vi.restoreAllMocks()
})

describe('InteractionPanel', () => {
  it('stays empty and does not read the screen without a pending question or a blocked pane', async () => {
    const view = mount({ records: answered(ask(), 'ask1') })
    await settle(INTERACTION_POLL_MS * 3)
    expect(read).not.toHaveBeenCalled()
    expect(view.container).toBeEmptyDOMElement()
    expect(view.onActiveChange).not.toHaveBeenCalledWith(true)
  })

  it('shows the question from the transcript and answers with the screen index', async () => {
    shown = q1
    const view = mount({ records: ask() })
    await settle()
    expect(read).toHaveBeenCalledWith('host', 'p1', expect.objectContaining({ signal: expect.any(AbortSignal) }))
    expect(screen.getByRole('group', { name: 'Claude 在问：测试框架用 pytest 还是 unittest？' })).toBeInTheDocument()
    expect(screen.getByText('第 1/2 题')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: /^unittest/ })).toHaveTextContent('使用 unittest 作为测试框架')
    expect(view.onActiveChange).toHaveBeenLastCalledWith(true)

    fireEvent.click(screen.getByRole('button', { name: /^pytest/ }))
    await settle()
    expect(answer).toHaveBeenCalledWith('host', 'p1', 'fp-q1', 1)
    // 画面还没变：按钮锁住，显示正在确认。
    expect(screen.getByRole('status')).toHaveTextContent('已作答，正在确认终端…')
    expect(screen.getByRole('button', { name: /^unittest/ })).toBeDisabled()

    shown = q2
    await settle(INTERACTION_POLL_MS)
    expect(screen.getByRole('group', { name: 'Claude 在问：要不要加类型注解？' })).toBeInTheDocument()
    expect(screen.getByText('第 2/2 题')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: /^要/ })).toBeEnabled()
  })

  it('answers with number keys unless the key belongs to a text field or an input method', async () => {
    shown = q1
    mount({ records: ask() })
    await settle()
    const input = document.createElement('input')
    document.body.append(input)
    fireEvent.keyDown(input, { key: '1' })
    fireEvent.keyDown(window, { key: '1', isComposing: true })
    fireEvent.keyDown(window, { key: '1', ctrlKey: true })
    fireEvent.keyDown(window, { key: '9' })
    await settle()
    expect(answer).not.toHaveBeenCalled()
    fireEvent.keyDown(window, { key: '2' })
    await settle()
    expect(answer).toHaveBeenCalledWith('host', 'p1', 'fp-q1', 2)
    input.remove()
  })

  it('moves to the free-text item, then sends the typed answer once through the guarded prompt', async () => {
    shown = q1
    const view = mount({ records: ask() })
    await settle()
    fireEvent.click(screen.getByRole('button', { name: /自己填写答案/ }))
    await settle()
    expect(answer).toHaveBeenCalledWith('host', 'p1', 'fp-q1', 3)
    // 光标还没停到自由填写项上：不出输入框。
    expect(screen.queryByRole('textbox')).toBeNull()
    shown = q1Free
    await settle(INTERACTION_POLL_MS)
    const box = screen.getByRole('textbox', { name: '自己填写的答案' })
    fireEvent.change(box, { target: { value: 'nose2' } })
    fireEvent.keyDown(box, { key: 'Enter', isComposing: true })
    expect(prompt).not.toHaveBeenCalled()
    fireEvent.keyDown(box, { key: 'Enter' })
    await settle()
    expect(prompt).toHaveBeenCalledTimes(1)
    expect(prompt).toHaveBeenCalledWith('host', 'p1', { text: 'nose2', submit: true, client_msg_id: expect.stringMatching(/^[A-Za-z0-9_-]{1,64}$/) })
    expect(screen.getByRole('status')).toHaveTextContent('已作答，正在确认终端…')
    shown = null
    await settle(INTERACTION_POLL_MS)
    expect(view.container).toBeEmptyDOMElement()
    expect(view.onActiveChange).toHaveBeenLastCalledWith(false)
  })

  it('opens the input at once when the cursor already sits on the free-text item', async () => {
    shown = q1Free
    mount({ records: ask() })
    await settle()
    fireEvent.keyDown(window, { key: '3' })
    await settle()
    expect(answer).not.toHaveBeenCalled()
    const box = screen.getByRole('textbox', { name: '自己填写的答案' })
    // 输入框里打数字不算快捷键。
    fireEvent.keyDown(box, { key: '1' })
    fireEvent.keyDown(box, { key: 'Escape' })
    expect(screen.queryByRole('textbox')).toBeNull()
    expect(answer).not.toHaveBeenCalled()
  })

  it('keeps the text and does not resend when the terminal refuses or the reply is lost', async () => {
    shown = q1Free
    mount({ records: ask() })
    await settle()
    fireEvent.click(screen.getByRole('button', { name: /自己填写答案/ }))
    const box = screen.getByRole('textbox', { name: '自己填写的答案' })
    fireEvent.change(box, { target: { value: 'nose2' } })
    prompt.mockRejectedValueOnce(new APIError(409, 'pane_dialog', '终端正弹着一个选择对话框'))
    fireEvent.click(screen.getByRole('button', { name: '提交答案' }))
    await settle()
    expect(screen.getByRole('status')).toHaveTextContent('终端没有进入输入状态，这段文字没有发出去。请到 Shell 模式操作')
    expect(box).toHaveValue('nose2')

    prompt.mockRejectedValueOnce(new TypeError('Failed to fetch'))
    fireEvent.click(screen.getByRole('button', { name: '提交答案' }))
    await settle(RECEIPT_TIMEOUT_MS * 2)
    expect(screen.getByRole('status')).toHaveTextContent('网络中断，没有收到服务端的回应')
    expect(prompt).toHaveBeenCalledTimes(2)
    // 用户看过终端后自己再点：沿用同一个编号，服务端能认出已送达的那次。
    fireEvent.click(screen.getByRole('button', { name: '提交答案' }))
    await settle()
    expect(prompt).toHaveBeenCalledTimes(3)
    const ids = prompt.mock.calls.map((call) => call[2].client_msg_id)
    expect(ids[2]).toBe(ids[1])
  })

  it('says it could not confirm the answer after 15 seconds and never presses again', async () => {
    shown = q1
    mount({ records: ask() })
    await settle()
    fireEvent.click(screen.getByRole('button', { name: /^pytest/ }))
    await settle(RECEIPT_TIMEOUT_MS - 100)
    expect(screen.getByRole('status')).toHaveTextContent('已作答，正在确认终端…')
    await settle(200)
    expect(screen.getByRole('status')).toHaveTextContent('没有确认到结果，请查看终端后再决定')
    expect(screen.getByRole('button', { name: /^pytest/ })).toBeEnabled()
    await settle(RECEIPT_TIMEOUT_MS * 2)
    expect(answer).toHaveBeenCalledTimes(1)
  })

  it('reloads the dialog and asks to choose again when it changed before the key press', async () => {
    shown = q1
    mount({ records: ask() })
    await settle()
    answer.mockRejectedValueOnce(new APIError(409, 'dialog_changed', '对话框已变化，请重新选择'))
    shown = q2
    const reads = read.mock.calls.length
    fireEvent.click(screen.getByRole('button', { name: /^pytest/ }))
    await settle()
    expect(read.mock.calls.length).toBeGreaterThan(reads)
    expect(screen.getByRole('group', { name: 'Claude 在问：要不要加类型注解？' })).toBeInTheDocument()
    expect(screen.getByRole('status')).toHaveTextContent('对话框已变化，请重新选择')
    expect(answer).toHaveBeenCalledTimes(1)
  })

  it('does not count a read that started before the key press, and rereads right after it', async () => {
    shown = q1
    mount({ records: ask() })
    await settle()
    let finish: (value: { dialog: PaneDialog | null }) => void = () => {}
    read.mockImplementationOnce(() => new Promise((resolve) => { finish = resolve }))
    await settle(INTERACTION_POLL_MS)
    fireEvent.click(screen.getByRole('button', { name: /^pytest/ }))
    await settle()
    expect(answer).toHaveBeenCalledTimes(1)
    await act(async () => finish({ dialog: q2 }))
    expect(screen.getByRole('status')).toHaveTextContent('已作答，正在确认终端…')
    // 作答后要的那次读屏在旧读屏回来后立刻补上，不等下一轮。
    shown = q2
    await settle()
    expect(screen.getByText('第 2/2 题')).toBeInTheDocument()
    expect(screen.queryByText('已作答，正在确认终端…')).toBeNull()
  })

  it('rereads at once when the live screen changes while confirming', async () => {
    shown = q1
    const view = mount({ records: ask() })
    await settle()
    fireEvent.click(screen.getByRole('button', { name: /^pytest/ }))
    await settle()
    const reads = read.mock.calls.length
    shown = q2
    view.update({ records: ask(), live: { ...EMPTY_CONSOLE_LIVE, preview: ['要不要加类型注解？'] } })
    await settle()
    expect(read.mock.calls.length).toBe(reads + 1)
    expect(screen.getByText('第 2/2 题')).toBeInTheDocument()
  })

  it('shows the plan from the transcript while Herdr reports idle and maps the three actions', async () => {
    shown = planDialog
    mount({ records: exitPlan('# 给 calc.py 添加 sub 函数\n\n新增 `sub`。'), status: 'idle' })
    await settle()
    expect(screen.getByRole('heading', { name: '给 calc.py 添加 sub 函数' })).toBeInTheDocument()
    const labels = screen.getAllByRole('listitem').map((item) => item.textContent)
    expect(labels).toEqual(['批准并切换到免授权', '批准，改动逐个确认', '提修改意见'])
    // 计划卡只能点击，数字键不作答。
    fireEvent.keyDown(window, { key: '1' })
    await settle()
    expect(answer).not.toHaveBeenCalled()
    fireEvent.click(screen.getByRole('button', { name: /批准并切换到免授权/ }))
    await settle()
    expect(answer).toHaveBeenCalledWith('host', 'p1', 'fp-plan', 1)
  })

  it('keeps the plan buttons disabled until the pending plan is in the transcript', async () => {
    shown = planDialog
    // 只有一份已经有结果的旧计划：不能拿它配屏幕上新计划的按钮。
    const view = mount({ records: answered(exitPlan('# 旧计划 A'), 'plan1'), status: 'blocked' })
    await settle()
    expect(screen.getByRole('status')).toHaveTextContent('计划加载中')
    expect(screen.queryByRole('heading', { name: '旧计划 A' })).toBeNull()
    for (const name of [/批准并切换到免授权/, /批准，改动逐个确认/, /提修改意见/]) expect(screen.getByRole('button', { name })).toBeDisabled()
    view.update({ records: [...answered(exitPlan('# 旧计划 A'), 'plan1'), { id: 'r-plan2', role: 'assistant', blocks: [{ type: 'tool-call', call_id: 'plan2', name: 'ExitPlanMode', input: { plan: '# 新计划 B' } }] }], status: 'blocked' })
    await settle()
    expect(screen.getByRole('heading', { name: '新计划 B' })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: /批准并切换到免授权/ })).toBeEnabled()
  })

  it('disables the plan buttons when the plan on screen is not the one in the transcript', async () => {
    shown = { ...planDialog, lines: ['给 calc.py 添加 sub 函数'] }
    mount({ records: exitPlan('# 把 add 改名为 plus'), status: 'idle' })
    await settle()
    expect(screen.getByRole('alert')).toHaveTextContent('屏幕上的计划和对话记录里的这份对不上')
    expect(screen.getByRole('button', { name: /批准并切换到免授权/ })).toBeDisabled()
    fireEvent.click(screen.getByRole('button', { name: /批准并切换到免授权/ }))
    await settle()
    expect(answer).not.toHaveBeenCalled()
  })

  it('collects plan feedback after moving to the feedback item', async () => {
    shown = planDialog
    mount({ records: exitPlan('# 计划'), status: 'idle' })
    await settle()
    fireEvent.click(screen.getByRole('button', { name: /提修改意见/ }))
    await settle()
    expect(answer).toHaveBeenCalledWith('host', 'p1', 'fp-plan', 3)
    shown = { ...planDialog, fingerprint: 'fp-plan-3', options: options(planDialog.options.map((option) => option.label), 2) }
    await settle(INTERACTION_POLL_MS)
    const box = screen.getByRole('textbox', { name: '修改意见' })
    fireEvent.change(box, { target: { value: '请顺便加上类型注解' } })
    fireEvent.click(screen.getByRole('button', { name: '提交意见' }))
    await settle()
    expect(prompt).toHaveBeenCalledWith('host', 'p1', expect.objectContaining({ text: '请顺便加上类型注解', submit: true }))
  })

  it('asks before trusting a folder, by the option text and without number keys', async () => {
    shown = trust
    mount({ status: 'blocked' })
    await settle()
    expect(screen.getByText('/tmp/project')).toBeInTheDocument()
    fireEvent.keyDown(window, { key: '1' })
    await settle()
    expect(answer).not.toHaveBeenCalled()
    expect(screen.getByRole('button', { name: '退出' })).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: '信任此目录并继续' }))
    await settle()
    expect(answer).toHaveBeenCalledWith('host', 'p1', 'fp-trust', 2)
  })

  it('reviews the collected answers before submitting', async () => {
    shown = review
    mount({ records: ask() })
    await settle()
    expect(screen.getByText('测试框架用 pytest 还是 unittest？')).toBeInTheDocument()
    expect(screen.getByText('pytest')).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: /提交答案/ }))
    await settle()
    expect(answer).toHaveBeenCalledWith('host', 'p1', 'fp-review', 1)
    fireEvent.click(screen.getByRole('button', { name: /取消/ }))
    expect(answer).toHaveBeenCalledTimes(1)
  })

  it('falls back to the generic dialog card for other dialogs', async () => {
    shown = permission
    const view = mount({ status: 'blocked' })
    await settle()
    expect(screen.getByRole('group', { name: 'Do you want to proceed?' })).toHaveTextContent('rm -rf build')
    expect(view.onActiveChange).toHaveBeenLastCalledWith(true)
    fireEvent.click(screen.getByRole('button', { name: '收起对话框卡片' }))
    expect(screen.getByRole('button', { name: '展开卡片：终端在等你选择：Do you want to proceed?' })).toBeInTheDocument()
    // 收成条后输入框回来。
    expect(view.onActiveChange).toHaveBeenLastCalledWith(false)
    // 对话框消失后同一个对话框再出现，卡片重新展开。
    shown = null
    await settle(INTERACTION_POLL_MS)
    expect(view.onActiveChange).toHaveBeenLastCalledWith(false)
    shown = permission
    await settle(INTERACTION_POLL_MS)
    expect(screen.getByRole('group', { name: 'Do you want to proceed?' })).toBeInTheDocument()
  })

  it('shows only the screen text when the question on screen is not the one in the transcript', async () => {
    shown = { ...q1, title: '测试框架用哪个？', options: [{ index: 1, label: 'pytest', selected: true, description: '屏幕上的说明' }, ...q1.options.slice(1)] }
    mount({ records: ask() })
    await settle()
    expect(screen.getByRole('group', { name: 'Claude 在问：测试框架用哪个？' })).toBeInTheDocument()
    expect(screen.queryByText(/第 \d\/\d 题/)).toBeNull()
    expect(screen.getByRole('button', { name: /^pytest/ })).toHaveTextContent('屏幕上的说明')
    expect(screen.queryByText('使用 pytest 作为测试框架')).toBeNull()
    fireEvent.click(screen.getByRole('button', { name: /^unittest/ }))
    await settle()
    expect(answer).toHaveBeenCalledWith('host', 'p1', 'fp-q1', 2)
  })

  it('folds away and stops reading after three reads without a dialog, until the transcript or the status changes', async () => {
    shown = null
    const multi = ask('multi', { questions: [{ ...questions[0], multiSelect: true }] })
    const view = mount({ records: multi, status: 'idle' })
    await settle()
    // 多选题的说明卡在读不到对话框时出现，连续 3 次读不到就收起。
    expect(screen.getByText(/这道题可以多选/)).toBeInTheDocument()
    await settle(INTERACTION_POLL_MS * 2)
    expect(read).toHaveBeenCalledTimes(3)
    expect(screen.queryByText(/这道题可以多选/)).toBeNull()
    expect(view.onActiveChange).toHaveBeenLastCalledWith(false)
    await settle(INTERACTION_POLL_MS * 4)
    expect(read).toHaveBeenCalledTimes(3)
    view.update({ records: multi, status: 'working' })
    await settle()
    expect(read).toHaveBeenCalledTimes(4)
  })

  it('keeps reading a blocked pane however many reads come back empty', async () => {
    shown = null
    mount({ records: ask(), status: 'blocked' })
    await settle()
    // 一轮一轮推进，让每次读屏后的状态都来得及生效。
    for (let round = 0; round < 5; round++) await settle(INTERACTION_POLL_MS)
    expect(read).toHaveBeenCalledTimes(6)
  })

  it('reports whether a card or a dialog is present', async () => {
    shown = q1
    // 收起状态按交互记在模块里：用单独的调用编号，不影响别的用例。
    const view = mount({ records: ask('ask-presence') })
    await settle()
    expect(view.onDialogChange).toHaveBeenLastCalledWith(true)
    fireEvent.click(screen.getByRole('button', { name: '收起卡片' }))
    // 收成条也算：这时按 Shift+Tab 一样会落进对话框。
    expect(view.onDialogChange).toHaveBeenLastCalledWith(true)
    view.update({ records: answered(ask('ask-presence'), 'ask-presence') })
    await settle()
    expect(view.onDialogChange).toHaveBeenLastCalledWith(false)
  })

  it('guides multi-select questions to the Shell mode', async () => {
    const onOpenShell = vi.fn()
    const view = mount({ records: ask('multi', { questions: [{ ...questions[0], multiSelect: true }] }), status: 'blocked', onOpenShell })
    await settle()
    expect(screen.getByText(/这道题可以多选/)).toBeInTheDocument()
    expect(view.onActiveChange).toHaveBeenLastCalledWith(true)
    fireEvent.click(screen.getByRole('button', { name: '在 Shell 中打开' }))
    expect(onOpenShell).toHaveBeenCalledTimes(1)
  })

  it('folds into a bar on this device and unfolds for a new question', async () => {
    shown = q1
    const view = mount({ records: ask() })
    await settle()
    fireEvent.click(screen.getByRole('button', { name: '收起卡片' }))
    const bar = screen.getByRole('button', { name: '展开卡片：Claude 在等你回答：测试框架' })
    expect(bar).toHaveTextContent('第 1/2 题')
    // 收成条后输入框回来。
    expect(view.onActiveChange).toHaveBeenLastCalledWith(false)
    // 收起时数字键不作答。
    fireEvent.keyDown(window, { key: '1' })
    await settle()
    expect(answer).not.toHaveBeenCalled()
    view.unmount()
    const again = mount({ records: ask() })
    await settle()
    expect(screen.getByRole('button', { name: /^展开卡片/ })).toBeInTheDocument()
    again.update({ records: ask('ask2') })
    await settle()
    expect(screen.getByRole('group', { name: /Claude 在问/ })).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: '收起卡片' }))
    expect(again.onActiveChange).toHaveBeenLastCalledWith(false)
    fireEvent.click(screen.getByRole('button', { name: /^展开卡片/ }))
    await settle()
    expect(screen.getByRole('group', { name: /Claude 在问/ })).toBeInTheDocument()
    expect(again.onActiveChange).toHaveBeenLastCalledWith(true)
  })

  it('polls only while the page is visible and stops once the transcript has the answer', async () => {
    setVisibility('hidden')
    shown = q1
    const view = mount({ records: ask() })
    await settle(INTERACTION_POLL_MS * 3)
    expect(read).not.toHaveBeenCalled()
    setVisibility('visible')
    await settle()
    expect(read).toHaveBeenCalledTimes(1)
    await settle(INTERACTION_POLL_MS)
    expect(read).toHaveBeenCalledTimes(2)
    view.update({ records: answered(ask(), 'ask1') })
    await settle()
    expect(view.container).toBeEmptyDOMElement()
    expect(view.onActiveChange).toHaveBeenLastCalledWith(false)
    await settle(INTERACTION_POLL_MS * 3)
    expect(read).toHaveBeenCalledTimes(2)
  })

  it('reports false when unmounted with a card', async () => {
    shown = q1
    const view = mount({ records: ask() })
    await settle()
    view.unmount()
    expect(view.onActiveChange).toHaveBeenLastCalledWith(false)
  })
})
