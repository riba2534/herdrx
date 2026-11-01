import { describe, expect, it } from 'vitest'
import type { ChatRecord } from '../structuredChatTypes'
import { buildChatTurns, HISTORY_GAP_SPLIT_MS, type TurnBlock, type WorkBlock } from './workRuns'

const at = (seconds: number) => new Date(Date.UTC(2026, 9, 1, 8, 0, seconds)).toISOString()
const user = (id: string, text: string, seconds?: number, queued = false): ChatRecord => ({ id, role: 'user', ...(seconds === undefined ? {} : { at: at(seconds) }), blocks: [{ type: 'text', text }], ...(queued ? { queued } : {}) })
const say = (id: string, text: string, seconds?: number): ChatRecord => ({ id, role: 'assistant', ...(seconds === undefined ? {} : { at: at(seconds) }), blocks: [{ type: 'text', text }] })
const call = (id: string, callID: string, name: string, input: unknown, seconds?: number): ChatRecord => ({ id, role: 'assistant', ...(seconds === undefined ? {} : { at: at(seconds) }), blocks: [{ type: 'tool-call', call_id: callID, name, input }] })
const result = (id: string, callID: string, output: string, seconds?: number, isError = false): ChatRecord => ({ id, role: 'tool', ...(seconds === undefined ? {} : { at: at(seconds) }), blocks: [{ type: 'tool-result', call_id: callID, output, is_error: isError }] })
const think = (id: string, text: string, durationMs?: number, seconds?: number): ChatRecord => ({ id, role: 'assistant', ...(seconds === undefined ? {} : { at: at(seconds) }), blocks: [{ type: 'thinking', text, ...(durationMs === undefined ? {} : { duration_ms: durationMs }) }] })

function shape(blocks: readonly TurnBlock[]) {
  return blocks.map((block) => {
    if (block.kind === 'work') return `${block.live ? 'live' : 'work'}(${block.entries.map((entry) => entry.kind === 'tool' ? entry.name || 'result' : entry.kind).join(',')})`
    if (block.kind === 'answer') return `answer(${block.entries.map((entry) => entry.text).join('|')})`
    if (block.kind === 'interaction') return `ask(${block.entry.name})`
    return block.kind
  })
}

const finished: ChatRecord[] = [
  user('u1', '补测试', 0),
  think('a0', '先想想', 1500, 1),
  say('a1', '先看看', 2),
  call('a2', 'c1', 'Bash', { command: 'ls' }, 3),
  result('t2', 'c1', 'a.go', 4),
  call('a3', 'c2', 'Read', { file_path: '/srv/a.go' }, 5),
  result('t3', 'c2', 'package a', 6),
  say('a4', '已补齐', 18),
]

describe('buildChatTurns', () => {
  it('folds a finished turn into one work group followed by the final answer', () => {
    const [turn] = buildChatTurns(finished, null)
    expect(turn.user?.id).toBe('u1')
    expect(shape(turn.blocks)).toEqual(['work(thinking,text,Bash,Read)', 'answer(已补齐)'])
    const work = turn.blocks[0] as WorkBlock
    // 时长从提问到最终回复。
    expect(work.durationMs).toBe(18_000)
    // 工具结果按 call_id 回填到调用上。
    expect(work.entries.flatMap((entry) => entry.kind === 'tool' ? [entry.result?.output] : [])).toEqual(['a.go', 'package a'])
  })

  it('keeps every answer visible when actions follow the last text (interrupted turn)', () => {
    const records = [...finished.slice(0, 4), { id: 's1', role: 'system' as const, blocks: [{ type: 'text' as const, text: '本轮已被中断' }] }]
    const [turn] = buildChatTurns(records, null)
    expect(shape(turn.blocks)).toEqual(['work(thinking)', 'answer(先看看)', 'work(Bash)', 'system'])
  })

  it('keeps only the newest actions live in a working turn', () => {
    const [turn] = buildChatTurns(finished.slice(0, 6), 'working')
    expect(turn.live).toBe(true)
    expect(shape(turn.blocks)).toEqual(['work(thinking)', 'answer(先看看)', 'live(Bash,Read)'])
    const live = turn.blocks[2] as WorkBlock
    expect(live.durationMs).toBeUndefined()
    // 计时从上一个可见边界（最新正文）开始。
    expect(live.startAt).toBe(Date.parse(at(2)))
  })

  it('adds a timer-only live placeholder when the working turn has nothing new yet', () => {
    const [turn] = buildChatTurns(finished, 'working')
    expect(shape(turn.blocks)).toEqual(['work(thinking,Bash,Read)', 'answer(先看看)'.replace('先看看', '已补齐'), 'live()'].map((value, index) => index === 0 ? 'work(thinking,text,Bash,Read)' : value))
    expect(buildChatTurns([user('u1', '开始', 0)], 'working')[0].blocks).toEqual([expect.objectContaining({ kind: 'work', live: true, entries: [], startAt: Date.parse(at(0)) })])
    expect(buildChatTurns([], 'working')).toHaveLength(1)
  })

  it('marks the live part as waiting while the agent is blocked', () => {
    const [turn] = buildChatTurns(finished.slice(0, 6), 'blocked')
    expect(turn.blocks.at(-1)).toMatchObject({ kind: 'work', live: true, waiting: true })
    expect(buildChatTurns(finished.slice(0, 6), 'working')[0].blocks.at(-1)).not.toHaveProperty('waiting')
  })

  it('only makes the last turn live', () => {
    const turns = buildChatTurns([...finished, user('u2', '再来', 30), say('b1', '好', 31)], 'working')
    expect(turns.map((turn) => turn.live)).toEqual([false, true])
    expect(shape(turns[0].blocks)).toEqual(['work(thinking,text,Bash,Read)', 'answer(已补齐)'])
  })

  it('keeps questions and plans as standalone cards between work groups', () => {
    const records = [
      user('u1', '问我', 0),
      call('a1', 'c1', 'Read', { file_path: 'a' }, 1),
      result('t1', 'c1', 'x', 1),
      call('a2', 'q1', 'AskUserQuestion', { questions: [{ question: '用哪个？', options: [{ label: 'a' }] }] }, 2),
      result('t2', 'q1', '"用哪个？"="a"', 9),
      call('a3', 'c3', 'Edit', { file_path: 'a', old_string: 'x', new_string: 'y' }, 10),
      result('t3', 'c3', 'ok', 11),
      say('a4', '好了', 12),
    ]
    const [turn] = buildChatTurns(records, null)
    expect(shape(turn.blocks)).toEqual(['work(Read)', 'ask(AskUserQuestion)', 'work(Edit)', 'answer(好了)'])
    // 卡片之后的组从卡片拿到答案的时刻开始计时。
    expect((turn.blocks[2] as WorkBlock).durationMs).toBe(3_000)
  })

  it('keeps queued messages and system notices outside the folded work', () => {
    const records = [user('u1', '补测试', 0), say('a1', '先看看', 1), call('a2', 'c1', 'Bash', { command: 'ls' }, 2), user('q1', '顺便看 README', 5, true), say('a3', '已补齐', 9)]
    const turns = buildChatTurns(records, null)
    expect(turns).toHaveLength(1)
    expect(shape(turns[0].blocks)).toEqual(['work(text,Bash)', 'queued', 'answer(已补齐)'])
    expect((turns[0].blocks[0] as WorkBlock).durationMs).toBe(5_000)
  })

  it('splits a turn across a long history gap instead of reporting one huge duration', () => {
    const late = 2 + HISTORY_GAP_SPLIT_MS / 1000 + 60
    const records = [user('u1', '开始', 0), call('a1', 'c1', 'Bash', { command: 'ls' }, 1), result('t1', 'c1', 'ok', 2), call('a2', 'c2', 'Bash', { command: 'pwd' }, late), result('t2', 'c2', '/', late + 1), say('a3', '结束', late + 2)]
    const [turn] = buildChatTurns(records, null)
    expect(shape(turn.blocks)).toEqual(['work(Bash)', 'work(Bash)', 'answer(结束)'])
    expect((turn.blocks[0] as WorkBlock).durationMs).toBe(2_000)
    // 空洞另一侧没有提问做起点，只按自己的动作计时。
    expect((turn.blocks[1] as WorkBlock).durationMs).toBe(2_000)
  })

  it('shows results whose call is not loaded yet as their own rows', () => {
    const [turn] = buildChatTurns([result('t1', 'gone', 'orphan output', 0), say('a1', '完成', 1)], null)
    expect(shape(turn.blocks)).toEqual(['work(result)', 'answer(完成)'])
  })

  it('attaches server elision notes to the block they belong to', () => {
    const records: ChatRecord[] = [
      user('u1', '读日志', 0),
      { id: 'a1', role: 'assistant', blocks: [{ type: 'tool-call', call_id: 'c1', name: 'Bash', input: { command: 'cat log' } }] },
      { id: 't1', role: 'tool', blocks: [{ type: 'tool-result', call_id: 'c1', output: 'x'.repeat(10), is_error: false }, { type: 'text', text: '…（已截断 4096 字节）' }] },
      { id: 'a2', role: 'assistant', blocks: [{ type: 'text', text: '很长' }, { type: 'text', text: '…（已截断 100 字节）' }] },
    ]
    const [turn] = buildChatTurns(records, null)
    expect(shape(turn.blocks)).toEqual(['work(Bash)', 'answer(很长)'])
    const work = turn.blocks[0] as WorkBlock
    expect(work.entries[0].kind === 'tool' && work.entries[0].result?.elided).toBe('…（已截断 4096 字节）')
    expect(turn.blocks[1].kind === 'answer' && turn.blocks[1].entries[0].elided).toBe('…（已截断 100 字节）')
  })

  it('drops empty thinking placeholders but keeps timed or written thinking', () => {
    const [turn] = buildChatTurns([user('u1', '想', 0), think('a1', '', undefined), think('a2', '', 800), think('a3', '有内容'), say('a4', '好')], null)
    expect(shape(turn.blocks)).toEqual(['work(thinking,thinking)', 'answer(好)'])
  })
})
