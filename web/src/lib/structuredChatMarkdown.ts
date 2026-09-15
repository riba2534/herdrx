// 助手正文的 Markdown 支撑逻辑（纯函数，供 ChatView / ChatMarkdown 使用）。
//
// 渲染本身走 `react-markdown` + `remark-gfm` 的 React 元素输出（见
// `web/src/components/chat/ChatMarkdown.tsx`）：**不开 rehype-raw**，所以日志里的原始 HTML
// 不会被当作 HTML 执行；本文件只放可以脱离 React 单测的安全判定与文本工具。
//
// 安全边界（与契约一致）：
//   - 链接只允许 http / https / mailto，`javascript:` / `data:` / 其它协议一律降级为纯文本；
//   - 图片一律不加载（会话日志里的 URL 可能指向内网或带凭据，加载即泄露）；
//   - 工具入参与输出只做有上限的只读预览，不解析结构、不执行。

import { STRUCTURED_CHAT_ELISION_PREFIX, type ChatRecord } from './structuredChatTypes'

const SAFE_SCHEMES = ['http:', 'https:', 'mailto:']

/**
 * 去掉会被浏览器忽略的不可见字符：C0/C1 控制字符、零宽与双向控制字符、行分隔符、BOM。
 * `java\tscript:` 这类伪装必须先还原成 `javascript:` 再判定协议。
 */
export function stripInvisible(text: string) {
  let out = ''
  for (const char of text) {
    const code = char.codePointAt(0) ?? 0
    const invisible = code <= 0x20
      || (code >= 0x7f && code <= 0x9f)
      || (code >= 0x200b && code <= 0x200f)
      || code === 0x2028
      || code === 0x2029
      || code === 0xfeff
    if (!invisible) out += char
  }
  return out
}

/** 只放行 http / https / mailto；其余（javascript:、data:、file:、blob:…）返回 undefined。 */
export function safeLinkHref(raw?: string | null) {
  if (!raw) return undefined
  const href = stripInvisible(raw)
  if (!href) return undefined
  const scheme = href.toLowerCase().match(/^([a-z][a-z0-9+.-]*):/)
  if (scheme) return SAFE_SCHEMES.includes(`${scheme[1]}:`) ? href : undefined
  // 无协议（相对地址）的链接没有可信基准，一律不放行。
  return undefined
}

/** 语言标记只保留安全字符，避免把日志内容拼进 className。 */
export function codeLanguage(className?: string | null) {
  if (!className) return ''
  const match = /^language-([A-Za-z0-9+#._-]{1,20})$/.exec(className.trim())
  return match ? match[1] : ''
}

/** 记录里所有 text 块的正文，按出现顺序拼接（用户气泡与整条复制都用它）。 */
export function recordText(record: Pick<ChatRecord, 'blocks'>) {
  return record.blocks
    .filter((block) => block.type === 'text')
    .map((block) => block.text)
    .join('\n\n')
}

/** 整条复制的正文：正文 + 工具调用 / 工具结果的只读摘要，不含任何隐藏字段。 */
export function recordCopyText(record: ChatRecord) {
  const parts: string[] = []
  const text = recordText(record)
  if (text) parts.push(text)
  for (const block of record.blocks) {
    if (block.type === 'tool-call') parts.push(`[工具调用] ${block.name}${block.call_id ? ` (${block.call_id})` : ''}\n${toolInputText(block.input)}`)
    if (block.type === 'tool-result') parts.push(`[工具结果${block.is_error ? '·错误' : ''}]${block.call_id ? ` (${block.call_id})` : ''}\n${block.output}`)
  }
  return parts.join('\n\n')
}

/** 工具入参预览：JSON 优先，无法序列化时退回字符串，并强制长度上限。 */
export function toolInputText(input: unknown, limit = 4000) {
  let text: string
  if (typeof input === 'string') text = input
  else {
    try {
      text = JSON.stringify(input, null, 2) ?? String(input)
    } catch {
      text = String(input)
    }
  }
  return truncateText(text, limit)
}

/** 工具输出预览：与入参同样的上限，避免把整段日志塞进 DOM。 */
export function toolOutputText(output: string, limit = 8000) {
  return truncateText(output, limit)
}

export function truncateText(text: string, limit: number) {
  if (limit <= 0 || text.length <= limit) return text
  return `${text.slice(0, limit)}\n…（已省略 ${text.length - limit} 个字符）`
}

/** 识别服务端截断说明块（`STRUCTURED_CHAT_ELISION_PREFIX`），用于弱化提示。 */
export function elisionNotice(text: string) {
  const index = text.indexOf(STRUCTURED_CHAT_ELISION_PREFIX)
  if (index < 0) return null
  const line = text.slice(index).split('\n', 1)[0]
  return line.length > 160 ? `${line.slice(0, 160)}…` : line
}

/** 写剪贴板：不可用（无权限 / 非安全上下文 / 无 API）时返回 false，由调用方给出反馈。 */
export async function copyChatText(text: string) {
  if (!text) return false
  try {
    const clipboard = typeof navigator === 'undefined' ? undefined : navigator.clipboard
    if (!clipboard || typeof clipboard.writeText !== 'function') return false
    await clipboard.writeText(text)
    return true
  } catch {
    return false
  }
}

export const CHAT_COPY_OK = '已复制'
export const CHAT_COPY_UNAVAILABLE = '当前环境无法写入剪贴板，请手动选择文本复制'
