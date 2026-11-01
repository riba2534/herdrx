import { describe, expect, it } from 'vitest'
import type { ChatRecord } from '../structuredChatTypes'
import { ArrivalTracker } from './arrivals'
import { collectUnreadKeys } from './unreadCount'
import { insideScrollableAbove, isUpwardWheelIntent, shouldRepinOnWheel } from './autoFollow'
import { rehypeWordFade, splitWords, wordFadeDelay, WORD_FADE_MAX_DELAY_MS } from './wordFade'

const record = (id: string, role: ChatRecord['role'], extra: Partial<ChatRecord> = {}): ChatRecord => ({ id, role, blocks: [{ type: 'text', text: id }], ...extra })
const ask = (id: string): ChatRecord => ({ id, role: 'assistant', blocks: [{ type: 'tool-call', call_id: id, name: 'AskUserQuestion', input: {} }] })

describe('collectUnreadKeys', () => {
  const appended = (...ids: string[]) => new Set(ids)

  it('counts one key per turn for appended replies', () => {
    const messages = [record('u1', 'user'), record('a1', 'assistant'), record('a2', 'assistant'), record('t2', 'tool'), record('a3', 'assistant')]
    expect(collectUnreadKeys({ messages, appended: appended('a2', 't2', 'a3'), nearBottom: false })).toEqual(['turn:u1'])
  })

  it('opens a new key for a new turn and for each question', () => {
    const messages = [record('u1', 'user'), record('a1', 'assistant'), record('u2', 'user'), record('b1', 'assistant'), ask('q1'), record('b2', 'assistant')]
    expect(collectUnreadKeys({ messages, appended: appended('u2', 'b1', 'q1', 'b2'), nearBottom: false })).toEqual(['turn:u2', 'q1'])
  })

  it('keeps queued messages inside the running turn', () => {
    const messages = [record('u1', 'user'), record('a1', 'assistant'), record('q1', 'user', { queued: true }), record('a2', 'assistant')]
    expect(collectUnreadKeys({ messages, appended: appended('q1', 'a2'), nearBottom: false })).toEqual(['turn:u1'])
  })

  it('counts nothing without appended records or while following the tail', () => {
    const messages = [record('u1', 'user'), record('a1', 'assistant'), record('a2', 'assistant')]
    expect(collectUnreadKeys({ messages, appended: appended(), nearBottom: false })).toEqual([])
    expect(collectUnreadKeys({ messages, appended: appended('a2'), nearBottom: true })).toEqual([])
  })
})

describe('ArrivalTracker', () => {
  const first = [record('u1', 'user'), record('a1', 'assistant')]

  it('treats the first page as history and later tail records as arrivals', () => {
    const tracker = new ArrivalTracker()
    tracker.observe('s1', 'loading', [], 1)
    tracker.observe('s1', 'ready', first, 2)
    expect(tracker.arrivedAt('a1')).toBeUndefined()
    const next = [...first, record('a2', 'assistant'), record('t2', 'tool')]
    tracker.observe('s1', 'ready', next, 3)
    expect(tracker.arrivedAt('a2')).toBe(3)
    expect(tracker.arrivedAt('t2')).toBeUndefined()
    // 追加到尾部的记录（不分角色）供新消息计数使用。
    expect([...tracker.appendedIn(next)]).toEqual(['a2', 't2'])
    expect(tracker.appendedIn(first).size).toBe(0)
    tracker.observe('s1', 'ready', next, 4)
    expect([...tracker.appendedIn(next)]).toEqual(['a2', 't2'])
    // 重复观察同一份输入不改变到达时刻。
    tracker.observe('s1', 'ready', [...first, record('a2', 'assistant')], 9)
    expect(tracker.arrivedAt('a2')).toBe(3)
  })

  it('never fades earlier pages, rebuilt sessions or another session', () => {
    const tracker = new ArrivalTracker()
    tracker.observe('s1', 'ready', first, 1)
    const earlier = [record('a0', 'assistant'), ...first]
    tracker.observe('s1', 'ready', earlier, 2)
    expect(tracker.arrivedAt('a0')).toBeUndefined()
    expect(tracker.appendedIn(earlier).size).toBe(0)
    tracker.observe('s1', 'ready', [record('x1', 'assistant')], 3)
    expect(tracker.arrivedAt('x1')).toBeUndefined()
    tracker.observe('s2', 'ready', [record('y1', 'assistant')], 4)
    expect(tracker.arrivedAt('y1')).toBeUndefined()
  })

  it('fades the first replies of a session that started empty', () => {
    const tracker = new ArrivalTracker()
    tracker.observe('s1', 'empty', [], 1)
    tracker.observe('s1', 'ready', first, 2)
    expect(tracker.arrivedAt('a1')).toBe(2)
  })
})

describe('auto-follow intent', () => {
  it('only treats vertical-dominant upward wheels as leaving the tail', () => {
    expect(isUpwardWheelIntent({ deltaX: 0, deltaY: -1 })).toBe(true)
    expect(isUpwardWheelIntent({ deltaX: 40, deltaY: -3 })).toBe(false)
    expect(isUpwardWheelIntent({ deltaX: 0, deltaY: 10 })).toBe(false)
  })

  it('re-pins on a downward wheel only when already at the bottom', () => {
    expect(shouldRepinOnWheel({ deltaX: 0, deltaY: 30, distanceFromBottom: 4 })).toBe(true)
    expect(shouldRepinOnWheel({ deltaX: 0, deltaY: 30, distanceFromBottom: 40 })).toBe(false)
    expect(shouldRepinOnWheel({ deltaX: 50, deltaY: 30, distanceFromBottom: 0 })).toBe(false)
  })

  it('ignores wheels inside a nested scroller that can still scroll up', () => {
    const root = document.createElement('div')
    const inner = document.createElement('pre')
    const leaf = document.createElement('span')
    inner.style.overflowY = 'auto'
    inner.append(leaf)
    root.append(inner)
    document.body.append(root)
    Object.defineProperties(inner, { scrollHeight: { value: 500 }, clientHeight: { value: 100 }, scrollTop: { value: 20, writable: true } })
    expect(insideScrollableAbove(leaf, root)).toBe(true)
    inner.scrollTop = 0
    expect(insideScrollableAbove(leaf, root)).toBe(false)
    expect(insideScrollableAbove(root, root)).toBe(false)
    root.remove()
  })
})

describe('word fade', () => {
  it('staggers 16ms per word, compresses to 4ms after 96ms and caps at 300ms', () => {
    expect([0, 1, 6, 7, 8].map(wordFadeDelay)).toEqual([0, 16, 96, 100, 104])
    expect(wordFadeDelay(500)).toBe(WORD_FADE_MAX_DELAY_MS)
  })

  it('keeps whitespace attached to the previous word', () => {
    expect(splitWords('hello world  again').join('')).toBe('hello world  again')
    expect(splitWords(' lead').length).toBeGreaterThanOrEqual(1)
    expect(splitWords('中文分词也可以').join('')).toBe('中文分词也可以')
  })

  type Node = { type: string; tagName?: string; value?: string; properties?: Record<string, unknown>; children?: Node[] }
  const tree = (): { type: 'root'; children: Node[] } => ({
    type: 'root',
    children: [
      { type: 'element', tagName: 'p', children: [{ type: 'text', value: 'one two ' }, { type: 'element', tagName: 'code', children: [{ type: 'text', value: 'x()' }] }] },
      { type: 'element', tagName: 'pre', children: [{ type: 'element', tagName: 'code', children: [{ type: 'text', value: 'block code' }] }] },
    ],
  })
  const spans = (node: Node): Node[] => [...(node.tagName === 'span' ? [node] : []), ...(node.children ?? []).flatMap(spans)]

  it('wraps words and inline code but never code blocks', () => {
    const root = tree()
    rehypeWordFade({ elapsedMs: 0 })(root)
    const wrapped = spans(root as Node)
    expect(wrapped.map((span) => span.properties?.style)).toEqual(['--wf-delay:0ms', '--wf-delay:16ms', '--wf-delay:32ms'])
    expect(wrapped[2].children?.[0].tagName).toBe('code')
    expect(JSON.stringify(root.children[1])).not.toContain('chat-word')
  })

  it('shares one span among words queued at the delay cap', () => {
    const words = Array.from({ length: 200 }, (_, index) => `w${index}`).join(' ')
    const root: { type: 'root'; children: Node[] } = { type: 'root', children: [{ type: 'element', tagName: 'p', children: [{ type: 'text', value: words }] }] }
    rehypeWordFade({ elapsedMs: 0 })(root)
    const wrapped = spans(root as Node)
    expect(wrapped.length).toBeLessThan(70)
    expect(wrapped.at(-1)?.properties?.style).toBe(`--wf-delay:${WORD_FADE_MAX_DELAY_MS}ms`)
    const text = (node: Node): string => node.value ?? (node.children ?? []).map(text).join('')
    expect(text(root as Node)).toBe(words)
  })

  it('resumes with negative delays after a remount and skips words already shown', () => {
    const root = tree()
    rehypeWordFade({ elapsedMs: 160 })(root)
    expect(spans(root as Node).map((span) => span.properties?.style)).toEqual(['--wf-delay:-144ms', '--wf-delay:-128ms'])
    const done = tree()
    rehypeWordFade({ elapsedMs: 10_000 })(done)
    expect(spans(done as Node)).toHaveLength(0)
  })
})
