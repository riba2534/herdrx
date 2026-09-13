import { Image as ImageIcon, LoaderCircle, RotateCcw, X } from 'lucide-react'
import type { ChatMediaAttachment } from '../../lib/chatMediaTypes'
import { chatMediaStagedCount, formatChatMediaPath } from '../../lib/chatMediaAttachments'
import './ChatMediaComposer.css'

/**
 * 附件占位区（tray）。渲染在输入框上方，**只展示状态**：
 * 缩略图、文件名、上传中/已就绪/失败、可移除、失败可重试。
 *
 * 它没有任何发送路径，也不会向终端写入任何东西——
 * 已就绪的远端路径只在用户点发送时由 `composeChatMediaSubmission` 合成一次。
 */
export function ImageAttachments({ attachments, onRemove, onRetry, retryable, overflow }: {
  attachments: ChatMediaAttachment[]
  onRemove: (id: string) => void
  onRetry: (id: string) => void
  /** 失败占位是否可重试（由状态机判定：必须有原始文件）。 */
  retryable: (id: string) => boolean
  /** 批次级上限提示（数量/总量）；命中时不会有占位。 */
  overflow?: string
}) {
  if (attachments.length === 0 && !overflow) return null
  const staged = chatMediaStagedCount(attachments)
  return <div className="chat-media-tray" data-count={attachments.length}>
    {attachments.length > 0 && <ul className="chat-media-list" aria-label={`图片附件，共 ${attachments.length} 张`}>
      {attachments.map((attachment) => {
        const state = attachment.status === 'staging' ? '上传中…'
          : attachment.status === 'staged' ? '已就绪'
          : '未上传'
        return <li key={attachment.id} className={`chat-media-item chat-media-item-${attachment.status}`} data-status={attachment.status}>
          {attachment.previewURL
            ? <img className="chat-media-thumb" src={attachment.previewURL} alt="" />
            : <span className="chat-media-thumb chat-media-thumb-fallback" aria-hidden="true"><ImageIcon size={18} /></span>}
          <span className="chat-media-info">
            <span className="chat-media-name" title={attachment.name}>{attachment.name}</span>
            <span className="chat-media-state">
              <span className="chat-media-state-label" role="status" aria-live="polite">
                {attachment.status === 'staging' && <LoaderCircle className="spin" size={12} aria-hidden="true" />}
                {state}
              </span>
              {attachment.status === 'staged' && attachment.remotePath &&
                <code className="chat-media-path" title={attachment.remotePath}>{formatChatMediaPath(attachment.remotePath)}</code>}
              {attachment.status === 'failed' &&
                <span className="chat-media-error" role="alert">{attachment.error || '上传失败，请重试'}</span>}
            </span>
          </span>
          <span className="chat-media-actions">
            {attachment.status === 'failed' && retryable(attachment.id) &&
              <button type="button" className="chat-media-action" onClick={() => onRetry(attachment.id)} aria-label={`重试 ${attachment.name}`}>
                <RotateCcw size={16} aria-hidden="true" />
              </button>}
            <button type="button" className="chat-media-action" onClick={() => onRemove(attachment.id)} aria-label={`移除图片 ${attachment.name}`}>
              <X size={16} aria-hidden="true" />
            </button>
          </span>
        </li>
      })}
    </ul>}
    {overflow && <p className="chat-media-notice" role="alert">{overflow}</p>}
    {staged > 0 && <p className="chat-media-hint">
      图片以路径引用随消息一起发送。目标若是普通 Shell，裸路径会被当成命令执行；请确认当前 pane 的程序支持图片路径。
    </p>}
  </div>
}
