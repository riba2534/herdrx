import { memo, type ReactNode } from 'react'
import Markdown, { type Options } from 'react-markdown'
import remarkGfm from 'remark-gfm'
import remarkCjkFriendly from 'remark-cjk-friendly'
import { ExternalLink, ImageOff } from 'lucide-react'
import { codeLanguage, safeLinkHref } from '../../lib/structuredChatMarkdown'
import { rehypeWordFade, WORD_FADE_WINDOW_MS } from '../../lib/chat/wordFade'
import { ChatCodeBlock } from './ChatCodeBlock'

// 助手正文渲染。三条硬约束：
//   1. 不开 rehype-raw：日志里的原始 HTML 不会被当作 HTML 解析或执行；
//   2. 链接只允许 http / https / mailto，一律新标签页打开并带 `noopener noreferrer`；
//   3. 图片一律不加载，只渲染占位（日志里的 URL 可能指向内网或带凭据的地址）。
// remark-cjk-friendly 修正中文紧贴标点时 `**加粗**` 不生效的问题。

const REMARK_PLUGINS: Options['remarkPlugins'] = [remarkGfm, remarkCjkFriendly]

function nodeText(node: ReactNode): string {
  if (node === null || node === undefined || typeof node === 'boolean') return ''
  if (typeof node === 'string' || typeof node === 'number') return String(node)
  if (Array.isArray(node)) return node.map(nodeText).join('')
  const props = (node as { props?: { children?: ReactNode } }).props
  return props ? nodeText(props.children) : ''
}

function CodePre({ children }: { children?: ReactNode }) {
  const child = Array.isArray(children) ? children[0] : children
  const props = (child as { props?: { className?: string; children?: ReactNode } } | null)?.props
  return <ChatCodeBlock language={codeLanguage(props?.className)} text={nodeText(props?.children ?? child)}/>
}

const COMPONENTS: Options['components'] = {
  pre: CodePre,
  a: ({ href, children }) => {
    const safe = safeLinkHref(href)
    if (!safe) return <span className="chat-link-blocked" data-tooltip="已阻止不安全的链接协议">{children}</span>
    return <a href={safe} target="_blank" rel="noopener noreferrer">{children}<ExternalLink size={11} aria-hidden="true"/></a>
  },
  img: ({ alt }) => <span className="chat-media-omitted" role="note"><ImageOff size={12} aria-hidden="true"/>{alt ? `图片已省略：${alt}` : '图片已省略'}</span>,
  table: ({ children }) => <div className="chat-table-wrap"><table>{children}</table></div>,
}

function reducedMotion() {
  return typeof window !== 'undefined' && typeof window.matchMedia === 'function' && window.matchMedia('(prefers-reduced-motion: reduce)').matches
}

/**
 * `fadeFrom` 是这段正文到达页面的时刻（`performance.now()`）。只有加载之后新到的正文才带它，
 * 历史记录不播淡入；减弱动效时也不播。
 */
export const ChatMarkdown = memo(function ChatMarkdown({ text, fadeFrom }: { text: string; fadeFrom?: number }) {
  const elapsedMs = fadeFrom === undefined ? Infinity : performance.now() - fadeFrom
  const fade = elapsedMs < WORD_FADE_WINDOW_MS && !reducedMotion()
  return <div className="chat-markdown">
    <Markdown
      remarkPlugins={REMARK_PLUGINS}
      rehypePlugins={fade ? [[rehypeWordFade, { elapsedMs }]] : undefined}
      urlTransform={(url) => safeLinkHref(url) ?? ''}
      components={COMPONENTS}
    >{text}</Markdown>
  </div>
})
