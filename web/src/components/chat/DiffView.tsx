// Adapted from Cindy (https://github.com/makecindy/cindy, Apache-2.0) apps/desktop/src/renderer/components/chat/DiffView.tsx@19dad9c225
//
// GitHub 式整行红绿底色的 diff。行数超过 200 时只挂载可见范围内的行（行高固定），
// 不一次性创建上千个 DOM 节点。长行在自己的容器里横向滚动，整行底色跟着内容延伸。

import { useMemo, useState } from 'react'
import { DIFF_VIRTUALIZE_THRESHOLD, type DiffDetails, type DiffLine } from '../../lib/chat/diffStats'

const ROW_HEIGHT = 20
const VIEWPORT_ROWS = 18
const OVERSCAN = 24

function DiffRow({ row, gutter }: { row: DiffLine; gutter: number }) {
  return <div className="chat-diff-row" data-type={row.type}>
    <span className="chat-diff-num" style={{ minWidth: `${gutter + 2}ch` }}>{row.lineNum}</span>
    <span className="chat-diff-sign">{row.type === 'del' ? '-' : row.type === 'add' ? '+' : ' '}</span>
    <span className="chat-diff-text">{row.text}</span>
  </div>
}

export function DiffView({ details }: { details: DiffDetails }) {
  const { rows } = details
  // 只记第一行可见行的序号：滚动没跨过一行时 React 不重新渲染。
  const [firstRow, setFirstRow] = useState(0)
  const gutter = String(rows.length ? rows[rows.length - 1].lineNum : 1).length
  // 虚拟化时只挂载一部分行，固有宽度要按最长的一行预留，否则横向滚动与整行底色会断开。
  const width = useMemo(() => rows.reduce((longest, row) => Math.max(longest, row.text.length), 0) + gutter + 6, [rows, gutter])
  const virtual = rows.length > DIFF_VIRTUALIZE_THRESHOLD
  const start = virtual ? Math.max(0, firstRow - OVERSCAN) : 0
  const end = virtual ? Math.min(rows.length, firstRow + 1 + VIEWPORT_ROWS + OVERSCAN) : rows.length
  const omitted = details.omittedRows ?? 0
  return <div className="chat-diff" data-virtual={virtual || undefined} tabIndex={0} aria-label="文件改动"
    style={virtual ? { maxHeight: VIEWPORT_ROWS * ROW_HEIGHT } : undefined}
    onScroll={virtual ? (event) => setFirstRow(Math.floor(event.currentTarget.scrollTop / ROW_HEIGHT)) : undefined}>
    <div className="chat-diff-body" style={{ minWidth: `${width}ch` }}>
      {start > 0 && <div style={{ height: start * ROW_HEIGHT }} aria-hidden="true"/>}
      {rows.slice(start, end).map((row, index) => <DiffRow key={start + index} row={row} gutter={gutter}/>)}
      {end < rows.length && <div style={{ height: (rows.length - end) * ROW_HEIGHT }} aria-hidden="true"/>}
    </div>
    {(details.truncated || omitted > 0) && <p className="chat-diff-note" role="note">{details.truncated ? '改动太大，只显示开头和结尾的预览。' : `另有 ${omitted} 行未显示。`}</p>}
  </div>
}
