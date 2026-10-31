import { useEffect, useRef, useState } from 'react'
import type { Agent, Snapshot, Tab } from '../types'

// The official app draws how long a tab's agent has been working
// (herdr.dev home page; herdr-app CHANGELOG 0.1.10, "the time" on a tab).
// Herdr's snapshot has a state_change_seq but no timestamp, so this clock
// starts when this browser tab first sees the current round.

const STORAGE_PREFIX = 'herdrx.activity.'
export const ACTIVITY_VISIBLE_AFTER_MS = 5000

type Marks = Record<string, number>

export function activityKey(paneID: string, status: string, seq: number) {
  return `${paneID}\u0000${status}\u0000${seq}`
}

export function formatActivityAge(ms: number) {
  const seconds = Math.max(0, Math.floor(ms / 1000))
  if (seconds < 60) return `${seconds}秒`
  const minutes = Math.floor(seconds / 60)
  if (minutes < 60) return `${minutes}分`
  const hours = Math.floor(minutes / 60)
  const remainder = minutes % 60
  if (hours < 48) return remainder ? `${hours}小时${remainder}分` : `${hours}小时`
  return `${Math.floor(hours / 24)}天`
}

export function activityDetail(status: string, age: string | null) {
  if (!age) return ''
  return status === 'blocked' ? `等待确认已 ${age}` : `这一轮已工作 ${age}`
}

export function readActivityMarks(hostID: string): Marks {
  try {
    const parsed = JSON.parse(sessionStorage.getItem(STORAGE_PREFIX + hostID) || '{}') as unknown
    if (!parsed || typeof parsed !== 'object' || Array.isArray(parsed)) return {}
    const marks: Marks = {}
    for (const [key, value] of Object.entries(parsed)) {
      if (typeof value === 'number' && Number.isFinite(value)) marks[key] = value
    }
    return marks
  } catch {
    return {}
  }
}

export function writeActivityMarks(hostID: string, marks: Marks) {
  sessionStorage.setItem(STORAGE_PREFIX + hostID, JSON.stringify(marks))
}

function liveKeys(snapshot: Snapshot) {
  const keys = new Set<string>()
  for (const agent of snapshot.agents) {
    if (agent.agent_status !== 'working' && agent.agent_status !== 'blocked') continue
    keys.add(activityKey(agent.pane_id, agent.agent_status, agent.state_change_seq || 0))
  }
  for (const tab of snapshot.tabs) {
    if (tab.agent_status !== 'working' && tab.agent_status !== 'blocked') continue
    const covered = snapshot.agents.some((agent) => agent.tab_id === tab.tab_id && agent.agent_status === tab.agent_status)
    if (!covered) keys.add(activityKey(`tab:${tab.tab_id}`, tab.agent_status, 0))
  }
  return keys
}

export function syncActivityMarks(snapshot: Snapshot, marks: Marks, now: number) {
  const live = liveKeys(snapshot)
  const next: Marks = {}
  let changed = Object.keys(marks).length !== live.size
  for (const key of live) {
    if (typeof marks[key] === 'number') next[key] = marks[key]
    else {
      next[key] = now
      changed = true
    }
  }
  if (!changed) {
    for (const key of Object.keys(marks)) {
      if (!(key in next)) changed = true
    }
  }
  return { marks: changed ? next : marks, changed }
}

export function tabActivityText(tab: Pick<Tab, 'tab_id' | 'agent_status'>, agents: Agent[], marks: Marks, now: number) {
  if (tab.agent_status !== 'working' && tab.agent_status !== 'blocked') return null
  const matching = agents.filter((agent) => agent.tab_id === tab.tab_id && agent.agent_status === tab.agent_status)
  const keys = matching.length
    ? matching.map((agent) => activityKey(agent.pane_id, agent.agent_status, agent.state_change_seq || 0))
    : [activityKey(`tab:${tab.tab_id}`, tab.agent_status, 0)]
  let since = Number.POSITIVE_INFINITY
  for (const key of keys) {
    const value = marks[key]
    if (typeof value === 'number' && value < since) since = value
  }
  if (!Number.isFinite(since)) return null
  const age = now - since
  if (age < ACTIVITY_VISIBLE_AFTER_MS) return null
  return formatActivityAge(age)
}

export function snapshotHasActivity(snapshot: Snapshot) {
  return liveKeys(snapshot).size > 0
}

export function useAgentActivity(hostID: string, snapshot: Snapshot | null) {
  const [now, setNow] = useState(() => Date.now())
  const hostRef = useRef(hostID)
  const marks = useRef(readActivityMarks(hostID))
  const [, setTick] = useState(0)
  if (hostRef.current !== hostID) {
    hostRef.current = hostID
    marks.current = readActivityMarks(hostID)
  }
  const watching = Boolean(snapshot && snapshotHasActivity(snapshot))
  useEffect(() => {
    if (!watching) return
    const timer = window.setInterval(() => setNow(Date.now()), 1000)
    return () => window.clearInterval(timer)
  }, [watching])
  useEffect(() => {
    if (!snapshot) return
    const synced = syncActivityMarks(snapshot, marks.current, Date.now())
    if (!synced.changed) return
    marks.current = synced.marks
    writeActivityMarks(hostID, synced.marks)
    setTick((value) => value + 1)
  }, [snapshot, hostID])
  return (tab: Pick<Tab, 'tab_id' | 'agent_status'>) => tabActivityText(tab, snapshot?.agents || [], marks.current, now)
}
