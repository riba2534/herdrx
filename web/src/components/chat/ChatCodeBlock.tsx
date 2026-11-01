import { createElement, useEffect, useState, type ReactNode } from 'react'
import { ChevronDown, ChevronUp } from 'lucide-react'
import { highlightCode, highlightLanguage, type HighlightTree } from '../../lib/chat/highlight'
import { ChatCopyButton } from './ChatCopyButton'

/** 超过这么多行的代码块默认收起，只露出开头一段。 */
export const CODE_COLLAPSE_LINES = 30

type HighlightNode = HighlightTree['children'][number]

/** lowlight 只产出 span 与文本；其它节点一律丢弃，不当作 HTML 处理。 */
function renderHighlight(nodes: readonly HighlightNode[]): ReactNode[] {
  return nodes.map((node, index) => {
    if (node.type === 'text') return node.value
    if (node.type !== 'element' || node.tagName !== 'span') return null
    const className = Array.isArray(node.properties?.className) ? node.properties.className.join(' ') : undefined
    return createElement('span', { key: index, className }, renderHighlight(node.children))
  })
}

function useHighlight(text: string, language: string) {
  const supported = highlightLanguage(language) !== null
  const [tree, setTree] = useState<{ text: string; language: string; value: HighlightTree } | null>(null)
  useEffect(() => {
    if (!supported) return
    let alive = true
    void highlightCode(text, language).then((value) => { if (alive && value) setTree({ text, language, value }) })
    return () => { alive = false }
  }, [supported, text, language])
  // 文本变了、旧结果还没换掉时先显示纯文本，绝不显示上一段代码的高亮。
  return tree && tree.text === text && tree.language === language ? tree.value : null
}

/**
 * 代码块：语言标签、整块复制、可横向滚动；语法高亮按语言懒加载，加载前和不支持的语言显示纯文本。
 * 超过约 30 行默认收起，可展开全部。
 */
export function ChatCodeBlock({ language, text }: { language: string; text: string }) {
  const lines = text.replace(/\n$/, '').split('\n').length
  const long = lines > CODE_COLLAPSE_LINES
  const [expanded, setExpanded] = useState(false)
  const tree = useHighlight(text, language)
  const collapsed = long && !expanded
  return <div className="chat-code" data-collapsed={collapsed || undefined}>
    <div className="chat-code-head">
      <span className="chat-code-lang">{language || '文本'}</span>
      <ChatCopyButton label="复制代码" text={text}/>
    </div>
    <pre className="chat-code-pre" tabIndex={0}><code className="hljs">{tree ? renderHighlight(tree.children) : text}</code></pre>
    {long && <button type="button" className="chat-code-toggle" aria-expanded={expanded} onClick={() => setExpanded((value) => !value)}>
      {expanded ? <ChevronUp size={13} aria-hidden="true"/> : <ChevronDown size={13} aria-hidden="true"/>}
      {expanded ? '收起代码' : `展开全部 ${lines} 行`}
    </button>}
  </div>
}
