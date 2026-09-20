import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { WorkbenchClient } from './workbench'

class Socket {
  static OPEN = 1
  static instances: Socket[] = []
  readyState = 1
  onopen?: () => void
  onmessage?: (event: { data: string }) => void
  send = vi.fn()
  close = vi.fn()
  constructor() { Socket.instances.push(this) }
  receive(message: object) { this.onmessage?.({ data: JSON.stringify(message) }) }
  messages(type?: string) { return this.send.mock.calls.map(([data]) => typeof data === 'string' ? JSON.parse(data) : null).filter((m) => m && (!type || m.t === type)) }
}
let client: WorkbenchClient
let socket: Socket
beforeEach(() => {
  vi.useFakeTimers()
  Socket.instances = []
  vi.stubGlobal('WebSocket', Socket)
  vi.spyOn(document, 'visibilityState', 'get').mockReturnValue('visible')
  client = new WorkbenchClient('host')
  client.connect()
  socket = Socket.instances[0]
  socket.onopen?.()
  socket.receive({ t: 'server_info', features: { terminal_control: 1, terminal_resize_v2: 1 } })
})
afterEach(() => { client.dispose(); vi.useRealTimers(); vi.restoreAllMocks(); vi.unstubAllGlobals() })
async function open(streamID = 7, epoch = 'epoch-a') {
  const pending = client.openTerminal('p1', 295, 40)
  const message = socket.messages('terminal.open').at(-1)
  socket.receive({ t: 'terminal.opened', id: message.id, pane_id: 'p1', terminal_id: 't1', stream_id: streamID, stream_epoch: epoch })
  expect(await pending).toBe(streamID)
}
function acquired(generation = 'generation-a', epoch = 'epoch-a', streamID = 7) {
  const request = socket.messages('terminal.control.acquire').at(-1)
  socket.receive({ t: 'terminal.control.acquired', id: request.id, stream_id: streamID, stream_epoch: epoch, control_generation: generation, expires_in_ms: 20000 })
}
async function control() {
  const pending = client.acquireControl(7, 80, 24)
  acquired()
  expect(await pending).toBe(true)
}

describe('terminal size control contract', () => {
  it('always opens an observer and only explicit acquisition enables sequenced resize', async () => {
    await open()
    expect(socket.messages('terminal.open')[0]).toMatchObject({ mode: 'observe', cols: 295, rows: 40 })
    expect(socket.messages('terminal.open')[0]).not.toHaveProperty('resize_remote')
    client.resize(7, 100, 30)
    expect(socket.messages('terminal.resize_v2')).toHaveLength(0)
    client.sendInput(7, 'hello')
    expect(socket.send.mock.calls.some(([data]) => data instanceof Uint8Array && data[2] === 3)).toBe(true)
    await control()
    client.resize(7, 100, 30)
    client.resize(7, 110, 31)
    expect(socket.messages('terminal.resize_v2')).toEqual([
      { t: 'terminal.resize_v2', stream_id: 7, stream_epoch: 'epoch-a', control_generation: 'generation-a', resize_seq: 1, cols: 100, rows: 30 },
      { t: 'terminal.resize_v2', stream_id: 7, stream_epoch: 'epoch-a', control_generation: 'generation-a', resize_seq: 2, cols: 110, rows: 31 },
    ])
    expect(socket.send.mock.calls.some(([data]) => data instanceof Uint8Array && data[2] === 4)).toBe(false)
  })

  it('renews in the visible page, releases when hidden and never reacquires on return', async () => {
    await open(); await control()
    await vi.advanceTimersByTimeAsync(5000)
    expect(socket.messages('terminal.control.renew')).toHaveLength(1)
    vi.spyOn(document, 'visibilityState', 'get').mockReturnValue('hidden')
    document.dispatchEvent(new Event('visibilitychange'))
    expect(socket.messages('terminal.control.release')).toHaveLength(1)
    await vi.advanceTimersByTimeAsync(20000)
    expect(socket.messages('terminal.control.renew')).toHaveLength(1)
    vi.spyOn(document, 'visibilityState', 'get').mockReturnValue('visible')
    document.dispatchEvent(new Event('visibilitychange'))
    await vi.advanceTimersByTimeAsync(5000)
    client.resize(7, 30, 10)
    expect(socket.messages('terminal.control.acquire')).toHaveLength(1)
    expect(socket.messages('terminal.resize_v2')).toHaveLength(0)
  })

  it('releases a late acquisition when the page hid while waiting for Herdr', async () => {
    await open()
    const state = vi.fn(); client.onTerminalControl('p1', state)
    const pending = client.acquireControl(7, 80, 24)
    vi.spyOn(document, 'visibilityState', 'get').mockReturnValue('hidden')
    document.dispatchEvent(new Event('visibilitychange'))
    acquired()
    expect(await pending).toBe(false)
    expect(socket.messages('terminal.control.release').at(-1)).toMatchObject({ stream_epoch: 'epoch-a', control_generation: 'generation-a' })
    expect(state.mock.calls.some(([value]) => value.state === 'owned')).toBe(false)
  })

  it('cancels a pending controller as soon as its generation is known and the page hides', async () => {
    await open()
    const pending = client.acquireControl(7, 80, 24)
    socket.receive({ t: 'terminal.control.state', pane_id: 'p1', stream_id: 7, stream_epoch: 'epoch-a', control_generation: 'generation-a', state: 'pending' })
    vi.spyOn(document, 'visibilityState', 'get').mockReturnValue('hidden')
    document.dispatchEvent(new Event('visibilitychange'))
    expect(socket.messages('terminal.control.release')).toHaveLength(1)
    expect(socket.messages('terminal.control.release')[0]).toMatchObject({ stream_epoch: 'epoch-a', control_generation: 'generation-a' })
    acquired()
    expect(await pending).toBe(false)
  })

  it('contains lease errors to their stream and ignores errors from an old generation', async () => {
    await open(); await control()
    const hostState = vi.fn(); client.onState(hostState)
    const controlState = vi.fn(); client.onTerminalControl('p1', controlState)
    socket.receive({ t: 'error', stream_id: 7, stream_epoch: 'epoch-a', control_generation: 'old-generation', code: 'control_expired', message: 'stale' })
    expect(controlState).toHaveBeenCalledTimes(1)
    socket.receive({ t: 'error', stream_id: 7, stream_epoch: 'epoch-a', control_generation: 'generation-a', code: 'control_expired', message: '尺寸控制已过期，请重新选择' })
    expect(controlState).toHaveBeenLastCalledWith(expect.objectContaining({ state: 'blocked', reason: '尺寸控制已过期，请重新选择' }))
    expect(hostState).not.toHaveBeenCalled()
    client.resize(7, 50, 20)
    expect(socket.messages('terminal.resize_v2')).toHaveLength(0)
    client.sendInput(7, '输入继续')
    expect(socket.send.mock.calls.at(-1)?.[0][2]).toBe(3)
  })

  it('never lets a late old stream state or status replace a new stream lease', async () => {
    await open(); await control()
    client.closeTerminal(7)
    await open(7, 'epoch-b')
    const pending = client.acquireControl(7, 90, 25)
    acquired('generation-b', 'epoch-b')
    await pending
    const state = vi.fn(); client.onTerminalControl('p1', state)
    const status = vi.fn(); client.onTerminalResize(status)
    client.resize(7, 90, 26)
    socket.receive({ t: 'terminal.control.state', pane_id: 'p1', stream_id: 7, stream_epoch: 'epoch-a', state: 'available' })
    socket.receive({ t: 'terminal.resize.status', stream_id: 7, stream_epoch: 'epoch-a', control_generation: 'generation-a', resize_seq: 1, status: 'observed', cols: 90, rows: 26 })
    expect(state).toHaveBeenCalledTimes(1)
    expect(state).toHaveBeenLastCalledWith(expect.objectContaining({ state: 'owned', stream_epoch: 'epoch-b' }))
    expect(status).not.toHaveBeenCalled()
    client.resize(7, 91, 26)
    socket.receive({ t: 'terminal.resize.status', stream_id: 7, stream_epoch: 'epoch-b', control_generation: 'generation-b', resize_seq: 1, status: 'observed', cols: 90, rows: 26 })
    expect(status).not.toHaveBeenCalled()
    socket.receive({ t: 'terminal.resize.status', stream_id: 7, stream_epoch: 'epoch-b', control_generation: 'generation-b', resize_seq: 2, status: 'observed', cols: 91, rows: 26 })
    expect(status).toHaveBeenCalledTimes(1)
  })

  it('stops old-owner resize after a handoff while ordinary input remains usable', async () => {
    await open(); await control()
    socket.receive({ t: 'terminal.control.state', pane_id: 'p1', stream_id: 7, stream_epoch: 'epoch-a', state: 'other' })
    client.resize(7, 50, 20)
    client.sendInput(7, 'still allowed')
    expect(socket.messages('terminal.resize_v2')).toHaveLength(0)
    expect(socket.send.mock.calls.at(-1)?.[0][2]).toBe(3)
    const pending = client.acquireControl(7, 50, 20, true)
    expect(socket.messages('terminal.control.acquire').at(-1)).toMatchObject({ transfer: true })
    acquired('generation-b')
    await pending
    client.resize(7, 51, 20)
    expect(socket.messages('terminal.resize_v2').at(-1)).toMatchObject({ control_generation: 'generation-b' })
  })

  it('keeps observation usable against old servers without advertising control', async () => {
    socket.receive({ t: 'server_info', features: { terminal_ack: 1 } })
    await open()
    expect(client.supportsTerminalControl()).toBe(false)
    await expect(client.acquireControl(7, 80, 24)).rejects.toThrow('更新工作台')
    expect(socket.messages('terminal.control.acquire')).toHaveLength(0)
  })

  it('distinguishes a known-site conflict from an unknown external controller', async () => {
    await open()
    const state = vi.fn(); client.onTerminalControl('p1', state)
    let pending = client.acquireControl(7, 80, 24)
    socket.receive({ t: 'error', id: socket.messages('terminal.control.acquire').at(-1).id, code: 'control_conflict', message: '另一个窗口正在控制尺寸' })
    await expect(pending).rejects.toThrow('另一个窗口')
    expect(state).toHaveBeenLastCalledWith(expect.objectContaining({ state: 'other' }))
    pending = client.acquireControl(7, 80, 24, true)
    socket.receive({ t: 'error', id: socket.messages('terminal.control.acquire').at(-1).id, code: 'control_unavailable', message: '请先在对应客户端释放' })
    await expect(pending).rejects.toThrow('对应客户端')
    expect(state).toHaveBeenLastCalledWith(expect.objectContaining({ state: 'blocked' }))
  })
})
