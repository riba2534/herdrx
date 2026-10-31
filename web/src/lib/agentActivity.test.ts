import { describe, expect, it } from 'vitest'
import { activityDetail, activityKey, formatActivityAge, syncActivityMarks, tabActivityText, ACTIVITY_VISIBLE_AFTER_MS } from './agentActivity'
import type { Agent, Snapshot, Tab } from '../types'

const tab = (status: Tab['agent_status']): Tab => ({ tab_id: 't1', workspace_id: 'w1', label: '1', number: 1, pane_count: 1, agent_status: status, focused: true })
const agent = (status: Agent['agent_status'], seq = 4): Agent => ({ agent: 'claude', agent_status: status, pane_id: 'p1', workspace_id: 'w1', tab_id: 't1', focused: true, state_change_seq: seq })

function snapshot(status: Agent['agent_status'], seq = 4): Snapshot {
  return {
    version: 'test', protocol: 1, focused_workspace_id: 'w1', focused_tab_id: 't1', focused_pane_id: 'p1',
    workspaces: [], tabs: [tab(status)], panes: [], layouts: [], agents: [agent(status, seq)],
  }
}

describe('tab activity clock', () => {
  it('formats the age the tab shows', () => {
    expect(formatActivityAge(12_000)).toBe('12秒')
    expect(formatActivityAge(3 * 60_000)).toBe('3分')
    expect(formatActivityAge((2 * 60 + 5) * 60_000)).toBe('2小时5分')
    expect(formatActivityAge(3 * 24 * 60 * 60_000)).toBe('3天')
    expect(activityDetail('working', '3分')).toBe('这一轮已工作 3分')
    expect(activityDetail('blocked', '3分')).toBe('等待确认已 3分')
  })

  it('hides a round this page just noticed and keeps a round it already saw', () => {
    const now = 1_000_000
    const key = activityKey('p1', 'working', 4)
    const fresh = syncActivityMarks(snapshot('working'), {}, now)
    expect(fresh.changed).toBe(true)
    expect(tabActivityText(tab('working'), [agent('working')], fresh.marks, now + 1000)).toBeNull()
    expect(tabActivityText(tab('working'), [agent('working')], { [key]: now - ACTIVITY_VISIBLE_AFTER_MS }, now)).toBe('5秒')
    expect(tabActivityText(tab('idle'), [agent('idle')], { [key]: now - 60_000 }, now)).toBeNull()
  })

  it('starts again when the round sequence changes and drops stale panes', () => {
    const now = 5_000_000
    const oldKey = activityKey('p1', 'working', 4)
    const synced = syncActivityMarks(snapshot('working', 5), { [oldKey]: now - 60_000 }, now)
    expect(synced.marks[oldKey]).toBeUndefined()
    expect(synced.marks[activityKey('p1', 'working', 5)]).toBe(now)
    expect(tabActivityText(tab('blocked'), [agent('blocked', 9)], { [activityKey('p1', 'blocked', 9)]: now - 120_000 }, now)).toBe('2分')
  })
})
