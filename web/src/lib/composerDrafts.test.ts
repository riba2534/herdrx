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

  it('treats proxy errors and unknown codes as uncertain and keeps the id, but definite rejections as failed', async () => {
    const prompt = vi.spyOn(api, 'panePrompt')
    writeComposerDraft('host', 'p1', '可能已发出')
    for (const error of [
      new APIError(502, 'request_failed', '请求失败，请检查网络后重试', {}),
      new APIError(504, 'request_failed', '请求失败，请检查网络后重试', {}),
      new APIError(500, 'internal_error', 'boom', { code: 'internal_error' }),
    ]) {
      prompt.mockRejectedValueOnce(error)
      await runComposerPrompt('host', 'p1')
      expect(readComposerSend('host', 'p1').status).toBe('unknown')
    }
    const ids = new Set(prompt.mock.calls.map((call) => call[2].client_msg_id))
    expect(ids.size).toBe(1)
    for (const error of [
      new APIError(502, 'pane_read_failed', '读取终端画面失败'),
      new APIError(400, 'invalid_client_msg_id', '消息编号无效'),
      new APIError(404, 'host_not_found', '主机不存在'),
      new APIError(502, 'host_unreachable', '连不上主机', { code: 'host_unreachable', retryable: true }),
      new APIError(429, 'prompt_busy', '同时进行中的发送太多'),
    ]) {
      prompt.mockRejectedValueOnce(error)
      await runComposerPrompt('host', 'p1')
      expect(readComposerSend('host', 'p1').status).toBe('failed')
    }
  })

  it('keeps the message id across a page reload until the draft changes', async () => {
    const prompt = vi.spyOn(api, 'panePrompt').mockRejectedValueOnce(new TypeError('Failed to fetch'))
    writeComposerDraft('host', 'p1', '刷新前发的')
    await runComposerPrompt('host', 'p1')
    const first = prompt.mock.calls[0][2].client_msg_id
    // 模拟刷新页面：模块状态重建，localStorage 留着。
    vi.resetModules()
    const fresh = await import('./api')
    const drafts = await import('./composerDrafts')
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response(JSON.stringify({ user, csrf_token: 'csrf-sess-a', session_id: 'sess-a' }))))
    await fresh.api.login({ email: 'user@example.test', password: 'test' })
    const again = vi.spyOn(fresh.api, 'panePrompt').mockResolvedValue({ delivered: 'duplicate' })
    expect(drafts.readComposerDraft('host', 'p1')).toBe('刷新前发的')
    await drafts.runComposerPrompt('host', 'p1')
    expect(again.mock.calls[0][2].client_msg_id).toBe(first)
    // 改了正文就是新消息。
    again.mockRejectedValueOnce(new TypeError('Failed to fetch'))
    drafts.writeComposerDraft('host', 'p1', '改过的')
    await drafts.runComposerPrompt('host', 'p1')
    expect(again.mock.calls[1][2].client_msg_id).not.toBe(first)
    // 退出登录后不留下编号。
    drafts.clearComposerDrafts()
    expect([...Array(localStorage.length)].map((_, i) => localStorage.key(i)).filter((key) => key?.endsWith('#msg'))).toEqual([])
  })

  it('explains a dialog that only the terminal can answer', async () => {
    vi.spyOn(api, 'panePrompt').mockRejectedValue(new APIError(409, 'pane_dialog', '终端正弹着一个多选对话框'))
    writeComposerDraft('host', 'p1', '1')
    await expect(runComposerPrompt('host', 'p1')).resolves.toEqual({ kind: 'dialog', dialog: null })
    expect(readComposerSend('host', 'p1').error).toContain('需要在终端里操作')
  })

  it('keeps message ids per tab: another tab never reuses or deletes this tab id', async () => {
    // 标签页 B 先打开（模块状态独立，共用 localStorage）。
    vi.resetModules()
    const tabB = { api: (await import('./api')).api, drafts: await import('./composerDrafts') }
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response(JSON.stringify({ user, csrf_token: 'csrf-sess-a', session_id: 'sess-a' }))))
    await tabB.api.login({ email: 'user@example.test', password: 'test' })
    // 标签页 A 发送结果未知：它的 id 存进 localStorage 供刷新后去重。
    const promptA = vi.spyOn(api, 'panePrompt').mockRejectedValue(new TypeError('Failed to fetch'))
    writeComposerDraft('host', 'p1', '同一段话')
    await runComposerPrompt('host', 'p1')
    const idA = promptA.mock.calls[0][2].client_msg_id
    const stored = () => [...Array(localStorage.length)].map((_, i) => localStorage.key(i)).filter((key) => key?.endsWith('#msg'))
    expect(stored()).toHaveLength(1)
    // 标签页 B 发相同正文：是另一条消息，用自己的 id；送达后也不删 A 的记录。
    const promptB = vi.spyOn(tabB.api, 'panePrompt').mockResolvedValue({ delivered: 'sent' })
    tabB.drafts.writeComposerDraft('host', 'p1', '同一段话')
    await tabB.drafts.runComposerPrompt('host', 'p1')
    expect(promptB.mock.calls[0][2].client_msg_id).not.toBe(idA)
    expect(stored()).toHaveLength(1)
    expect(JSON.parse(localStorage.getItem(stored()[0]!)!).id).toBe(idA)
  })

  it('stores nothing for a send that was delivered', async () => {
    vi.spyOn(api, 'panePrompt').mockResolvedValue({ delivered: 'sent' })
    writeComposerDraft('host', 'p1', '普通发送')
    await runComposerPrompt('host', 'p1')
    expect([...Array(localStorage.length)].map((_, i) => localStorage.key(i)).filter((key) => key?.endsWith('#msg'))).toEqual([])
  })

  it('drops the uncertain record once the draft moves on, so an old text is never mistaken for a duplicate', async () => {
    const storedKeys = () => [...Array(localStorage.length)].map((_, i) => localStorage.key(i)).filter((key): key is string => Boolean(key?.endsWith('#msg')))
    const prompt = vi.spyOn(api, 'panePrompt').mockRejectedValueOnce(new TypeError('Failed to fetch'))
    writeComposerDraft('host', 'p1', 'T1')
    await runComposerPrompt('host', 'p1')
    const first = prompt.mock.calls[0][2].client_msg_id
    expect(storedKeys()).toHaveLength(1)
    // 用户改成 T2：那次结果未知的 T1 不会再原样重试，记录删掉。
    writeComposerDraft('host', 'p1', 'T2')
    expect(storedKeys()).toEqual([])
    prompt.mockResolvedValue({ delivered: 'sent' })
    await runComposerPrompt('host', 'p1')
    // 刷新后再发 T1：是新消息，换新 id。
    vi.resetModules()
    const fresh = await import('./api')
    const drafts = await import('./composerDrafts')
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response(JSON.stringify({ user, csrf_token: 'csrf-sess-a', session_id: 'sess-a' }))))
    await fresh.api.login({ email: 'user@example.test', password: 'test' })
    const again = vi.spyOn(fresh.api, 'panePrompt').mockResolvedValue({ delivered: 'sent' })
    drafts.writeComposerDraft('host', 'p1', 'T1')
    await drafts.runComposerPrompt('host', 'p1')
    expect(again.mock.calls[0][2].client_msg_id).not.toBe(first)
  })

  it('drops a stale record for a different text after a successful send', async () => {
    const storedKeys = () => [...Array(localStorage.length)].map((_, i) => localStorage.key(i)).filter((key): key is string => Boolean(key?.endsWith('#msg')))
    vi.spyOn(api, 'panePrompt').mockRejectedValueOnce(new TypeError('Failed to fetch')).mockResolvedValue({ delivered: 'sent' })
    writeComposerDraft('host', 'p1', '旧的')
    await runComposerPrompt('host', 'p1')
    const record = localStorage.getItem(storedKeys()[0])!
    writeComposerDraft('host', 'p1', '新的')
    // 别的标签页刚好又写回了旧记录：发送“新的”成功后仍清掉它。
    localStorage.setItem(storedKeys()[0] ?? `herdrx.composer.v1.${encodeURIComponent('sess-a')}/host/p1#msg`, record)
    expect(storedKeys()).toHaveLength(1)
    await runComposerPrompt('host', 'p1')
    expect(storedKeys()).toEqual([])
  })
})
