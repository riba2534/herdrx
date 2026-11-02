// Adapted from Cindy (https://github.com/makecindy/cindy, Apache-2.0) apps/desktop/src/renderer/components/chat/rehypeStreamWordFade.ts@19dad9c225
//
// 新到达的助手正文按词淡入：opacity 0→1、150ms，每词错开 16ms，排到 96ms 之后步长压到 4ms，
// 单词最长等 300ms（记录是整块到达的，不像 token 流那样边到边播）。代码块整块跳过，行内代码
// 作为一个整体淡入。`elapsedMs` 是这块正文到达后已经过去的时间：组件重新挂载时用负的
// delay 接着播，不从头重播；早已播完的词不再包装。

type HastText = { type: 'text'; value: string }
type HastElement = { type: 'element'; tagName: string; properties?: Record<string, unknown>; children: HastNode[] }
type HastNode = HastText | HastElement | { type: string; children?: HastNode[] }
type HastParent = { children: HastNode[] }

const WORD_FADE_DURATION_MS = 150
const STEP_MS = 16
const BACKLOG_MS = 96
const BACKLOG_STEP_MS = 4
export const WORD_FADE_MAX_DELAY_MS = 300
/** 超过这个时间的正文不再需要任何淡入包装。 */
export const WORD_FADE_WINDOW_MS = WORD_FADE_MAX_DELAY_MS + WORD_FADE_DURATION_MS

/** 第 index 个词的开播时刻（相对整块到达）。 */
export function wordFadeDelay(index: number) {
  const linear = index * STEP_MS
  if (linear <= BACKLOG_MS) return linear
  return Math.min(WORD_FADE_MAX_DELAY_MS, BACKLOG_MS + (index - BACKLOG_MS / STEP_MS) * BACKLOG_STEP_MS)
}

const segmenter = typeof Intl !== 'undefined' && typeof Intl.Segmenter === 'function' ? new Intl.Segmenter(undefined, { granularity: 'word' }) : null

/** 切词：词段独立，空白并入前一段（段首空白单独保留为纯文本）。 */
export function splitWords(text: string) {
  const out: string[] = []
  const pieces = segmenter ? Array.from(segmenter.segment(text), (part) => part.segment) : text.split(/(\s+)/)
  for (const piece of pieces) {
    if (!piece) continue
    if (!piece.trim() && out.length) out[out.length - 1] += piece
    else out.push(piece)
  }
  return out
}

function isElement(node: HastNode): node is HastElement {
  return node.type === 'element'
}

function fadeSpan(children: HastNode[], delay: number): HastElement {
  return { type: 'element', tagName: 'span', properties: { className: ['chat-word'], style: `--wf-delay:${delay}ms` }, children }
}

export function rehypeWordFade({ elapsedMs }: { elapsedMs: number }) {
  return (tree: HastParent) => {
    let index = 0
    // 已经播完的词保持原样，没播完的按剩余 delay 包装；排到上限之后的词共用同一个 delay。
    const next = () => {
      const start = wordFadeDelay(index++)
      const delay = start - elapsedMs
      return { delay: delay + WORD_FADE_DURATION_MS > 0 ? Math.round(delay) : null, capped: start === WORD_FADE_MAX_DELAY_MS }
    }
    const walk = (parent: HastParent) => {
      const children: HastNode[] = []
      for (const child of parent.children) {
        if (isElement(child)) {
          if (child.tagName === 'pre') {
            children.push(child)
          } else if (child.tagName === 'code') {
            const { delay } = next()
            children.push(delay === null ? child : fadeSpan([child], delay))
          } else {
            walk(child)
            children.push(child)
          }
          continue
        }
        if (child.type !== 'text') {
          children.push(child)
          continue
        }
        // 同一段文字里排到上限的词同帧淡入，并进一个 span，长回答不会留下成千上万个包装节点。
        let shared: HastElement | null = null
        for (const word of splitWords((child as HastText).value)) {
          const text: HastText = { type: 'text', value: word }
          if (!word.trim()) {
            if (shared) shared.children.push(text)
            else children.push(text)
            continue
          }
          const { delay, capped } = next()
          if (delay === null) {
            children.push(text)
          } else if (capped && shared) {
            shared.children.push(text)
          } else {
            const span = fadeSpan([text], delay)
            children.push(span)
            shared = capped ? span : null
          }
        }
      }
      parent.children = children
    }
    walk(tree)
  }
}
