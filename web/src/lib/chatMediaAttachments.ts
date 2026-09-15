/**
 * Chat 图片附件的状态机（占位 → stage-only 上传 → 提交 → 结算）。
 *
 * 语义与接线点见 docs/design/chat-media-contract.md §6–§7；
 * 类型与常量的唯一正文是 ./chatMediaTypes.ts。
 *
 * 本文件不含 React、不含 DOM 事件；它只依赖可注入的 `stageImage` 与
 * ./api 的会话身份（与 composerDrafts 同一套 session/host/pane 隔离）。
 *
 * 硬边界：
 * - 上传固定 `inject=false`（stage-only）；拖拽/粘贴/选择**绝不**向终端打字。
 * - 提交只发生在 `compose()`，且只把已 `staged` 的远端路径交给冻结的
 *   `composeChatMediaSubmission`；上传中不计入，失败/未知不清理。
 * - 目标绑定**加入时**的 host/pane。移除、切 pane、登出之后到达的上传结果
 *   一律丢弃（epoch + bucket 身份双重校验），绝不落到别的 pane。
 */

import { currentSessionID, onAuthEvent } from './api'
import {
  CHAT_MEDIA_MAX_ATTACHMENTS,
  canStageChatMedia,
  chatMediaSubmissionSendable,
  composeChatMediaSubmission,
  type ChatMediaAttachment,
} from './chatMediaTypes'
import type { ComposerSendStatus } from './composerDrafts'

/**
 * 附件总量上限，对齐服务端 `internal/httpapi/paste.go` 整体 multipart 25MB。
 * 单图 20MB / 数量 10 的上限来自冻结的 chatMediaTypes.ts。
 */
export const CHAT_MEDIA_MAX_TOTAL_BYTES = 25 * 1024 * 1024

/** stage-only 上传。生产实现固定为 `api.pasteImage(hostID, paneID, file, false)`。 */
export type ChatMediaStageImage = (hostID: string, paneID: string, file: File) => Promise<{ path: string }>

/** 前端预检失败的文件：仍然显示一个 `failed` 占位，但**不发请求**。 */
export type ChatMediaAddRejection = { name: string; reason: string }

export type ChatMediaAddResult = {
  /** 已建立 `staging` 占位并开始上传的文件数。 */
  accepted: number
  /** 逐文件预检失败（类型/大小），已落 `failed` 占位。 */
  rejected: ChatMediaAddRejection[]
  /** 批次级上限（数量/总量）命中的中文提示；命中时**不**新增占位。 */
  overflow?: string
}

export type ChatMediaAddPlan = {
  accept: File[]
  failed: ChatMediaAddRejection[]
  overflow?: string
}

type MediaEntry = {
  attachment: ChatMediaAttachment
  /** 上传代次；重试/移除/切 pane 后旧结果因 epoch 不匹配被丢弃。 */
  epoch: number
  /** 重试所需的原始文件；upload 成功后释放引用。 */
  file: File | null
}

type Submission = { ids: string[]; paths: string[] }

type Bucket = {
  entries: MediaEntry[]
  /** 传给 React 的稳定快照；任何变更后置空并按需重建。 */
  snapshot: ChatMediaAttachment[] | null
  submission: Submission | null
}

const EMPTY: ChatMediaAttachment[] = []

function errorMessage(error: unknown) {
  const raw = error instanceof Error ? error.message : String(error || '')
  return raw.trim() === '' ? '上传失败，请重试' : raw
}

/**
 * 准入判定（纯函数）。已存在附件计入数量与总量上限；
 * `failed` 占位不占总量预算（它没有上传意图），但仍占数量。
 */
export function planChatMediaAdd(existing: ChatMediaAttachment[], files: File[]): ChatMediaAddPlan {
  const plan: ChatMediaAddPlan = { accept: [], failed: [] }
  let count = existing.length
  let bytes = existing.reduce((sum, item) => sum + (item.status === 'failed' ? 0 : item.size), 0)
  for (const file of files) {
    const allowed = canStageChatMedia(file)
    if (!allowed.ok) {
      plan.failed.push({ name: file.name, reason: allowed.reason })
      continue
    }
    if (count >= CHAT_MEDIA_MAX_ATTACHMENTS) {
      plan.overflow ??= `最多只能同时添加 ${CHAT_MEDIA_MAX_ATTACHMENTS} 张图片`
      continue
    }
    if (bytes + file.size > CHAT_MEDIA_MAX_TOTAL_BYTES) {
      plan.overflow ??= '图片总大小超过 25MB，请先移除部分图片'
      continue
    }
    count += 1
    bytes += file.size
    plan.accept.push(file)
  }
  return plan
}

/**
 * 附件的展示用路径引用（**只用于 UI**）。
 * 提交文本按冻结契约保持「不加引号」；含空白/引号/`$`/反引号/反斜杠时，
 * 这里补上 shell 语义正确的单引号转义，避免用户误读或误复制。
 */
export function formatChatMediaPath(path: string): string {
  const clean = path.trim()
  if (clean === '') return ''
  return /[\s'"\\$`]/.test(clean) ? `'${clean.replaceAll("'", `'\\''`)}'` : clean
}

/** 已 `staged`（有远端引用）的附件数量。 */
export function chatMediaStagedCount(attachments: ChatMediaAttachment[]) {
  return attachments.filter((item) => item.status === 'staged' && item.remotePath).length
}

/** 拖拽事件是否携带文件（用于决定要不要 preventDefault 接管这次拖放）。 */
export function chatMediaDropActive(types: readonly string[] | undefined) {
  return Array.from(types || []).includes('Files')
}

/**
 * 从拖放 payload 提取文件：沿用 imagePaste 的「优先 items、回退 files、
 * 不同时读两份」规则，但**不在这里按图片类型过滤**。
 *
 * 与 `clipboardImages` 的差别是有意的：非图片文件不静默消失，而是落到状态机
 * 的预检里，变成带中文原因的 `failed` 占位（契约 §7.7「占位直接置 failed 并给出
 * 中文原因」）。终端粘贴路径仍由 Composer 自己用 `clipboardImages` 过滤，避免劫持文本粘贴。
 */
export function chatMediaFiles(data: DataTransfer | null): File[] {
  if (!data) return []
  const items = Array.from(data.items || [])
    .filter((item) => item.kind === 'file')
    .map((item) => item.getAsFile())
    .filter((file): file is File => file !== null)
  return items.length ? items : Array.from(data.files || [])
}

/**
 * 附件状态机工厂。每个 ChatMediaComposer 实例持有一个；
 * `stageImage` 通过稳定的转发函数读取最新 prop，避免实例随渲染重建。
 */
export function createChatMediaStore(options: {
  stageImage: ChatMediaStageImage
  /** 可注入的预览 URL 工厂；默认 `URL.createObjectURL`，不可用时返回 undefined。 */
  createPreviewURL?: (file: File) => string | undefined
}) {
  const buckets = new Map<string, Bucket>()
  const listeners = new Set<() => void>()
  let sequence = 0
  let listening = false
  let releaseAuth: (() => void) | null = null
  let counter = 0

  const createPreviewURL = options.createPreviewURL ?? ((file: File) => {
    try {
      if (typeof URL === 'undefined' || typeof URL.createObjectURL !== 'function') return undefined
      return URL.createObjectURL(file)
    } catch {
      return undefined
    }
  })

  const releasePreview = (url?: string) => {
    if (!url) return
    try {
      if (typeof URL !== 'undefined' && typeof URL.revokeObjectURL === 'function') URL.revokeObjectURL(url)
    } catch {
      // jsdom 与隐私模式下可能没有 revokeObjectURL；预览 URL 仅本地存在，忽略即可。
    }
  }

  const newID = () => {
    try {
      if (typeof crypto !== 'undefined' && typeof crypto.randomUUID === 'function') return crypto.randomUUID()
    } catch {
      // 非安全上下文没有 randomUUID，退回进程内自增；id 不离开浏览器。
    }
    counter += 1
    return `chat-media-${counter}`
  }

  const notify = () => { for (const listener of listeners) listener() }

  /** session/host/pane 三元组；缺任一项时返回空串，所有操作变成 no-op。 */
  const bucketKey = (hostID: string, paneID: string) => {
    const sessionID = currentSessionID()
    if (!sessionID || !hostID || !paneID) return ''
    return `${sessionID}\n${hostID}\n${paneID}`
  }

  function bucketOf(hostID: string, paneID: string, create: boolean): Bucket | null {
    const key = bucketKey(hostID, paneID)
    if (!key) return null
    let bucket = buckets.get(key)
    if (!bucket && create) {
      bucket = { entries: [], snapshot: null, submission: null }
      buckets.set(key, bucket)
    }
    return bucket ?? null
  }

  function touch(bucket: Bucket) {
    bucket.snapshot = null
    notify()
  }

  function releaseBucket(bucket: Bucket) {
    for (const entry of bucket.entries) releasePreview(entry.attachment.previewURL)
    bucket.entries = []
    bucket.snapshot = null
    bucket.submission = null
  }

  /** 迟到上传的守卫：bucket 仍是当前 key 的那个，且 entry 的 epoch 未被换过。 */
  function current(hostID: string, paneID: string, bucket: Bucket, id: string, epoch: number) {
    if (buckets.get(bucketKey(hostID, paneID)) !== bucket) return false
    return bucket.entries.some((entry) => entry.attachment.id === id && entry.epoch === epoch)
  }

  function replace(bucket: Bucket, id: string, patch: (attachment: ChatMediaAttachment) => ChatMediaAttachment) {
    for (const entry of bucket.entries) {
      if (entry.attachment.id === id) entry.attachment = patch(entry.attachment)
    }
    touch(bucket)
  }

  async function upload(hostID: string, paneID: string, bucket: Bucket, id: string, epoch: number, file: File) {
    let path = ''
    try {
      const result = await options.stageImage(hostID, paneID, file)
      path = typeof result?.path === 'string' ? result.path.trim() : ''
      if (path === '') throw new Error('上传结果缺少远端路径')
    } catch (error) {
      // 迟到结果只丢弃，不写回、不重试、不换 pane。
      if (!current(hostID, paneID, bucket, id, epoch)) return
      replace(bucket, id, (attachment) => ({ ...attachment, status: 'failed', error: errorMessage(error), remotePath: undefined }))
      return
    }
    if (!current(hostID, paneID, bucket, id, epoch)) return
    for (const entry of bucket.entries) if (entry.attachment.id === id) entry.file = null
    replace(bucket, id, (attachment) => ({ ...attachment, status: 'staged', remotePath: path, error: undefined }))
  }

  function beginUpload(hostID: string, paneID: string, bucket: Bucket, entry: MediaEntry) {
    const epoch = entry.epoch
    const file = entry.file
    if (!file) return
    void upload(hostID, paneID, bucket, entry.attachment.id, epoch, file)
  }

  function find(hostID: string, paneID: string, id: string) {
    const bucket = bucketOf(hostID, paneID, false)
    if (!bucket) return null
    const entry = bucket.entries.find((item) => item.attachment.id === id)
    return entry ? { bucket, entry } : null
  }

  function listOf(hostID: string, paneID: string): ChatMediaAttachment[] {
    const bucket = bucketOf(hostID, paneID, false)
    return bucket ? bucket.entries.map((entry) => entry.attachment) : EMPTY
  }

  function busyOf(hostID: string, paneID: string) {
    const bucket = bucketOf(hostID, paneID, false)
    if (!bucket) return false
    return bucket.entries.some((entry) => entry.attachment.status === 'staging')
  }

  function onAuth(event: { kind: 'expired' } | { kind: 'authenticated'; sessionID: string }) {
    if (event.kind === 'expired') { reset(); return }
    const keep = `${event.sessionID}\n`
    for (const [key, bucket] of Array.from(buckets)) {
      if (key.startsWith(keep)) continue
      releaseBucket(bucket)
      buckets.delete(key)
    }
    notify()
  }

  function ensureAuthListener() {
    if (listening) return
    listening = true
    releaseAuth = onAuthEvent(onAuth)
  }

  /**
   * 最后一个订阅者离开时释放全局 auth 监听：这是每个 ChatMediaComposer 实例一份的
   * 工厂，监听器不能跟着实例一起泄漏。
   */
  function dropAuthListener() {
    if (!listening || listeners.size > 0) return
    listening = false
    releaseAuth?.()
    releaseAuth = null
  }

  function reset() {
    for (const bucket of buckets.values()) releaseBucket(bucket)
    buckets.clear()
    notify()
  }

  return {
    subscribe(listener: () => void) {
      ensureAuthListener()
      listeners.add(listener)
      return () => {
        listeners.delete(listener)
        dropAuthListener()
      }
    },

    /** 稳定快照：只有内容真的变了才换引用，可直接喂给 useSyncExternalStore。 */
    list(hostID: string, paneID: string): ChatMediaAttachment[] {
      ensureAuthListener()
      const bucket = bucketOf(hostID, paneID, false)
      if (!bucket) return EMPTY
      if (!bucket.snapshot) bucket.snapshot = bucket.entries.map((entry) => entry.attachment)
      return bucket.snapshot
    },

    /**
     * 拖拽 / 粘贴 / 选择入口。只做「占位 + stage-only 上传」，
     * 不写草稿、不调用任何发送路径。返回本批次结果供 UI 提示。
     */
    add(hostID: string, paneID: string, files: File[]): ChatMediaAddResult {
      ensureAuthListener()
      const result: ChatMediaAddResult = { accepted: 0, rejected: [] }
      const bucket = bucketOf(hostID, paneID, true)
      if (!bucket || files.length === 0) return result
      const plan = planChatMediaAdd(bucket.entries.map((entry) => entry.attachment), files)
      for (const rejection of plan.failed) {
        result.rejected.push(rejection)
        bucket.entries.push({
          attachment: { id: newID(), kind: 'image', name: rejection.name, mime: '', size: 0, status: 'failed', error: rejection.reason },
          epoch: 0,
          file: null,
        })
      }
      result.overflow = plan.overflow
      const started: MediaEntry[] = []
      for (const file of plan.accept) {
        sequence += 1
        const entry: MediaEntry = {
          attachment: {
            id: newID(),
            kind: 'image',
            name: file.name,
            mime: file.type,
            size: file.size,
            status: 'staging',
            previewURL: createPreviewURL(file),
          },
          epoch: sequence,
          file,
        }
        bucket.entries.push(entry)
        started.push(entry)
        result.accepted += 1
      }
      touch(bucket)
      for (const entry of started) beginUpload(hostID, paneID, bucket, entry)
      return result
    },

    /** 移除占位并回收它的预览 object URL。迟到上传由 epoch/bucket 校验丢弃。 */
    remove(hostID: string, paneID: string, id: string) {
      const found = find(hostID, paneID, id)
      if (!found) return
      const { bucket, entry } = found
      releasePreview(entry.attachment.previewURL)
      bucket.entries = bucket.entries.filter((item) => item.attachment.id !== id)
      if (bucket.submission) bucket.submission = { ...bucket.submission, ids: bucket.submission.ids.filter((item) => item !== id) }
      touch(bucket)
    },

    /**
     * 重试失败的附件。`staged` 与 `staging` 都不重传（不重传已 stage 的图），
     * 无原始文件（例如前端预检就失败的占位）也不重传。
     */
    retry(hostID: string, paneID: string, id: string): boolean {
      const found = find(hostID, paneID, id)
      if (!found) return false
      const { bucket, entry } = found
      if (!entry.file || entry.attachment.status !== 'failed') return false
      sequence += 1
      entry.epoch = sequence
      entry.attachment = { ...entry.attachment, status: 'staging', error: undefined }
      touch(bucket)
      beginUpload(hostID, paneID, bucket, entry)
      return true
    },

    /** 该占位是否可以重试（失败且有原始文件）。UI 据此决定是否显示重试按钮。 */
    retryable(hostID: string, paneID: string, id: string): boolean {
      const found = find(hostID, paneID, id)
      return Boolean(found && found.entry.file && found.entry.attachment.status === 'failed')
    },

    /** 已 staged 的远端路径，按附件顺序。上传中/失败的不在其中。 */
    stagedPaths(hostID: string, paneID: string): string[] {
      const bucket = bucketOf(hostID, paneID, false)
      if (!bucket) return []
      return bucket.entries
        .filter((entry) => entry.attachment.status === 'staged' && entry.attachment.remotePath)
        .map((entry) => entry.attachment.remotePath as string)
    },

    /** 仍有上传在飞；此时发送按钮必须 disabled。 */
    busy(hostID: string, paneID: string): boolean {
      return busyOf(hostID, paneID)
    },

    /**
     * 「仅图片也能发」的判定：有 staged 附件时，空草稿也算可发送。
     * 由 Composer 的接线点使用（见 handoff 的最小新 props）。
     */
    canSend(hostID: string, paneID: string, draft = ''): boolean {
      if (busyOf(hostID, paneID)) return false
      return chatMediaSubmissionSendable(draft, listOf(hostID, paneID))
    },

    /**
     * 把草稿合成为**一次**提交文本，并记下本次提交快照。
     * - 没有 staged 附件时**原样返回草稿**（不裁剪、不加分隔符），保证与今天逐字节一致。
     * - 正文与引用都为空时返回 null，调用方放弃本次发送。
     * - 幂等：重复调用得到同一字符串（冻结的 composeChatMediaSubmission 会剥掉重复引用行）。
     */
    compose(hostID: string, paneID: string, draft: string): string | null {
      const bucket = bucketOf(hostID, paneID, false)
      const ids: string[] = []
      const paths: string[] = []
      for (const entry of bucket?.entries ?? []) {
        if (entry.attachment.status !== 'staged' || !entry.attachment.remotePath) continue
        ids.push(entry.attachment.id)
        paths.push(entry.attachment.remotePath)
      }
      if (paths.length === 0) {
        if (bucket) bucket.submission = null
        return draft.trim() === '' ? null : draft
      }
      const composed = composeChatMediaSubmission(draft, paths)
      if (composed.trim() === '') {
        if (bucket) bucket.submission = null
        return null
      }
      if (bucket) bucket.submission = { ids, paths }
      return composed
    },

    /**
     * 一次发送事务结束后的结算。
     * - `delivered`：只清掉本次提交快照里的附件（发送期间新加的保留），引用不再重复。
     * - `failed` / `unknown`：保留附件与草稿，绝不自动重放。
     */
    settle(hostID: string, paneID: string, status: ComposerSendStatus) {
      const bucket = bucketOf(hostID, paneID, false)
      if (!bucket) return
      const submission = bucket.submission
      bucket.submission = null
      if (!submission || status !== 'delivered') return
      const done = new Set(submission.ids)
      for (const entry of bucket.entries) {
        if (done.has(entry.attachment.id)) releasePreview(entry.attachment.previewURL)
      }
      bucket.entries = bucket.entries.filter((entry) => !done.has(entry.attachment.id))
      touch(bucket)
    },

    /** 释放某个 host/pane 的全部占位与 object URL（切 pane、卸载时调用）。 */
    discard(hostID: string, paneID: string) {
      const key = bucketKey(hostID, paneID)
      if (!key) return
      const bucket = buckets.get(key)
      if (!bucket) return
      releaseBucket(bucket)
      buckets.delete(key)
      notify()
    },

    /** 会话结束/登出：释放全部。 */
    reset,
  }
}

export type ChatMediaStore = ReturnType<typeof createChatMediaStore>
