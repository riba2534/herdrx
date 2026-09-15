import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { ChatMessage } from './ChatMessage'
import type { ChatRecord } from '../../lib/structuredChatTypes'

const tools = new Map([
  ['call-1', { name: 'Bash', input: { command: 'pnpm test' } }],
])

function stubClipboard(writeText: (text: string) => Promise<void>) {
  Object.defineProperty(window.navigator, 'clipboard', { value: { writeText }, configurable: true })
}

afterEach(() => {
  Reflect.deleteProperty(window.navigator, 'clipboard')
  vi.restoreAllMocks()
})

describe('user records', () => {
  it('renders a right aligned bubble with the raw text, not parsed markdown', () => {
    const record: ChatRecord = { id: 'u1', role: 'user', blocks: [{ type: 'text', text: '**不是粗体** <b>也不是标签</b>' }] }
    const { container } = render(<ChatMessage record={record}/>)
    const article = container.querySelector('.chat-message-user')
    expect(article).not.toBeNull()
    expect(article?.querySelector('.chat-bubble')).toHaveTextContent('**不是粗体** <b>也不是标签</b>')
    expect(article?.querySelector('strong')).toBeNull()
    expect(article?.querySelector('b')).toBeNull()
    expect(article?.getAttribute('data-role')).toBe('user')
  })

  it('never parses user text as HTML', () => {
    const { container } = render(<ChatMessage record={{ id: 'u2', role: 'user', blocks: [{ type: 'text', text: '<img src=x onerror=alert(1)>' }] }}/>)
    expect(container.querySelector('img')).toBeNull()
  })
})

describe('assistant records', () => {
  it('renders markdown as a left aligned document flow', () => {
    const record: ChatRecord = {
      id: 'a1',
      role: 'assistant',
      blocks: [{ type: 'text', text: '第一行\n\n- 项目一\n- 项目二\n\n**加粗** 与 `代码`\n\n| a | b |\n| - | - |\n| 1 | 2 |' }],
    }
    const { container } = render(<ChatMessage record={record}/>)
    const article = container.querySelector('.chat-message-assistant')
    expect(article?.getAttribute('data-role')).toBe('assistant')
    expect(article?.querySelector('strong')).toHaveTextContent('加粗')
    expect(article?.querySelectorAll('li')).toHaveLength(2)
    expect(article?.querySelector('table')).not.toBeNull()
    expect(article?.querySelector('.chat-table-wrap')).not.toBeNull()
  })

  it('does not execute raw HTML from the log', () => {
    const record: ChatRecord = { id: 'a2', role: 'assistant', blocks: [{ type: 'text', text: '<script>window.__pwned = 1</script>\n\n<img src="https://evil.test/x.png">' }] }
    const { container } = render(<ChatMessage record={record}/>)
    expect(container.querySelector('script')).toBeNull()
    expect(container.querySelector('img')).toBeNull()
    expect((window as unknown as { __pwned?: number }).__pwned).toBeUndefined()
  })

  it('never loads images referenced by the log', () => {
    const record: ChatRecord = { id: 'a3', role: 'assistant', blocks: [{ type: 'text', text: '![截图](https://internal.example.test/secret.png)' }] }
    const { container } = render(<ChatMessage record={record}/>)
    expect(container.querySelector('img')).toBeNull()
    expect(screen.getByText(/图片已省略：截图/)).toBeInTheDocument()
  })

  it('blocks javascript: links and opens allowed links safely', () => {
    const record: ChatRecord = { id: 'a4', role: 'assistant', blocks: [{ type: 'text', text: '[危险](javascript:alert(1)) 与 [文档](https://example.test/doc)' }] }
    const { container } = render(<ChatMessage record={record}/>)
    expect(container.querySelector('a[href^="javascript"]')).toBeNull()
    const link = container.querySelector('a')
    expect(link?.getAttribute('href')).toBe('https://example.test/doc')
    expect(link?.getAttribute('target')).toBe('_blank')
    expect(link?.getAttribute('rel')).toBe('noopener noreferrer')
  })

  it('renders tool calls with the real tool name and call id', () => {
    const record: ChatRecord = { id: 'a5', role: 'assistant', blocks: [{ type: 'text', text: '开始执行' }, { type: 'tool-call', call_id: 'call-1', name: 'Bash', input: { command: 'pnpm test' } }] }
    const { container } = render(<ChatMessage record={record}/>)
    const card = container.querySelector('.chat-tool-call')
    expect(card).not.toBeNull()
    expect(card?.querySelector('.chat-tool-name')).toHaveTextContent('Bash')
    expect(card?.querySelector('.chat-tool-id')).toHaveTextContent('call-1')
    expect(card?.querySelector('.chat-tool-pre')).toHaveTextContent('pnpm test')
    expect(card?.hasAttribute('open')).toBe(false)
  })

  it('copies a whole message and reports clipboard failure', async () => {
    const writeText = vi.fn().mockResolvedValue(undefined)
    stubClipboard(writeText)
    const record: ChatRecord = { id: 'a6', role: 'assistant', blocks: [{ type: 'text', text: '回答正文' }] }
    render(<ChatMessage record={record}/>)
    fireEvent.click(screen.getByRole('button', { name: '复制这条助手消息' }))
    await waitFor(() => expect(screen.getByText('已复制')).toBeInTheDocument())
    expect(writeText).toHaveBeenCalledWith('回答正文')

    stubClipboard(() => Promise.reject(new Error('denied')))
    fireEvent.click(screen.getByRole('button', { name: '复制这条助手消息' }))
    await waitFor(() => expect(screen.getByText(/无法写入剪贴板/)).toBeInTheDocument())
  })

  it('copies a fenced code block verbatim with its language label', async () => {
    const writeText = vi.fn().mockResolvedValue(undefined)
    stubClipboard(writeText)
    const record: ChatRecord = { id: 'a7', role: 'assistant', blocks: [{ type: 'text', text: '示例：\n\n```bash\npnpm test\n```\n' }] }
    const { container } = render(<ChatMessage record={record}/>)
    expect(container.querySelector('.chat-code-lang')).toHaveTextContent('bash')
    expect(container.querySelector('.chat-code-pre')).toHaveTextContent('pnpm test')
    fireEvent.click(screen.getByRole('button', { name: '复制代码' }))
    await waitFor(() => expect(writeText).toHaveBeenCalledWith('pnpm test\n'))
  })

  it('flags truncated text blocks instead of pretending they are complete', () => {
    const record: ChatRecord = { id: 'a8', role: 'assistant', blocks: [{ type: 'text', text: '很长\n…（已截断 4096 字节）' }] }
    render(<ChatMessage record={record}/>)
    expect(screen.getByRole('note')).toHaveTextContent('已截断 4096 字节')
  })
})

describe('tool records', () => {
  it('renders a collapsed result area with the resolved tool name', () => {
    const record: ChatRecord = { id: 't1', role: 'tool', blocks: [{ type: 'tool-result', call_id: 'call-1', output: '2 passed', is_error: false }] }
    const { container } = render(<ChatMessage record={record} tools={tools}/>)
    const card = container.querySelector('.chat-tool-result')
    expect(card).not.toBeNull()
    expect(card?.querySelector('.chat-tool-name')).toHaveTextContent('Bash')
    expect(card?.querySelector('.chat-tool-id')).toHaveTextContent('call-1')
    expect(card?.querySelector('.chat-tool-pre')).toHaveTextContent('2 passed')
    expect(container.querySelector('.chat-message-user')).toBeNull()
  })

  it('falls back to the call id when the tool name is unknown and marks errors', () => {
    const record: ChatRecord = { id: 't2', role: 'tool', blocks: [{ type: 'tool-result', call_id: 'call-9', output: 'boom', is_error: true }] }
    const { container } = render(<ChatMessage record={record} tools={tools}/>)
    const card = container.querySelector('.chat-tool-result')
    expect(card?.className).toContain('chat-tool-error')
    expect(card?.querySelector('.chat-tool-name')).toHaveTextContent('工具结果')
    expect(card?.querySelector('.chat-tool-kind')).toHaveTextContent('工具出错')
  })

  it('shows a placeholder for a result without call id and never becomes a user question', () => {
    const record: ChatRecord = { id: 't3', role: 'tool', blocks: [{ type: 'tool-result', call_id: '', output: 'orphan', is_error: false }] }
    const { container } = render(<ChatMessage record={record}/>)
    expect(container.querySelector('.chat-tool-name')).toHaveTextContent('工具结果')
    expect(container.querySelector('.chat-message-user')).toBeNull()
    expect(container.querySelector('.chat-bubble')).toBeNull()
  })
})

describe('system records', () => {
  it('renders as a muted notice', () => {
    const { container } = render(<ChatMessage record={{ id: 's1', role: 'system', blocks: [{ type: 'text', text: '本轮被中断' }] }}/>)
    const article = container.querySelector('.chat-message-system')
    expect(article).toHaveTextContent('本轮被中断')
    expect(container.querySelector('.chat-message-user')).toBeNull()
  })
})
