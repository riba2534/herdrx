import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { api, invalidateAuthentication } from './api'
import { clearComposerDrafts, composerInFlight, composerOutcome, composerSubmitParams, idleComposerSend, readComposerDraft, readComposerSend, retainComposerDrafts, runComposerSend, writeComposerDraft } from './composerDrafts'

const user = { id: 'user', email: 'user@example.test', role: 'user', display_name: 'User' }

async function login(sessionID: string) {
  vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response(JSON.stringify({ user, csrf_token: `csrf-${sessionID}`, session_id: sessionID }))))
  await api.login({ email: 'user@example.test', password: 'test' })
}

beforeEach(async () => {
  invalidateAuthentication()
  clearComposerDrafts()
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
    expect(sessionStorage.getItem(`herdrx.composer.v1.${encodeURIComponent('sess-a')}/${encodeURIComponent('host-1')}/${encodeURIComponent('pane-1')}`)).toBe('secret prompt')
    invalidateAuthentication()
    expect(readComposerDraft('host-1', 'pane-1')).toBe('')
    await login('sess-b')
    expect(readComposerDraft('host-1', 'pane-1')).toBe('')
    writeComposerDraft('host-1', 'pane-1', 'other user')
    expect(readComposerDraft('host-1', 'pane-1')).toBe('other user')
    retainComposerDrafts('sess-b')
    expect([...Array(sessionStorage.length)].map((_, i) => sessionStorage.key(i)).filter((key) => key?.includes('sess-a'))).toEqual([])
  })

  it('does not persist a draft without a pane id', () => {
    writeComposerDraft('host-1', '', 'orphan')
    expect(readComposerDraft('host-1', '')).toBe('')
  })
})

describe('composer submit helpers', () => {
  it('uses Herdr pane.send_input text plus a single Enter', () => {
    expect(composerSubmitParams('w1:p2', '第一行\n第二行 🙂')).toEqual({
      pane_id: 'w1:p2',
      text: '第一行\n第二行 🙂',
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
