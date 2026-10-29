import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { APIError, api, invalidateAuthentication } from './api'
import { clearComposerDrafts, composerInFlight, idleComposerSend, readComposerDraft, readComposerSend, retainComposerDrafts, runComposerPrompt, writeComposerDraft } from './composerDrafts'

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

describe('composer guarded send', () => {
  it('sends the whole draft once with a stable id and clears it after delivery', async () => {
    const prompt = vi.spyOn(api, 'panePrompt').mockResolvedValue({ delivered: 'sent' })
    writeComposerDraft('host', 'p1', '第一行\n第二行 🙂')
    await expect(runComposerPrompt('host', 'p1')).resolves.toEqual({ kind: 'sent' })
    expect(prompt).toHaveBeenCalledTimes(1)
    expect(prompt).toHaveBeenCalledWith('host', 'p1', { text: '第一行\n第二行 🙂', submit: true, client_msg_id: expect.stringMatching(/^[0-9a-f]{32}$/), force: undefined })
    expect(readComposerDraft('host', 'p1')).toBe('')
    expect(readComposerSend('host', 'p1').status).toBe('delivered')
  })

  it('keeps the draft and the same id after a network failure, without auto replay', async () => {
    let finish: (error?: Error) => void = () => {}
    const prompt = vi.spyOn(api, 'panePrompt').mockImplementation(() => new Promise((resolve, reject) => { finish = (error) => error ? reject(error) : resolve({ delivered: 'sent' }) }))
    writeComposerDraft('host', 'p1', 'maybe delivered')
    const pending = runComposerPrompt('host', 'p1')
    expect(readComposerSend('host', 'p1').status).toBe('sending')
    expect(composerInFlight('host', 'p1')).toBe(true)
    finish(new TypeError('Failed to fetch'))
    await pending
    expect(readComposerSend('host', 'p1').status).toBe('unknown')
    expect(readComposerDraft('host', 'p1')).toBe('maybe delivered')
    expect(composerInFlight('host', 'p1')).toBe(false)
    idleComposerSend('host', 'p1')
    expect(readComposerSend('host', 'p1').status).toBe('unknown')
    expect(prompt).toHaveBeenCalledTimes(1)
    // A manual retry reuses the id so the server can drop a duplicate.
    const first = (prompt.mock.calls[0][2]).client_msg_id
    prompt.mockResolvedValue({ delivered: 'duplicate' })
    await runComposerPrompt('host', 'p1')
    expect(prompt.mock.calls[1][2].client_msg_id).toBe(first)
  })

  it('uses a new id once the server reports the earlier delivery as unknown', async () => {
    const prompt = vi.spyOn(api, 'panePrompt').mockRejectedValueOnce(new APIError(502, 'delivery_unknown', '上一次发送结果不确定'))
    writeComposerDraft('host', 'p1', 'already written')
    await runComposerPrompt('host', 'p1')
    expect(readComposerSend('host', 'p1')).toMatchObject({ status: 'unknown', error: '上一次发送结果不确定' })
    expect(readComposerDraft('host', 'p1')).toBe('already written')
    prompt.mockResolvedValue({ delivered: 'sent' })
    await runComposerPrompt('host', 'p1')
    expect(prompt.mock.calls[1][2].client_msg_id).not.toBe(prompt.mock.calls[0][2].client_msg_id)
  })

  it('refuses without sending when the pane shows a dialog or a desktop draft', async () => {
    const dialog = { fingerprint: 'f', lines: [], options: [{ index: 1, label: 'Yes', selected: true }] }
    vi.spyOn(api, 'panePrompt')
      .mockRejectedValueOnce(new APIError(409, 'pane_dialog', 'dialog', { dialog }))
      .mockRejectedValueOnce(new APIError(409, 'pane_draft', 'draft'))
    writeComposerDraft('host', 'p1', '继续')
    await expect(runComposerPrompt('host', 'p1')).resolves.toEqual({ kind: 'dialog', dialog })
    expect(readComposerSend('host', 'p1').status).toBe('refused')
    await expect(runComposerPrompt('host', 'p1')).resolves.toEqual({ kind: 'draft' })
    expect(readComposerDraft('host', 'p1')).toBe('继续')
  })

  it('reports a rejected send as failed and keeps the draft', async () => {
    vi.spyOn(api, 'panePrompt').mockRejectedValue(new APIError(502, 'pane_send_failed', '发送失败（pane not found）'))
    writeComposerDraft('host', 'p1', '保留')
    await expect(runComposerPrompt('host', 'p1')).resolves.toEqual({ kind: 'failed' })
    expect(readComposerSend('host', 'p1').status).toBe('failed')
    expect(readComposerDraft('host', 'p1')).toBe('保留')
  })
})
