import type { ReactNode } from 'react'
import Markdown from 'react-markdown'
import remarkGfm from 'remark-gfm'
import { ExternalLink, ImageOff } from 'lucide-react'
import { codeLanguage, safeLinkHref } from '../../lib/structuredChatMarkdown'
import { ChatCodeBlock } from './ChatCodeBlock'

// 助手正文渲染。三条硬约束：
//   1. 不开 rehype-raw：日志里的原始 HTML 不会被当作 HTML 解析或执行；
//   2. 链接只允许 http / https / mailto，外链带 `noopener noreferrer`；
//   3. 图片一律不加载，只渲染占位（日志里的 URL 可能指向内网或带凭据的地址）。

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

export function ChatMarkdown({ text }: { text: string }) {
  return <div className="chat-markdown">
    <Markdown
      remarkPlugins={[remarkGfm]}
      urlTransform={(url) => safeLinkHref(url) ?? ''}
      components={{
        pre: CodePre,
        a: ({ href, children }) => {
          const safe = safeLinkHref(href)
          if (!safe) return <span className="chat-link-blocked" data-tooltip="已阻止不安全的链接协议">{children}</span>
          return <a href={safe} target="_blank" rel="noopener noreferrer">{children}<ExternalLink size={11} aria-hidden="true"/></a>
        },
        img: ({ alt }) => <span className="chat-media-omitted" role="note"><ImageOff size={12} aria-hidden="true"/>{alt ? `图片已省略：${alt}` : '图片已省略'}</span>,
        table: ({ children }) => <div className="chat-table-wrap"><table>{children}</table></div>,
      }}
    >{text}</Markdown>
  </div>
}
