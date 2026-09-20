// @vitest-environment node
import { readFileSync } from 'node:fs'
import { createRequire } from 'node:module'
import { dirname, join } from 'node:path'
import { runInNewContext } from 'node:vm'
import { describe, expect, it } from 'vitest'

// Resolve the actual transitive dependency used by React DOM, including pnpm's
// installed patch. Neither a copied scheduler nor the unstable mock is tested.
const require = createRequire(import.meta.url)
const reactRequire = createRequire(require.resolve('react-dom'))
const schedulerRoot = dirname(reactRequire.resolve('scheduler/package.json'))
const RECOVERY_MS = 250

type Callback = (didTimeout: boolean) => Callback | void
type Task = { callback: Callback | null }
type Scheduler = {
  unstable_NormalPriority: number
  unstable_UserBlockingPriority: number
  unstable_LowPriority: number
  unstable_ImmediatePriority: number
  unstable_scheduleCallback: (priority: number, callback: Callback, options?: { delay: number }) => Task
  unstable_cancelCallback: (task: Task) => void
  unstable_getCurrentPriorityLevel: () => number
}
type Timer = { id: number; due: number; callback: () => void }
type MessageHandler = (event: { data: unknown }) => void
type Port = { onmessage: MessageHandler | null; closed: boolean; close: () => void; postMessage: (data: unknown) => void }
type Message = { data: unknown; receiver: Port; queuedHandler: MessageHandler | null }
type HarnessOptions = { messageChannel?: boolean; setImmediate?: boolean; timerMinimum?: number }

function loadScheduler(mode: 'production' | 'development', options: HarnessOptions = {}) {
  let now = 0
  let nextTimer = 1
  let posts = 0
  const timers = new Map<number, Timer>()
  const messages: Message[] = []
  const immediates: Array<() => void> = []
  const channels: Array<{ port1: Port; port2: Port }> = []
  const setTimeout = (callback: () => void, delay = 0) => {
    const id = nextTimer++
    timers.set(id, { id, due: now + Math.max(delay, options.timerMinimum ?? 0), callback })
    return id
  }
  const next = () => [...timers.values()].sort((a, b) => a.due - b.due || a.id - b.id)[0]
  const runNextTimer = () => {
    const timer = next()
    if (!timer) throw new Error('No pending timer')
    timers.delete(timer.id)
    now = Math.max(now, timer.due)
    timer.callback()
  }
  const advance = (milliseconds: number) => {
    const target = now + milliseconds
    let count = 0
    while (next() && next().due <= target) {
      if (++count > 1000) throw new Error('Timer loop did not become idle')
      runNextTimer()
    }
    now = target
  }
  class MessageChannel {
    port1: Port
    port2: Port
    constructor() {
      const port = (): Port => ({
        onmessage: null,
        closed: false,
        close() { this.closed = true; this.onmessage = null },
        postMessage() { throw new Error('Port is not entangled') },
      })
      this.port1 = port()
      this.port2 = port()
      const connect = (sender: Port, receiver: Port) => {
        sender.postMessage = data => {
          posts++
          // Match WebKit's detached port: postMessage succeeds silently.
          if (sender.closed || receiver.closed) return
          messages.push({ data, receiver, queuedHandler: receiver.onmessage })
        }
      }
      connect(this.port1, this.port2)
      connect(this.port2, this.port1)
      channels.push(this)
    }
  }
  const takeMessage = () => {
    const message = messages.shift()
    if (!message) throw new Error('No pending message')
    return message
  }
  const deliver = (message: Message, stale = false) => {
    // stale=true also simulates a callback already captured by the browser:
    // clearing onmessage must not be the only protection against late delivery.
    const handler = stale ? message.queuedHandler : message.receiver.closed ? null : message.receiver.onmessage
    handler?.({ data: message.data })
  }
  const scheduler = {} as Scheduler
  const file = join(schedulerRoot, 'cjs', `scheduler.${mode}.js`)
  runInNewContext(readFileSync(file, 'utf8'), {
    exports: scheduler,
    module: { exports: scheduler },
    process: { env: { NODE_ENV: mode } },
    performance: { now: () => now },
    console,
    setTimeout,
    clearTimeout: (id: number) => { timers.delete(id) },
    ...(options.messageChannel === false ? {} : { MessageChannel }),
    ...(options.setImmediate ? { setImmediate: (callback: () => void) => { immediates.push(callback) } } : {}),
  }, { filename: file })
  return {
    scheduler, timers, messages, channels, immediates,
    advance, runNextTimer, takeMessage, deliver,
    deliverNext: () => deliver(takeMessage()),
    elapse: (milliseconds: number) => { now += milliseconds },
    now: () => now,
    posts: () => posts,
    invalidatePorts: () => { for (const channel of channels) { channel.port1.close(); channel.port2.close() } },
  }
}

describe.each(['production', 'development'] as const)('actual Scheduler %s MessageChannel recovery', mode => {
  it('keeps healthy messages asynchronous, cancels their watchdogs, and becomes idle', () => {
    const h = loadScheduler(mode)
    let calls = 0
    for (let index = 0; index < 20; index++) {
      h.scheduler.unstable_scheduleCallback(h.scheduler.unstable_NormalPriority, () => { calls++ })
      expect(calls).toBe(index)
      expect(h.timers.size).toBe(1)
      h.deliverNext()
      expect(calls).toBe(index + 1)
      expect(h.timers.size).toBe(0)
    }
    h.advance(RECOVERY_MS * 2)
    expect(calls).toBe(20)
    expect(h.posts()).toBe(20)
    expect(h.channels[0].port1.closed).toBe(false)
  })

  it('recovers a silently invalidated port without expiring normal-priority work early', () => {
    const h = loadScheduler(mode)
    const calls: boolean[] = []
    h.invalidatePorts()
    h.scheduler.unstable_scheduleCallback(h.scheduler.unstable_NormalPriority, expired => { calls.push(expired) })
    expect(h.messages).toHaveLength(0)
    h.advance(RECOVERY_MS - 1)
    expect(calls).toEqual([])
    h.advance(1)
    expect(calls).toEqual([false])
    expect(h.timers.size).toBe(0)
    h.scheduler.unstable_scheduleCallback(h.scheduler.unstable_NormalPriority, expired => { calls.push(expired) })
    expect(calls).toEqual([false])
    h.advance(0)
    expect(calls).toEqual([false, false])
    expect(h.posts()).toBe(1)
    expect(h.timers.size).toBe(0)
  })

  it('ignores late or duplicate old messages instead of consuming a future timer or continuation', () => {
    const h = loadScheduler(mode)
    const calls: string[] = []
    h.scheduler.unstable_scheduleCallback(h.scheduler.unstable_NormalPriority, () => {
      calls.push('initial')
      return () => { calls.push('continuation') }
    })
    const oldMessage = h.takeMessage()
    h.runNextTimer()
    expect(calls).toEqual(['initial'])
    expect(h.channels[0].port1.closed).toBe(true)
    expect(h.channels[0].port2.closed).toBe(true)
    h.deliver(oldMessage, true)
    h.deliver(oldMessage, true)
    expect(calls).toEqual(['initial'])
    h.runNextTimer()
    expect(calls).toEqual(['initial', 'continuation'])
    h.scheduler.unstable_scheduleCallback(h.scheduler.unstable_NormalPriority, () => { calls.push('future') })
    h.deliver(oldMessage, true)
    expect(calls).toEqual(['initial', 'continuation'])
    h.runNextTimer()
    expect(calls).toEqual(['initial', 'continuation', 'future'])
    expect(h.timers.size).toBe(0)
  })

  it('does not let an earlier healthy message acknowledge a later pending post', () => {
    const h = loadScheduler(mode)
    const calls: string[] = []
    h.scheduler.unstable_scheduleCallback(h.scheduler.unstable_NormalPriority, () => {
      calls.push('initial')
      return () => { calls.push('continuation') }
    })
    const first = h.takeMessage()
    h.deliver(first)
    expect(calls).toEqual(['initial'])
    expect(h.messages).toHaveLength(1)
    h.deliver(first, true)
    expect(calls).toEqual(['initial'])
    expect(h.timers.size).toBe(1)
    h.advance(RECOVERY_MS)
    expect(calls).toEqual(['initial', 'continuation'])
    expect(h.timers.size).toBe(0)
  })

  it('preserves priority, cancellation, current priority, and delayed task deadlines', () => {
    const h = loadScheduler(mode)
    const s = h.scheduler
    const calls: Array<{ name: string; priority: number; expired: boolean }> = []
    const record = (name: string): Callback => expired => { calls.push({ name, priority: s.unstable_getCurrentPriorityLevel(), expired }) }
    s.unstable_scheduleCallback(s.unstable_LowPriority, record('low'))
    s.unstable_scheduleCallback(s.unstable_NormalPriority, record('normal'))
    const cancelled = s.unstable_scheduleCallback(s.unstable_ImmediatePriority, record('cancelled'))
    s.unstable_cancelCallback(cancelled)
    s.unstable_scheduleCallback(s.unstable_ImmediatePriority, record('immediate'))
    s.unstable_scheduleCallback(s.unstable_UserBlockingPriority, record('delayed'), { delay: 500 })
    h.invalidatePorts()
    h.advance(RECOVERY_MS)
    expect(calls).toEqual([
      { name: 'immediate', priority: s.unstable_ImmediatePriority, expired: true },
      { name: 'normal', priority: s.unstable_NormalPriority, expired: false },
      { name: 'low', priority: s.unstable_LowPriority, expired: false },
    ])
    h.advance(249)
    expect(calls).toHaveLength(3)
    h.advance(1)
    expect(calls[3]).toEqual({ name: 'delayed', priority: s.unstable_UserBlockingPriority, expired: false })
    expect(h.timers.size).toBe(0)
  })

  it('cancels the only queued task without running it or leaving a repeating watchdog', () => {
    const h = loadScheduler(mode)
    let calls = 0
    const task = h.scheduler.unstable_scheduleCallback(h.scheduler.unstable_NormalPriority, () => { calls++ })
    h.scheduler.unstable_cancelCallback(task)
    h.invalidatePorts()
    h.advance(RECOVERY_MS * 4)
    expect(calls).toBe(0)
    expect(h.timers.size).toBe(0)
    expect(h.posts()).toBe(1)
  })

  it('continues the queue after a callback throws during watchdog recovery', () => {
    const h = loadScheduler(mode)
    const calls: string[] = []
    h.scheduler.unstable_scheduleCallback(h.scheduler.unstable_NormalPriority, () => {
      calls.push('throw')
      throw new Error('scheduler callback failed')
    })
    h.scheduler.unstable_scheduleCallback(h.scheduler.unstable_NormalPriority, () => { calls.push('next') })
    h.invalidatePorts()
    expect(h.runNextTimer).toThrow('scheduler callback failed')
    expect(calls).toEqual(['throw'])
    h.runNextTimer()
    expect(calls).toEqual(['throw', 'next'])
    expect(h.timers.size).toBe(0)
  })

  it.each(['message', 'watchdog'] as const)('claims work once when a long task delays both sources and %s runs first', first => {
    const h = loadScheduler(mode)
    const calls: boolean[] = []
    h.scheduler.unstable_scheduleCallback(h.scheduler.unstable_NormalPriority, expired => { calls.push(expired) })
    const message = h.takeMessage()
    h.elapse(10_000)
    if (first === 'message') h.deliver(message)
    else { h.runNextTimer(); h.deliver(message, true) }
    h.advance(0)
    expect(calls).toEqual([true])
    expect(h.timers.size).toBe(0)
    expect(h.channels[0].port1.closed).toBe(first === 'watchdog')
  })

  it('continues making progress when fallback timers are clamped', () => {
    const h = loadScheduler(mode, { timerMinimum: 4 })
    const times: number[] = []
    const work: Callback = () => {
      times.push(h.now())
      if (times.length < 4) return work
    }
    h.invalidatePorts()
    h.scheduler.unstable_scheduleCallback(h.scheduler.unstable_NormalPriority, work)
    h.advance(RECOVERY_MS + 12)
    expect(times).toEqual([250, 254, 258, 262])
    expect(h.timers.size).toBe(0)
    expect(h.posts()).toBe(1)
  })

  it('keeps the existing setImmediate branch without opening a channel or watchdog', () => {
    const h = loadScheduler(mode, { setImmediate: true })
    let calls = 0
    h.scheduler.unstable_scheduleCallback(h.scheduler.unstable_NormalPriority, () => { calls++ })
    expect(calls).toBe(0)
    expect(h.channels).toHaveLength(0)
    expect(h.timers.size).toBe(0)
    expect(h.immediates).toHaveLength(1)
    h.immediates.shift()?.()
    expect(calls).toBe(1)
    expect(h.timers.size).toBe(0)
  })

  it('keeps the existing timer branch when MessageChannel is unavailable', () => {
    const h = loadScheduler(mode, { messageChannel: false })
    let calls = 0
    h.scheduler.unstable_scheduleCallback(h.scheduler.unstable_NormalPriority, () => { calls++ })
    expect(calls).toBe(0)
    expect(h.channels).toHaveLength(0)
    h.advance(0)
    expect(calls).toBe(1)
    expect(h.now()).toBe(0)
    expect(h.timers.size).toBe(0)
  })
})
