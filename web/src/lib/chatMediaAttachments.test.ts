import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import * as apiModule from './api'
import { api, invalidateAuthentication } from './api'
import { CHAT_MEDIA_MAX_ATTACHMENTS } from './chatMediaTypes'
import {
  CHAT_MEDIA_MAX_TOTAL_BYTES,
  chatMediaDropActive,
  chatMediaFiles,
  createChatMediaStore,
  formatChatMediaPath,
  planChatMediaAdd,
} from './chatMediaAttachments'

const user = { id: 'user', email: 'user@example.test', role: 'user', display_name: 'User' }

async function login(sessionID = 'media-session') {
  vi.stubGlobal('fetch', vi.fn().mockResolvedValue(
    new Response(JSON.stringify({ user, csrf_token: 'csrf', session_id: sessionID })),
  ))
  await api.login({ email: 'user@example.test', password: 'test' })
}

/** 构造指定大小的 File，不真的分配内存。 */
function fileOf(name: string, size = 1024, type = 'image/png') {
  const file = new File([new Uint8Array(1)], name, { type })
  Object.defineProperty(file, 'size', { value: size })
  return file
}

function deferred<T>() {
  let resolve!: (value: T) => void
  let reject!: (error: unknown) => void
  const promise = new Promise<T>((res, rej) => { resolve = res; reject = rej })
  return { promise, resolve, reject }
}

let revoked: string[] = []
let previewSeq = 0

beforeEach(async () => {
  invalidateAuthentication()
  revoked = []
  previewSeq = 0
  Object.defineProperty(URL, 'createObjectURL', { configurable: true, value: () => `blob:preview-${++previewSeq}` })
  Object.defineProperty(URL, 'revokeObjectURL', { configurable: true, value: (url: string) => { revoked.push(url) } })
  await login()
})

afterEach(() => {
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
})

describe('auth 监听的生命周期', () => {
  it('整个 store 只挂一个 auth 监听，最后一个订阅者离开时释放它', () => {
    const realOnAuth = apiModule.onAuthEvent
    const state = { attached: 0, released: 0 }
    vi.spyOn(apiModule, 'onAuthEvent').mockImplementation((listener) => {
      state.attached += 1
      const off = realOnAuth(listener)
      return () => { state.released += 1; off() }
    })

    const store = createChatMediaStore({ stageImage: async () => ({ path: '/remote/a.png' }) })
    const offA = store.subscribe(() => {})
    const offB = store.subscribe(() => {})
    expect(state.attached).toBe(1)

    // 还有订阅者时不能释放：登出清理必须继续生效。
    offA()
    expect(state.released).toBe(0)

    // 最后一个订阅者离开后必须释放，否则每挂载一次就多泄漏一个闭包。
    offB()
    expect(state.released).toBe(1)

    // 重新订阅会重新挂上，功能不因释放而丢失。
    const offC = store.subscribe(() => {})
    expect(state.attached).toBe(2)
    offC()
    expect(state.released).toBe(2)
  })
})

describe('planChatMediaAdd', () => {
  it('全量接受合规图片，并按单图大小逐张判定', () => {
    const plan = planChatMediaAdd([], [fileOf('a.png'), fileOf('b.jpg', 2048, 'image/jpeg')])
    expect(plan.accept).toHaveLength(2)
    expect(plan.failed).toEqual([])
    expect(plan.overflow).toBeUndefined()
  })

  it('类型不合规与超过 20MB 的单图不进入上传，但给出中文原因', () => {
    const plan = planChatMediaAdd([], [
      fileOf('notes.txt', 10, 'text/plain'),
      fileOf('huge.png', 21 * 1024 * 1024),
      fileOf('empty.png', 0),
    ])
    expect(plan.accept).toEqual([])
    expect(plan.failed.map((item) => item.name)).toEqual(['notes.txt', 'huge.png', 'empty.png'])
    expect(plan.failed[0].reason).toContain('仅支持')
    expect(plan.failed[1].reason).toContain('20MB')
    expect(plan.failed[2].reason).toContain('为空')
  })

  it('数量与总量上限命中时返回提示且不产生占位', () => {
    const existing = Array.from({ length: CHAT_MEDIA_MAX_ATTACHMENTS }, (_, index) => ({
      id: `existing-${index}`, kind: 'image' as const, name: `e${index}.png`, mime: 'image/png', size: 1, status: 'staged' as const,
    }))
    const counted = planChatMediaAdd(existing, [fileOf('extra.png')])
    expect(counted.accept).toEqual([])
    expect(counted.overflow).toContain(`${CHAT_MEDIA_MAX_ATTACHMENTS}`)

    // 每张都在单图 20MB 以内，但两张相加超过 25MB 总量。
    const half = Math.floor(CHAT_MEDIA_MAX_TOTAL_BYTES / 2) + 1
    const heavy = planChatMediaAdd([], [fileOf('a.png', half), fileOf('b.png', half)])
    expect(heavy.accept.map((file) => file.name)).toEqual(['a.png'])
    expect(heavy.overflow).toContain('25MB')
  })

  it('已失败的占位不占总量预算，但仍占数量', () => {
    const failed = [{ id: 'f', kind: 'image' as const, name: 'f.png', mime: '', size: 0, status: 'failed' as const }]
    const plan = planChatMediaAdd(failed, [fileOf('ok.png')])
    expect(plan.accept).toHaveLength(1)
    expect(plan.overflow).toBeUndefined()
  })
})

describe('chatMediaFiles / chatMediaDropActive', () => {
  it('优先 items，缺失时回退 files，且不同时读两份', () => {
    const fromItems = fileOf('items.png')
    const fallback = fileOf('files.png')
    expect(chatMediaFiles({ items: [{ kind: 'file', getAsFile: () => fromItems }], files: [fallback] } as unknown as DataTransfer))
      .toEqual([fromItems])
    expect(chatMediaFiles({ items: [], files: [fallback] } as unknown as DataTransfer)).toEqual([fallback])
    expect(chatMediaFiles(null)).toEqual([])
  })

  it('不按图片类型过滤：非图片文件交给状态机给出可见失败原因', () => {
    const text = fileOf('notes.txt', 8, 'text/plain')
    expect(chatMediaFiles({ items: [], files: [text] } as unknown as DataTransfer)).toEqual([text])
  })

  it('只在拖放真的带文件时接管事件', () => {
    expect(chatMediaDropActive(['Files', 'text/plain'])).toBe(true)
    expect(chatMediaDropActive(['text/plain'])).toBe(false)
    expect(chatMediaDropActive(undefined)).toBe(false)
  })
})

describe('formatChatMediaPath', () => {
  it('不含特殊字符时原样显示，含空白或引号时加单引号转义', () => {
    expect(formatChatMediaPath('/tmp/herdrx/a.png')).toBe('/tmp/herdrx/a.png')
    expect(formatChatMediaPath('/tmp/my shots/a.png')).toBe("'/tmp/my shots/a.png'")
    expect(formatChatMediaPath("/tmp/it's.png")).toBe("'/tmp/it'\\''s.png'")
    expect(formatChatMediaPath('  ')).toBe('')
  })
})

describe('createChatMediaStore', () => {
  it('拖拽多图先落 staging 占位，上传成功后写入远端路径', async () => {
    const stageImage = vi.fn(async (_host: string, _pane: string, file: File) => ({ path: `/remote/${file.name}` }))
    const store = createChatMediaStore({ stageImage })
    const result = store.add('host', 'p1', [fileOf('a.png'), fileOf('b.png')])

    expect(result.accepted).toBe(2)
    expect(store.list('host', 'p1').map((item) => item.status)).toEqual(['staging', 'staging'])
    expect(stageImage).toHaveBeenCalledTimes(2)
    // 目标绑定加入时的 host/pane，且只接受 (host, pane, file) 三个实参。
    expect(stageImage.mock.calls[0].slice(0, 2)).toEqual(['host', 'p1'])

    await vi.waitFor(() => {
      expect(store.list('host', 'p1').map((item) => item.status)).toEqual(['staged', 'staged'])
    })
    expect(store.stagedPaths('host', 'p1')).toEqual(['/remote/a.png', '/remote/b.png'])
    expect(store.busy('host', 'p1')).toBe(false)
  })

  it('类型/大小不合规的文件落 failed 占位且不发请求', () => {
    const stageImage = vi.fn(async () => ({ path: '/remote/x.png' }))
    const store = createChatMediaStore({ stageImage })
    const result = store.add('host', 'p1', [fileOf('notes.txt', 4, 'text/plain'), fileOf('big.png', 21 * 1024 * 1024)])

    expect(stageImage).not.toHaveBeenCalled()
    expect(result.rejected).toHaveLength(2)
    const list = store.list('host', 'p1')
    expect(list.map((item) => item.status)).toEqual(['failed', 'failed'])
    expect(list[0].error).toContain('仅支持')
    // 预检失败的占位没有原始文件，不可重试。
    expect(store.retryable('host', 'p1', list[0].id)).toBe(false)
    expect(store.retry('host', 'p1', list[0].id)).toBe(false)
  })

  it('数量上限命中时只返回提示，不新增占位', () => {
    const store = createChatMediaStore({ stageImage: async () => ({ path: '/remote/x.png' }) })
    const files = Array.from({ length: CHAT_MEDIA_MAX_ATTACHMENTS + 2 }, (_, index) => fileOf(`i${index}.png`))
    const result = store.add('host', 'p1', files)
    expect(result.accepted).toBe(CHAT_MEDIA_MAX_ATTACHMENTS)
    expect(result.overflow).toContain(`${CHAT_MEDIA_MAX_ATTACHMENTS}`)
    expect(store.list('host', 'p1')).toHaveLength(CHAT_MEDIA_MAX_ATTACHMENTS)
  })

  it('上传中 busy 为真，且在此期间不能发送', async () => {
    const gate = deferred<{ path: string }>()
    const store = createChatMediaStore({ stageImage: () => gate.promise })
    store.add('host', 'p1', [fileOf('a.png')])
    expect(store.busy('host', 'p1')).toBe(true)
    expect(store.canSend('host', 'p1', '')).toBe(false)
    gate.resolve({ path: '/remote/a.png' })
    await vi.waitFor(() => expect(store.busy('host', 'p1')).toBe(false))
    expect(store.canSend('host', 'p1', '')).toBe(true) // 仅图片也能发
  })

  it('移除占位会回收预览 URL，并忽略迟到到达的上传结果', async () => {
    const gate = deferred<{ path: string }>()
    const store = createChatMediaStore({ stageImage: () => gate.promise, createPreviewURL: () => 'blob:preview-x' })
    store.add('host', 'p1', [fileOf('a.png')])
    const [attachment] = store.list('host', 'p1')
    expect(attachment.previewURL).toBe('blob:preview-x')

    store.remove('host', 'p1', attachment.id)
    expect(store.list('host', 'p1')).toEqual([])
    expect(revoked).toContain('blob:preview-x')

    gate.resolve({ path: '/remote/a.png' })
    await Promise.resolve()
    await Promise.resolve()
    expect(store.list('host', 'p1')).toEqual([]) // 迟到结果被丢弃，不复活占位
    expect(store.stagedPaths('host', 'p1')).toEqual([])
  })

  it('discard（切 pane/卸载）后迟到上传不会落到任何 pane', async () => {
    const gate = deferred<{ path: string }>()
    const store = createChatMediaStore({ stageImage: () => gate.promise })
    store.add('host', 'old', [fileOf('a.png')])
    store.discard('host', 'old')
    expect(store.list('host', 'old')).toEqual([])

    gate.resolve({ path: '/remote/a.png' })
    await Promise.resolve()
    await Promise.resolve()
    expect(store.list('host', 'old')).toEqual([])
    expect(store.list('host', 'new')).toEqual([])
  })

  it('上传失败置 failed 并保留原始文件；重试成功后 staged，且不重传已 staged 的图', async () => {
    const stageImage = vi.fn()
      .mockRejectedValueOnce(new Error('连接已断开'))
      .mockResolvedValueOnce({ path: '/remote/a.png' })
    const store = createChatMediaStore({ stageImage })
    store.add('host', 'p1', [fileOf('a.png')])
    await vi.waitFor(() => expect(store.list('host', 'p1')[0].status).toBe('failed'))
    const [attachment] = store.list('host', 'p1')
    expect(attachment.error).toBe('连接已断开')
    expect(store.retryable('host', 'p1', attachment.id)).toBe(true)

    expect(store.retry('host', 'p1', attachment.id)).toBe(true)
    expect(store.list('host', 'p1')[0].status).toBe('staging')
    await vi.waitFor(() => expect(store.list('host', 'p1')[0].status).toBe('staged'))
    expect(stageImage).toHaveBeenCalledTimes(2)

    // 已 staged 的附件不再重传。
    expect(store.retry('host', 'p1', attachment.id)).toBe(false)
    expect(stageImage).toHaveBeenCalledTimes(2)
  })

  it('上传成功但结果缺少 path 时按失败处理', async () => {
    const store = createChatMediaStore({ stageImage: async () => ({ path: '' }) })
    store.add('host', 'p1', [fileOf('a.png')])
    await vi.waitFor(() => expect(store.list('host', 'p1')[0].status).toBe('failed'))
    expect(store.list('host', 'p1')[0].error).toContain('缺少远端路径')
  })

  it('compose 没有附件时原样返回草稿（不裁剪空白、不加分隔符）', () => {
    const store = createChatMediaStore({ stageImage: async () => ({ path: '/remote/a.png' }) })
    expect(store.compose('host', 'p1', 'hello\n')).toBe('hello\n')
    expect(store.compose('host', 'p1', '')).toBeNull()
  })

  it('compose 把正文与已 staged 的引用合成为一次提交，且幂等', async () => {
    const store = createChatMediaStore({ stageImage: async (_h, _p, file: File) => ({ path: `/remote/${file.name}` }) })
    store.add('host', 'p1', [fileOf('a.png'), fileOf('b.png')])
    await vi.waitFor(() => expect(store.busy('host', 'p1')).toBe(false))

    const first = store.compose('host', 'p1', '看这两张图')
    expect(first).toBe('看这两张图\n/remote/a.png\n/remote/b.png')
    // 失败重试路径重复合成不会追加第二遍引用。
    expect(store.compose('host', 'p1', '看这两张图')).toBe(first)
    expect(store.compose('host', 'p1', `${first}`)).toBe(first)
  })

  it('仅图片：空正文时提交文本就是引用行本身', async () => {
    const store = createChatMediaStore({ stageImage: async () => ({ path: '/remote/a.png' }) })
    store.add('host', 'p1', [fileOf('a.png')])
    await vi.waitFor(() => expect(store.busy('host', 'p1')).toBe(false))
    expect(store.compose('host', 'p1', '')).toBe('/remote/a.png')
  })

  it('delivered 只清掉本次提交快照里的附件，发送期间新加的保留', async () => {
    const store = createChatMediaStore({ stageImage: async (_h, _p, file: File) => ({ path: `/remote/${file.name}` }) })
    store.add('host', 'p1', [fileOf('a.png')])
    await vi.waitFor(() => expect(store.busy('host', 'p1')).toBe(false))
    store.compose('host', 'p1', '发送 a')
    store.add('host', 'p1', [fileOf('b.png')])
    await vi.waitFor(() => expect(store.busy('host', 'p1')).toBe(false))

    store.settle('host', 'p1', 'delivered')
    expect(store.list('host', 'p1').map((item) => item.name)).toEqual(['b.png'])
  })

  it('failed / unknown 保留附件与草稿，绝不自动重放', async () => {
    const store = createChatMediaStore({ stageImage: async () => ({ path: '/remote/a.png' }) })
    store.add('host', 'p1', [fileOf('a.png')])
    await vi.waitFor(() => expect(store.busy('host', 'p1')).toBe(false))

    for (const status of ['failed', 'unknown'] as const) {
      const composed = store.compose('host', 'p1', '发送 a')
      expect(composed).toBe('发送 a\n/remote/a.png')
      store.settle('host', 'p1', status)
      expect(store.list('host', 'p1').map((item) => item.status)).toEqual(['staged'])
    }
    // 结算后再次 compose 仍然只有一行引用。
    expect(store.compose('host', 'p1', '发送 a')).toBe('发送 a\n/remote/a.png')
  })

  it('附件按 host/pane 隔离，互不串台', async () => {
    const store = createChatMediaStore({ stageImage: async () => ({ path: '/remote/a.png' }) })
    store.add('host', 'p1', [fileOf('a.png')])
    await vi.waitFor(() => expect(store.busy('host', 'p1')).toBe(false))

    expect(store.list('host', 'p1')).toHaveLength(1)
    expect(store.list('host', 'p2')).toEqual([])
    expect(store.list('other', 'p1')).toEqual([])
    expect(store.stagedPaths('host', 'p2')).toEqual([])
    expect(store.compose('host', 'p2', 'x')).toBe('x')
  })

  it('登出（认证失效）会清空全部占位并回收预览 URL', async () => {
    const store = createChatMediaStore({ stageImage: async () => ({ path: '/remote/a.png' }), createPreviewURL: () => 'blob:preview-y' })
    store.subscribe(() => {})
    store.add('host', 'p1', [fileOf('a.png')])
    await vi.waitFor(() => expect(store.busy('host', 'p1')).toBe(false))
    expect(store.list('host', 'p1')).toHaveLength(1)

    invalidateAuthentication()
    expect(store.list('host', 'p1')).toEqual([])
    expect(revoked).toContain('blob:preview-y')
  })

  it('list 在内容未变时返回同一引用，可直接用于 useSyncExternalStore', async () => {
    const store = createChatMediaStore({ stageImage: async () => ({ path: '/remote/a.png' }) })
    store.add('host', 'p1', [fileOf('a.png')])
    const first = store.list('host', 'p1')
    expect(store.list('host', 'p1')).toBe(first)
    await vi.waitFor(() => expect(store.list('host', 'p1')[0].status).toBe('staged'))
    expect(store.list('host', 'p1')).not.toBe(first)
  })
})
