import { afterEach, describe, expect, it, vi } from 'vitest'
import {
  CHAT_COPY_OK,
  CHAT_COPY_UNAVAILABLE,
  codeLanguage,
  copyChatText,
  elisionNotice,
  recordCopyText,
  recordText,
  safeLinkHref,
  stripInvisible,
  toolInputText,
  toolOutputText,
  truncateText,
} from './structuredChatMarkdown'
import type { ChatRecord } from './structuredChatTypes'

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('safeLinkHref', () => {
  it('allows http, https and mailto only', () => {
    expect(safeLinkHref('https://example.test/a')).toBe('https://example.test/a')
    expect(safeLinkHref('http://example.test/a')).toBe('http://example.test/a')
    expect(safeLinkHref('mailto:user@example.test')).toBe('mailto:user@example.test')
    expect(safeLinkHref('HTTPS://EXAMPLE.TEST')).toBe('HTTPS://EXAMPLE.TEST')
  })

  it('rejects script, data, file and blob protocols', () => {
    for (const href of ['javascript:alert(1)', 'JavaScript:alert(1)', 'data:text/html,<script>1</script>', 'file:///etc/passwd', 'blob:https://example.test/x', 'vbscript:msgbox']) {
      expect(safeLinkHref(href)).toBeUndefined()
    }
  })

  it('rejects obfuscated protocols and relative paths', () => {
    expect(safeLinkHref(`java${String.fromCharCode(9)}script:alert(1)`)).toBeUndefined()
    expect(safeLinkHref(`java${String.fromCharCode(10)}script:alert(1)`)).toBeUndefined()
    expect(safeLinkHref('//example.test/a')).toBeUndefined()
    expect(safeLinkHref('/etc/passwd')).toBeUndefined()
    expect(safeLinkHref('')).toBeUndefined()
    expect(safeLinkHref(undefined)).toBeUndefined()
  })

  it('strips invisible characters used to disguise protocols', () => {
    expect(stripInvisible(`a${String.fromCharCode(0x200b)}b`)).toBe('ab')
  })
})

describe('code language', () => {
  it('extracts a safe language token from the class name', () => {
    expect(codeLanguage('language-ts')).toBe('ts')
    expect(codeLanguage('language-c++')).toBe('c++')
    expect(codeLanguage('foo bar')).toBe('')
    expect(codeLanguage(undefined)).toBe('')
    expect(codeLanguage(`language-${'x'.repeat(40)}`)).toBe('')
  })
})

describe('record text helpers', () => {
  const record: ChatRecord = {
    id: 'a',
    role: 'assistant',
    blocks: [
      { type: 'text', text: '第一段' },
      { type: 'tool-call', call_id: 'c1', name: 'Bash', input: { command: 'ls -la' } },
      { type: 'text', text: '第二段' },
      { type: 'tool-result', call_id: 'c1', output: 'ok', is_error: false },
    ],
  }

  it('joins only the text blocks for the visible body', () => {
    expect(recordText(record)).toBe('第一段\n\n第二段')
  })

  it('copies body plus tool summaries with real identifiers', () => {
    const text = recordCopyText(record)
    expect(text).toContain('第一段')
    expect(text).toContain('[工具调用] Bash (c1)')
    expect(text).toContain('ls -la')
    expect(text).toContain('[工具结果] (c1)')
  })

  it('marks tool errors in the copied text', () => {
    const text = recordCopyText({ id: 'b', role: 'tool', blocks: [{ type: 'tool-result', call_id: 'c2', output: 'boom', is_error: true }] })
    expect(text).toContain('[工具结果·错误] (c2)')
  })
})

describe('tool previews', () => {
  it('formats json input and falls back for circular values', () => {
    expect(toolInputText({ a: 1 })).toBe('{\n  "a": 1\n}')
    expect(toolInputText('plain')).toBe('plain')
    const circular: Record<string, unknown> = {}
    circular.self = circular
    expect(toolInputText(circular)).toBe('[object Object]')
  })

  it('bounds both input and output previews', () => {
    const long = 'x'.repeat(20)
    expect(toolInputText({ long: 'y'.repeat(50) }, 10)).toContain('已省略')
    expect(toolOutputText(long, 5)).toContain('已省略 15 个字符')
    expect(truncateText('short', 10)).toBe('short')
  })
})

describe('elision notice', () => {
  it('recognises the server side truncation marker', () => {
    expect(elisionNotice('正文\n…（已截断 1024 字节）')).toContain('已截断')
    expect(elisionNotice('普通正文')).toBeNull()
  })
})

describe('copyChatText', () => {
  it('reports success and failure instead of throwing', async () => {
    const writeText = vi.fn().mockResolvedValue(undefined)
    vi.stubGlobal('navigator', { clipboard: { writeText } })
    expect(await copyChatText('内容')).toBe(true)
    expect(writeText).toHaveBeenCalledWith('内容')

    vi.stubGlobal('navigator', { clipboard: { writeText: vi.fn().mockRejectedValue(new Error('denied')) } })
    expect(await copyChatText('内容')).toBe(false)

    vi.stubGlobal('navigator', {})
    expect(await copyChatText('内容')).toBe(false)
    expect(await copyChatText('')).toBe(false)
  })

  it('exposes readable feedback texts', () => {
    expect(CHAT_COPY_OK).toBe('已复制')
    expect(CHAT_COPY_UNAVAILABLE).toContain('剪贴板')
  })
})
