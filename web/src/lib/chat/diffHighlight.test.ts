import { describe, expect, it } from 'vitest'
import { computeDiffDetails, DIFF_MAIN_THREAD_MAX_CHARS, diffSegmentsForTool, requestToolDiff, toolDiffSync } from './diffStats'
import { highlightCode, highlightLanguage } from './highlight'

describe('diff stats', () => {
  it('counts added and removed lines with full-row types', () => {
    const details = computeDiffDetails('a\nb\nc\n', 'a\nB\nc\nd\n')
    expect(details.stats).toEqual({ add: 2, del: 1 })
    expect(details.rows.map((row) => `${row.type}:${row.text}`)).toEqual(['ctx:a', 'del:b', 'add:B', 'ctx:c', 'add:d'])
    expect(details.rows.map((row) => row.lineNum)).toEqual([1, 2, 3, 4, 5])
  })

  it('normalizes Edit, Write and MultiEdit inputs', () => {
    expect(diffSegmentsForTool('Edit', { old_string: 'x', new_string: 'y' })?.segments).toEqual([{ key: 'edit:0', oldString: 'x', newString: 'y' }])
    expect(diffSegmentsForTool('Write', { content: 'new' })?.segments).toEqual([{ key: 'write:0', oldString: '', newString: 'new' }])
    expect(diffSegmentsForTool('MultiEdit', { edits: [{ old_string: 'a', new_string: 'b' }, 'junk'] })?.segments).toEqual([{ key: 'edit:0', oldString: 'a', newString: 'b' }, { key: 'edit:1', oldString: '', newString: '' }])
    expect(diffSegmentsForTool('Bash', { command: 'ls' })).toBeNull()
    expect(diffSegmentsForTool('Edit', null)).toBeNull()
    // 残缺记录（一个字都没有）不画空 diff，改为显示入参。
    expect(diffSegmentsForTool('Edit', { file_path: '/a' })).toBeNull()
  })

  it('aggregates every MultiEdit segment synchronously for small inputs', () => {
    const diff = toolDiffSync('MultiEdit', { edits: [{ old_string: 'a\n', new_string: 'b\n' }, { old_string: '', new_string: 'c\nd\n' }] })
    expect(diff?.stats).toEqual({ add: 3, del: 1 })
    expect(diff?.segments).toHaveLength(2)
  })

  it('leaves large inputs to the async path and falls back to a bounded preview without a worker', async () => {
    const input = { content: 'line\n'.repeat(DIFF_MAIN_THREAD_MAX_CHARS / 4) }
    expect(toolDiffSync('Write', input)).toBeNull()
    const diff = await requestToolDiff('Write', input)
    expect(diff?.truncated).toBe(true)
    expect(diff?.segments[0].details.rows.length).toBeLessThanOrEqual(600)
    // 结果按入参对象缓存：之后同步读取直接命中，不再计算。
    expect(toolDiffSync('Write', input)).toBe(diff)
  })
})

describe('code highlighting', () => {
  it('resolves common aliases and rejects unknown languages', () => {
    expect(highlightLanguage('sh')).toBe('bash')
    expect(highlightLanguage('TSX')).toBe('typescript')
    expect(highlightLanguage('yml')).toBe('yaml')
    expect(highlightLanguage('mermaid')).toBeNull()
    expect(highlightLanguage('')).toBeNull()
    expect(highlightLanguage('__proto__')).toBeNull()
  })

  it('loads a grammar on demand and returns token spans as a tree', async () => {
    const tree = await highlightCode('const a = 1', 'ts')
    const classes = JSON.stringify(tree)
    expect(classes).toContain('hljs-keyword')
    expect(await highlightCode('x', 'not-a-language')).toBeNull()
  })
})
