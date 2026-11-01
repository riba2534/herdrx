import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { ChatMarkdown } from './ChatMarkdown'
import { CODE_COLLAPSE_LINES } from './ChatCodeBlock'

function stubClipboard(writeText: (text: string) => Promise<void>) {
  Object.defineProperty(window.navigator, 'clipboard', { value: { writeText }, configurable: true })
}

afterEach(() => {
  Reflect.deleteProperty(window.navigator, 'clipboard')
  vi.restoreAllMocks()
})

describe('markdown safety', () => {
  it('renders markdown as a document flow with tables in a horizontal scroller', () => {
    const { container } = render(<ChatMarkdown text={'第一行\n\n- 项目一\n- 项目二\n\n**加粗** 与 `代码`\n\n| a | b |\n| - | - |\n| 1 | 2 |'}/>)
    expect(container.querySelector('strong')).toHaveTextContent('加粗')
    expect(container.querySelectorAll('li')).toHaveLength(2)
    expect(container.querySelector('.chat-table-wrap > table')).not.toBeNull()
  })

  it('applies bold next to Chinese punctuation', () => {
    // CommonMark 认不出「标点结尾的加粗后面紧跟汉字」，这两处都需要 remark-cjk-friendly。
    const { container } = render(<ChatMarkdown text="注意：**推送已经执行。**远端是 main，**「main」**分支。"/>)
    expect(Array.from(container.querySelectorAll('strong'), (node) => node.textContent)).toEqual(['推送已经执行。', '「main」'])
  })

  it('does not execute raw HTML or load images from the log', () => {
    const { container } = render(<ChatMarkdown text={'<script>window.__pwned = 1</script>\n\n<img src="https://evil.test/x.png">\n\n![截图](https://internal.example.test/secret.png)'}/>)
    expect(container.querySelector('script')).toBeNull()
    expect(container.querySelector('img')).toBeNull()
    expect((window as unknown as { __pwned?: number }).__pwned).toBeUndefined()
    expect(screen.getByText(/图片已省略：截图/)).toBeInTheDocument()
  })

  it('blocks javascript: links and opens allowed links in a new tab', () => {
    const { container } = render(<ChatMarkdown text="[危险](javascript:alert(1)) 与 [文档](https://example.test/doc)"/>)
    expect(container.querySelector('a[href^="javascript"]')).toBeNull()
    const link = container.querySelector('a')
    expect(link).toHaveAttribute('href', 'https://example.test/doc')
    expect(link).toHaveAttribute('target', '_blank')
    expect(link).toHaveAttribute('rel', 'noopener noreferrer')
  })
})

describe('code blocks', () => {
  it('shows the language and copies the block verbatim', async () => {
    const writeText = vi.fn().mockResolvedValue(undefined)
    stubClipboard(writeText)
    const { container } = render(<ChatMarkdown text={'示例：\n\n```bash\npnpm test\n```\n'}/>)
    expect(container.querySelector('.chat-code-lang')).toHaveTextContent('bash')
    expect(container.querySelector('.chat-code-pre')).toHaveTextContent('pnpm test')
    fireEvent.click(screen.getByRole('button', { name: '复制代码' }))
    await waitFor(() => expect(writeText).toHaveBeenCalledWith('pnpm test\n'))
    expect(await screen.findByText('已复制')).toBeInTheDocument()
  })

  it('highlights known languages after the grammar loads', async () => {
    const { container } = render(<ChatMarkdown text={'```ts\nconst answer = 42\n```'}/>)
    await waitFor(() => expect(container.querySelector('.chat-code-pre .hljs-keyword')).toHaveTextContent('const'))
    expect(container.querySelector('.chat-code-pre')).toHaveTextContent('const answer = 42')
  })

  it('collapses long blocks and expands them on request', () => {
    const lines = Array.from({ length: CODE_COLLAPSE_LINES + 5 }, (_, index) => `line ${index}`).join('\n')
    const { container } = render(<ChatMarkdown text={`\`\`\`\n${lines}\n\`\`\``}/>)
    const block = container.querySelector('.chat-code') as HTMLElement
    expect(block).toHaveAttribute('data-collapsed', 'true')
    fireEvent.click(screen.getByRole('button', { name: `展开全部 ${CODE_COLLAPSE_LINES + 5} 行` }))
    expect(block).not.toHaveAttribute('data-collapsed')
    expect(screen.getByRole('button', { name: '收起代码' })).toHaveAttribute('aria-expanded', 'true')
  })

  it('keeps short blocks open without a toggle', () => {
    render(<ChatMarkdown text={'```\nshort\n```'}/>)
    expect(screen.queryByRole('button', { name: /展开全部/ })).toBeNull()
  })

  it('shows mermaid sources as an ordinary labelled code block', () => {
    const { container } = render(<ChatMarkdown text={'```mermaid\nflowchart LR\n  A --> B\n```'}/>)
    expect(container.querySelector('.chat-code-lang')).toHaveTextContent('mermaid')
    expect(container.querySelector('.chat-code-pre')).toHaveTextContent('flowchart LR A --> B')
    expect(container.querySelector('svg[id^="mermaid"], .chat-mermaid')).toBeNull()
  })
})

describe('word fade', () => {
  it('wraps words of freshly arrived text and leaves history untouched', () => {
    const fresh = render(<ChatMarkdown text="新到的回答 with words" fadeFrom={performance.now()}/>)
    expect(fresh.container.querySelectorAll('.chat-word').length).toBeGreaterThan(2)
    expect(fresh.container).toHaveTextContent('新到的回答 with words')
    const history = render(<ChatMarkdown text="历史回答"/>)
    expect(history.container.querySelector('.chat-word')).toBeNull()
  })

  it('does not animate when the user prefers reduced motion', () => {
    vi.stubGlobal('matchMedia', (query: string) => ({ matches: query.includes('reduce'), media: query, addEventListener() {}, removeEventListener() {} }))
    const { container } = render(<ChatMarkdown text="新到的回答" fadeFrom={performance.now()}/>)
    expect(container.querySelector('.chat-word')).toBeNull()
    vi.unstubAllGlobals()
  })
})
