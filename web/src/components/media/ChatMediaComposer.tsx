import { useCallback, useEffect, useRef, useState, useSyncExternalStore, type ChangeEvent, type DragEvent } from 'react'
import { ImagePlus } from 'lucide-react'
import { Composer } from '../Composer'
import { ImageAttachments } from './ImageAttachments'
import { chatMediaDropActive, chatMediaFiles, createChatMediaStore, type ChatMediaStore } from '../../lib/chatMediaAttachments'
import type { ChatMediaComposerProps, ComposerMediaHost } from '../../lib/chatMediaTypes'
import './ChatMediaComposer.css'

/** 与服务端 `internal/httpapi/paste.go` 的魔数白名单一致。 */
const IMAGE_ACCEPT = 'image/png,image/jpeg,image/webp,image/gif'

/**
 * 带图片附件的 Chat 输入框：**包裹**现有 Composer，不复制它的草稿/发送状态机。
 *
 * 本组件只做四件事：
 * 1. 拖拽 / 粘贴 / 选择图片 → 占位（`staging`）→ stage-only 上传（`inject=false`）；
 *    任何一步都**不会**向终端打字，只有用户点发送才会把已 staged 的路径合成一次提交。
 * 2. 渲染工具栏（含语音槽位）与附件占位区，并把占位区经 `mediaHost.tray` 交给 Composer
 *    渲染在输入框上方。
 * 3. 通过 `mediaHost`（canSend / compose / onSettled / onFiles）接入 Composer 的
 *    提交事务：**仅图片也能发**，合成只有一条路径（Composer 发送前调 `compose`）。
 * 4. 发送结束后按最终状态结算附件：delivered 只清本次提交快照，失败/未知保留附件与草稿。
 *
 * VoiceInput 由调用方按冻结的 `VoiceInputProps` 构造后从 `voice` 槽位传入，
 * 本文件不导入也不修改 VoiceInput。
 */
export function ChatMediaComposer(props: ChatMediaComposerProps) {
  const { hostID, paneID } = props

  // 通过 ref 读取最新 prop，保证状态机实例不随渲染重建。
  const stageRef = useRef(props.stageImage)
  stageRef.current = props.stageImage
  const submitRef = useRef(props.submit)
  submitRef.current = props.submit

  const storeRef = useRef<ChatMediaStore | null>(null)
  if (storeRef.current === null) {
    storeRef.current = createChatMediaStore({ stageImage: (host, pane, file) => stageRef.current(host, pane, file) })
  }
  const store = storeRef.current

  const attachments = useSyncExternalStore(store.subscribe, () => store.list(hostID, paneID))
  const busy = attachments.some((item) => item.status === 'staging')
  const [dragging, setDragging] = useState(false)
  const [notice, setNotice] = useState('')
  const pickRef = useRef<HTMLInputElement>(null)

  /** 拖拽 / 粘贴 / 选择唯一的入口：只占位 + 上传，绝不发送。 */
  const addFiles = useCallback((files: File[]) => {
    if (files.length === 0) return
    const result = store.add(hostID, paneID, files)
    setNotice(result.overflow || '')
  }, [store, hostID, paneID])

  const remove = useCallback((id: string) => {
    setNotice('')
    store.remove(hostID, paneID, id)
  }, [store, hostID, paneID])

  const retry = useCallback((id: string) => {
    setNotice('')
    store.retry(hostID, paneID, id)
  }, [store, hostID, paneID])

  const retryable = useCallback((id: string) => store.retryable(hostID, paneID, id), [store, hostID, paneID])

  // 切 pane / 卸载时释放该 pane 的占位与 object URL。迟到上传由状态机按
  // 加入时的 host/pane + epoch 丢弃，不会落到新的 pane。
  useEffect(() => () => { store.discard(hostID, paneID) }, [store, hostID, paneID])

  /**
   * 提交事务：正文与图片引用的合成由 Composer 在发送前调用 `mediaHost.compose` 完成
   * （唯一一条合成路径，见契约 §8.3）。这里只保留一道防御性守卫：
   * 图片仍在上传时，宁可抛中文错误，也不静默丢掉用户刚加上的图片。
   */
  // 两腿提交：正文与回车分别调用，pane 固定为本次提交开始时选中的终端。
  const submit = useCallback(async (targetPane: string, text: string, keys: string[]) => {
    // 新增附件只阻止下一次正文提交，不能打断已经发送正文的提交回车。
    if (keys.length === 0 && store.busy(hostID, paneID)) throw new Error('图片仍在上传，请稍候再发送')
    await submitRef.current(targetPane, text, keys)
  }, [store, hostID, paneID])

  /**
   * 交给 Composer 的媒体接线点。`tray` 只在真的有条目时渲染，
   * 免得空占位区在 Composer 的网格里留下一行空隙。
   */
  const mediaHost: ComposerMediaHost = {
    canSend: () => store.canSend(hostID, paneID),
    compose: (draft) => store.compose(hostID, paneID, draft),
    onSettled: (status) => store.settle(hostID, paneID, status),
    onFiles: addFiles,
    tray: attachments.length > 0 || notice !== ''
      ? <ImageAttachments attachments={attachments} onRemove={remove} onRetry={retry} retryable={retryable} overflow={notice}/>
      : undefined,
  }

  const onDragOver = (event: DragEvent<HTMLDivElement>) => {
    if (!chatMediaDropActive(event.dataTransfer?.types)) return
    event.preventDefault()
    event.stopPropagation()
    if (event.dataTransfer) event.dataTransfer.dropEffect = 'copy'
    setDragging(true)
  }

  const onDragLeave = (event: DragEvent<HTMLDivElement>) => {
    if (event.currentTarget.contains(event.relatedTarget as Node | null)) return
    setDragging(false)
  }

  const onDrop = (event: DragEvent<HTMLDivElement>) => {
    if (!chatMediaDropActive(event.dataTransfer?.types)) return
    event.preventDefault()
    event.stopPropagation()
    setDragging(false)
    addFiles(chatMediaFiles(event.dataTransfer))
  }

  const onPick = (event: ChangeEvent<HTMLInputElement>) => {
    const files = Array.from(event.target.files || [])
    event.target.value = ''
    addFiles(files)
  }

  if (!props.visible) return null

  return <div
    className={`chat-media-composer${props.compact ? ' chat-media-composer-compact' : ''}`}
    data-drag={dragging ? 'true' : undefined}
    onDragOver={onDragOver}
    onDragLeave={onDragLeave}
    onDrop={onDrop}
  >
    {dragging && <div className="chat-media-dropzone" aria-hidden="true">松开以添加图片</div>}
    <div className="chat-media-toolbar">
      <button
        type="button"
        className="chat-media-pick"
        aria-label="添加图片"
        disabled={!paneID}
        onClick={() => pickRef.current?.click()}
      >
        <ImagePlus size={16} aria-hidden="true" />图片
      </button>
      {props.voice && <div className="chat-media-voice">{props.voice}</div>}
    </div>
    <input
      ref={pickRef}
      className="chat-media-file"
      type="file"
      accept={IMAGE_ACCEPT}
      multiple
      tabIndex={-1}
      onChange={onPick}
    />
    <Composer
      hostID={hostID}
      paneID={paneID}
      visible={props.visible}
      directInput={false}
      compact={props.compact}
      variant={props.variant}
      sendDisabled={props.sendDisabled || busy}
      placeholder={props.placeholder}
      onDirectInput={props.onDirectInput}
      onLocalInput={props.onLocalInput}
      submit={submit}
      mediaHost={mediaHost}
    />
  </div>
}
