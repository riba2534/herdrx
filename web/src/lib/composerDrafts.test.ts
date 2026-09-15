import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { api, invalidateAuthentication } from './api'
import { COMPOSER_SUBMIT_SETTLE_MS, clearComposerDrafts, composerInFlight, composerOutcome, composerSubmitParams, idleComposerSend, readComposerDraft, readComposerSend, retainComposerDrafts, runComposerSend, writeComposerDraft } from './composerDrafts'

const user = { id: 'user', email: 'user@example.test', role: 'user', display_name: 'User' }

async function login(sessionID: string) {
  vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response(JSON.stringify({ user, csrf_token: `csrf-${sessionID}`, session_id: sessionID }))))
  await api.login({ email: 'user@example.test', password: 'test' })
}

beforeEach(async () => {
  invalidateAuthentication()
  clearComposerDrafts()
  localStorage.clear()
  sessionStorage.clear()
  await login('sess-a')
})
afterEach(() => {
  clearComposerDrafts()
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
})

describe('composer drafts', () => {
  it('keeps independent drafts per host and pane for the current login', () => {
    writeComposerDraft('host-1', 'pane-1', 'alpha')
    writeComposerDraft('host-1', 'pane-2', 'beta')
    writeComposerDraft('host-2', 'pane-1', 'gamma')
    expect(readComposerDraft('host-1', 'pane-1')).toBe('alpha')
    expect(readComposerDraft('host-1', 'pane-2')).toBe('beta')
    expect(readComposerDraft('host-2', 'pane-1')).toBe('gamma')
  })

  it('does not write drafts without a login session and forgets them after logout', async () => {
    writeComposerDraft('host-1', 'pane-1', 'secret prompt')
    expect(localStorage.getItem(`herdrx.composer.v1.${encodeURIComponent('sess-a')}/${encodeURIComponent('host-1')}/${encodeURIComponent('pane-1')}`)).toBe('secret prompt')
    invalidateAuthentication()
    expect(readComposerDraft('host-1', 'pane-1')).toBe('')
    await login('sess-b')
    expect(readComposerDraft('host-1', 'pane-1')).toBe('')
    writeComposerDraft('host-1', 'pane-1', 'other user')
    expect(readComposerDraft('host-1', 'pane-1')).toBe('other user')
    retainComposerDrafts('sess-b')
    expect([...Array(localStorage.length)].map((_, i) => localStorage.key(i)).filter((key) => key?.includes('sess-a'))).toEqual([])
  })

  it('does not persist a draft without a pane id', () => {
    writeComposerDraft('host-1', '', 'orphan')
    expect(readComposerDraft('host-1', '')).toBe('')
  })
})

describe('composer submit helpers', () => {
  it('builds the text leg without keys and the Enter leg without text', () => {
    // 第一腿：整段正文、没有按键，bracketed paste 由 herdr 决定。
    expect(composerSubmitParams('w1:p2', '第一行\n第二行 🙂', [])).toEqual({
      pane_id: 'w1:p2',
      text: '第一行\n第二行 🙂',
      keys: [],
    })
    // 第二腿：只发一次回车。
    expect(composerSubmitParams('w1:p2', '', ['Enter'])).toEqual({
      pane_id: 'w1:p2',
      text: '',
      keys: ['Enter'],
    })
  })

  it('classifies close, timeout and deadline as unknown rather than a confirmed failure', () => {
    expect(composerOutcome(new Error('请求超时'))).toBe('unknown')
    expect(composerOutcome(new Error('连接已断开'))).toBe('unknown')
    expect(composerOutcome(new Error('连接已关闭'))).toBe('unknown')
    expect(composerOutcome(new Error('context deadline exceeded'))).toBe('unknown')
    expect(composerOutcome(new Error('pane not found'))).toBe('failed')
    expect(composerOutcome(new Error('远端拒绝'))).toBe('failed')
  })

  it('classifies post-dispatch Herdr transport interrupts as unknown', () => {
    expect(composerOutcome(new Error('read herdr response: EOF'))).toBe('unknown')
    expect(composerOutcome(new Error('read remote herdr response: unexpected EOF'))).toBe('unknown')
    expect(composerOutcome(new Error('write herdr request: broken pipe'))).toBe('unknown')
    expect(composerOutcome(new Error('read herdr response: connection reset by peer'))).toBe('unknown')
    expect(composerOutcome(new Error('herdr pane.send_input: invalid_key: unsupported key Enter'))).toBe('failed')
  })
})

describe('composer two-leg send', () => {
  function recordingSubmit() {
    const calls: Array<{ paneID: string; text: string; keys: string[] }> = []
    const submit = async (paneID: string, text: string, keys: string[]): Promise<void> => {
      calls.push({ paneID, text, keys })
      if (keys.length && keys.includes('Enter') && plan.rejectEnter) throw plan.rejectEnter
      if (!keys.length && plan.rejectText) throw plan.rejectText
      if (!keys.length && !plan.rejectEnter) await plan.afterText?.()
    }
    const plan: { rejectText?: Error; rejectEnter?: Error; afterText?: () => Promise<void> } = {}
    return { submit, calls, plan }
  }

  it('sends the text first, then a bare Enter, bound to the pane the send started on', async () => {
    const { submit, calls } = recordingSubmit()
    writeComposerDraft('host', 'p1', '两腿一次提交')
    await runComposerSend('host', 'p1', submit)
    expect(calls).toEqual([
      { paneID: 'p1', text: '两腿一次提交', keys: [] },
      { paneID: 'p1', text: '', keys: ['Enter'] },
    ])
    expect(readComposerSend('host', 'p1').status).toBe('delivered')
    expect(readComposerDraft('host', 'p1')).toBe('')
  })

  // 两腿之间有 settle，回车腿不能与正文腿挤在同一个 tick：PTY 没有消息边界，
  // 目标来不及 read 时内核会把两腿合并成一次 read，字节流退化成单次调用、
  // 回车重新落进粘贴框架——正是本次修复要消除的形态。
  it('leaves a settle gap before the Enter leg so the target can read the paste', async () => {
    vi.useFakeTimers()
    try {
      const { submit, calls } = recordingSubmit()
      writeComposerDraft('host', 'p1', '两腿之间要留间隔')
      const pending = runComposerSend('host', 'p1', submit)
      await vi.advanceTimersByTimeAsync(0)
      expect(calls).toEqual([{ paneID: 'p1', text: '两腿之间要留间隔', keys: [] }])
      await vi.advanceTimersByTimeAsync(COMPOSER_SUBMIT_SETTLE_MS)
      await pending
      expect(calls).toEqual([
        { paneID: 'p1', text: '两腿之间要留间隔', keys: [] },
        { paneID: 'p1', text: '', keys: ['Enter'] },
      ])
      expect(readComposerSend('host', 'p1').status).toBe('delivered')
    } finally {
      vi.useRealTimers()
    }
  })

  it('keeps the draft and stays failed when the text leg is rejected, without sending an Enter', async () => {
    const { submit, calls, plan } = recordingSubmit()
    plan.rejectText = new Error('远端拒绝')
    writeComposerDraft('host', 'p1', 'keep me')
    await runComposerSend('host', 'p1', submit)
    expect(calls).toEqual([{ paneID: 'p1', text: 'keep me', keys: [] }])
    expect(readComposerSend('host', 'p1').status).toBe('failed')
    expect(readComposerDraft('host', 'p1')).toBe('keep me')
  })

  it('reports an unconfirmed Enter as unknown and never replays the text', async () => {
    const { submit, calls, plan } = recordingSubmit()
    plan.rejectEnter = new Error('连接已关闭')
    writeComposerDraft('host', 'p1', '正文已进终端')
    await runComposerSend('host', 'p1', submit)
    expect(calls).toEqual([
      { paneID: 'p1', text: '正文已进终端', keys: [] },
      { paneID: 'p1', text: '', keys: ['Enter'] },
    ])
    const state = readComposerSend('host', 'p1')
    expect(state.status).toBe('unknown')
    expect(state.error).toContain('正文已送入终端，但提交回车未确认')
    expect(readComposerDraft('host', 'p1')).toBe('正文已进终端')
  })
})

describe('composer send store', () => {
  it('keeps in-flight and unknown state after the UI unmounts, without auto replay', async () => {
    let finish: (error?: Error) => void = () => {}
    const submit = vi.fn().mockImplementation(() => new Promise<void>((resolve, reject) => { finish = (error) => error ? reject(error) : resolve() }))
    writeComposerDraft('host', 'p1', 'maybe delivered')
    const pending = runComposerSend('host', 'p1', submit)
    expect(readComposerSend('host', 'p1').status).toBe('sending')
    expect(composerInFlight('host', 'p1')).toBe(true)
    await finish(new Error('连接已关闭'))
    await pending
    expect(readComposerSend('host', 'p1').status).toBe('unknown')
    expect(readComposerDraft('host', 'p1')).toBe('maybe delivered')
    expect(composerInFlight('host', 'p1')).toBe(false)
    idleComposerSend('host', 'p1')
    expect(readComposerSend('host', 'p1').status).toBe('unknown')
  })

  it('keeps an unknown receipt after read herdr response EOF and does not auto replay', async () => {
    const submit = vi.fn().mockRejectedValue(new Error('read herdr response: EOF'))
    writeComposerDraft('host', 'p1', 'already written')
    await runComposerSend('host', 'p1', submit)
    expect(readComposerSend('host', 'p1').status).toBe('unknown')
    expect(readComposerDraft('host', 'p1')).toBe('already written')
    expect(submit).toHaveBeenCalledTimes(1)
  })
})
